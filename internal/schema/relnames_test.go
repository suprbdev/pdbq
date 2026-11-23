package schema_test

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/simplenames"
	"github.com/suprbdev/pdbq/internal/plugins/smartcomments"
	"github.com/suprbdev/pdbq/internal/schema"
)

// namingTable is a table of NOT NULL int4 columns, except the ones listed
// in text, with id as its primary key when it has an id column.
func namingTable(name string, cols []string, text []string, fks ...*introspect.ForeignKey) *introspect.Table {
	t := &introspect.Table{
		Schema: "public", Name: name, Kind: introspect.RelTable,
		Privileges:  introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		ForeignKeys: fks,
	}
	for i, c := range cols {
		typ := "int4"
		if slices.Contains(text, c) {
			typ = "text"
		}
		t.Columns = append(t.Columns, &introspect.Column{Name: c, Position: i + 1, PGType: typ, TypeSchema: "pg_catalog", NotNull: true})
	}
	if slices.Contains(cols, "id") {
		t.PrimaryKey = &introspect.Constraint{Name: name + "_pkey", Columns: []string{"id"}}
	}
	return t
}

func fkey(name string, cols []string, ref string, refCols ...string) *introspect.ForeignKey {
	if len(refCols) == 0 {
		refCols = []string{"id"}
	}
	return &introspect.ForeignKey{Name: name, Columns: cols, RefSchema: "public", RefTable: ref, RefColumns: refCols}
}

// buildNaming runs cat through smart_comments and simple_names (enabled
// when simple) and builds it, logging to log when set.
func buildNaming(t *testing.T, cat *introspect.Catalog, simple bool, log *bytes.Buffer) (*schema.Built, error) {
	t.Helper()
	reg := plugin.NewRegistry(smartcomments.New(), simplenames.New(simplenames.Settings{Enabled: simple}))
	if err := reg.TransformCatalog(context.Background(), nil, cat); err != nil {
		t.Fatal(err)
	}
	opts := schema.Options{FilterIndexedOnly: true, Functions: true}
	if log != nil {
		opts.Logger = slog.New(slog.NewTextHandler(log, nil))
	}
	b := schema.New(cat, reg.Inflector(nil), opts)
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	return b.Build()
}

