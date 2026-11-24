package schema_test

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/simplenames"
	"github.com/suprbdev/pdbq/internal/schema"
)

func buildFixture(t *testing.T, cat *introspect.Catalog, inflector inflect.Next) *schema.Built {
	t.Helper()
	built, err := schema.New(cat, inflector, schema.Options{FilterIndexedOnly: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func wantBlocks(t *testing.T, sdl string, blocks ...string) {
	t.Helper()
	for _, want := range blocks {
		header, _, _ := strings.Cut(want, "{")
		if got := sdlType(sdl, header); got != want {
			t.Errorf("SDL block %q:\n%s\nwant:\n%s", header, got, want)
		}
	}
}

// TestMutationShapes pins every table mutation of users to PostGraphile
// v4's shape: one input argument per mutation, clientMutationId only in
// that input, the row values in UserInput / UserPatch.
func TestMutationShapes(t *testing.T) {
	built := buildFixture(t, testutil.FixtureCatalog(), inflect.Default)
	mutation := sdlType(built.SDL, "type Mutation")
	for _, want := range []string{
		"  createUser(input: CreateUserInput!): CreateUserPayload!\n",
		"  createManyUsers(input: CreateManyUsersInput!): CreateManyUsersPayload!\n",
		"  upsertUserByEmail(input: UpsertUserByEmailInput!): UpsertUserPayload!\n",
		"  updateUserById(input: UpdateUserByIdInput!): UpdateUserPayload!\n",
		"  updateUserByEmail(input: UpdateUserByEmailInput!): UpdateUserPayload!\n",
		"  updateManyUsers(input: UpdateManyUsersInput!): UpdateManyUsersPayload!\n",
		"  deleteUserById(input: DeleteUserByIdInput!): DeleteUserPayload!\n",
		"  deleteUserByEmail(input: DeleteUserByEmailInput!): DeleteUserPayload!\n",
		"  deleteManyUsers(input: DeleteManyUsersInput!): DeleteManyUsersPayload!\n",
	} {
		if !strings.Contains(mutation, want) {
			t.Errorf("Mutation missing %q", want)
		}
	}
	for _, reject := range []string{"createUsers(", "updateUsers(", "deleteUsers(", "patch:", "clientMutationId: String)"} {
		if strings.Contains(mutation, reject) {
			t.Errorf("Mutation has old shape %q", reject)
		}
	}
	wantBlocks(t, built.SDL,
		"input CreateUserInput {\n  clientMutationId: String\n  \"\"\"The User to create.\"\"\"\n  user: UserInput!\n}",
		"input CreateManyUsersInput {\n  clientMutationId: String\n  \"\"\"The User rows to create.\"\"\"\n  users: [UserInput!]!\n}",
		"input UpsertUserByEmailInput {\n  clientMutationId: String\n  \"\"\"The User to create or update.\"\"\"\n  user: UserInput!\n}",
		"input UpdateUserByIdInput {\n  clientMutationId: String\n  id: Int!\n  \"\"\"The values to set on the User.\"\"\"\n  userPatch: UserPatch!\n}",
		"input UpdateUserByEmailInput {\n  clientMutationId: String\n  email: String!\n  \"\"\"The values to set on the User.\"\"\"\n  userPatch: UserPatch!\n}",
		"input UpdateManyUsersInput {\n  clientMutationId: String\n  \"\"\"The filter selecting the User rows to update.\"\"\"\n  filter: UserFilter!\n  \"\"\"The values to set on every matching User.\"\"\"\n  userPatch: UserPatch!\n}",
		"input DeleteUserByIdInput {\n  clientMutationId: String\n  id: Int!\n}",
		"input DeleteUserByEmailInput {\n  clientMutationId: String\n  email: String!\n}",
		"input DeleteManyUsersInput {\n  clientMutationId: String\n  \"\"\"The filter selecting the User rows to delete.\"\"\"\n  filter: UserFilter!\n}",
	)
	for _, name := range []string{"input UserInput ", "input UserPatch "} {
		in := sdlType(built.SDL, name)
		if in == "" || strings.Contains(in, "clientMutationId") || !strings.Contains(in, "fullName: String\n") {
			t.Errorf("%s must hold the columns and no clientMutationId:\n%s", name, in)
		}
	}
	if !strings.Contains(sdlType(built.SDL, "input UserInput "), "email: String!\n") {
		t.Error("UserInput.email must stay required")
	}
	if !strings.Contains(sdlType(built.SDL, "input UserPatch "), "email: String\n") {
		t.Error("UserPatch.email must be optional")
	}
	for _, reject := range []string{"UserCreateInput", "UserUpdateInput", "CreateManyUserPayload"} {
		if strings.Contains(built.SDL, reject) {
			t.Errorf("SDL still declares %s", reject)
		}
	}
	for _, payload := range []string{"CreateManyUsersPayload", "UpdateManyUsersPayload", "DeleteManyUsersPayload"} {
		if p := sdlType(built.SDL, "type "+payload+" "); !strings.Contains(p, "users: [User!]!") || !strings.Contains(p, "clientMutationId: String") {
			t.Errorf("%s:\n%s", payload, p)
		}
	}
	meta := built.Meta["Mutation"]
	for field, values := range map[string]string{
		"createUser": "user", "createManyUsers": "users", "upsertUserByEmail": "user",
		"updateUserById": "userPatch", "updateManyUsers": "userPatch", "deleteUserById": "",
	} {
		if m := meta[field]; m == nil || m.Values != values {
			t.Errorf("Mutation.%s meta Values = %+v, want %q", field, m, values)
		}
	}
	if cols := built.InputColumns["UpdateUserByEmailInput"]; cols["email"] != "email" {
		t.Errorf("UpdateUserByEmailInput key binding = %v", cols)
	}
}

func TestMutationShapesSimpleNames(t *testing.T) {
	reg := plugin.NewRegistry(simplenames.New(simplenames.Settings{Enabled: true}))
	cat := testutil.FixtureCatalog()
	if err := reg.TransformCatalog(context.Background(), nil, cat); err != nil {
		t.Fatal(err)
	}
	built := buildFixture(t, cat, reg.Inflector(nil))
	mutation := sdlType(built.SDL, "type Mutation")
	for _, want := range []string{
		"  updateUser(input: UpdateUserInput!): UpdateUserPayload!\n",
		"  deleteUser(input: DeleteUserInput!): DeleteUserPayload!\n",
		"  updateUserByEmail(input: UpdateUserByEmailInput!): UpdateUserPayload!\n",
		"  createManyUsers(input: CreateManyUsersInput!): CreateManyUsersPayload!\n",
	} {
		if !strings.Contains(mutation, want) {
			t.Errorf("Mutation missing %q", want)
		}
	}
	wantBlocks(t, built.SDL,
		"input UpdateUserInput {\n  clientMutationId: String\n  id: Int!\n  \"\"\"The values to set on the User.\"\"\"\n  userPatch: UserPatch!\n}",
		"input DeleteUserInput {\n  clientMutationId: String\n  id: Int!\n}",
	)
}

// newsCatalog adds a news table, whose plural is its singular.
func newsCatalog() *introspect.Catalog {
	cat := testutil.FixtureCatalog()
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "news", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true, GeneratedKind: introspect.GeneratedIdentityAlways},
			{Name: "headline", Position: 2, PGType: "text", TypeSchema: "pg_catalog", NotNull: true},
		},
		PrimaryKey: &introspect.Constraint{Name: "news_pkey", Columns: []string{"id"}},
		Indexes:    []*introspect.Index{{Name: "news_pkey", Columns: []string{"id"}, Unique: true}},
	})
	return cat
}

