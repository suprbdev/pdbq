package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/suprbdev/pdbcore/httpx"
	"github.com/suprbdev/pdbcore/metrics"
	"github.com/suprbdev/pdbq/internal/config"
)

// Group serves several Servers, one per schema contract, each mounted
// under its own path prefix: <prefix>/graphql (with its WebSocket
// upgrade), <prefix>/schema.graphql and the GraphiQL page at <prefix>/
// when enabled. A Server mounted at "/" serves the usual root paths.
// /healthz, /readyz and the metrics endpoint are served at the root, and
// every other path is a 404. The WebSocket connection and operation limits
// count over all mounted servers.
type Group struct {
	cfg    config.Server
	log    *slog.Logger
	mounts []groupMount
	ws     *wsLimits

	ping        func(context.Context) error
	metrics     *metrics.Metrics
	metricsPath string
}

type groupMount struct {
	prefix string
	srv    *Server
}

// NewGroup returns an empty Group; cfg supplies the listen address,
// request timeout and readyz timeout.
func NewGroup(cfg config.Server, log *slog.Logger) *Group {
	if log == nil {
		log = slog.Default()
	}
	return &Group{cfg: cfg, log: log, ws: &wsLimits{}}
}

// Add mounts s under prefix ("/" or a path without a trailing slash).
// Call it before Handler.
func (g *Group) Add(prefix string, s *Server) {
	prefix = strings.TrimSuffix(prefix, "/")
	s.prefix = prefix
	s.ws = g.ws
	g.mounts = append(g.mounts, groupMount{prefix: prefix, srv: s})
}

// SetReadiness makes GET /readyz call ping, as [Server.SetReadiness].
func (g *Group) SetReadiness(ping func(context.Context) error) { g.ping = ping }

// SetMetrics serves m's metrics at path (empty: not at all). The mounted
// servers instrument their own requests (see [Server.SetMetrics]); health
// and metrics requests at the root are not counted.
func (g *Group) SetMetrics(m *metrics.Metrics, path string) {
	g.metrics, g.metricsPath = m, path
}

// Handler returns the HTTP handler.
func (g *Group) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", httpx.Healthz)
	ready := &Server{cfg: g.cfg, log: g.log, ping: g.ping}
	mux.HandleFunc("GET /readyz", ready.handleReadyz)
	if g.metrics != nil && g.metricsPath != "" {
		mux.Handle("GET "+g.metricsPath, g.metrics.Handler())
	}
	root := false
	for _, m := range g.mounts {
		if m.prefix == "" {
			root = true
			mux.Handle("/", m.srv.Handler())
			continue
		}
		mux.Handle(m.prefix+"/", http.StripPrefix(m.prefix, m.srv.Handler()))
	}
	if !root {
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "not found")
		})
	}
	return mux
}

// ListenAndServe runs the group until ctx is cancelled, then closes the
// WebSocket connections of every mounted server.
func (g *Group) ListenAndServe(ctx context.Context) error {
	stop := context.AfterFunc(ctx, g.CloseWebSockets)
	defer stop()
	paths := make([]string, len(g.mounts))
	for i, m := range g.mounts {
		paths[i] = m.prefix + "/graphql"
	}
	return httpx.ListenAndServe(ctx, g.Handler(), httpx.ServeOptions{
		Addr:           g.cfg.Addr,
		RequestTimeout: g.cfg.RequestTimeout,
		Log:            g.log,
		LogAttrs:       []any{"graphiql", g.cfg.GraphiQL, "contracts", paths},
	})
}

// CloseWebSockets closes the WebSocket connections of every mounted
// server.
func (g *Group) CloseWebSockets() {
	var wg sync.WaitGroup
	for _, m := range g.mounts {
		wg.Go(m.srv.CloseWebSockets)
	}
	wg.Wait()
}
