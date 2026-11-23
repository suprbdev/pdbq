package compile_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/contribfixture"
	"github.com/suprbdev/pdbq/internal/inflect"
	contribplugin "github.com/suprbdev/pdbq/internal/plugins/contrib"
	"github.com/suprbdev/pdbq/internal/schema"
)

// TestHeadlineDocumentBound checks a headline reads at most
// server.max_headline_document characters of its document.
func TestHeadlineDocumentBound(t *testing.T) {
	b := schema.New(contribfixture.Catalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, MaxHeadlineDocument: 500})
	if err := contribplugin.New(nil).TransformSchema(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	doc, perr := parser.ParseQuery(&ast.Source{Input: `{ allDocuments(filter: {search: {matches: "a"}}) { nodes { searchHeadline(document: NOTES) } } }`})
	if perr != nil {
		t.Fatal(perr)
	}
	if errs := validator.ValidateWithRules(built.Schema, doc, nil); len(errs) > 0 {
		t.Fatal(errs)
	}
	stmt, err := compile.New(built).Compile(context.Background(), &compile.Request{
		Field: doc.Operations[0].SelectionSet[0].(*ast.Field), Vars: map[string]any{}, Built: built, MaxDepth: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := `left(__t_1."notes", (500)::int4)`; !strings.Contains(stmt.SQL, want) {
		t.Errorf("SQL lacks %s:\n%s", want, stmt.SQL)
	}
}
