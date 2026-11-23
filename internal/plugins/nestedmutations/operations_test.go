package nestedmutations_test

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/nestedmutations"
	"github.com/suprbdev/pdbq/internal/schema"
)

func TestNestedInputShapes(t *testing.T) {
	_, built := setupWith(t, plugin.NewRegistry(nestedmutations.New(nil)))
	for _, want := range []string{
		"input PostAuthorNestedInput {\n  connectById: UserConnectByIdInput\n  connectByEmail: UserConnectByEmailInput\n  connectByNodeId: UserConnectByNodeIdInput\n  create: UserCreateInput\n}",
		"input PostAuthorNestedPatchInput {\n  connectById: UserConnectByIdInput\n  connectByEmail: UserConnectByEmailInput\n  connectByNodeId: UserConnectByNodeIdInput\n  create: UserCreateInput\n  updateById: UserUpdateByIdInput\n  updateByEmail: UserUpdateByEmailInput\n  updateByNodeId: UserUpdateByNodeIdInput\n  deleteById: UserDeleteByIdInput\n  deleteByEmail: UserDeleteByEmailInput\n  deleteByNodeId: UserDeleteByNodeIdInput\n}",
		"input UserPostsByAuthorIdNestedInput {\n  connectById: [PostConnectByIdInput!]\n  connectByNodeId: [PostConnectByNodeIdInput!]\n  create: [PostCreateInput!]\n}",
		"input UserPostsByAuthorIdNestedPatchInput {\n  connectById: [PostConnectByIdInput!]\n  connectByNodeId: [PostConnectByNodeIdInput!]\n  create: [PostCreateInput!]\n  updateById: [PostUpdateByIdInput!]\n",
		"  disconnectById: [PlaceDisconnectByIdInput!]\n  disconnectByNodeId: [PlaceDisconnectByNodeIdInput!]\n}",
		"input UserUpdateByIdInput {\n  id: Int!\n",
		"  userPatch: UserUpdateInput!\n}",
		"input UserUpdateByNodeIdInput {\n",
		"input UserConnectByNodeIdInput {\n",
		"  nodeId: ID!\n}",
		"  disconnectById: CommentDisconnectByIdInput\n",
	} {
		if !strings.Contains(built.SDL, want) {
			t.Errorf("SDL missing %q", want)
		}
	}
	for header, fields := range map[string][]string{
		"input PostCreateInput {": {"author: PostAuthorNestedInput", "commentsByPostId: PostCommentsByPostIdNestedInput"},
		"input PostUpdateInput {": {"author: PostAuthorNestedPatchInput", "commentsByPostId: PostCommentsByPostIdNestedPatchInput"},
	} {
		block := sdlBlock(built.SDL, header)
		for _, want := range fields {
			if !strings.Contains(block, want) {
				t.Errorf("%s missing nested field %q:\n%s", header, want, block)
			}
		}
	}
	for _, header := range []string{"input UserPostsByAuthorIdNestedPatchInput {", "input PostAuthorNestedPatchInput {", "input UserPlacesByOwnerIdNestedInput {", "input CommentParentNestedInput {"} {
		if strings.Contains(sdlBlock(built.SDL, header), "disconnect") {
			t.Errorf("%s must not offer disconnect (NOT NULL foreign key or create input)", header)
		}
	}
}

