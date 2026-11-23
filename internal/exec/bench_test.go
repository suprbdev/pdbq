package exec

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

var benchShapes = []struct{ name, query string }{
	{"single_row", `{userById(id: 1) {id email fullName}}`},
	{"nested_relations", `{allUsers(first: 10) {nodes {id email postsByAuthorId(first: 5) {nodes {id title userByAuthorId {id}}}}}}`},
	{"connection", `{allUsers(first: 10) {totalCount nodes {id} pageInfo {hasNextPage endCursor}}}`},
	{"filters", `{allUsers(filter: {and: [{email: {like: "%x%"}}, {or: [{id: {greaterThan: 5}}, {mood: {equalTo: HAPPY}}]}]}) {nodes {id}}}`},
}

func benchExecutor(b *testing.B, opts Options) *Executor {
	b.Helper()
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, Functions: true}).Build()
	if err != nil {
		b.Fatal(err)
	}
	return New(nil, built, compile.New(built).Compile, nil, opts)
}

// BenchmarkCompileRequest measures the request pipeline short of the
// database: parse, validate, variables, limits and compile, with and without
// the document cache.
func BenchmarkCompileRequest(b *testing.B) {
	ctx := context.Background()
	for _, mode := range []struct {
		name string
		size int
	}{{"uncached", 0}, {"cached", 1000}} {
		ex := benchExecutor(b, Options{MaxDepth: 15, MaxCost: 10000, MaxPageSize: 100, DocumentCacheSize: mode.size})
		for _, s := range benchShapes {
			b.Run(s.name+"/"+mode.name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, err := ex.CompileRequest(ctx, Request{Query: s.query}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkCompileRequestParallel is BenchmarkCompileRequest for one shape
// under concurrent callers sharing one executor.
func BenchmarkCompileRequestParallel(b *testing.B) {
	ex := benchExecutor(b, Options{MaxDepth: 15, MaxCost: 10000, MaxPageSize: 100, DocumentCacheSize: 1000})
	ctx := context.Background()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := ex.CompileRequest(ctx, Request{Query: benchShapes[0].query}); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// BenchmarkResultEncode measures serializing a one-root-field response into
// a reused buffer, as the HTTP server does.
func BenchmarkResultEncode(b *testing.B) {
	for _, size := range []struct {
		name string
		n    int
	}{{"1KB", 1 << 10}, {"120KB", 120 << 10}} {
		row := `{"id": 1, "email": "ada@example.com", "fullName": "Ada Lovelace"}`
		var sb strings.Builder
		sb.WriteString(`{"nodes": [`)
		for sb.Len() < size.n {
			if sb.Len() > len(`{"nodes": [`) {
				sb.WriteString(", ")
			}
			sb.WriteString(row)
		}
		sb.WriteString("]}")
		res := &Result{Data: map[string]json.RawMessage{"allUsers": json.RawMessage(sb.String())}}
		b.Run(size.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(sb.Len()))
			var buf []byte
			for b.Loop() {
				out, err := res.AppendJSON(buf[:0])
				if err != nil {
					b.Fatal(err)
				}
				buf = out
			}
		})
	}
}
