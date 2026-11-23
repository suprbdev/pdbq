package e2e

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
)

func metricsConfig(t *testing.T) config.Config {
	t.Helper()
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	cfg := config.DefaultConfig()
	cfg.Database.URL = url
	noIdentity(&cfg)
	cfg.Errors.Detail = "dev"
	cfg.Metrics.Enabled = true
	return cfg
}

// samples parses a Prometheus text exposition into series -> value.
func samples(t *testing.T, body string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		v, err := strconv.ParseFloat(line[i+1:], 64)
		if err != nil {
			t.Fatalf("bad sample %q", line)
		}
		out[line[:i]] = v
	}
	return out
}

func TestMetricsScrape(t *testing.T) {
	cfg := metricsConfig(t)
	cfg.Limits.MaxCost = 200
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app := pdbq.New(cfg)
	t.Cleanup(app.Close)
	h, err := app.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)

	for range 2 {
		requireNoErrors(t, post(t, hs.URL, `{userByEmail(email: "ada@example.com") {id}}`, nil))
	}
	if res := post(t, hs.URL, `mutation {createUser(input: {email: "ada@example.com", mood: OK}) {user {id}}}`, nil); len(res.Errors) == 0 {
		t.Fatal("duplicate email must fail")
	}
	if res := post(t, hs.URL, `{allUsers(first: 100) {nodes {id email fullName}}}`, nil); len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "cost") {
		t.Fatalf("want a cost-limit error, got %v", res.Errors)
	}
	if _, err := httpGet(hs.URL + "/healthz"); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(hs.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("/metrics: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	got := samples(t, readAll(t, resp))
	for series, want := range map[string]float64{
		`pdbq_http_requests_total{method="POST",route="graphql",status_class="2xx"}`: 4,
		`pdbq_http_requests_total{method="GET",route="health",status_class="2xx"}`:   1,
		`pdbq_document_cache_requests_total{result="hit"}`:                           1,
		`pdbq_document_cache_requests_total{result="miss"}`:                          3,
		`pdbq_cost_limit_rejections_total`:                                           1,
		`pdbq_db_duration_seconds_count{op="query"}`:                                 2,
		`pdbq_db_duration_seconds_count{op="query_tx"}`:                              1,
		`pdbq_db_errors_total{op="query_tx",sqlstate_class="23"}`:                    1,
		`pdbq_field_errors_total{class="conflict"}`:                                  1,
		`pdbq_compile_duration_seconds_count`:                                        3,
		`pdbq_db_pool_max_conns`:                                                     float64(cfg.Database.MaxConns),
		`pdbq_db_pool_acquired_conns`:                                                0,
		`pdbq_schema_last_rebuild_timestamp_seconds`:                                 0,
	} {
		if v, ok := got[series]; !ok || v != want {
			t.Errorf("%s = %v (present %v), want %v", series, v, ok, want)
		}
	}
	if got["pdbq_db_pool_acquires_total"] < 3 {
		t.Errorf("pdbq_db_pool_acquires_total = %v, want >= 3", got["pdbq_db_pool_acquires_total"])
	}
	if _, ok := got["go_goroutines"]; !ok {
		t.Error("runtime metrics missing")
	}
}

func TestMetricsDisabled(t *testing.T) {
	cfg := metricsConfig(t)
	cfg.Metrics.Enabled = false
	app := pdbq.New(cfg)
	t.Cleanup(app.Close)
	if app.Metrics() != nil {
		t.Fatal("Metrics() must be nil when disabled")
	}
	h, err := app.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/metrics status %d, want 404", rec.Code)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestMetricsSeparateListener(t *testing.T) {
	cfg := metricsConfig(t)
	cfg.Server.Addr = freeAddr(t)
	cfg.Metrics.Addr = freeAddr(t)
	cfg.Metrics.Path = "/internal/metrics"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app := pdbq.New(cfg)
	t.Cleanup(app.Close)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- app.Serve(ctx) }()

	metricsURL := "http://" + cfg.Metrics.Addr + cfg.Metrics.Path
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(metricsURL)
		if err == nil {
			body := readAll(t, resp)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || !strings.Contains(body, "pdbq_db_pool_max_conns") {
				t.Fatalf("metrics listener: %d %.200s", resp.StatusCode, body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("metrics listener never came up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	resp, err := http.Get("http://" + cfg.Server.Addr + cfg.Metrics.Path)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("main listener serves %s: %d", cfg.Metrics.Path, resp.StatusCode)
	}
	cancel()
	select {
	case err := <-served:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
	if _, err := http.Get(metricsURL); err == nil {
		t.Fatal("metrics listener still up after Serve returned")
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
