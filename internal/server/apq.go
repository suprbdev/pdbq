package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/suprbdev/pdbq/internal/errcode"
)

// apqCacheSize bounds the client-registered APQ cache (FIFO eviction);
// file-loaded persisted queries are never evicted.
const apqCacheSize = 1024

// pqStore holds persisted queries: static entries preloaded from a file plus
// APQ-registered entries.
type pqStore struct {
	mu     sync.Mutex
	static map[string]string
	apq    map[string]string
	order  []string
}

// newPQStore loads the optional hash->document JSON file.
func newPQStore(path string) (*pqStore, error) {
	s := &pqStore{static: map[string]string{}, apq: map[string]string{}}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("server: persisted queries: %w", err)
		}
		if err := json.Unmarshal(b, &s.static); err != nil {
			return nil, fmt.Errorf("server: persisted queries: %s: %w", path, err)
		}
		for hash, query := range s.static {
			if hashQuery(query) != hash {
				return nil, fmt.Errorf("server: persisted queries: %s: hash %s does not match its document", path, hash)
			}
		}
	}
	return s, nil
}

func (s *pqStore) get(hash string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q, ok := s.static[hash]; ok {
		return q, true
	}
	q, ok := s.apq[hash]
	return q, ok
}

func (s *pqStore) put(hash, query string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.static[hash]; ok {
		return
	}
	if _, ok := s.apq[hash]; ok {
		return
	}
	if len(s.order) >= apqCacheSize {
		delete(s.apq, s.order[0])
		s.order = s.order[1:]
	}
	s.apq[hash] = query
	s.order = append(s.order, hash)
}

func hashQuery(q string) string {
	sum := sha256.Sum256([]byte(q))
	return hex.EncodeToString(sum[:])
}

type persistedQueryExt struct {
	Version    int    `json:"version"`
	Sha256Hash string `json:"sha256Hash"`
}

// resolvePersisted applies the Apollo APQ protocol to the request before
// execution. It returns true when it already wrote a response (protocol
// error, not-found, or persisted-only rejection); otherwise req.Query is
// ready to execute.
func (s *Server) resolvePersisted(w http.ResponseWriter, req *gqlHTTPRequest) (handled bool) {
	perr := s.applyPersisted(req)
	if perr == nil {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(perr.status)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": gqlerror.List{perr.err}})
	return true
}

// persistedError is a persisted-query protocol failure: the HTTP status it
// answers with and the GraphQL error the client sees. PersistedQueryNotFound
// and PersistedQueryNotSupported answer 200 with Apollo's well-known
// messages, so the client retries with the full query attached or without
// persisted queries.
type persistedError struct {
	status int
	err    *gqlerror.Error
}

// applyPersisted resolves req's persistedQuery extension into req.Query,
// registers APQ documents, and enforces persisted_queries.only. It is shared
// by HTTP and WebSocket requests. With persisted queries off it only
// answers a hash-only request, with PersistedQueryNotSupported.
func (s *Server) applyPersisted(req *gqlHTTPRequest) *persistedError {
	fail := func(status int, code errcode.Code, msg string) *persistedError {
		return &persistedError{status: status, err: errcode.Errorf(code, "%s", msg)}
	}
	var ext struct {
		PersistedQuery *persistedQueryExt `json:"persistedQuery"`
	}
	if len(req.Extensions) > 0 {
		_ = json.Unmarshal(req.Extensions, &ext)
	}
	pq := ext.PersistedQuery
	if s.pq == nil {
		if pq != nil && req.Query == "" {
			return fail(http.StatusOK, errcode.PersistedQueryNotSupported, "PersistedQueryNotSupported")
		}
		return nil
	}
	if pq == nil {
		if s.cfg.PersistedQueries.Only {
			return fail(http.StatusBadRequest, errcode.PersistedQueryRequired, "persisted queries only: send a persistedQuery extension")
		}
		return nil
	}
	if pq.Version != 0 && pq.Version != 1 {
		return fail(http.StatusBadRequest, errcode.InvalidRequest, "unsupported persistedQuery version")
	}
	if pq.Sha256Hash == "" {
		return fail(http.StatusBadRequest, errcode.InvalidRequest, "persistedQuery: missing sha256Hash")
	}
	if req.Query == "" {
		q, ok := s.pq.get(pq.Sha256Hash)
		if !ok {
			return fail(http.StatusOK, errcode.PersistedQueryNotFound, "PersistedQueryNotFound")
		}
		req.Query = q
		return nil
	}
	if hashQuery(req.Query) != pq.Sha256Hash {
		return fail(http.StatusBadRequest, errcode.InvalidRequest, "provided sha256Hash does not match query")
	}
	if !s.cfg.PersistedQueries.APQ {
		// Static allowlist deployments do not accept client registrations.
		return fail(http.StatusBadRequest, errcode.PersistedQueryNotSupported, "persisted query registration is disabled")
	}
	s.pq.put(pq.Sha256Hash, req.Query)
	return nil
}
