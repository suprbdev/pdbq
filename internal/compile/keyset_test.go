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
	contribplugin "github.com/suprbdev/pdbq/internal/plugins/contrib"
)

// TestKeysetExtensionLeadingBound covers non-nullable extension-typed
// leading order terms: they never become a row comparison (which would
// resolve its operators through the search_path) and take the redundant
// leading bound with the type's schema-qualified btree operator.
func TestKeysetExtensionLeadingBound(t *testing.T) {
	cat := contribfixture.Catalog()
	for _, tbl := range cat.Tables {
		if tbl.Name != "documents" {
			continue
		}
		for _, c := range tbl.Columns {
			if c.Name == "slug" || c.Name == "attrs" {
				c.NotNull = true
			}
		}
	}
	built := buildCatalog(t, cat, nil, contribplugin.New(nil))
	const cursor = `"WyJEb2N1bWVudCIsIDNd"`
	for name, tc := range map[string]struct {
		args string
		want string
	}{
		"citext after":  {`first: 2, after: ` + cursor + `, orderBy: [SLUG_ASC]`, `WHERE __t_1."slug" OPERATOR("ext".>=) (SELECT __a."slug" `},
		"citext before": {`last: 2, before: ` + cursor + `, orderBy: [SLUG_ASC]`, `WHERE __t_1."slug" OPERATOR("ext".<=) (SELECT __a."slug" `},
		"hstore desc":   {`first: 2, after: ` + cursor + `, orderBy: [ATTRS_DESC]`, `WHERE __t_1."attrs" OPERATOR("ext".#<=#) (SELECT __a."attrs" `},
		"text":          {`first: 2, after: ` + cursor + `, orderBy: [TITLE_ASC]`, `WHERE (__t_1."title", __t_1."id") > ((SELECT __a."title" `},
	} {
		t.Run(name, func(t *testing.T) {
			doc, perr := parser.ParseQuery(&ast.Source{Input: `{ allDocuments(` + tc.args + `) { nodes { id } } }`})
			if perr != nil {
				t.Fatal(perr)
			}
			if errs := validator.ValidateWithRules(built.Schema, doc, nil); len(errs) > 0 {
				t.Fatal(errs)
			}
			stmt, err := compile.New(built).Compile(context.Background(), &compile.Request{
				Field:    doc.Operations[0].SelectionSet[0].(*ast.Field),
				Built:    built,
				MaxDepth: 15,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stmt.SQL, tc.want) {
				t.Errorf("SQL lacks %q:\n%s", tc.want, stmt.SQL)
			}
		})
	}
}