func TestNestedUpdateMutation(t *testing.T) {
	reg, built := setupWith(t, plugin.NewRegistry(nestedmutations.New(nil)))
	sql := compileRoot(t, reg, built, `mutation {
		updatePostById(id: 1, patch: {title: "T", author: {connectByEmail: {email: "a@b.c"}},
			commentsByPostId: {connectById: [{id: 2}], updateById: {id: 3, commentPatch: {body: "b"}}, deleteById: [{id: 4}]}}) {
			post { id commentsByPostId { nodes { body } } }
		}
	}`).SQL
	for _, want := range []string{
		`__p_1 AS (` + "\n" + `  SELECT "id" FROM "public"."users" WHERE "public"."users"."email" = $3`,
		`__mut AS (` + "\n" + `  UPDATE "public"."posts" SET "title" = $2, "author_id" = __p_1."id" FROM __p_1 WHERE "public"."posts"."id" = $1 RETURNING "public"."posts"."id"` + "\n",
		`UPDATE "public"."comments" SET "post_id" = __mut."id" FROM __mut WHERE "public"."comments"."id" = $4 RETURNING "public"."comments"."id", "public"."comments"."post_id", "public"."comments"."body"`,
		`DELETE FROM "public"."comments" USING __mut WHERE "public"."comments"."id" = $5 AND "public"."comments"."post_id" = __mut."id" RETURNING "public"."comments"."id"` + "\n",
		`UPDATE "public"."comments" SET "body" = $7 FROM __mut WHERE "public"."comments"."id" = $6 AND "public"."comments"."post_id" = __mut."id" RETURNING`,
		`SELECT "id", "post_id", "body" FROM "public"."comments" WHERE NOT EXISTS (SELECT 1 FROM __c_2 AS __m WHERE __m."id" = "public"."comments"."id") AND NOT EXISTS (SELECT 1 FROM __c_3 AS __m`,
		"\n  UNION ALL SELECT \"id\", \"post_id\", \"body\" FROM __c_2\n  UNION ALL SELECT \"id\", \"post_id\", \"body\" FROM __c_4\n)",
		"__guard AS MATERIALIZED (\n  SELECT (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('nested-mutations: author.connectByEmail matched ', count(*), ' rows')::boolean END FROM __p_1)",
		"concat('nested-mutations: commentsByPostId.updateById[0] matched ', count(*), ' rows')",
		"concat('nested-mutations: updatePostById matched ', count(*), ' rows')::boolean END FROM __mut) AS ok\n)",
		"FROM __guard\nLEFT JOIN LATERAL (\n",
		") AS __guarded ON true\nWHERE __guard.ok",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "UNION ALL SELECT \"id\", \"post_id\", \"body\" FROM __c_3") {
		t.Errorf("deleted rows must not be unioned back:\n%s", sql)
	}
	if strings.Contains(sql, "*") && strings.Contains(sql, "RETURNING *") {
		t.Errorf("RETURNING must list columns:\n%s", sql)
	}
	if strings.Contains(sql, `FROM "public"."posts"`+"\n") || strings.Contains(sql, `SELECT "id" FROM "public"."posts"`) {
		t.Errorf("a table the payload does not read needs no visibility CTE:\n%s", sql)
	}
}

func TestNestedUpdateWithoutColumnsLocksMainRow(t *testing.T) {
	reg, built := setupWith(t, plugin.NewRegistry(nestedmutations.New(nil)))
	sql := compileRoot(t, reg, built, `mutation {
		updateUserById(id: 1, patch: {placesByOwnerId: {create: [{name: "Home"}], disconnectById: [{id: 2}]}}) { user { id } }
	}`).SQL
	for _, want := range []string{
		"__mut AS (\n  SELECT __s.\"id\" FROM \"public\".\"users\" AS __s WHERE __s.\"id\" = $1 FOR UPDATE OF __s\n)",
		`UPDATE "public"."places" SET "owner_id" = NULL FROM __mut WHERE "public"."places"."id" = $3 AND "public"."places"."owner_id" = __mut."id"`,
		"concat('nested-mutations: updateUserById matched ', count(*), ' rows')",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q:\n%s", want, sql)
		}
	}
	if strings.Count(sql, "CASE WHEN count(*)") != 1 {
		t.Errorf("create and disconnect need no guard:\n%s", sql)
	}
}

func TestNestedForwardOperationsTargetTheReferencedRow(t *testing.T) {
	reg, built := setupWith(t, plugin.NewRegistry(nestedmutations.New(nil)))
	user := base64.StdEncoding.EncodeToString([]byte(`["User", 7]`))
	sql := compileRoot(t, reg, built, `mutation {
		updatePostById(id: 9, patch: {author: {updateByNodeId: {nodeId: "`+user+`", userPatch: {fullName: "U"}}, deleteByEmail: {email: "e"}}}) { post { id } }
	}`).SQL
	owner := `EXISTS (SELECT 1 FROM "public"."posts" AS __o WHERE __o."id" = $1 AND __o."author_id" = "public"."users"."id")`
	for _, want := range []string{
		`DELETE FROM "public"."users" WHERE "public"."users"."email" = $2 AND ` + owner + ` RETURNING 1`,
		`UPDATE "public"."users" SET "full_name" = $4 WHERE "public"."users"."id" = $3 AND ` + owner + ` RETURNING`,
		"__mut AS (\n  SELECT __s.\"id\" FROM \"public\".\"posts\" AS __s WHERE __s.\"id\" = $1 FOR UPDATE OF __s\n)",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q:\n%s", want, sql)
		}
	}
}

