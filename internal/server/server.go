// Package server exposes the GraphQL API over HTTP: POST /graphql, an
// embedded GraphiQL page, and health endpoints. The schema is swappable at
// runtime (watch mode): requests in flight keep the schema they started with.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/suprbdev/pdbcore/auth"
	"github.com/suprbdev/pdbcore/confx"
	"github.com/suprbdev/pdbcore/httpx"
	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
)

// Authenticator turns an HTTP request into (claims, role); see pdbcore/auth.
type Authenticator = auth.Authenticator

// NewAuthenticator builds the request authenticator from the rls section.
func NewAuthenticator(cfg config.RLS) *Authenticator {
	return auth.New(confx.AuthConfig(cfg))
}

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

func New(cfg config.Server, authn *Authenticator, ex *exec.Executor, sdl string, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, auth: authn, log: log}
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
		mux.HandleFunc("OPTIONS /graphql", httpx.Preflight(s.cfg.CORSOrigins, "POST, GET, OPTIONS"))
	}
	mux.HandleFunc("GET /healthz", httpx.Healthz)
	mux.HandleFunc("GET /readyz", httpx.Healthz)
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
		h = httpx.CORS(s.cfg.CORSOrigins, h)
	}
	if s.cfg.Compression {
		h = httpx.Gzip(h)
	}
	return h
}

// ListenAndServe runs the server until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	return httpx.ListenAndServe(ctx, s.Handler(), httpx.ServeOptions{
		Addr:           s.cfg.Addr,
		RequestTimeout: s.cfg.RequestTimeout,
		Log:            s.log,
		LogAttrs:       []any{"graphiql", s.cfg.GraphiQL},
	})
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
	if role != "" && !pgexec.ValidRole(role) {
		writeError(w, http.StatusUnauthorized, fmt.Sprintf("role %q is not allowed", role))
		return
	}

	res := s.executor.Load().Execute(r.Context(), exec.Request{
		Query:         req.Query,
		OperationName: req.OperationName,
		Variables:     req.Variables,
		Claims:        claims,
		Role:          role,
	})
	s.writeResult(w, res)
}

// maxPooledResponse caps the response buffers kept for reuse, so one large
// response does not pin its buffer for the life of the pool.
const maxPooledResponse = 1 << 20

var responseBuffers = sync.Pool{New: func() any { return new([]byte) }}

// writeResult writes res as the JSON response body. Root field values are
// copied through as PostgreSQL produced them, without re-encoding.
func (s *Server) writeResult(w http.ResponseWriter, res *exec.Result) {
	bp := responseBuffers.Get().(*[]byte)
	defer func() {
		if cap(*bp) <= maxPooledResponse {
			responseBuffers.Put(bp)
		}
	}()
	b, err := res.AppendJSON((*bp)[:0])
	if err != nil {
		s.log.Error("write response", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	*bp = append(b, '\n')
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(*bp); err != nil {
		s.log.Error("write response", "err", err)
	}
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

// graphiqlHTML pins exact asset versions with Subresource Integrity hashes so
// a compromised CDN (or MITM) cannot inject script. Bumping a version means
// recomputing its sha384: curl -sL <url> | openssl dgst -sha384 -binary | openssl base64 -A
const graphiqlHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <title>pdbq GraphiQL</title>
  <style>body{margin:0;height:100vh}#graphiql{height:100vh}</style>
  <link rel="stylesheet" href="https://unpkg.com/graphiql@3.9.0/graphiql.min.css"
    integrity="sha384-QMux00XgRtwRLSYIY3kw2rj1ovk5AuuliAchk+HSQbqdbGFnz9GYuqIlOqxhwCE2" crossorigin="anonymous"/>
</head>
<body>
  <div id="graphiql">Loading GraphiQL…</div>
  <script src="https://unpkg.com/react@18.3.1/umd/react.production.min.js"
    integrity="sha384-DGyLxAyjq0f9SPpVevD6IgztCFlnMF6oW/XQGmfe+IsZ8TqEiDrcHkMLKI6fiB/Z" crossorigin="anonymous"></script>
  <script src="https://unpkg.com/react-dom@18.3.1/umd/react-dom.production.min.js"
    integrity="sha384-gTGxhz21lVGYNMcdJOyq01Edg0jhn/c22nsx0kyqP0TxaV5WVdsSH1fSDUf5YJj1" crossorigin="anonymous"></script>
  <script src="https://unpkg.com/graphiql@3.9.0/graphiql.min.js"
    integrity="sha384-8NGfVj4CVlqHajlZj+bPJT4thxPMHMJYn7DWK2CvtopLd02E7qsPHazniBYvVjOO" crossorigin="anonymous"></script>
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
