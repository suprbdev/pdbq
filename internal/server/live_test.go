package server

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/live"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/subscribe"
)

func liveTestServer(t *testing.T, mutate func(*config.Server)) (*Server, *exec.Executor) {
	t.Helper()
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, Subscriptions: true, Live: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	ex := exec.New(nil, built, compile.New(built).Compile, nil, exec.Options{MaxDepth: 15, MaxCost: 10000})
	cfg := config.DefaultConfig().Server
	cfg.Subscriptions, cfg.Live = true, true
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := New(cfg, testAuth(), ex, built.SDL, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	mc := &memConn{notified: make(chan *pgconn.Notification, 16)}
	hub := subscribe.NewHub(func(context.Context) (subscribe.Conn, error) { return mc, nil }, nil)
	tracker := live.NewTracker(hub, live.Options{Channel: cfg.LiveChannel})
	srv.SetSubscriptions(hub)
	srv.SetLive(tracker)
	t.Cleanup(func() {
		tracker.Close()
		hub.Close()
	})
	return srv, ex
}

func TestLiveFields(t *testing.T) {
	srv, ex := liveTestServer(t, nil)
	for _, tc := range []struct {
		name, query string
		live        bool
		fields      []string
	}{
		{"subscription", `subscription { users: allUsers { nodes { id } } }`, true, []string{"users"}},
		{"node", `subscription { node(nodeId: "WyJVc2VyIiwxXQ==") { nodeId } }`, true, []string{"node"}},
		{"listen", `subscription { listen(topic: "x") { relatedNodeId } }`, false, nil},
		{"live query", `query @live { allUsers { totalCount } allPosts { totalCount } }`, true, []string{"allUsers", "allPosts"}},
		{"plain query", `{ allUsers { totalCount } }`, false, nil},
		{"mutation", `mutation { createUser(input: {email: "x"}) { clientMutationId } }`, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, op, err := ex.Prepare(context.Background(), exec.Request{Query: tc.query})
			if err != nil {
				t.Fatal(err)
			}
			fields, ok := srv.liveFields(ex, op)
			if ok != tc.live {
				t.Fatalf("live = %v, want %v", ok, tc.live)
			}
			if !ok {
				return
			}
			if len(fields) != len(tc.fields) {
				t.Fatalf("fields = %d, want %d", len(fields), len(tc.fields))
			}
			for i, f := range fields {
				if f.Alias != tc.fields[i] {
					t.Errorf("field %d alias %q, want %q", i, f.Alias, tc.fields[i])
				}
				if f.ObjectDefinition != ex.Built.Schema.Query || f.Definition != ex.Built.Schema.Query.Fields.ForName(f.Name) {
					t.Errorf("field %s is not bound to the Query root", f.Name)
				}
			}
		})
	}

	off, exOff := liveTestServer(t, func(c *config.Server) { c.Live = false })
	_, op, err := exOff.Prepare(context.Background(), exec.Request{Query: `query @live { allUsers { totalCount } }`})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := off.liveFields(exOff, op); ok {
		t.Error("live query served with server.live off")
	}
}

func TestListenRejectsLiveChannel(t *testing.T) {
	srv, ex := liveTestServer(t, func(c *config.Server) { c.SubscriptionsTopicPrefix = "" })
	ctx, op, err := ex.Prepare(context.Background(), exec.Request{Query: `subscription { listen(topic: "pdbq_live") { relatedNodeId } }`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.startListen(ctx, ex, op); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("listen on the live channel: %v", err)
	}
}

func TestLiveCapsUnit(t *testing.T) {
	srv, _ := liveTestServer(t, func(c *config.Server) { c.LiveMaxOperations = 2 })
	for range 2 {
		if !srv.reserveLiveOperation() {
			t.Fatal("reserve below the cap failed")
		}
	}
	if srv.reserveLiveOperation() {
		t.Fatal("reserve beyond live_max_operations succeeded")
	}
	srv.releaseLiveOperation()
	if !srv.reserveLiveOperation() {
		t.Fatal("released slot not reusable")
	}
	c := &wsSession{s: srv}
	srv.cfg.LiveMaxPerConnection = 1
	if !c.reserveLive() {
		t.Fatal("reserve below the per-connection cap failed")
	}
	if c.reserveLive() {
		t.Fatal("per-connection cap not enforced")
	}
	c.releaseLive()
	if !c.reserveLive() {
		t.Fatal("released per-connection slot not reusable")
	}
}