func TestForwardDisconnect(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "memos", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "owner_id", Position: 2, PGType: "text", TypeSchema: "pg_catalog"},
		},
		PrimaryKey: &introspect.Constraint{Name: "memos_pkey", Columns: []string{"id"}},
		ForeignKeys: []*introspect.ForeignKey{{
			Name: "memos_owner_id_fkey", Columns: []string{"owner_id"},
			RefSchema: "public", RefTable: "users", RefColumns: []string{"email"},
		}},
	})
	reg := plugin.NewRegistry(nestedmutations.New(nil))
	b := schema.New(cat, inflect.Default, schema.Options{FilterIndexedOnly: true})
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	sql := compileRoot(t, reg, built, `mutation {
		updateMemoById(id: 1, patch: {owner: {disconnectByEmail: {email: "a@b.c"}}}) { memo { id } }
	}`).SQL
	if want := `SET "owner_id" = CASE WHEN "public"."memos"."owner_id" = $2 THEN NULL ELSE "public"."memos"."owner_id" END`; !strings.Contains(sql, want) {
		t.Errorf("disconnect by the referenced key compares the FK:\n%s", sql)
	}
	sql = compileRoot(t, reg, built, `mutation {
		updateMemoById(id: 1, patch: {owner: {disconnectById: {id: 7}}}) { memo { id } }
	}`).SQL
	if want := `CASE WHEN EXISTS (SELECT 1 FROM "public"."users" AS __r WHERE __r."id" = $2 AND __r."email" = "public"."memos"."owner_id") THEN NULL`; !strings.Contains(sql, want) {
		t.Errorf("disconnect by another key looks the row up:\n%s", sql)
	}
}

func TestNestedErrors(t *testing.T) {
	reg, built := setupWith(t, plugin.NewRegistry(nestedmutations.New(map[string]any{"max_depth": 2, "max_operations": 3})))
	post := base64.StdEncoding.EncodeToString([]byte(`["Post", 1]`))
	for _, tc := range []struct{ name, query, want string }{
		{"two links", `mutation { createPost(input: {title: "x", author: {connectById: {id: 1}, create: {email: "e"}}}) { post { id } } }`,
			"author: specify at most one of create, connect or disconnect"},
		{"no operation", `mutation { createPost(input: {title: "x", author: {}}) { post { id } } }`,
			"author: specify an operation"},
		{"column and link", `mutation { createPost(input: {title: "x", authorId: 1, author: {connectById: {id: 1}}}) { post { id } } }`,
			"sets a column already set by the input"},
		{"connect and disconnect", `mutation { updateCommentById(id: 1, patch: {parent: {connectById: {id: 2}, disconnectById: {id: 3}}}) { comment { id } } }`,
			"parent: specify at most one of create, connect or disconnect"},
		{"operations", `mutation { createUser(input: {email: "e", postsByAuthorId: {create: [{title: "1"}, {title: "2"}, {title: "3"}, {title: "4"}]}}) { user { id } } }`,
			"more than 3 nested operations"},
		{"foreign nodeId", `mutation { createPost(input: {title: "x", author: {connectByNodeId: {nodeId: "` + post + `"}}}) { post { id } } }`,
			"author.connectByNodeId: nodeId does not identify a User"},
		{"bulk create", `mutation { createPosts(input: [{title: "x", author: {connectById: {id: 1}}}]) { affectedCount } }`,
			"createPosts does not take nested mutation fields"},
		{"bulk update", `mutation { updatePosts(filter: {}, patch: {author: {connectById: {id: 1}}}) { affectedCount } }`,
			"updatePosts does not take nested mutation fields"},
		{"depth", `mutation { createComment(input: {body: "x", post: {create: {title: "t", author: {create: {email: "e"}}}}}) { comment { id } } }`,
			"nesting depth exceeds limit 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := compileErr(t, reg, built, tc.query)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestBeforeOperationForcesTx(t *testing.T) {
	p := nestedmutations.New(nil)
	_, built := setupWith(t, plugin.NewRegistry(p))
	for query, want := range map[string]bool{
		`mutation { updateUserById(id: 1, patch: {postsByAuthorId: {create: [{title: "x"}]}}) { user { id } } }`:                                  true,
		`mutation { updateUserById(id: 1, patch: {fullName: "x"}) { user { id } } }`:                                                              false,
		`mutation { ...M } fragment M on Mutation { createUser(input: {email: "e", postsByAuthorId: {create: [{title: "x"}]}}) { user { id } } }`: true,
		`mutation { ... on Mutation { createUser(input: {email: "e", postsByAuthorId: {create: [{title: "x"}]}}) { user { id } } } }`:             true,
		`mutation { ... @skip(if: true) { createUser(input: {email: "e", postsByAuthorId: {create: [{title: "x"}]}}) { user { id } } } }`:         false,
	} {
		doc, perr := parser.ParseQuery(&ast.Source{Input: query})
		if perr != nil {
			t.Fatal(perr)
		}
		if errs := validator.ValidateWithRules(built.Schema, doc, nil); len(errs) > 0 {
			t.Fatal(errs)
		}
		op := &exec.Operation{Type: ast.Mutation, Document: doc, Definition: doc.Operations[0], Built: built}
		if _, err := p.BeforeOperation(context.Background(), op); err != nil {
			t.Fatal(err)
		}
		if op.ForceTx != want {
			t.Errorf("%s: ForceTx = %v, want %v", query, op.ForceTx, want)
		}
	}
}

func TestPatchMustKeepReferencedKey(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "memos", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "owner_id", Position: 2, PGType: "text", TypeSchema: "pg_catalog"},
		},
		PrimaryKey: &introspect.Constraint{Name: "memos_pkey", Columns: []string{"id"}},
		ForeignKeys: []*introspect.ForeignKey{{
			Name: "memos_owner_id_fkey", Columns: []string{"owner_id"},
			RefSchema: "public", RefTable: "users", RefColumns: []string{"email"},
		}},
	})
	reg := plugin.NewRegistry(nestedmutations.New(nil))
	b := schema.New(cat, inflect.Default, schema.Options{FilterIndexedOnly: true})
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	err = compileErr(t, reg, built, `mutation { updateUserById(id: 1, patch: {email: "new@b.c", memosByOwnerId: {disconnectById: [{id: 1}]}}) { user { id } } }`)
	if err == nil || !strings.Contains(err.Error(), "memosByOwnerId: the patch changes a column the relation references") {
		t.Fatalf("err = %v", err)
	}
	compileRoot(t, reg, built, `mutation { updateUserById(id: 1, patch: {fullName: "n", memosByOwnerId: {disconnectById: [{id: 1}]}}) { user { id } } }`)
}

