package exec

import (
	"container/list"
	"errors"
	"strings"
	"sync"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"
	"github.com/vektah/gqlparser/v2/validator/core"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

// documentCacheBytes bounds the total query text held by one document cache,
// so a client sending many large unique documents cannot grow it without
// limit even below the entry cap.
const documentCacheBytes = 32 << 20

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
type docCache struct {
	schema   *ast.Schema
	mu       sync.Mutex
	maxItems int
	maxBytes int
	bytes    int
	order    list.List
	items    map[string]*list.Element
}

type docEntry struct {
	query string
	doc   *ast.QueryDocument
	err   error
}

func newDocCache(schema *ast.Schema, maxItems, maxBytes int) *docCache {
	if maxItems <= 0 {
		return nil
	}
	return &docCache{schema: schema, maxItems: maxItems, maxBytes: maxBytes, items: make(map[string]*list.Element)}
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

func (c *docCache) put(query string, doc *ast.QueryDocument, err error) {
	if len(query) > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[query]; ok {
		return
	}
	query = strings.Clone(query)
	c.items[query] = c.order.PushFront(&docEntry{query: query, doc: doc, err: err})
	c.bytes += len(query)
	for len(c.items) > c.maxItems || c.bytes > c.maxBytes {
		oldest := c.order.Back()
		e := oldest.Value.(*docEntry)
		c.order.Remove(oldest)
		delete(c.items, e.query)
		c.bytes -= len(e.query)
	}
}

func (c *docCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// parseAndValidate parses and validates query against schema with rs,
// without caching.
func parseAndValidate(schema *ast.Schema, rs *rules.Rules, query string) (*ast.QueryDocument, error) {
	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil {
		return nil, err
	}
	if listErr := validator.ValidateWithRules(schema, doc, rs); len(listErr) > 0 {
		return nil, listErr
	}
	return doc, nil
}

// document returns the parsed and validated document for query, consulting
// and filling the executor's document cache when one is configured.
func (e *Executor) document(query string) (*ast.QueryDocument, error) {
	rs := defaultRules
	if e.Opts.DisableIntrospection {
		rs = hiddenSchemaRules
	}
	if e.docs == nil || e.docs.schema != e.Built.Schema {
		return parseAndValidate(e.Built.Schema, rs, query)
	}
	if ent, ok := e.docs.get(query); ok {
		e.Opts.Metrics.documentLookup(true)
		return ent.doc, cloneError(ent.err)
	}
	e.Opts.Metrics.documentLookup(false)
	doc, err := parseAndValidate(e.Built.Schema, rs, query)
	e.docs.put(query, doc, err)
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
