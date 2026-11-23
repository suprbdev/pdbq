package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

func TestIncrementalAccept(t *testing.T) {
	cases := []struct {
		accept          string
		format          exec.IncrementalFormat
		ok, unsupported bool
	}{
		{"application/json", 0, false, false},
		{"", 0, false, false},
		{"multipart/mixed", exec.IncrementalV02, true, false},
		{"multipart/mixed;incrementalSpec=v0.2, application/json", exec.IncrementalV02, true, false},
		{`multipart/mixed; deferSpec="20220824", application/json`, exec.IncrementalDefer20220824, true, false},
		{"application/json, multipart/mixed;deferspec=20220824", exec.IncrementalDefer20220824, true, false},
		{"multipart/mixed;deferSpec=20200101", 0, false, true},
		{"multipart/mixed;incrementalSpec=v9, multipart/mixed;deferSpec=20220824", exec.IncrementalDefer20220824, true, false},
		{"multipart/mixed;boundary=x;incrementalSpec=V0.2", exec.IncrementalV02, true, false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodPost, "/graphql", nil)
		if tc.accept != "" {
			r.Header.Set("Accept", tc.accept)
		}
		format, ok, unsupported := incrementalAccept(r)
		if format != tc.format || ok != tc.ok || unsupported != tc.unsupported {
			t.Errorf("Accept %q = (%v, %v, %v), want (%v, %v, %v)", tc.accept, format, ok, unsupported, tc.format, tc.ok, tc.unsupported)
		}
	}
}

func incrementalServer(t *testing.T, cfg config.Server) http.Handler {
	t.Helper()
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, IncrementalDelivery: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	ex := exec.New(nil, built, nil, nil, exec.Options{Incremental: exec.IncrementalOptions{Enabled: true}})
	cfg.MaxBodyBytes = 1 << 20
	cfg.IncrementalDelivery = true
	s, err := New(cfg, testAuth(), ex, built.SDL, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func postAccept(h http.Handler, accept, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec, req)
	return rec
}

// TestIncrementalSingleResult checks that an operation with nothing to
// deliver incrementally is answered as plain, uncompressed JSON when the
// client accepts multipart/mixed.
func TestIncrementalSingleResult(t *testing.T) {
	h := incrementalServer(t, config.Server{RequestTimeout: time.Second, Compression: true})
	rec := postAccept(h, "multipart/mixed;incrementalSpec=v0.2, application/json", `{"query":"{ __typename }"}`)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("got %d %v", rec.Code, rec.Header())
	}
	if got := rec.Body.String(); got != `{"data":{"__typename":"Query"}}`+"\n" {
		t.Fatalf("body = %q", got)
	}
	rec = postAccept(h, "multipart/mixed", `{"query":"{ userById(id: 1) { email @stream } }"}`)
	if !strings.Contains(rec.Body.String(), "non-list field") {
		t.Fatalf("stream on a scalar: %s", rec.Body.String())
	}
}

func TestIncrementalNotAcceptable(t *testing.T) {
	h := incrementalServer(t, config.Server{})
	rec := postAccept(h, "multipart/mixed;deferSpec=20200101", `{"query":"{ __typename }"}`)
	if rec.Code != http.StatusNotAcceptable {
		t.Fatalf("got %d, want 406: %s", rec.Code, rec.Body.String())
	}
}

// TestIncrementalOffKeepsJSON checks that with the option off a client
// accepting multipart/mixed still gets the buffered (gzip) JSON response.
func TestIncrementalOffKeepsJSON(t *testing.T) {
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Server{MaxBodyBytes: 1 << 20, Compression: true}, testAuth(), exec.New(nil, built, nil, nil, exec.Options{}), built.SDL, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	rec := postAccept(s.Handler(), "multipart/mixed;deferSpec=20200101", `{"query":"{ __typename }"}`)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("got %d %v", rec.Code, rec.Header())
	}
}

// blockingWriter is a ResponseWriter without deadline support whose Write
// blocks until release is closed.
type blockingWriter struct {
	header  http.Header
	release chan struct{}
}

func (w *blockingWriter) Header() http.Header { return w.header }
func (w *blockingWriter) WriteHeader(int)     {}
func (w *blockingWriter) Write(p []byte) (int, error) {
	<-w.release
	return len(p), nil
}

func TestBoundedWriterStalledWriteWithoutDeadlineSupport(t *testing.T) {
	w := &blockingWriter{header: http.Header{}, release: make(chan struct{})}
	b := &boundedWriter{w: w, rc: http.NewResponseController(w), ctx: context.Background(), timeout: 50 * time.Millisecond}
	start := time.Now()
	if err := b.write([]byte("x")); !errors.Is(err, errWriteStalled) {
		t.Fatalf("write = %v, want errWriteStalled", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("stalled write returned after %v", d)
	}
	if err := b.write([]byte("y")); !errors.Is(err, errWriteStalled) {
		t.Fatalf("write after a stall = %v, want errWriteStalled", err)
	}
	waited := make(chan struct{})
	go func() { _ = b.wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("wait returned while the stalled write was still running")
	case <-time.After(20 * time.Millisecond):
	}
	close(w.release)
	<-waited
}

func TestBoundedWriterStopsAtTheContextDeadline(t *testing.T) {
	w := &blockingWriter{header: http.Header{}, release: make(chan struct{})}
	defer close(w.release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	b := &boundedWriter{w: w, rc: http.NewResponseController(w), ctx: ctx, timeout: time.Hour}
	start := time.Now()
	if err := b.write([]byte("x")); !errors.Is(err, errWriteStalled) {
		t.Fatalf("write = %v, want errWriteStalled", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("write outlived the request deadline: %v", d)
	}
}

// TestBoundedWriterTimesOutOnAStalledConnection writes to a real client
// that never reads: the connection's write deadline fails the write.
func TestBoundedWriterTimesOutOnAStalledConnection(t *testing.T) {
	result := make(chan error, 1)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := &boundedWriter{w: w, rc: http.NewResponseController(w), ctx: r.Context(), timeout: 200 * time.Millisecond}
		defer func() { _ = b.wait() }()
		chunk := make([]byte, 1<<20)
		var err error
		for i := 0; i < 256 && err == nil; i++ {
			err = b.write(chunk)
		}
		result <- err
	}))
	defer hs.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(hs.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.(*net.TCPConn).SetReadBuffer(4096); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("256 MiB reached a client that never reads")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the write to a stalled client never failed")
	}
}

func TestSlots(t *testing.T) {
	var l slots
	for range 2 {
		if !l.acquire(2) {
			t.Fatal("acquire under the limit failed")
		}
	}
	if l.acquire(2) {
		t.Fatal("acquire beyond the limit succeeded")
	}
	l.release()
	if !l.acquire(2) {
		t.Fatal("acquire after a release failed")
	}
	if !l.acquire(0) {
		t.Fatal("limit 0 must not limit")
	}
}