func compileErr(t *testing.T, reg *plugin.Registry, built *schema.Built, query string) error {
	t.Helper()
	doc, perr := parser.ParseQuery(&ast.Source{Input: query})
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	if errs := validator.ValidateWithRules(built.Schema, doc, nil); len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	fn := reg.CompileChain(nil, compile.New(built).Compile)
	_, err := fn(context.Background(), &compile.Request{
		Field: doc.Operations[0].SelectionSet[0].(*ast.Field), Built: built, MaxDepth: 15,
	})
	var uerr *compile.UserError
	if err != nil && !errors.As(err, &uerr) {
		t.Errorf("error %v is not client-safe", err)
	}
	return err
}

// sdlBlock returns the SDL type definition starting at header.
func sdlBlock(sdl, header string) string {
	i := strings.Index(sdl, header)
	if i < 0 {
		return ""
	}
	rest := sdl[i:]
	if j := strings.Index(rest, "\n}"); j >= 0 {
		return rest[:j+2]
	}
	return rest
}

// TestStatePerSchema builds a second schema without the posts relation on
// the same plugin, as a second contract or a watch-mode rebuild does: the
// first schema's operations keep their nested fields.
func TestStatePerSchema(t *testing.T) {
	p := nestedmutations.New(nil)
	reg, first := setupWith(t, plugin.NewRegistry(p))
	cat := testutil.FixtureCatalog()
	cat.Tables = slices.DeleteFunc(cat.Tables, func(t *introspect.Table) bool { return t.Name == "posts" || t.Name == "comments" })
	b := schema.New(cat, inflect.Default, schema.Options{FilterIndexedOnly: true})
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	query := `mutation { updateUserById(id: 1, patch: {postsByAuthorId: {create: [{title: "x"}]}}) { user { id } } }`
	doc, perr := parser.ParseQuery(&ast.Source{Input: query})
	if perr != nil {
		t.Fatal(perr)
	}
	if errs := validator.ValidateWithRules(first.Schema, doc, nil); len(errs) > 0 {
		t.Fatal(errs)
	}
	op := &exec.Operation{Type: ast.Mutation, Document: doc, Definition: doc.Operations[0], Built: first}
	if _, err := p.BeforeOperation(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if !op.ForceTx {
		t.Error("the first schema's nested field was forgotten after a second build")
	}
	if err := compileErr(t, reg, first, query); err != nil {
		t.Errorf("compiling against the first schema: %v", err)
	}
}
