package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
)

// stalledClient sends an incremental request over a socket with a small
// receive buffer and never reads the response.
func stalledClient(t *testing.T, addr, query string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.(*net.TCPConn).SetReadBuffer(4096); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"query": query})
	req := fmt.Sprintf("POST /graphql HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nAccept: multipart/mixed\r\nContent-Length: %d\r\n\r\n%s", addr, len(body), body)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	return conn
}

// idleInTx counts the connections to the test database left idle inside a
// transaction.
func idleInTx(t *testing.T) int {
	t.Helper()
	return countWhere(t, `SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database() AND state = 'idle in transaction' AND pid <> pg_backend_pid()`)
}

// TestIncrementalStalledClientReleasesConnection pins that clients which
// send an incremental request and stop reading cannot keep pooled
// connections in open transactions: at most server.incremental_max_responses
// of them (half of database.max_conns by default) hold one at a time, and
// each is rolled back within server.incremental_write_timeout of its write
// stalling, or at server.request_timeout, while the clients keep their
// sockets open. The server keeps answering other requests.
func TestIncrementalStalledClientReleasesConnection(t *testing.T) {
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	title := fmt.Sprintf("stall-%d-", time.Now().UnixNano())
	execAll(t, testConn(t), fmt.Sprintf(`INSERT INTO posts (author_id, title, body, published)
		SELECT 1, '%s' || g, repeat(md5(g::text), 6000), true FROM generate_series(1, 64) g`, title))
	t.Cleanup(func() {
		_, _ = testConn(t).Exec(context.Background(), `DELETE FROM posts WHERE title LIKE $1`, title+"%")
	})
	const query = `{ allPosts(first: 64, orderBy: [ID_DESC]) { nodes @stream(initialCount: 1) { id body } } }`

	for _, tc := range []struct {
		name                         string
		writeTimeout, requestTimeout time.Duration
		release                      time.Duration
	}{
		{"write timeout", time.Second, time.Minute, 4 * time.Second},
		{"request timeout", time.Hour, 3 * time.Second, 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Database.URL = url
			cfg.Database.MaxConns = 4
			noIdentity(&cfg)
			cfg.Plugins.Disabled = []string{"simple_names"}
			cfg.Server.IncrementalDelivery = true
			cfg.Server.IncrementalWriteTimeout = tc.writeTimeout
			cfg.Server.RequestTimeout = tc.requestTimeout
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			if got := cfg.IncrementalResponses(); got != 2 {
				t.Fatalf("IncrementalResponses() = %d, want half of max_conns", got)
			}
			app := pdbq.New(cfg)
			t.Cleanup(app.Close)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			h, err := app.Handler(ctx)
			if err != nil {
				t.Fatal(err)
			}
			hs := httptest.NewServer(h)
			t.Cleanup(hs.Close)
			addr := strings.TrimPrefix(hs.URL, "http://")

			for range 4 {
				stalledClient(t, addr, query)
			}
			stalled := time.Time{}
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
				n := idleInTx(t)
				if n > 2 {
					t.Fatalf("%d transactions held by stalled clients, want at most incremental_max_responses (2)", n)
				}
				if n > 0 {
					stalled = time.Now()
					break
				}
			}
			if stalled.IsZero() {
				t.Fatal("no stalled client ever held a transaction: the response fit in the socket buffers")
			}
			if tc.requestTimeout < time.Minute {
				resp := incPost(t, hs.URL+"/graphql", `{ allUsers(first: 2, orderBy: [ID_ASC]) { nodes @stream(initialCount: 1) { id } } }`, "multipart/mixed", "")
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if ct := resp.Header.Get("Content-Type"); ct != "application/json" || strings.ReplaceAll(string(bytes.TrimSpace(body)), " ", "") != `{"data":{"allUsers":{"nodes":[{"id":1},{"id":2}]}}}` {
					t.Fatalf("request beyond incremental_max_responses: %s %s, want one JSON result", ct, body)
				}
			}
			for n := idleInTx(t); n > 0; n = idleInTx(t) {
				if n > 2 {
					t.Fatalf("%d transactions held by stalled clients, want at most incremental_max_responses (2)", n)
				}
				if time.Since(stalled) > tc.release {
					t.Fatalf("stalled clients still hold transactions %v after their writes stalled", time.Since(stalled))
				}
				time.Sleep(50 * time.Millisecond)
			}

			res := postRaw(t, hs.URL, `{ allUsers(first: 1, orderBy: [ID_ASC]) { nodes { id } } }`, nil)
			if res != `{"data":{"allUsers":{"nodes":[{"id":1}]}}}` {
				t.Fatalf("query after the stall = %s", res)
			}
		})
	}
}
