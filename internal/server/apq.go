package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
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
	var ext struct {
		PersistedQuery *persistedQueryExt `json:"persistedQuery"`
	}
	if len(req.Extensions) > 0 {
		_ = json.Unmarshal(req.Extensions, &ext)
	}
	pq := ext.PersistedQuery
	if pq == nil {
		if s.cfg.PersistedOnly {
			writeError(w, http.StatusBadRequest, "persisted queries only: send a persistedQuery extension")
			return true
		}
		return false
	}
	if pq.Version != 0 && pq.Version != 1 {
		writeError(w, http.StatusBadRequest, "unsupported persistedQuery version")
		return true
	}
	if pq.Sha256Hash == "" {
		writeError(w, http.StatusBadRequest, "persistedQuery: missing sha256Hash")
		return true
	}
	if req.Query == "" {
		q, ok := s.pq.get(pq.Sha256Hash)
		if !ok {
			// APQ protocol shape: 200 response carrying the well-known
			// message so the client retries with the full query attached.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errors": []map[string]any{{
					"message":    "PersistedQueryNotFound",
					"extensions": map[string]string{"code": "PERSISTED_QUERY_NOT_FOUND"},
				}},
			})
			return true
		}
		req.Query = q
		return false
	}
	if hashQuery(req.Query) != pq.Sha256Hash {
		writeError(w, http.StatusBadRequest, "provided sha256Hash does not match query")
		return true
	}
	if !s.cfg.APQ {
		// Static allowlist deployments do not accept client registrations.
		writeError(w, http.StatusBadRequest, "persisted query registration is disabled")
		return true
	}
	s.pq.put(pq.Sha256Hash, req.Query)
	return false
}
