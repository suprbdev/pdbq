package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

func cacheExecutor(t testing.TB, size int) *Executor {
	t.Helper()
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, Functions: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	return New(nil, built, compile.New(built).Compile, nil, Options{DocumentCacheSize: size})
}

func TestDocumentCacheReusesValidatedDocument(t *testing.T) {
	e := cacheExecutor(t, 10)
	q := `query($id: Int!) {userById(id: $id) {id email}}`
	_, op1, err := e.prepare(context.Background(), Request{Query: q, Variables: map[string]any{"id": 1}})
	if err != nil {
		t.Fatal(err)
	}
	_, op2, err := e.prepare(context.Background(), Request{Query: q, Variables: map[string]any{"id": 2}})
	if err != nil {
		t.Fatal(err)
	}
	if op1.Document != op2.Document {
		t.Fatal("second request re-parsed a cached document")
	}
	if op1.Vars["id"] == op2.Vars["id"] {
		t.Fatalf("variables leaked between requests: %v / %v", op1.Vars, op2.Vars)
	}
	if n := e.docs.len(); n != 1 {
		t.Fatalf("cache holds %d entries, want 1", n)
	}
}

func TestDocumentCacheDisabled(t *testing.T) {
	e := cacheExecutor(t, 0)
	if e.docs != nil {
		t.Fatal("document_cache_size 0 built a cache")
	}
	q := `{__typename}`
	_, op1, err := e.prepare(context.Background(), Request{Query: q})
	if err != nil {
		t.Fatal(err)
	}
	_, op2, err := e.prepare(context.Background(), Request{Query: q})
	if err != nil {
		t.Fatal(err)
	}
	if op1.Document == op2.Document {
		t.Fatal("disabled cache shared a document")
	}
}

func TestDocumentCacheCachesErrors(t *testing.T) {
	e := cacheExecutor(t, 10)
	for _, q := range []string{`{noSuchField}`, `{userById(id: 1) {`} {
		first := e.Execute(context.Background(), Request{Query: q})
		if len(first.Errors) == 0 {
			t.Fatalf("%s: expected errors", q)
		}
		want, _ := json.Marshal(first)
		first.Errors[0].Message = "tampered"
		first.Errors = append(first.Errors, gqlerror.Errorf("appended"))

		second := e.Execute(context.Background(), Request{Query: q})
		got, _ := json.Marshal(second)
		if string(got) != string(want) {
			t.Fatalf("%s: cached error changed:\n got %s\nwant %s", q, got, want)
		}
	}
	if n := e.docs.len(); n != 2 {
		t.Fatalf("cache holds %d entries, want 2", n)
	}
}

func TestDocumentCacheEvictsLeastRecentlyUsed(t *testing.T) {
	e := cacheExecutor(t, 2)
	ctx := context.Background()
	prep := func(q string) *Operation {
		t.Helper()
		_, op, err := e.prepare(ctx, Request{Query: q})
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	a, b, c := `{a: __typename}`, `{b: __typename}`, `{c: __typename}`
	docA := prep(a).Document
	prep(b)
	if prep(a).Document != docA {
		t.Fatal("a was evicted early")
	}
	prep(c)
	if n := e.docs.len(); n != 2 {
		t.Fatalf("cache holds %d entries, want 2", n)
	}
	if _, ok := e.docs.get(b); ok {
		t.Fatal("least recently used entry b survived")
	}
	if prep(a).Document != docA {
		t.Fatal("recently used entry a was evicted")
	}
}

func TestDocumentCacheByteBudget(t *testing.T) {
	c := newDocCache(nil, 100, 64)
	c.put(strings.Repeat("a", 30), nil, nil)
	c.put(strings.Repeat("b", 30), nil, nil)
	c.put(strings.Repeat("c", 30), nil, nil)
	if c.len() != 2 || c.bytes != 60 {
		t.Fatalf("len=%d bytes=%d, want 2 entries / 60 bytes", c.len(), c.bytes)
	}
	if _, ok := c.get(strings.Repeat("a", 30)); ok {
		t.Fatal("oldest entry survived the byte budget")
	}
	c.put(strings.Repeat("d", 65), nil, nil)
	if c.len() != 2 {
		t.Fatal("an entry larger than the whole budget was cached")
	}
}

func TestDocumentCacheBoundToSchema(t *testing.T) {
	e := cacheExecutor(t, 10)
	q := `{__typename}`
	_, op1, err := e.prepare(context.Background(), Request{Query: q})
	if err != nil {
		t.Fatal(err)
	}
	other := cacheExecutor(t, 10)
	e.Built = other.Built
	_, op2, err := e.prepare(context.Background(), Request{Query: q})
	if err != nil {
		t.Fatal(err)
	}
	if op1.Document == op2.Document {
		t.Fatal("document validated against another schema was reused")
	}
}

// TestDocumentCacheConcurrent runs shared cached documents through every
// stage that reads the AST (limits, compile, introspection, @skip/@include
// evaluation) from many goroutines; the race detector flags any write into a
// cached document.
func TestDocumentCacheConcurrent(t *testing.T) {
	e := cacheExecutor(t, 4)
	ctx := context.Background()
	compiled := []string{
		`query($id: Int!, $skip: Boolean!) {userById(id: $id) {id email @skip(if: $skip) ...F}} fragment F on User {fullName}`,
		`{allUsers(first: 10) {nodes {id email postsByAuthorId(first: 5) {nodes {id title author {id}}}}}}`,
		`{allUsers(filter: {and: [{email: {like: "%x%"}}, {or: [{id: {greaterThan: 5}}, {mood: {equalTo: HAPPY}}]}]}) {nodes {id}}}`,
		`{a: userById(id: 1) {id} b: userById(id: 1) {email} ... on Query {c: allUsers(first: 2) {totalCount}}}`,
	}
	introspection := []string{
		IntrospectionQuery,
		`{z: __typename a: __type(name: "User") {name fields {name type {name kind ofType {name}}}}}`,
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := range 16 {
		wg.Go(func() {
			for i := range 50 {
				q := compiled[(g+i)%len(compiled)]
				vars := map[string]any{"id": g*100 + i, "skip": i%2 == 0}
				if _, err := e.CompileRequest(ctx, Request{Query: q, Variables: vars}); err != nil {
					errs <- fmt.Errorf("compile %q: %w", q, err)
					return
				}
				res := e.Execute(ctx, Request{Query: introspection[(g+i)%len(introspection)]})
				if len(res.Errors) > 0 {
					errs <- fmt.Errorf("introspection: %w", res.Errors)
					return
				}
				if res := e.Execute(ctx, Request{Query: `{nope}`}); len(res.Errors) == 0 {
					errs <- fmt.Errorf("invalid query accepted")
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
