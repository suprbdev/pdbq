package e2e

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
)

const (
	watchSchema   = "watch_e2e"
	watchDeadline = 45 * time.Second
)

// TestWatchHotSwap boots the server through App.Handler with watch mode on
// (the watcher starting from the boot catalog's hash) and checks that DDL in
// the watched schema reaches the API without a restart.
func TestWatchHotSwap(t *testing.T) {
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string) {
		t.Helper()
		if _, err := conn.Exec(context.Background(), sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`CREATE SCHEMA ` + watchSchema + `; CREATE TABLE ` + watchSchema + `.first (id int PRIMARY KEY)`)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = conn.Exec(bg, `DROP SCHEMA IF EXISTS `+watchSchema+` CASCADE`)
		_, _ = conn.Exec(bg, `DROP EVENT TRIGGER IF EXISTS pdbq_watch; DROP EVENT TRIGGER IF EXISTS pdbq_watch_drop; DROP FUNCTION IF EXISTS pdbq_watch_notify()`)
		_ = conn.Close(bg)
	})

	cfg := config.DefaultConfig()
	cfg.Database.URL = url
	cfg.RLS.Enabled = false
	cfg.Errors.Detail = "dev"
	cfg.Plugins.Disabled = []string{"simple-names"}
	cfg.Schema.Schemas = []string{watchSchema}
	cfg.Watch.Enabled = true
	cfg.Watch.PollInterval = 200 * time.Millisecond
	cfg.Metrics.Enabled = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app := pdbq.New(cfg)
	t.Cleanup(app.Close)
	t.Cleanup(cancel)
	h, err := app.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)

	waitFor := func(query string) {
		t.Helper()
		deadline := time.Now().Add(watchDeadline)
		for {
			res := post(t, hs.URL, query, nil)
			if len(res.Errors) == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s still failing after DDL: %v", query, res.Errors)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitFor(`{ allFirsts { totalCount } }`)

	select {
	case <-app.WatchReady():
	case <-time.After(watchDeadline):
		t.Fatal("watcher never became ready")
	}

	exec(`CREATE TABLE ` + watchSchema + `.second (id int PRIMARY KEY)`)
	waitFor(`{ allSeconds { totalCount } }`)

	body, err := httpGet(hs.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	got := samples(t, string(body))
	if n := got[`pdbq_schema_rebuilds_total{result="success"}`]; n < 1 {
		t.Errorf("successful rebuilds = %v, want >= 1", n)
	}
	if ts := got["pdbq_schema_last_rebuild_timestamp_seconds"]; ts <= 0 {
		t.Errorf("last rebuild timestamp = %v, want > 0", ts)
	}
}
