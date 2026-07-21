package compile_test

import (
	"context"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/testutil"
)

// BenchmarkSchemaBuild measures Catalog -> validated GraphQL schema.
func BenchmarkSchemaBuild(b *testing.B) {
	cat := testutil.FixtureCatalog()
	for b.Loop() {
		builder := schema.New(cat, inflect.Default, schema.Options{FilterIndexedOnly: true, Functions: true})
		if _, err := builder.Build(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCompile measures GraphQL -> SQL for representative query shapes.
func BenchmarkCompile(b *testing.B) {
	built := mustBuild(b)
	compiler := compile.New(built)
	shapes := map[string]string{
		"single_row":       `{userById(id: 1) {id email fullName}}`,
		"nested_relations": `{allUsers(first: 10) {nodes {id email postsByAuthorId(first: 5) {nodes {id title author {id}}}}}}`,
		"connection":       `{allUsers(first: 10) {totalCount nodes {id} pageInfo {hasNextPage endCursor}}}`,
		"filters":          `{allUsers(filter: {and: [{email: {like: "%x%"}}, {or: [{id: {greaterThan: 5}}, {mood: {equalTo: HAPPY}}]}]}) {nodes {id}}}`,
		"node":             `{node(nodeId: "WyJQb3N0IiwxXQ==") {nodeId ... on Post {title}}}`,
	}
	for name, q := range shapes {
		doc, perr := parser.ParseQuery(&ast.Source{Input: q})
		if perr != nil {
			b.Fatal(perr)
		}
		if errs := validator.Validate(built.Schema, doc); len(errs) > 0 {
			b.Fatal(errs)
		}
		field := doc.Operations[0].SelectionSet[0].(*ast.Field)
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				if _, err := compiler.Compile(context.Background(), &compile.Request{
					Field: field, Built: built, MaxDepth: 15,
				}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func mustBuild(b *testing.B) *schema.Built {
	b.Helper()
	builder := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, Functions: true})
	built, err := builder.Build()
	if err != nil {
		b.Fatal(err)
	}
	return built
}
