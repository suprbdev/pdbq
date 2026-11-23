package exec

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// dosBudget is how long one rejected request may take, with room for -race,
// which slows the checks down by an order of magnitude.
const dosBudget = 2 * time.Second

func limitExecutor(t testing.TB, opts Options) *Executor {
	t.Helper()
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, Functions: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	if opts.MaxDepth == 0 {
		opts.MaxDepth = 15
	}
	if opts.MaxCost == 0 {
		opts.MaxCost = 10000
	}
	return New(nil, built, compile.New(built).Compile, nil, opts)
}

// scanRejection matches the errors of scanDocument's expansion limits.
const scanRejection = "selections with its fragments expanded|field comparisons"

// fragmentBomb is a document of fragments that each spread the next one
// twice, so its root selects 2^k copies of leaf.
func fragmentBomb(k int, leaf string) string {
	var b strings.Builder
	b.WriteString("{ ...F0 }\n")
	for i := range k {
		fmt.Fprintf(&b, "fragment F%d on Query { ...F%d ...F%d }\n", i, i+1, i+1)
	}
	fmt.Fprintf(&b, "fragment F%d on Query { %s }\n", k, leaf)
	return b.String()
}

// rejectQuickly prepares query and requires, within dosBudget, an error
// containing one of the |-separated alternatives of want.
func rejectQuickly(t *testing.T, e *Executor, query string, vars map[string]any, want string) {
	t.Helper()
	start := time.Now()
	_, _, err := e.prepare(context.Background(), Request{Query: query, Variables: vars})
	took := time.Since(start)
	if err == nil || !slices.ContainsFunc(strings.Split(want, "|"), func(w string) bool { return strings.Contains(err.Error(), w) }) {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if took > dosBudget {
		t.Fatalf("rejection took %v (budget %v)", took, dosBudget)
	}
}

func TestFragmentBombRejected(t *testing.T) {
	for _, intro := range []bool{false, true} {
		for _, leaf := range []string{"__typename", "__typename @skip(if: true)", "allPosts { nodes { id } }"} {
			t.Run(fmt.Sprintf("introspection_disabled=%v/%s", intro, leaf), func(t *testing.T) {
				e := limitExecutor(t, Options{DisableIntrospection: intro, DocumentCacheSize: 10})
				rejectQuickly(t, e, fragmentBomb(40, leaf), nil, scanRejection)
			})
		}
	}
}

func TestMeasureMemoizesFragments(t *testing.T) {
	e := limitExecutor(t, Options{})
	doc, err := parser.ParseQuery(&ast.Source{Input: fragmentBomb(60, "allPosts { nodes { id } }")})
	if err != nil {
		t.Fatal(err)
	}
	if errs := validator.ValidateWithRules(e.Built.Schema, doc, nil); len(errs) > 0 {
		t.Fatal(errs)
	}
	start := time.Now()
	depth, cost := measure(doc.Operations[0].SelectionSet, doc.Fragments, nil, 100, 1, e.Built)
	if took := time.Since(start); took > dosBudget {
		t.Fatalf("measure took %v", took)
	}
	if depth != 3 || cost != costCeiling {
		t.Fatalf("depth=%d cost=%d, want 3 and the saturated %d", depth, cost, costCeiling)
	}
	start = time.Now()
	if err := e.checkLimits(doc, doc.Operations[0], nil); err == nil || !strings.Contains(err.Error(), "exceeds limit 10000") {
		t.Fatalf("checkLimits = %v", err)
	}
	if took := time.Since(start); took > dosBudget {
		t.Fatalf("checkLimits took %v", took)
	}
}

func TestFragmentCycleLeftToValidation(t *testing.T) {
	e := limitExecutor(t, Options{})
	rejectQuickly(t, e, `{ ...A } fragment A on Query { __typename ...B } fragment B on Query { ...A }`, nil, "Cannot spread fragment")
}

func TestDeepInputLiteralRejected(t *testing.T) {
	e := limitExecutor(t, Options{DocumentCacheSize: 10})
	deep := func(n int) string {
		return "{ allPosts(filter:" + strings.Repeat("{not:", n) + "{id:{equalTo:1}}" + strings.Repeat("}", n) + ") { totalCount } }"
	}
	rejectQuickly(t, e, deep(3000), nil, "input value nesting exceeds limit 32")
	rejectQuickly(t, e, deep(20000), nil, "exceeded token limit of 15000")
	if _, _, err := e.prepare(context.Background(), Request{Query: deep(29)}); err != nil {
		t.Fatalf("filter nested 31 levels deep: %v", err)
	}
	rejectQuickly(t, e, `{ allPosts(filter: {id: {in: `+strings.Repeat("[", 40)+"1"+strings.Repeat("]", 40)+`}}) { totalCount } }`, nil, "input value nesting exceeds limit 32")
}

func TestDeepInputVariableRejected(t *testing.T) {
	e := limitExecutor(t, Options{})
	var v any = map[string]any{"id": map[string]any{"equalTo": 1}}
	for range 4000 {
		v = map[string]any{"not": v}
	}
	q := `query($f: PostFilter) { allPosts(filter: $f) { totalCount } }`
	rejectQuickly(t, e, q, map[string]any{"f": v}, "input value nesting exceeds limit 32")

	shallow := map[string]any{"not": map[string]any{"id": map[string]any{"equalTo": 1}}}
	if _, _, err := e.prepare(context.Background(), Request{Query: q, Variables: map[string]any{"f": shallow}}); err != nil {
		t.Fatalf("shallow filter variable: %v", err)
	}
}

func TestOverlappingFieldsBlowupRejected(t *testing.T) {
	e := limitExecutor(t, Options{})
	rejectQuickly(t, e, "{"+strings.Repeat(" __typename", 9000)+"}", nil, "field comparisons")
	rejectQuickly(t, e, "{"+strings.Repeat(" a: __typename b: __typename", 2400)+"}", nil, "field comparisons")
	rejectQuickly(t, e, "{"+strings.Repeat(" a: __typename", 1000)+strings.Repeat(" __typename", 10000)+"}", nil, scanRejection)

	var b strings.Builder
	b.WriteString("{ allUsers { nodes {")
	for range 100 {
		b.WriteString(" a: postsByAuthorId { nodes { ...F } }")
	}
	b.WriteString(" } } } fragment F on Post {" + strings.Repeat(" id", 90) + " }")
	rejectQuickly(t, e, b.String(), nil, "field comparisons")

	var tree func(d int) string
	tree = func(d int) string {
		if d == 0 {
			return "id"
		}
		s := tree(d - 1)
		return "a: postsByAuthorId { nodes { userByAuthorId { " + s + " } } } a: postsByAuthorId { nodes { userByAuthorId { " + s + " } } }"
	}
	rejectQuickly(t, e, "{ allUsers { nodes { "+tree(9)+" } } }", nil, scanRejection)

	var distinct strings.Builder
	distinct.WriteString("{")
	for i := range 3500 {
		fmt.Fprintf(&distinct, " a%d: __typename", i)
	}
	distinct.WriteString(strings.Repeat(" ...F", 8) + " } fragment F on Query {")
	for i := range 1200 {
		fmt.Fprintf(&distinct, " f%d: __typename", i)
	}
	distinct.WriteString(" }")
	rejectQuickly(t, e, distinct.String(), nil, "selections with its fragments expanded")
}

func TestLargeListLiteralNotCached(t *testing.T) {
	e := limitExecutor(t, Options{DocumentCacheSize: 1000})
	base := "{ allPosts(filter: {id: {in: [" + strings.Repeat("1,", 520000) + "1]}}) { totalCount } }"
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for j := range 8 {
		rejectQuickly(t, e, fmt.Sprintf("%s # %d", base, j), nil, "exceeded token limit")
	}
	if s := e.docs.size(); s > DefaultDocumentCacheBytes {
		t.Fatalf("cache accounts %d bytes, budget %d", s, DefaultDocumentCacheBytes)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 32<<20 {
		t.Fatalf("heap grew by %d MiB", grown>>20)
	}
}

func TestDocumentCacheBoundsRetainedMemory(t *testing.T) {
	const budget = 8 << 20
	e := limitExecutor(t, Options{DocumentCacheSize: 1000, DocumentCacheBytes: budget, DocumentCacheMaxEntryBytes: 1 << 20})
	base := "{ allPosts(filter: {id: {in: [" + strings.Repeat("1,", 3000) + "1]}}) { totalCount } }"
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for j := range 200 {
		if _, _, err := e.prepare(context.Background(), Request{Query: fmt.Sprintf("%s # %d", base, j)}); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if s := e.docs.size(); s > budget {
		t.Fatalf("cache accounts %d bytes, budget %d", s, budget)
	}
	n := e.docs.len()
	if n == 0 || n >= 200 {
		t.Fatalf("cache holds %d of 200 documents", n)
	}
	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > budget+(4<<20) {
		t.Fatalf("heap grew by %d MiB for a %d MiB cache", grown>>20, budget>>20)
	}
}

func TestDocumentSizeEstimateCoversHeap(t *testing.T) {
	e := limitExecutor(t, Options{})
	aliases := func(n int, sel string) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, " a%d: %s", i, sel)
		}
		return b.String()
	}
	lim := docLimits{inputDepth: 100, selections: 1 << 30, comparisons: 1 << 40}
	for name, q := range map[string]string{
		"list":     "{ allPosts(filter: {id: {in: [" + strings.Repeat("1,", 14000) + "1]}}) { totalCount } }",
		"objects":  "{ allPosts(filter: {or: [" + strings.Repeat("{id: {equalTo: 1}},", 1500) + "{id: {equalTo: 1}}]}) { totalCount } }",
		"typename": "{" + aliases(4000, "__typename") + "}",
		"fields":   "{ allUsers { nodes {" + aliases(3000, "id") + " } } }",
		"nested":   "{ allUsers { nodes { " + strings.Repeat("postsByAuthorId { nodes { id title userByAuthorId { id ", 300) + strings.Repeat(" } } }", 300) + " } } }",
	} {
		const n = 8
		docs := make([]*ast.QueryDocument, n)
		est := 0
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for i := range docs {
			doc, size, err := parseAndValidate(e.Built.Schema, defaultRules, fmt.Sprintf("%s # %d", q, i), 1<<30, lim)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			docs[i], est = doc, est+size
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		actual := int64(after.HeapAlloc) - int64(before.HeapAlloc)
		runtime.KeepAlive(docs)
		if float64(est) < 0.9*float64(actual) {
			t.Errorf("%s: estimated %d bytes, retained %d", name, est, actual)
		}
	}
}

func TestLegitimateDocumentsWithinLimits(t *testing.T) {
	e := limitExecutor(t, Options{})
	for _, q := range []string{
		IntrospectionQuery,
		`{ allUsers(first: 10) { nodes { id ...U ...V postsByAuthorId(first: 5) { nodes { id ...P } } } } }
		 fragment U on User { id email } fragment V on User { id fullName } fragment P on Post { id title userByAuthorId { id ...U } }`,
	} {
		if _, _, err := e.prepare(context.Background(), Request{Query: q}); err != nil {
			t.Errorf("%.40q: %v", q, err)
		}
	}
	for _, s := range benchShapes {
		if _, _, err := e.prepare(context.Background(), Request{Query: s.query}); err != nil {
			t.Errorf("%s: %v", s.name, err)
		}
	}
}

func TestPrepareStopsWhenCancelled(t *testing.T) {
	e := limitExecutor(t, Options{DocumentCacheSize: 10})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := e.prepare(ctx, Request{Query: `{__typename}`}); err == nil {
		t.Fatal("cancelled request was prepared")
	}
	if n := e.docs.len(); n != 0 {
		t.Fatalf("cancelled request parsed its document (%d cached)", n)
	}
}

func BenchmarkRejectFragmentBomb(b *testing.B) {
	e := limitExecutor(b, Options{})
	q := fragmentBomb(40, "__typename")
	for b.Loop() {
		if _, _, err := e.prepare(context.Background(), Request{Query: q}); err == nil {
			b.Fatal("accepted")
		}
	}
}

func BenchmarkRejectDeepInput(b *testing.B) {
	e := limitExecutor(b, Options{})
	q := "{ allPosts(filter:" + strings.Repeat("{not:", 4000) + "{id:{equalTo:1}}" + strings.Repeat("}", 4000) + ") { totalCount } }"
	for b.Loop() {
		if _, _, err := e.prepare(context.Background(), Request{Query: q}); err == nil {
			b.Fatal("accepted")
		}
	}
}

func BenchmarkRejectRepeatedFields(b *testing.B) {
	e := limitExecutor(b, Options{})
	q := "{" + strings.Repeat(" __typename", 9000) + "}"
	for b.Loop() {
		if _, _, err := e.prepare(context.Background(), Request{Query: q}); err == nil {
			b.Fatal("accepted")
		}
	}
}
