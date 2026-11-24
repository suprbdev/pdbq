package server

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/suprbdev/pdbq/internal/errcode"
	"github.com/suprbdev/pdbq/internal/exec"
)

// Defaults of persisted_queries.max_bytes and max_entry_bytes, used when the
// configured value is not positive.
const (
	defaultAPQBytes      = 8 << 20
	defaultAPQEntryBytes = 64 << 10
)

// apqEntryOverhead approximates the bookkeeping one APQ registration holds
// besides its hash and query text (map slot, list element, entry).
const apqEntryOverhead = 128

// pqStore holds persisted queries: static entries preloaded from a file plus
// APQ-registered entries. The registered entries are a least recently used
// cache of at most maxBytes (see apqEntryOverhead); a document heavier than
// maxEntry is never registered. Static entries are never evicted and do not
// count.
type pqStore struct {
	mu       sync.Mutex
	static   map[string]string
	apq      map[string]*list.Element
	order    list.List
	bytes    int
	maxBytes int
	maxEntry int
}

type apqEntry struct {
	hash, query string
	size        int
}

// newPQStore loads the optional hash->document JSON file; APQ registrations
// are bounded by maxBytes and maxEntry (defaultAPQBytes and
// defaultAPQEntryBytes when not positive).
func newPQStore(path string, maxBytes, maxEntry int) (*pqStore, error) {
	if maxBytes <= 0 {
		maxBytes = defaultAPQBytes
	}
	if maxEntry <= 0 {
		maxEntry = defaultAPQEntryBytes
	}
	s := &pqStore{static: map[string]string{}, apq: map[string]*list.Element{}, maxBytes: maxBytes, maxEntry: min(maxEntry, maxBytes)}
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
	el, ok := s.apq[hash]
	if !ok {
		return "", false
	}
	s.order.MoveToFront(el)
	return el.Value.(*apqEntry).query, true
}

// put registers query under hash, evicting the least recently used
// registrations beyond the byte budget. A document heavier than the entry
// bound is not registered.
func (s *pqStore) put(hash, query string) {
	size := len(hash) + len(query) + apqEntryOverhead
	if size > s.maxEntry {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.static[hash]; ok {
		return
	}
	if _, ok := s.apq[hash]; ok {
		return
	}
	s.apq[hash] = s.order.PushFront(&apqEntry{hash: strings.Clone(hash), query: strings.Clone(query), size: size})
	s.bytes += size
	for s.bytes > s.maxBytes {
		oldest := s.order.Back()
		e := oldest.Value.(*apqEntry)
		s.order.Remove(oldest)
		delete(s.apq, e.hash)
		s.bytes -= e.size
	}
}

// size returns the bytes the registered entries hold and their number.
func (s *pqStore) size() (bytes, entries int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes, len(s.apq)
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

// applyPersisted resolves req's persistedQuery extension into req.Query and
// enforces persisted_queries.only. A query sent with its hash for APQ
// registration is only marked (req.apqHash): the caller registers it with
// registerPersisted once the request authenticated and the document passed
// the document limits and validation. It is shared by HTTP and WebSocket
// requests. With persisted queries off it only answers a hash-only request,
// with PersistedQueryNotSupported.
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
	req.apqHash = pq.Sha256Hash
	return nil
}

// registerPersisted registers the document applyPersisted marked for APQ
// registration, unless ex rejects it (parse, document limits, validation).
// Call it only after the request authenticated.
func (s *Server) registerPersisted(ex *exec.Executor, req *gqlHTTPRequest) {
	if req.apqHash == "" || ex.CheckDocument(req.Query) != nil {
		return
	}
	s.pq.put(req.apqHash, req.Query)
}
