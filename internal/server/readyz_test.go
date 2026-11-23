package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbq/internal/config"
)

func readyzServer(ping func(context.Context) error) http.Handler {
	s := testServer(config.Server{ReadyzTimeout: 100 * time.Millisecond})
	s.SetReadiness(ping)
	return s.Handler()
}

func TestReadyz(t *testing.T) {
	ok := readyzServer(func(context.Context) error { return nil })
	if rec := get(t, ok, "/readyz"); rec.Code != 200 || rec.Body.String() != `{"status":"ok"}` {
		t.Errorf("healthy readyz = %d %q", rec.Code, rec.Body.String())
	}
	down := readyzServer(func(context.Context) error { return errors.New("refused") })
	rec := get(t, down, "/readyz")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"unavailable"`) {
		t.Errorf("failing readyz = %d %q", rec.Code, rec.Body.String())
	}
	if rec := get(t, down, "/healthz"); rec.Code != 200 {
		t.Errorf("healthz = %d", rec.Code)
	}
	hung := readyzServer(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	start := time.Now()
	if rec := get(t, hung, "/readyz"); rec.Code != 503 || time.Since(start) > 2*time.Second {
		t.Errorf("hung readyz = %d after %s", rec.Code, time.Since(start))
	}
}

func TestReadyzClosedPort(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/db?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := testServer(config.Server{ReadyzTimeout: time.Second})
	s.SetReadiness(pool.Ping)
	if rec := get(t, s.Handler(), "/readyz"); rec.Code != 503 {
		t.Errorf("readyz on a closed port = %d", rec.Code)
	}
}
