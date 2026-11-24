package exec

import (
	"container/list"
	"errors"
	"strings"
	"sync"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/suprbdev/pdbq/internal/errcode"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"
	"github.com/vektah/gqlparser/v2/validator/core"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

// Defaults of the document cache budget, used when the corresponding Options
// field is 0.
const (
	DefaultDocumentCacheBytes         = 64 << 20
	DefaultDocumentCacheMaxEntryBytes = 1 << 20
)

var defaultRules = rules.NewDefaultRules()

// hiddenSchemaRules is the default rule set with the variants of the rules
// that do not append "Did you mean" suggestions, which name schema
// elements close to an unknown one. Executors use it when introspection is
// disabled.
var hiddenSchemaRules = newHiddenSchemaRules()

func newHiddenSchemaRules() *rules.Rules {
	r := rules.NewDefaultRules()
	for _, rule := range []core.Rule{
		rules.FieldsOnCorrectTypeRuleWithoutSuggestions,
		rules.KnownArgumentNamesRuleWithoutSuggestions,
		rules.KnownTypeNamesRuleWithoutSuggestions,
		rules.ScalarLeafsRuleWithoutSuggestions,
		rules.ValuesOfCorrectTypeRuleWithoutSuggestions,
	} {
		r.ReplaceRule(strings.TrimSuffix(rule.Name, "WithoutSuggestions"), rule.RuleFunc)
	}
	return r
}

// docCache is a concurrency-safe LRU of parsed and validated query
// documents, keyed by query text. Entries hold either the document or the
// parse/validation error it produced against schema. Cached documents are
// shared between requests and must never be mutated.
//
// Entries are weighed by the memory they retain (see estimateDocumentBytes),
// not by their text: the cache holds at most maxBytes of them, and an entry
// heavier than maxEntry is not cached at all.
type docCache struct {
	schema   *ast.Schema
	mu       sync.Mutex
	maxItems int
	maxBytes int
	maxEntry int
	bytes    int
	order    list.List
	items    map[string]*list.Element
}

type docEntry struct {
	query string
	doc   *ast.QueryDocument
	err   error
	size  int
}

func newDocCache(schema *ast.Schema, maxItems, maxBytes, maxEntry int) *docCache {
	if maxItems <= 0 {
		return nil
	}
	return &docCache{schema: schema, maxItems: maxItems, maxBytes: maxBytes, maxEntry: min(maxEntry, maxBytes), items: make(map[string]*list.Element)}
}

func (c *docCache) get(query string) (*docEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[query]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*docEntry), true
}

// put caches the outcome of parsing and validating query; size is its
// estimated retained memory (estimateDocumentBytes).
func (c *docCache) put(query string, doc *ast.QueryDocument, err error, size int) {
	if size > c.maxEntry {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[query]; ok {
		return
	}
	query = strings.Clone(query)
	c.items[query] = c.order.PushFront(&docEntry{query: query, doc: doc, err: err, size: size})
	c.bytes += size
	for len(c.items) > c.maxItems || c.bytes > c.maxBytes {
		oldest := c.order.Back()
		e := oldest.Value.(*docEntry)
		c.order.Remove(oldest)
		delete(c.items, e.query)
		c.bytes -= e.size
	}
}

// size returns the estimated memory the cached entries retain.
func (c *docCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

func (c *docCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// parseAndValidate parses query with a limit of maxTokens tokens, checks it
// against lim (see scanDocument) and validates it against schema with rs,
// without caching. size is the estimated memory the outcome retains.
func parseAndValidate(schema *ast.Schema, rs *rules.Rules, query string, maxTokens int, lim docLimits) (doc *ast.QueryDocument, size int, err error) {
	doc, err = parser.ParseQueryWithTokenLimit(&ast.Source{Input: query}, maxTokens)
	if err != nil {
		code := errcode.GraphQLParseFailed
		if gerr := (*gqlerror.Error)(nil); errors.As(err, &gerr) && strings.HasPrefix(gerr.Message, "exceeded token limit") {
			code = errcode.DocumentLimitExceeded
		}
		return nil, estimateDocumentBytes(query, 0, err), errcode.WithAll(err, code)
	}
	astBytes, err := scanDocument(doc, lim)
	if err != nil {
		return nil, estimateDocumentBytes(query, 0, err), err
	}
	if listErr := validator.ValidateWithRules(schema, doc, rs); len(listErr) > 0 {
		return nil, estimateDocumentBytes(query, 0, listErr), errcode.WithAll(listErr, errcode.GraphQLValidationFailed)
	}
	return doc, estimateDocumentBytes(query, astBytes, nil), nil
}

// estimateDocumentBytes estimates the memory a document cache entry retains:
// the query text it is keyed by, plus its parsed and validated AST of
// astBytes (see scanDocument) or its error.
func estimateDocumentBytes(query string, astBytes int, err error) int {
	n := len(query) + astBytes + 256
	var list gqlerror.List
	switch {
	case errors.As(err, &list):
		for _, e := range list {
			n += 256 + len(e.Message)
		}
	case err != nil:
		n += 256 + len(err.Error())
	}
	return n
}

// CheckDocument parses query, checks it against the document limits and
// validates it against the schema, as Execute does before anything else,
// through the document cache. It returns the error Execute would report.
func (e *Executor) CheckDocument(query string) error {
	_, err := e.document(query)
	return err
}

// document returns the parsed and validated document for query, consulting
// and filling the executor's document cache when one is configured.
func (e *Executor) document(query string) (*ast.QueryDocument, error) {
	rs := defaultRules
	if e.Opts.DisableIntrospection {
		rs = hiddenSchemaRules
	}
	maxTokens, lim := orDefault(e.Opts.MaxQueryTokens, DefaultMaxQueryTokens), e.docLimits()
	if e.docs == nil || e.docs.schema != e.Built.Schema {
		doc, _, err := parseAndValidate(e.Built.Schema, rs, query, maxTokens, lim)
		return doc, err
	}
	if ent, ok := e.docs.get(query); ok {
		e.Opts.Metrics.documentLookup(true)
		return ent.doc, cloneError(ent.err)
	}
	e.Opts.Metrics.documentLookup(false)
	doc, size, err := parseAndValidate(e.Built.Schema, rs, query, maxTokens, lim)
	e.docs.put(query, doc, err, size)
	return doc, cloneError(err)
}

// cloneError copies a cached parse or validation error so that callers which
// append to or edit the returned list cannot affect other requests.
func cloneError(err error) error {
	if err == nil {
		return nil
	}
	var list gqlerror.List
	if errors.As(err, &list) {
		out := make(gqlerror.List, len(list))
		for i, ge := range list {
			if ge != nil {
				cp := *ge
				ge = &cp
			}
			out[i] = ge
		}
		return out
	}
	var ge *gqlerror.Error
	if errors.As(err, &ge) {
		cp := *ge
		return &cp
	}
	return err
}
