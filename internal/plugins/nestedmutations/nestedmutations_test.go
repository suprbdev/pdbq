package nestedmutations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/nestedmutations"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/testutil"
)

func compileRoot(t *testing.T, reg *plugin.Registry, built *schema.Built, query string) *compile.Statement {
	t.Helper()
	doc, perr := parser.ParseQuery(&ast.Source{Input: query})
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	if errs := validator.Validate(built.Schema, doc); len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	op := doc.Operations[0]
	vars, verr := validator.VariableValues(built.Schema, op, nil)
	if verr != nil {
		t.Fatalf("vars: %v", verr)
	}
	fn := reg.CompileChain(nil, compile.New(built).Compile)
	stmt, err := fn(context.Background(), &compile.Request{
		Field:    op.SelectionSet[0].(*ast.Field),
		Vars:     vars,
		Built:    built,
		MaxDepth: 15,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return stmt
}

func TestNestedCreateParent(t *testing.T) {
	reg, built := setupWith(t, plugin.NewRegistry(nestedmutations.New(nil)))
	stmt := compileRoot(t, reg, built, `mutation {
		createPost(input: {title: "Hi", author: {create: {email: "x@y.z"}}}) {
			post { id title }
		}
	}`)
	sql := stmt.SQL
	for _, want := range []string{
		`INSERT INTO "public"."users"`,
		`INSERT INTO "public"."posts"`,
		`SELECT $1, __p_1."id" FROM __p_1`,
		"__mut AS (",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q:\n%s", want, sql)
		}
	}
	// Parent insert must come before the main insert.
	if strings.Index(sql, `"public"."users"`) > strings.Index(sql, `"public"."posts"`) {
		t.Error("parent CTE must precede child insert")
	}
}

func TestNestedConnectAndChildren(t *testing.T) {
	reg := plugin.NewRegistry(nestedmutations.New(nil))
	reg, built := setupWith(t, reg)
	stmt := compileRoot(t, reg, built, `mutation {
		createUser(input: {
			email: "a@b.c",
			postsByAuthorId: {create: [{title: "One"}, {title: "Two"}]}
		}) {
			user { id }
		}
	}`)
	sql := stmt.SQL
	if got := strings.Count(sql, `INSERT INTO "public"."posts"`); got != 2 {
		t.Errorf("want 2 child inserts, got %d:\n%s", got, sql)
	}
	if !strings.Contains(sql, `FROM __mut RETURNING *`) {
		t.Errorf("children must reference __mut:\n%s", sql)
	}

	stmt = compileRoot(t, reg, built, `mutation {
		createPost(input: {title: "Hi", author: {connect: {id: 7}}}) {
			post { id }
		}
	}`)
	if !strings.Contains(stmt.SQL, `SELECT * FROM "public"."users" WHERE "id" = $2`) {
		t.Errorf("connect lookup missing:\n%s", stmt.SQL)
	}
}

func TestPlainCreateStillUsesBaseCompiler(t *testing.T) {
	reg := plugin.NewRegistry(nestedmutations.New(nil))
	reg, built := setupWith(t, reg)
	stmt := compileRoot(t, reg, built, `mutation {
		createPost(input: {title: "Hi", authorId: 1}) {
			post { id }
		}
	}`)
	if !strings.Contains(stmt.SQL, "VALUES (") {
		t.Errorf("plain create should use the base VALUES insert:\n%s", stmt.SQL)
	}
}

// setupWith builds schema + registry sharing one plugin instance.
func setupWith(t *testing.T, reg *plugin.Registry) (*plugin.Registry, *schema.Built) {
	t.Helper()
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true})
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return reg, built
}
