// Package server exposes the GraphQL API over HTTP: POST /graphql, an
// embedded GraphiQL page, and health endpoints. The schema is swappable at
// runtime (watch mode): requests in flight keep the schema they started with.
package server

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
)

// Server is the HTTP front end. Executor is stored atomically so watch mode
// can swap in a new schema without a restart.
type Server struct {
	cfg      config.Server
	executor atomic.Pointer[exec.Executor]
	auth     *Authenticator
	log      *slog.Logger
	sdl      atomic.Pointer[string]
	// pq holds persisted queries (nil when APQ and the allowlist are off).
	pq *pqStore
}

func New(cfg config.Server, auth *Authenticator, ex *exec.Executor, sdl string, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, auth: auth, log: log}
	s.executor.Store(ex)
	s.sdl.Store(&sdl)
	if cfg.APQ || cfg.PersistedQueriesPath != "" {
		pq, err := newPQStore(cfg.PersistedQueriesPath)
		if err != nil {
			return nil, err
		}
		s.pq = pq
	}
	return s, nil
}

// Swap atomically replaces the executor (and SDL) — watch mode.
func (s *Server) Swap(ex *exec.Executor, sdl string) {
	s.executor.Store(ex)
	s.sdl.Store(&sdl)
}

// Handler returns the HTTP mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /graphql", s.handleGraphQL)
	mux.HandleFunc("GET /graphql", s.handleGraphQL)
	if len(s.cfg.CORSOrigins) > 0 {
		mux.HandleFunc("OPTIONS /graphql", s.handleCORSPreflight)
	}
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleHealthz)
	if s.cfg.ExposeSchema {
		mux.HandleFunc("GET /schema.graphql", s.handleSDL)
	}
	if s.cfg.GraphiQL {
		mux.HandleFunc("GET /", s.handleGraphiQL)
	}
	var h http.Handler = mux
	if s.cfg.RequestTimeout > 0 {
		h = http.TimeoutHandler(h, s.cfg.RequestTimeout, `{"errors":[{"message":"request timeout"}]}`)
	}
	if len(s.cfg.CORSOrigins) > 0 {
		h = s.withCORS(h)
	}
	if s.cfg.Compression {
		h = withGzip(h)
	}
	return h
}

var gzipPool = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}

type gzipResponseWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (w *gzipResponseWriter) Write(p []byte) (int, error) { return w.gz.Write(p) }

// withGzip compresses responses for clients that accept gzip. OPTIONS
// (CORS preflight) is skipped: those responses have no body, and wrapping
// them would emit an empty gzip frame against a 204.
func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		gz := gzipPool.Get().(*gzip.Writer)
		gz.Reset(w)
		defer func() {
			gz.Close() //nolint:errcheck
			gzipPool.Put(gz)
		}()
		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, gz: gz}, r)
	})
}

// acceptsGzip reports whether an Accept-Encoding header value asks for gzip
// (directly or via "*"), honoring q=0 exclusions.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		token, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if tok := strings.TrimSpace(token); tok != "gzip" && tok != "*" {
			continue
		}
		if q, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if v, err := strconv.ParseFloat(strings.TrimSpace(q), 64); err == nil && v == 0 {
				continue
			}
		}
		return true
	}
	return false
}

// withCORS sets Access-Control-Allow-Origin on responses whose Origin header
// matches an allowed origin (exact match, or "*" to allow any). Preflight
// (OPTIONS) requests are answered directly by handleCORSPreflight.
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && s.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(origin string) bool {
	for _, o := range s.cfg.CORSOrigins {
		if o == "*" || o == origin {
			return true
		}
	}
	return false
}

func (s *Server) handleCORSPreflight(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" || !s.originAllowed(origin) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Vary", "Origin")
	h.Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
		h.Set("Access-Control-Allow-Headers", reqHeaders)
	}
	h.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

// ListenAndServe runs the server until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	// ReadTimeout must outlast the handler: the request body is read inside
	// the handler, so it needs headroom beyond RequestTimeout. IdleTimeout
	// reaps kept-alive connections; both close the slowloris window that
	// ReadHeaderTimeout alone leaves open on the body.
	readTimeout := 60 * time.Second
	if s.cfg.RequestTimeout > 0 {
		readTimeout = s.cfg.RequestTimeout + 10*time.Second
	}
	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTimeout,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	s.log.Info("listening", "addr", s.cfg.Addr, "graphiql", s.cfg.GraphiQL)
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

type gqlHTTPRequest struct {
	Query         string          `json:"query"`
	OperationName string          `json:"operationName"`
	Variables     map[string]any  `json:"variables"`
	Extensions    json.RawMessage `json:"extensions"`
}

func (s *Server) handleGraphQL(w http.ResponseWriter, r *http.Request) {
	var req gqlHTTPRequest
	switch r.Method {
	case http.MethodGet:
		req.Query = r.URL.Query().Get("query")
		req.OperationName = r.URL.Query().Get("operationName")
		if vars := r.URL.Query().Get("variables"); vars != "" {
			if err := json.Unmarshal([]byte(vars), &req.Variables); err != nil {
				writeError(w, http.StatusBadRequest, "malformed variables")
				return
			}
		}
		if ext := r.URL.Query().Get("extensions"); ext != "" {
			req.Extensions = json.RawMessage(ext)
		}
	default:
		body := http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}
	}
	if s.pq != nil {
		if handled := s.resolvePersisted(w, &req); handled {
			return
		}
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "missing query")
		return
	}

	claims, role, err := s.auth.Authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	res := s.executor.Load().Execute(r.Context(), exec.Request{
		Query:         req.Query,
		OperationName: req.OperationName,
		Variables:     req.Variables,
		Claims:        claims,
		Role:          role,
	})
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(res); err != nil {
		s.log.Error("write response", "err", err)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`)) //nolint:errcheck
}

func (s *Server) handleSDL(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(*s.sdl.Load())) //nolint:errcheck
}

func (s *Server) handleGraphiQL(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(graphiqlHTML)) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"errors": []map[string]string{{"message": msg}},
	})
}

const graphiqlHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <title>pdbq GraphiQL</title>
  <style>body{margin:0;height:100vh}#graphiql{height:100vh}</style>
  <link rel="stylesheet" href="https://unpkg.com/graphiql@3/graphiql.min.css"/>
</head>
<body>
  <div id="graphiql">Loading GraphiQL…</div>
  <script crossorigin src="https://unpkg.com/react@18/umd/react.production.min.js"></script>
  <script crossorigin src="https://unpkg.com/react-dom@18/umd/react-dom.production.min.js"></script>
  <script crossorigin src="https://unpkg.com/graphiql@3/graphiql.min.js"></script>
  <script>
    ReactDOM.createRoot(document.getElementById('graphiql')).render(
      React.createElement(GraphiQL, {
        fetcher: GraphiQL.createFetcher({url: '/graphql'}),
        defaultEditorToolsVisibility: true,
      })
    );
  </script>
</body>
</html>`
