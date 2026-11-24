package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/metrics"
	"github.com/suprbdev/pdbq/internal/config"
)

func TestMetricsEndpointAndRouteClasses(t *testing.T) {
	m, err := metrics.New(metrics.Options{Namespace: "pdbq"})
	if err != nil {
		t.Fatal(err)
	}
	s := testServer(config.Config{Server: config.Server{GraphiQL: true, Introspection: true}, Limits: config.Limits{MaxBodyBytes: 1 << 20}})
	s.SetMetrics(m, "/metrics")
	h := s.Handler()
	for _, p := range []string{"/healthz", "/readyz", "/", "/schema.graphql", "/nope"} {
		get(t, h, p)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader("{"))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status %d", rec.Code)
	}
	rec = get(t, h, "/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`pdbq_http_requests_total{method="GET",route="health",status_class="2xx"} 2`,
		`pdbq_http_requests_total{method="GET",route="graphiql",status_class="2xx"} 1`,
		`pdbq_http_requests_total{method="GET",route="schema",status_class="2xx"} 1`,
		`pdbq_http_requests_total{method="GET",route="other",status_class="4xx"} 1`,
		`pdbq_http_requests_total{method="POST",route="graphql",status_class="4xx"} 1`,
		`pdbq_http_requests_in_flight 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("metrics lack %q", want)
		}
	}
}

func TestMetricsPathNotServedWithoutPath(t *testing.T) {
	m, err := metrics.New(metrics.Options{Namespace: "pdbq"})
	if err != nil {
		t.Fatal(err)
	}
	s := testServer(config.Config{})
	s.SetMetrics(m, "")
	if rec := get(t, s.Handler(), "/metrics"); rec.Code != http.StatusNotFound {
		t.Fatalf("/metrics status %d, want 404 when served elsewhere", rec.Code)
	}
	if got := s.RouteClass(httptest.NewRequest(http.MethodGet, "/metrics", nil)); got != "other" {
		t.Fatalf("route class %q", got)
	}
}

func TestNoMetricsByDefault(t *testing.T) {
	if rec := get(t, testServer(config.Config{}).Handler(), "/metrics"); rec.Code != http.StatusNotFound {
		t.Fatalf("/metrics status %d, want 404", rec.Code)
	}
}
