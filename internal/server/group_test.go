package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/metrics"
	"github.com/suprbdev/pdbq/internal/config"
)

func groupServer(t *testing.T, cfg config.Config, sdl string) *Server {
	t.Helper()
	s, err := New(cfg, testAuth(), nil, sdl, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGroupRoutesByPrefix(t *testing.T) {
	cfg := config.Config{Server: config.Server{Introspection: true, GraphiQL: true, ReadyzTimeout: 1}}
	g := NewGroup(cfg, nil)
	internal := groupServer(t, cfg, "type Query { internal: Boolean }")
	public := groupServer(t, cfg, "type Query { public: Boolean }")
	partner := groupServer(t, cfg, "type Query { partner: Boolean }")
	g.Add("/", internal)
	g.Add("/public", public)
	g.Add("/partners/v2", partner)
	h := g.Handler()

	for path, want := range map[string]string{
		"/schema.graphql":             "internal",
		"/public/schema.graphql":      "public",
		"/partners/v2/schema.graphql": "partner",
	} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s = %d %q, want the %s SDL", path, rec.Code, rec.Body, want)
		}
	}
	rec := get(t, h, "/public/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "{url: '/public/graphql'}") {
		t.Errorf("GraphiQL under /public: %d, fetcher URL missing", rec.Code)
	}
	if rec := get(t, h, "/"); !strings.Contains(rec.Body.String(), "{url: '/graphql'}") {
		t.Error("root GraphiQL must post to /graphql")
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		if rec := get(t, h, path); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", path, rec.Code)
		}
	}
	if rec := get(t, h, "/partners/schema.graphql"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /partners/schema.graphql = %d, want 404", rec.Code)
	}
	if internal.ws != public.ws || public.ws != partner.ws || internal.ws != g.ws {
		t.Error("mounted servers must share the WebSocket limits")
	}
}

func TestGroupWithoutRootMount(t *testing.T) {
	cfg := config.Config{Server: config.Server{Introspection: true, ReadyzTimeout: 1}}
	g := NewGroup(cfg, nil)
	g.Add("/public", groupServer(t, cfg, "type Query { public: Boolean }"))
	g.SetReadiness(func(context.Context) error { return errors.New("down") })
	h := g.Handler()
	for _, path := range []string{"/schema.graphql", "/graphql", "/nope"} {
		if rec := get(t, h, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 without a contract at /", path, rec.Code)
		}
	}
	if rec := get(t, h, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz = %d, want 503 when the ping fails", rec.Code)
	}
}

func TestGroupMetrics(t *testing.T) {
	reg, err := metrics.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	newM := func(name string) *metrics.Metrics {
		m, err := metrics.New(metrics.Options{Namespace: "pdbq", Registerer: reg, ConstLabels: map[string]string{"contract": name}})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	cfg := config.Config{Server: config.Server{Introspection: true, ReadyzTimeout: 1}}
	g := NewGroup(cfg, nil)
	public, partner := groupServer(t, cfg, "type Query { a: Boolean }"), groupServer(t, cfg, "type Query { b: Boolean }")
	public.SetMetrics(newM("public"), "")
	partnerM := newM("partner")
	partner.SetMetrics(partnerM, "")
	g.Add("/public", public)
	g.Add("/partner", partner)
	g.SetMetrics(partnerM, "/metrics")
	h := g.Handler()
	get(t, h, "/public/schema.graphql")
	get(t, h, "/partner/schema.graphql")
	get(t, h, "/partner/schema.graphql")
	body := get(t, h, "/metrics").Body.String()
	for _, want := range []string{
		`pdbq_http_requests_total{contract="public",method="GET",route="schema",status_class="2xx"} 1`,
		`pdbq_http_requests_total{contract="partner",method="GET",route="schema",status_class="2xx"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
}
