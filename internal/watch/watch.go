// Package watch re-introspects on DDL changes (dev mode). Preferred
// mechanism: a DDL event trigger NOTIFYing a channel that we LISTEN on.
// Fallback (no permission to create event triggers): poll the catalog hash.
package watch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbq/internal/introspect"
)

// Watcher triggers OnChange whenever the database schema drifts.
type Watcher struct {
	Pool         *pgxpool.Pool
	Schemas      []string
	Channel      string
	PollInterval time.Duration
	OnChange     func(cat *introspect.Catalog)
	Log          *slog.Logger
}

// Run blocks until ctx is cancelled. It tries to install the event trigger;
// on permission failure it degrades to hash polling with a warning.
func (w *Watcher) Run(ctx context.Context) error {
	if w.Log == nil {
		w.Log = slog.Default()
	}
	if err := w.installTrigger(ctx); err != nil {
		w.Log.Warn("watch: cannot install DDL event trigger; falling back to polling",
			"err", err, "interval", w.PollInterval)
		return w.poll(ctx)
	}
	w.Log.Info("watch: DDL event trigger installed", "channel", w.Channel)
	return w.listen(ctx)
}

// installTrigger creates (idempotently) a function + event trigger that
// NOTIFYs our channel at the end of every DDL command.
func (w *Watcher) installTrigger(ctx context.Context) error {
	fn := fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION pdbq_watch_notify() RETURNS event_trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			PERFORM pg_notify(%s, tg_tag);
		END $$;`, quoteLit(w.Channel))
	if _, err := w.Pool.Exec(ctx, fn); err != nil {
		return err
	}
	_, err := w.Pool.Exec(ctx, `
		DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_event_trigger WHERE evtname = 'pdbq_watch') THEN
				CREATE EVENT TRIGGER pdbq_watch ON ddl_command_end EXECUTE FUNCTION pdbq_watch_notify();
			END IF;
		END $$;`)
	return err
}

func (w *Watcher) listen(ctx context.Context) error {
	conn, err := w.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+quoteIdent(w.Channel)); err != nil {
		return err
	}
	for {
		_, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		// Debounce bursts of DDL (e.g. a migration) before re-introspecting.
		time.Sleep(250 * time.Millisecond)
		drainNotifications(ctx, conn.Conn())
		w.reintrospect(ctx)
	}
}

func drainNotifications(ctx context.Context, conn *pgx.Conn) {
	for {
		drainCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		_, err := conn.WaitForNotification(drainCtx)
		cancel()
		if err != nil {
			return
		}
	}
}

func (w *Watcher) poll(ctx context.Context) error {
	var lastHash string
	ticker := time.NewTicker(w.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			cat, err := introspect.Introspect(ctx, w.Pool, w.Schemas)
			if err != nil {
				w.Log.Warn("watch: poll introspection failed", "err", err)
				continue
			}
			hash, err := cat.Hash()
			if err != nil {
				continue
			}
			if lastHash != "" && hash != lastHash {
				w.Log.Info("watch: schema drift detected (poll)")
				w.OnChange(cat)
			}
			lastHash = hash
		}
	}
}

func (w *Watcher) reintrospect(ctx context.Context) {
	cat, err := introspect.Introspect(ctx, w.Pool, w.Schemas)
	if err != nil {
		w.Log.Error("watch: re-introspection failed", "err", err)
		return
	}
	w.Log.Info("watch: schema changed, rebuilding")
	w.OnChange(cat)
}

func quoteIdent(s string) string {
	return `"` + s + `"`
}

func quoteLit(s string) string {
	return "'" + s + "'"
}