// relationFields lists the relation fields of typ.
func relationFields(built *schema.Built, typ string) []string {
	var out []string
	for name, m := range built.Meta[typ] {
		if m != nil && (m.Kind == schema.KindRelationForward || m.Kind == schema.KindRelationBackward) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func namingCatalog() *introspect.Catalog {
	cat := testutil.FixtureCatalog()
	cat.Tables = append(cat.Tables,
		namingTable("memos", []string{"id", "user_email"}, []string{"user_email"},
			fkey("memos_user_email_fkey", []string{"user_email"}, "users", "email")),
		namingTable("transfers", []string{"id", "from_id", "to_id", "created_by", "memo_id"}, nil,
			fkey("transfers_from_id_fkey", []string{"from_id"}, "users"),
			fkey("transfers_to_id_fkey", []string{"to_id"}, "users"),
			fkey("transfers_created_by_fkey", []string{"created_by"}, "users"),
			fkey("transfers_memo_id_fkey", []string{"memo_id"}, "memos")),
		namingTable("org_members", []string{"org_id", "user_id"}, nil,
			fkey("org_members_user_id_fkey", []string{"user_id"}, "users")),
		namingTable("assignments", []string{"id", "org_id", "user_id"}, nil,
			fkey("assignments_member_fkey", []string{"org_id", "user_id"}, "org_members", "org_id", "user_id")),
		namingTable("notes", []string{"id", "ownerId"}, nil,
			fkey("notes_owner_fkey", []string{"ownerId"}, "users")),
	)
	cat.Table("public", "org_members").PrimaryKey = &introspect.Constraint{Name: "org_members_pkey", Columns: []string{"org_id", "user_id"}}
	return cat
}

func TestRelationNames(t *testing.T) {
	for _, tc := range []struct {
		name   string
		simple bool
		exact  map[string][]string
		user   []string
	}{
		{
			name: "verbose default",
			exact: map[string][]string{
				"Transfer":   {"memoByMemoId", "userByCreatedBy", "userByFromId", "userByToId"},
				"Memo":       {"transfersByMemoId", "userByUserEmail"},
				"OrgMember":  {"assignmentsByOrgIdAndUserId", "userByUserId"},
				"Assignment": {"orgMemberByOrgIdAndUserId"},
				"Note":       {"userByOwnerId"},
				"Post":       {"commentsByPostId", "userByAuthorId"},
				"Comment":    {"commentByParentId", "commentsByParentId", "postByPostId"},
			},
			user: []string{"memosByUserEmail", "notesByOwnerId", "orgMembersByUserId", "postsByAuthorId",
				"transfersByCreatedBy", "transfersByFromId", "transfersByToId"},
		},
		{
			name:   "simple_names",
			simple: true,
			exact: map[string][]string{
				"Transfer":   {"from", "memo", "to", "userByCreatedBy"},
				"Memo":       {"transfers", "userByUserEmail"},
				"OrgMember":  {"assignments", "user"},
				"Assignment": {"orgMemberByOrgIdAndUserId"},
				"Note":       {"owner"},
				"Post":       {"author", "comments"},
				"Comment":    {"comments", "parent", "post"},
			},
			user: []string{"memos", "notes", "orgMembers", "posts",
				"transfersByCreatedBy", "transfersByFromId", "transfersByToId"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			built, err := buildNaming(t, namingCatalog(), tc.simple, nil)
			if err != nil {
				t.Fatal(err)
			}
			for typ, want := range tc.exact {
				if got := relationFields(built, typ); !slices.Equal(got, want) {
					t.Errorf("%s relations = %v, want %v", typ, got, want)
				}
			}
			user := relationFields(built, "User")
			for _, want := range tc.user {
				if !slices.Contains(user, want) {
					t.Errorf("User relations %v missing %q", user, want)
				}
			}
		})
	}
}

func TestSimpleNamesDisabledIsInert(t *testing.T) {
	verbose, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry(simplenames.New(simplenames.DefaultSettings()))
	cat := testutil.FixtureCatalog()
	if err := reg.TransformCatalog(context.Background(), nil, cat); err != nil {
		t.Fatal(err)
	}
	inert, err := schema.New(cat, reg.Inflector(nil), schema.Options{FilterIndexedOnly: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	if inert.SDL != verbose.SDL {
		t.Error("simple_names with default settings changed the schema")
	}
}

func TestRelationNameConflict(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prep    func(*introspect.Catalog)
		simple  bool
		want    []string
		renamed string
	}{
		{
			name: "forward relation and column",
			prep: func(c *introspect.Catalog) {
				c.Tables = append(c.Tables, namingTable("articles", []string{"id", "author", "author_id"}, []string{"author"},
					fkey("articles_author_id_fkey", []string{"author_id"}, "users")))
			},
			simple: true,
			want: []string{`schema: type Article: relation field "author" of constraint articles_author_id_fkey conflicts with a column field of the same name`,
				"@fieldName smart comment on constraint articles_author_id_fkey", "plugins.settings.simple_names.enabled"},
			renamed: "@fieldName writer",
		},
		{
			name: "backward relation and column",
			prep: func(c *introspect.Catalog) {
				c.Tables = append(c.Tables,
					namingTable("teams", []string{"id", "members"}, []string{"members"}),
					namingTable("members", []string{"id", "team_id"}, nil, fkey("members_team_id_fkey", []string{"team_id"}, "teams")))
			},
			simple: true,
			want: []string{`type Team: relation field "members" of constraint members_team_id_fkey conflicts with a column field`,
				"@foreignFieldName smart comment on constraint members_team_id_fkey"},
			renamed: "@foreignFieldName crew",
		},
		{
			name: "two relations",
			prep: func(c *introspect.Catalog) {
				c.Tables = append(c.Tables,
					namingTable("teams", []string{"id", "members_id"}, nil, fkey("teams_members_id_fkey", []string{"members_id"}, "members")),
					namingTable("members", []string{"id", "team_id"}, nil, fkey("members_team_id_fkey", []string{"team_id"}, "teams")))
			},
			simple: true,
			want: []string{`type Team: relation field "members" of constraint members_team_id_fkey conflicts with the relation of constraint teams_members_id_fkey`,
				"@foreignFieldName smart comment on constraint members_team_id_fkey"},
			renamed: "@foreignFieldName crew",
		},
		{
			name: "relation and computed column",
			prep: func(c *introspect.Catalog) {
				c.Functions = append(c.Functions, &introspect.Function{
					Schema: "public", Name: "posts_author",
					Args:       []introspect.FuncArg{{Name: "p", PGType: "posts", TypeSchema: "public"}},
					ReturnType: "text", ReturnTypeSchema: "pg_catalog", Volatility: introspect.VolatilityStable,
				})
			},
			simple: true,
			want: []string{`type Post: relation field "author" of constraint posts_author_id_fkey conflicts with the computed column public.posts_author`,
				"@fieldName smart comment on constraint posts_author_id_fkey or the function with @name"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat := testutil.FixtureCatalog()
			tc.prep(cat)
			_, err := buildNaming(t, cat, tc.simple, nil)
			if err == nil {
				t.Fatal("conflicting relation name built")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q\nmissing %q", err, want)
				}
			}
			cat = testutil.FixtureCatalog()
			tc.prep(cat)
			if _, err := buildNaming(t, cat, !tc.simple, nil); err != nil {
				t.Errorf("other naming mode: %v", err)
			}
			if tc.renamed == "" {
				return
			}
			cat = testutil.FixtureCatalog()
			tc.prep(cat)
			for _, tbl := range cat.Tables {
				for _, fk := range tbl.ForeignKeys {
					if strings.Contains(tc.want[0], "constraint "+fk.Name+" ") {
						fk.Comment = tc.renamed
					}
				}
			}
			if _, err := buildNaming(t, cat, tc.simple, nil); err != nil {
				t.Errorf("with %s: %v", tc.renamed, err)
			}
		})
	}
}

func TestPayloadRelationOmittedOnConflict(t *testing.T) {
	prep := func() *introspect.Catalog {
		cat := testutil.FixtureCatalog()
		cat.Tables = append(cat.Tables,
			namingTable("saved_queries", []string{"id"}, nil),
			namingTable("runs", []string{"id", "query_id"}, nil, fkey("runs_query_id_fkey", []string{"query_id"}, "saved_queries")),
			namingTable("logs", []string{"id", "log_edge_id", "deleted_log_node_id_id"}, nil,
				fkey("logs_log_edge_id_fkey", []string{"log_edge_id"}, "users"),
				fkey("logs_deleted_fkey", []string{"deleted_log_node_id_id"}, "users")),
		)
		return cat
	}
	var log bytes.Buffer
	built, err := buildNaming(t, prep(), true, &log)
	if err != nil {
		t.Fatal(err)
	}
	if run := sdlType(built.SDL, "type Run "); !strings.Contains(run, "query: SavedQuery") {
		t.Errorf("Run must keep its query relation:\n%s", run)
	}
	if m := built.Meta["CreateRunPayload"]["query"]; m == nil || m.Kind != schema.KindPayloadQuery {
		t.Errorf("CreateRunPayload.query must stay the Query root: %+v", m)
	}
	if got := relationFields(built, "CreateRunPayload"); len(got) != 0 {
		t.Errorf("CreateRunPayload relations = %v, want none", got)
	}
	if m := built.Meta["CreateLogPayload"]["logEdge"]; m == nil || m.Kind != schema.KindPayloadEdge {
		t.Errorf("CreateLogPayload.logEdge must stay the edge: %+v", m)
	}
	if m := built.Meta["DeleteLogPayload"]["deletedLogNodeId"]; m == nil || m.Kind != schema.KindPayloadDeletedNodeID {
		t.Errorf("DeleteLogPayload.deletedLogNodeId must stay the deleted nodeId: %+v", m)
	}
	if got := relationFields(built, "DeleteLogPayload"); len(got) != 0 {
		t.Errorf("DeleteLogPayload relations = %v, want none", got)
	}
	if got := relationFields(built, "Log"); !slices.Equal(got, []string{"deletedLogNodeId", "logEdge"}) {
		t.Errorf("Log relations = %v", got)
	}
	for _, want := range []string{
		"relation left out of mutation payload", "type=CreateRunPayload field=query constraint=runs_query_id_fkey",
		"type=CreateLogPayload field=logEdge", "type=DeleteLogPayload field=deletedLogNodeId",
	} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log missing %q:\n%s", want, log.String())
		}
	}

	built, err = buildNaming(t, prep(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := relationFields(built, "CreateRunPayload"); !slices.Equal(got, []string{"savedQueryByQueryId"}) {
		t.Errorf("verbose CreateRunPayload relations = %v", got)
	}
}
