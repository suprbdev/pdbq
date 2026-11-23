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

const closeWatchSchema = "close_watch_e2e"

// TestCloseStopsWatcher checks that App.Close returns without the context
// given to Handler being cancelled, and that it is idempotent.
func TestCloseStopsWatcher(t *testing.T) {
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `CREATE SCHEMA `+closeWatchSchema+`; CREATE TABLE `+closeWatchSchema+`.first (id int PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = conn.Exec(bg, `DROP SCHEMA IF EXISTS `+closeWatchSchema+` CASCADE`)
		_, _ = conn.Exec(bg, `DROP EVENT TRIGGER IF EXISTS pdbq_watch; DROP EVENT TRIGGER IF EXISTS pdbq_watch_drop; DROP FUNCTION IF EXISTS pdbq_watch_notify()`)
		_ = conn.Close(bg)
	})

	cfg := config.DefaultConfig()
	cfg.Database.URL = url
	noIdentity(&cfg)
	cfg.Schema.Schemas = []string{closeWatchSchema}
	cfg.Watch.Enabled = true
	cfg.Watch.PollInterval = 200 * time.Millisecond
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app := pdbq.New(cfg)
	h, err := app.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(h)
	defer hs.Close()
	if res := post(t, hs.URL, `{ allFirsts { totalCount } }`, nil); len(res.Errors) != 0 {
		t.Fatal(res.Errors)
	}

	done := make(chan struct{})
	go func() {
		app.Close()
		app.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("App.Close did not return while the watcher context was live")
	}
}

// TestCloseWithoutServe checks that Close is safe on an App that never
// started a server.
func TestCloseWithoutServe(t *testing.T) {
	app := pdbq.New(config.DefaultConfig())
	app.Close()
	app.Close()
}
