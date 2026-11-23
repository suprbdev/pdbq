package nestedmutations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/nestedmutations"
	"github.com/suprbdev/pdbq/internal/schema"
)

func compileRoot(t *testing.T, reg *plugin.Registry, built *schema.Built, query string) *compile.Statement {
	t.Helper()
	doc, perr := parser.ParseQuery(&ast.Source{Input: query})
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	if errs := validator.ValidateWithRules(built.Schema, doc, nil); len(errs) > 0 {
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
	reg, built := setupWith(t, plugin.NewRegistry(nestedmutations.New(nestedmutations.DefaultSettings())))
	stmt := compileRoot(t, reg, built, `mutation {
		createPost(input: {title: "Hi", userByAuthorId: {create: {email: "x@y.z"}}}) {
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
	reg := plugin.NewRegistry(nestedmutations.New(nestedmutations.DefaultSettings()))
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
	if !strings.Contains(sql, `SELECT $2, __mut."id" FROM __mut RETURNING 1`) {
		t.Errorf("children must reference __mut:\n%s", sql)
	}

	stmt = compileRoot(t, reg, built, `mutation {
		createPost(input: {title: "Hi", userByAuthorId: {connectById: {id: 7}}}) {
			post { id }
		}
	}`)
	if !strings.Contains(stmt.SQL, `SELECT "id" FROM "public"."users" WHERE "public"."users"."id" = $2`) {
		t.Errorf("connect lookup missing:\n%s", stmt.SQL)
	}
}

func TestConnectInputPerReferencedKey(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "memos", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "owner_id", Position: 2, PGType: "text", TypeSchema: "pg_catalog", NotNull: true},
		},
		PrimaryKey: &introspect.Constraint{Name: "memos_pkey", Columns: []string{"id"}},
		ForeignKeys: []*introspect.ForeignKey{{
			Name: "memos_owner_id_fkey", Columns: []string{"owner_id"},
			RefSchema: "public", RefTable: "users", RefColumns: []string{"email"},
		}},
	})
	reg := plugin.NewRegistry(nestedmutations.New(nestedmutations.DefaultSettings()))
	b := schema.New(cat, inflect.Default, schema.Options{FilterIndexedOnly: true})
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"input MemoUserByOwnerIdNestedInput {\n  connectById: UserConnectByIdInput\n  connectByEmail: UserConnectByEmailInput\n",
		"input UserConnectByIdInput {\n  id: Int!\n}",
		"input UserConnectByEmailInput {\n  email: String!\n}",
	} {
		if !strings.Contains(built.SDL, want) {
			t.Errorf("SDL missing %q", want)
		}
	}

	stmt := compileRoot(t, reg, built, `mutation {
		createMemo(input: {id: 1, userByOwnerId: {connectByEmail: {email: "a@b.c"}}}) { memo { id } }
	}`)
	if !strings.Contains(stmt.SQL, `SELECT "email" FROM "public"."users" WHERE "public"."users"."email" = $`) {
		t.Errorf("connect must match on email:\n%s", stmt.SQL)
	}
	stmt = compileRoot(t, reg, built, `mutation {
		createMemo(input: {id: 1, userByOwnerId: {connectById: {id: 7}}}) { memo { id } }
	}`)
	if !strings.Contains(stmt.SQL, `SELECT "email" FROM "public"."users" WHERE "public"."users"."id" = $`) {
		t.Errorf("connect by another key must project the referenced key:\n%s", stmt.SQL)
	}
}

func TestNestedCreateMapsEnumLabels(t *testing.T) {
	reg, built := setupWith(t, plugin.NewRegistry(nestedmutations.New(nestedmutations.DefaultSettings())))

	// Forward nested create: enum on the parent row.
	stmt := compileRoot(t, reg, built, `mutation {
		createPost(input: {title: "Hi", userByAuthorId: {create: {email: "x@y.z", mood: HAPPY}}}) {
			post { id }
		}
	}`)
	assertEnumArg(t, stmt)

	// Root row with reverse nested children: enum on the root row itself
	// (posts carry no enum column in the fixture; insertChildren shares the
	// same coercion helper).
	stmt = compileRoot(t, reg, built, `mutation {
		createUser(input: {
			email: "a@b.c",
			mood: HAPPY,
			postsByAuthorId: {create: [{title: "One"}]}
		}) {
			user { id }
		}
	}`)
	assertEnumArg(t, stmt)
}

// assertEnumArg checks the GraphQL enum value HAPPY was mapped to the
// PostgreSQL label "happy" in the parameter list.
func assertEnumArg(t *testing.T, stmt *compile.Statement) {
	t.Helper()
	mapped := false
	for _, a := range stmt.Args {
		switch a {
		case "HAPPY":
			t.Errorf("enum value passed raw as %q; want pg label: args=%v", a, stmt.Args)
		case "happy":
			mapped = true
		}
	}
	if !mapped {
		t.Errorf("pg enum label \"happy\" missing from args: %v", stmt.Args)
	}
}

func TestPlainCreateStillUsesBaseCompiler(t *testing.T) {
	reg := plugin.NewRegistry(nestedmutations.New(nestedmutations.DefaultSettings()))
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

func TestNestedForwardRelationNameCollidesWithColumn(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "memos", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "user_email", Position: 2, PGType: "text", TypeSchema: "pg_catalog", NotNull: true},
		},
		PrimaryKey: &introspect.Constraint{Name: "memos_pkey", Columns: []string{"id"}},
		ForeignKeys: []*introspect.ForeignKey{{
			Name: "memos_user_email_fkey", Columns: []string{"user_email"},
			RefSchema: "public", RefTable: "users", RefColumns: []string{"email"},
		}},
	})
	reg := plugin.NewRegistry(nestedmutations.New(nestedmutations.DefaultSettings()))
	b := schema.New(cat, inflect.Default, schema.Options{FilterIndexedOnly: true})
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(built.SDL, "userByUserEmail: MemoUserByUserEmailNestedInput") {
		t.Errorf("nested forward field missing from MemoCreateInput:\n%s", built.SDL)
	}
	stmt := compileRoot(t, reg, built, `mutation {
		createMemo(input: {id: 1, userByUserEmail: {connectByEmail: {email: "a@b.c"}}}) { memo { id } }
	}`)
	if !strings.Contains(stmt.SQL, `WHERE "public"."users"."email" = $`) {
		t.Errorf("connect must match on email:\n%s", stmt.SQL)
	}
}

func TestPayloadReadsVisibilityCTEs(t *testing.T) {
	reg, built := setupWith(t, plugin.NewRegistry(nestedmutations.New(nestedmutations.DefaultSettings())))
	query := `mutation {
		createUser(input: {
			email: "a@b.c",
			postsByAuthorId: {create: [{title: "One", commentsByPostId: {create: [{body: "x"}]}}]},
			placesByOwnerId: {create: [{name: "Home"}]}
		}) {
			user {
				postsByAuthorId { totalCount nodes { userByAuthorId { id } commentsByPostId { nodes { body } } } }
				placesByOwnerId { nodes { name } }
			}
		}
	}`
	sql := compileRoot(t, reg, built, query).SQL
	for _, want := range []string{
		`INSERT INTO "public"."comments" ("body", "post_id") SELECT $4, __c_2."id" FROM __c_2 RETURNING "id", "post_id", "body"`,
		"__vis_4 AS NOT MATERIALIZED (\n  SELECT \"id\" FROM \"public\".\"users\"\n  UNION ALL SELECT \"id\" FROM __mut\n)",
		"__vis_5 AS NOT MATERIALIZED (\n  SELECT \"id\", \"owner_id\", \"name\" FROM \"public\".\"places\"\n  UNION ALL SELECT \"id\", \"owner_id\", \"name\" FROM __c_1\n)",
		"__vis_6 AS NOT MATERIALIZED (\n  SELECT \"id\", \"author_id\" FROM \"public\".\"posts\"\n  UNION ALL SELECT \"id\", \"author_id\" FROM __c_2\n)",
		"__vis_7 AS NOT MATERIALIZED (\n  SELECT \"id\", \"post_id\", \"body\" FROM \"public\".\"comments\"\n  UNION ALL SELECT \"id\", \"post_id\", \"body\" FROM __c_3\n)",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q:\n%s", want, sql)
		}
	}
	for _, table := range []string{"users", "posts", "comments", "places"} {
		if ref := `FROM "public"."` + table + `" AS `; strings.Contains(sql, ref) {
			t.Errorf("payload reads %s instead of its visibility CTE:\n%s", table, sql)
		}
	}
	for range 10 {
		if again := compileRoot(t, reg, built, query).SQL; again != sql {
			t.Fatalf("SQL is not deterministic:\n%s\n---\n%s", sql, again)
		}
	}
}

func TestConnectGetsNoVisibilityCTE(t *testing.T) {
	reg, built := setupWith(t, plugin.NewRegistry(nestedmutations.New(nestedmutations.DefaultSettings())))
	sql := compileRoot(t, reg, built, `mutation {
		createPost(input: {title: "Hi", userByAuthorId: {connectById: {id: 7}}}) {
			post { userByAuthorId { email } }
		}
	}`).SQL
	if strings.Contains(sql, `SELECT * FROM "public"."users"`+"\n  UNION ALL") {
		t.Errorf("a connected parent's table needs no visibility CTE:\n%s", sql)
	}
	if !strings.Contains(sql, `FROM "public"."users" AS `) {
		t.Errorf("connected parent must be read from its table:\n%s", sql)
	}
}