func TestUncountableTableMutations(t *testing.T) {
	built := buildFixture(t, newsCatalog(), inflect.Default)
	mutation := sdlType(built.SDL, "type Mutation")
	for _, want := range []string{
		"  createNews(input: CreateNewsInput!): CreateNewsPayload!\n",
		"  createManyNews(input: CreateManyNewsInput!): CreateManyNewsPayload!\n",
		"  updateNewsById(input: UpdateNewsByIdInput!): UpdateNewsPayload!\n",
		"  updateManyNews(input: UpdateManyNewsInput!): UpdateManyNewsPayload!\n",
		"  deleteNewsById(input: DeleteNewsByIdInput!): DeleteNewsPayload!\n",
		"  deleteManyNews(input: DeleteManyNewsInput!): DeleteManyNewsPayload!\n",
	} {
		if !strings.Contains(mutation, want) {
			t.Errorf("Mutation missing %q", want)
		}
	}
	wantBlocks(t, built.SDL,
		"input CreateManyNewsInput {\n  clientMutationId: String\n  \"\"\"The News rows to create.\"\"\"\n  news: [NewsInput!]!\n}",
		"input CreateNewsInput {\n  clientMutationId: String\n  \"\"\"The News to create.\"\"\"\n  news: NewsInput!\n}",
	)
}

func TestTableMutationCollisionsAreBuildErrors(t *testing.T) {
	build := func(cat *introspect.Catalog, inflector inflect.Next) error {
		_, err := schema.New(cat, inflector, schema.Options{FilterIndexedOnly: true}).Build()
		return err
	}
	t.Run("root field", func(t *testing.T) {
		createManyIsCreate := func(kind inflect.Kind, in inflect.Input) string {
			if kind == inflect.KindCreateManyMutation {
				kind = inflect.KindCreateMutation
			}
			return inflect.Default(kind, in)
		}
		err := build(newsCatalog(), createManyIsCreate)
		want := `schema: mutation name collision: "createNews" is both the create mutation of table public.news and the bulk create mutation of table public.news; rename the table or column with the @name smart comment, or the generated name in an InflectionHook plugin`
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})
	t.Run("same table name in two schemas", func(t *testing.T) {
		cat := newsCatalog()
		other := *cat.Table("public", "news")
		other.Schema = "app"
		cat.Tables = append(cat.Tables, &other)
		err := build(cat, inflect.Default)
		for _, want := range []string{
			`schema: type name collision: "NewsInput" is both the create values of table public.news and the create values of table app.news`,
			`schema: mutation name collision: "createNews" is both the create mutation of table public.news and the create mutation of table app.news`,
		} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want %q", err, want)
			}
		}
	})
	t.Run("input type taken by another type", func(t *testing.T) {
		cat := newsCatalog()
		cat.Enums = append(cat.Enums, &introspect.Enum{Schema: "public", Name: "news_patch", Values: []string{"a"}})
		err := build(cat, inflect.Default)
		want := `schema: type name collision: "NewsPatch" is both an enum type and the patch of table public.news`
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})
	t.Run("input field", func(t *testing.T) {
		cat := newsCatalog()
		news := cat.Table("public", "news")
		news.Columns = append(news.Columns, &introspect.Column{Name: "news_patch", Position: 3, PGType: "text", TypeSchema: "pg_catalog", NotNull: true})
		news.Uniques = append(news.Uniques, &introspect.Constraint{Name: "news_patch_key", Columns: []string{"news_patch"}})
		err := build(cat, inflect.Default)
		want := `schema: input field collision: UpdateNewsByNewsPatchInput (the input of the update mutation of table public.news) has two fields named "newsPatch"`
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})
}

