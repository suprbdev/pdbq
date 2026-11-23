// Package server exposes the GraphQL API over HTTP: POST /graphql, an
// embedded GraphiQL page, and health endpoints. The schema is swappable at
// runtime (watch mode): requests in flight keep the schema they started with.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/suprbdev/pdbcore/auth"
	"github.com/suprbdev/pdbcore/confx"
	"github.com/suprbdev/pdbcore/httpx"
	"github.com/suprbdev/pdbcore/metrics"
	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/live"
	"github.com/suprbdev/pdbq/internal/subscribe"
	"github.com/vektah/gqlparser/v2/ast"
)

// Authenticator turns an HTTP request into (claims, role); see pdbcore/auth.
type Authenticator = auth.Authenticator

// NewAuthenticator builds the request authenticator from the rls section;
// log (may be nil) receives JWKS fetch failures.
func NewAuthenticator(cfg config.RLS, log *slog.Logger) *Authenticator {
	ac := confx.AuthConfig(cfg)
	ac.Logger = log
	return auth.New(ac)
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

	metrics     *metrics.Metrics
	metricsPath string
	ping        func(context.Context) error

	// hub and topics serve Subscription.listen (server.subscriptions).
	hub    *subscribe.Hub
	topics *subscribe.TopicPolicy
	// live serves live queries (server.live).
	live *live.Tracker

	wsInitTimeout time.Duration
	wsMu          sync.Mutex
	wsSessions    map[*wsSession]struct{}
	wsClosed      bool
	// ws counts WebSocket connections and operations against the limits;
	// a Group shares one between its servers.
	ws *wsLimits
	// incremental counts the incremental responses holding a transaction
	// against server.incremental_max_responses; a Group shares one between
	// its servers.
	incremental *slots

	// prefix is the path the server is mounted under by a Group ("" at
	// the root); the GraphiQL page posts to prefix + /graphql.
	prefix string
}

// wsLimits counts WebSocket connections, accepted or being accepted, their
// running operations and, among those, their live queries.
type wsLimits struct {
	mu    sync.Mutex
	conns int
	ops   atomic.Int64
	live  atomic.Int64
}