// TestCreateWithoutSettableColumns covers a table whose every column
// PostgreSQL computes: its create input has no row field (the insert takes
// the defaults), and there is no bulk create, patch or update.
func TestCreateWithoutSettableColumns(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "ticks", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true, GeneratedKind: introspect.GeneratedIdentityAlways},
		},
		PrimaryKey: &introspect.Constraint{Name: "ticks_pkey", Columns: []string{"id"}},
	})
	built := buildFixture(t, cat, inflect.Default)
	wantBlocks(t, built.SDL, "input CreateTickInput {\n  clientMutationId: String\n}")
	if m := built.Meta["Mutation"]["createTick"]; m == nil || m.Values != "" {
		t.Errorf("createTick meta = %+v", m)
	}
	for _, reject := range []string{"input TickInput ", "input TickPatch ", "createManyTicks(", "updateTickById("} {
		if strings.Contains(built.SDL, reject) {
			t.Errorf("SDL has %q", reject)
		}
	}
	if !strings.Contains(built.SDL, "deleteTickById(input: DeleteTickByIdInput!)") {
		t.Error("deleteTickById missing")
	}
}

// TestPrunedRowValues covers SchemaHooks emptying the row values or
// removing a filter: the mutations needing them go away with their input
// and payload types, and a create inserts the defaults.
func TestPrunedRowValues(t *testing.T) {
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true})
	b.Inputs["UserInput"].Fields = nil
	b.Inputs["PostPatch"].Fields = nil
	b.RemoveInputType("CommentFilter")
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	wantBlocks(t, built.SDL, "input CreateUserInput {\n  clientMutationId: String\n}")
	if m := built.Meta["Mutation"]["createUser"]; m == nil || m.Values != "" {
		t.Errorf("createUser meta = %+v", m)
	}
	for _, reject := range []string{
		"input UserInput ", "createManyUsers(", "upsertUserByEmail(", "input CreateManyUsersInput ",
		"input UpsertUserByEmailInput ", "type CreateManyUsersPayload ", "type UpsertUserPayload ",
		"input PostPatch ", "updatePostById(", "updateManyPosts(", "input UpdatePostByIdInput ", "type UpdatePostPayload ",
		"updateManyComments(", "deleteManyComments(", "input UpdateManyCommentsInput ", "input DeleteManyCommentsInput ",
	} {
		if strings.Contains(built.SDL, reject) {
			t.Errorf("SDL has %q", reject)
		}
	}
	for _, want := range []string{"updateUserById(", "deletePostById(", "updateCommentById("} {
		if !strings.Contains(built.SDL, want) {
			t.Errorf("SDL missing %q", want)
		}
	}
}

func TestPluralRowsField(t *testing.T) {
	cat := newsCatalog()
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "user_accounts", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true},
		Columns:    []*introspect.Column{{Name: "name", Position: 1, PGType: "text", TypeSchema: "pg_catalog", NotNull: true}},
	})
	built := buildFixture(t, cat, inflect.Default)
	for _, want := range []string{"  news: [News!]!\n", "  userAccounts: [UserAccount!]!\n"} {
		if !strings.Contains(built.SDL, want) {
			t.Errorf("SDL missing %q", want)
		}
	}
	if !strings.Contains(sdlType(built.SDL, "input CreateManyUserAccountsInput "), "userAccounts: [UserAccountInput!]!") {
		t.Error("CreateManyUserAccountsInput.userAccounts missing")
	}
}

func TestFunctionRowArgumentInput(t *testing.T) {
	cat := testutil.FixtureCatalog()
	built, err := schema.New(cat, inflect.Default, schema.Options{FilterIndexedOnly: true, Functions: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	if built.RowInputForPG["public.users"] != "UserRowInput" {
		t.Fatalf("RowInputForPG = %v", built.RowInputForPG)
	}
	if !strings.Contains(sdlType(built.SDL, "input UserRowInput "), "  id: Int\n") {
		t.Error("UserRowInput must hold every column")
	}
}