func New(cfg config.Server, authn *Authenticator, ex *exec.Executor, sdl string, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{cfg: cfg, auth: authn, log: log, wsInitTimeout: wsInitTimeout, wsSessions: map[*wsSession]struct{}{}, ws: &wsLimits{}, incremental: &slots{}}
	if cfg.Subscriptions {
		topics, err := subscribe.NewTopicPolicy(cfg.SubscriptionsTopicPrefix, cfg.SubscriptionsTopics)
		if err != nil {
			return nil, fmt.Errorf("server: %w", err)
		}
		s.topics = topics
	}
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

// SetMetrics instruments the handler with m (request counts, durations and
// in-flight requests by route class) and, when path is non-empty, serves
// m's metrics at path. Call it before Handler; a nil m leaves the handler
// uninstrumented.
func (s *Server) SetMetrics(m *metrics.Metrics, path string) {
	s.metrics, s.metricsPath = m, path
}

// SetSubscriptions makes the server answer Subscription.listen operations
// with notifications from hub. It has an effect only with
// server.subscriptions on; call it before Handler. The caller owns hub and
// closes it after the server.
func (s *Server) SetSubscriptions(hub *subscribe.Hub) { s.hub = hub }

// SetReadiness makes GET /readyz call ping with a server.readyz_timeout
// deadline and answer 503 when it fails; nil (the default) leaves /readyz a
// liveness alias. Call it before Handler.
func (s *Server) SetReadiness(ping func(context.Context) error) { s.ping = ping }

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.ping != nil {
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ReadyzTimeout)
		defer cancel()
		if err := s.ping(ctx); err != nil {
			s.log.Warn("readyz: database ping failed", "err", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"errors":[{"message":"database unavailable","extensions":{"code":"unavailable"}}]}`)) //nolint:errcheck
			return
		}
	}
	httpx.Healthz(w, r)
}

// RouteClass is the route label of r in the HTTP metrics: graphql, health,
// metrics, schema, graphiql or other.
func (s *Server) RouteClass(r *http.Request) string {
	switch p := r.URL.Path; {
	case p == "/graphql":
		return "graphql"
	case p == "/healthz" || p == "/readyz":
		return "health"
	case s.metricsPath != "" && p == s.metricsPath:
		return "metrics"
	case p == "/schema.graphql" && s.cfg.ExposeSchema:
		return "schema"
	case p == "/" && s.cfg.GraphiQL:
		return "graphiql"
	}
	return metrics.DefaultRoute
}

// Swap atomically replaces the executor (and SDL) — watch mode.
func (s *Server) Swap(ex *exec.Executor, sdl string) {
	s.executor.Store(ex)
	s.sdl.Store(&sdl)
}

// Handler returns the HTTP handler. With server.subscriptions on,
// WebSocket upgrades of /graphql are served before the HTTP middlewares
// (request timeout, CORS headers, gzip, metrics); their Origin must be the
// server's own host or match server.cors_origins. With
// server.incremental_delivery on, GraphQL requests that accept
// multipart/mixed skip the request timeout handler and gzip, which would
// buffer their payloads; serveIncremental bounds them by
// server.request_timeout itself.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /graphql", s.handleGraphQL)
	mux.HandleFunc("GET /graphql", s.handleGraphQL)
	if len(s.cfg.CORSOrigins) > 0 {
		mux.HandleFunc("OPTIONS /graphql", httpx.Preflight(s.cfg.CORSOrigins, "POST, GET, OPTIONS"))
	}
	mux.HandleFunc("GET /healthz", httpx.Healthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	if s.cfg.ExposeSchema {
		mux.HandleFunc("GET /schema.graphql", s.handleSDL)
	}
	if s.cfg.GraphiQL {
		mux.HandleFunc("GET /", s.handleGraphiQL)
	}
	if s.metrics != nil && s.metricsPath != "" {
		mux.Handle("GET "+s.metricsPath, s.metrics.Handler())
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
	if s.cfg.IncrementalDelivery {
		buffered, streaming := h, http.Handler(mux)
		if len(s.cfg.CORSOrigins) > 0 {
			streaming = httpx.CORS(s.cfg.CORSOrigins, streaming)
		}
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.streamsIncremental(r) {
				streaming.ServeHTTP(w, r)
				return
			}
			buffered.ServeHTTP(w, r)
		})
	}
	h = s.metrics.Middleware(h, s.RouteClass)
	if !s.cfg.Subscriptions {
		return h
	}
	next := h
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" && isWebSocketUpgrade(r) {
			s.handleWebSocket(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ListenAndServe runs the server until ctx is cancelled, then closes its
// WebSocket connections.
func (s *Server) ListenAndServe(ctx context.Context) error {
	stop := context.AfterFunc(ctx, s.CloseWebSockets)
	defer stop()
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

// partialMutations reports the request's extensions.partialMutations opt-in;
// anything but the JSON value true is off.
func (r *gqlHTTPRequest) partialMutations() bool {
	if len(r.Extensions) == 0 {
		return false
	}
	var ext struct {
		PartialMutations bool `json:"partialMutations"`
	}
	return json.Unmarshal(r.Extensions, &ext) == nil && ext.PartialMutations
}

// handleGraphQL serves GraphQL over HTTP. GET (and HEAD) runs query
// operations only and answers 405 for a mutation or subscription; POST
// requires an application/json body and answers 415 for any other content
// type. Both rules keep cross-site pages from running mutations with the
// browser's ambient credentials: neither a GET nor a POST of a
// CORS-safelisted content type is preflighted.
func (s *Server) handleGraphQL(w http.ResponseWriter, r *http.Request) {
	var req gqlHTTPRequest
	switch r.Method {
	case http.MethodGet, http.MethodHead:
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
		if !isJSONContentType(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, "unsupported content type: POST requests must send Content-Type: application/json")
			return
		}
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
	ex := s.executor.Load()
	if r.Method != http.MethodPost {
		if typ, err := ex.OperationType(req.Query, req.OperationName); err == nil && typ != ast.Query {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, fmt.Sprintf("%s operations are not allowed over GET; use POST", typ))
			return
		}
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

	execReq := exec.Request{
		Query:            req.Query,
		OperationName:    req.OperationName,
		Variables:        req.Variables,
		Claims:           claims,
		Role:             role,
		PartialMutations: req.partialMutations(),
	}
	if s.streamsIncremental(r) {
		s.serveIncremental(w, r, ex, execReq)
		return
	}
	s.writeResult(w, ex.Execute(r.Context(), execReq))
}

// isJSONContentType reports whether the Content-Type header value v is
// application/json, with or without parameters.
func isJSONContentType(v string) bool {
	mt, _, err := mime.ParseMediaType(v)
	return err == nil && mt == "application/json"
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
	b = append(b, '\n')
	*bp = b
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
	page := graphiqlHTML
	if s.prefix != "" {
		page = strings.Replace(page, "{url: '/graphql'}", "{url: '"+s.prefix+"/graphql'}", 1)
	}
	w.Write([]byte(page)) //nolint:errcheck
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
