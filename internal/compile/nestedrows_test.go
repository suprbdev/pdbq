package compile_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/aggregates"
	"github.com/suprbdev/pdbq/internal/plugins/smartcomments"
	"github.com/suprbdev/pdbq/internal/schema"
)

// nestedRowsCatalog is the fixture catalog with rows nested in other values:
// posts.body is tagged @omit, posts.cover is a raster, vaults is a fully
// omitted table, post_box (label, p posts, r raster) and vault_box (label,
// v vaults) are composites holding rows, only_vault (v vaults) one holding
// nothing else, crate (box post_box, vb
// vault_box) nests them, and public.holders has a column of each plus
// posts[] and posts. post_arr returns posts[], post_tab TABLE (n, p posts),
// vault_boxes SETOF vault_box, users_post_arr(users) posts[] (a computed
// column) and grab_posts posts[] (volatile).
func nestedRowsCatalog() *introspect.Catalog {
	cat := testutil.FixtureCatalog()
	col := func(pos int, name, typ, typeSchema string) *introspect.Column {
		return &introspect.Column{Name: name, Position: pos, PGType: typ, TypeSchema: typeSchema, BaseTypeSchema: typeSchema}
	}
	array := func(c *introspect.Column) *introspect.Column {
		c.IsArray = true
		return c
	}
	posts := cat.Table("public", "posts")
	posts.Column("body").Comment = "@omit"
	posts.Columns = append(posts.Columns, col(len(posts.Columns)+1, "cover", "raster", "public"))
	all := introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true}
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "vaults", Kind: introspect.RelTable, Privileges: all, Comment: "@omit",
		Columns: []*introspect.Column{
			col(1, "id", "int4", "pg_catalog"),
			col(2, "note", "text", "pg_catalog"),
		},
		PrimaryKey: &introspect.Constraint{Name: "vaults_pkey", Columns: []string{"id"}},
	}, &introspect.Table{
		Schema: "public", Name: "holders", Kind: introspect.RelTable, Privileges: all,
		Columns: []*introspect.Column{
			col(1, "id", "int4", "pg_catalog"),
			col(2, "box", "post_box", "public"),
			col(3, "vbox", "vault_box", "public"),
			col(4, "crate", "crate", "public"),
			array(col(5, "post_list", "_posts", "public")),
			col(6, "post", "posts", "public"),
		},
		PrimaryKey: &introspect.Constraint{Name: "holders_pkey", Columns: []string{"id"}},
		Indexes: []*introspect.Index{
			{Name: "holders_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
			{Name: "holders_post_idx", Columns: []string{"post"}, Method: "btree"},
			{Name: "holders_box_idx", Columns: []string{"box"}, Method: "btree"},
		},
	})
	cat.Composites = append(cat.Composites, &introspect.Composite{
		Schema: "public", Name: "post_box",
		Fields: []*introspect.Column{col(1, "label", "text", "pg_catalog"), col(2, "p", "posts", "public"), col(3, "r", "raster", "public")},
	}, &introspect.Composite{
		Schema: "public", Name: "vault_box",
		Fields: []*introspect.Column{col(1, "label", "text", "pg_catalog"), col(2, "v", "vaults", "public")},
	}, &introspect.Composite{
		Schema: "public", Name: "only_vault",
		Fields: []*introspect.Column{col(1, "v", "vaults", "public")},
	}, &introspect.Composite{
		Schema: "public", Name: "crate",
		Fields: []*introspect.Column{col(1, "box", "post_box", "public"), col(2, "vb", "vault_box", "public")},
	})
	stable := introspect.VolatilityStable
	cat.Functions = append(cat.Functions, &introspect.Function{
		Schema: "public", Name: "post_arr",
		ReturnType: "_posts", ReturnTypeSchema: "public", Volatility: stable,
	}, &introspect.Function{
		Schema: "public", Name: "post_tab",
		ReturnColumns: []introspect.FuncArg{
			{Name: "n", PGType: "int4", TypeSchema: "pg_catalog", Mode: introspect.ArgModeTable},
			{Name: "p", PGType: "posts", TypeSchema: "public", Mode: introspect.ArgModeTable},
		},
		ReturnType: "record", ReturnTypeSchema: "pg_catalog", ReturnsSet: true, Volatility: stable,
	}, &introspect.Function{
		Schema: "public", Name: "vault_boxes",
		ReturnType: "vault_box", ReturnTypeSchema: "public", ReturnsSet: true, Volatility: stable,
	}, &introspect.Function{
		Schema: "public", Name: "vault_arr",
		ReturnType: "_vaults", ReturnTypeSchema: "public", Volatility: stable,
	}, &introspect.Function{
		Schema: "public", Name: "users_post_arr",
		Args:       []introspect.FuncArg{{Name: "u", PGType: "users", TypeSchema: "public", Mode: introspect.ArgModeIn}},
		ReturnType: "_posts", ReturnTypeSchema: "public", Volatility: stable,
	}, &introspect.Function{
		Schema: "public", Name: "grab_posts",
		ReturnType: "_posts", ReturnTypeSchema: "public", Volatility: introspect.VolatilityVolatile,
	})
	return cat
}

// buildNestedRows builds nestedRowsCatalog through the smart-comments and
// (enabled) aggregates plugins, for the nestedrow_* goldens.
func buildNestedRows(t testing.TB) *schema.Built {
	t.Helper()
	cat := nestedRowsCatalog()
	reg := plugin.NewRegistry(smartcomments.New(), aggregates.New(map[string]any{"enabled": true}, nil))
	if err := reg.TransformCatalog(context.Background(), nil, cat); err != nil {
		t.Fatalf("catalog hooks: %v", err)
	}
	b := schema.New(cat, reg.Inflector(nil), schema.Options{FilterIndexedOnly: true, Functions: true})
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatalf("plugins: %v", err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatalf("build schema: %v", err)
	}
	return built
}

// sdlBody returns the field lines of object type name in sdl, or "".
func sdlBody(sdl, name string) string {
	m := regexp.MustCompile(`(?s)\ntype ` + name + `(?: implements [^{]*)? \{\n(.*?)\n\}`).FindStringSubmatch(sdl)
	if m == nil {
		return ""
	}
	return m[1]
}

func sdlFields(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, `"`) {
			continue
		}
		name, _, _ := strings.Cut(line, ":")
		name, _, _ = strings.Cut(name, "(")
		out = append(out, name)
	}
	return out
}

// TestNestedRowsSDL checks that rows nested in arrays, composite attributes,
// function output columns and table columns are typed as the table's row
// value object, which exposes the columns the table's own type exposes
// (no @omit column, no raster), that values of an omitted table's row type
// have no field, and that row-typed columns offer no filter, ordering or
// group-by.
func TestNestedRowsSDL(t *testing.T) {
	sdl := buildNestedRows(t).SDL
	for typ, want := range map[string][]string{
		"PostRow":       {"id", "authorId", "title", "published"},
		"PostBox":       {"label", "p"},
		"VaultBox":      {"label"},
		"OnlyVault":     {schema.PlaceholderField},
		"Crate":         {"box", "vb"},
		"PostTabRecord": {"n", "p"},
	} {
		body := sdlBody(sdl, typ)
		if body == "" {
			t.Errorf("SDL lacks type %s", typ)
			continue
		}
		if got := sdlFields(body); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("type %s fields = %v, want %v", typ, got, want)
		}
	}
	for typ, field := range map[string]string{
		"Holder":        "post: PostRow",
		"PostTabRecord": "p: PostRow",
		"PostBox":       "p: PostRow",
		"User":          "postArr: [PostRow!]",
		"Query":         "postArr: [PostRow!]",
	} {
		if !strings.Contains(sdlBody(sdl, typ), field) {
			t.Errorf("type %s lacks %q", typ, field)
		}
	}
	if !strings.Contains(sdl, "postList: [PostRow!]") {
		t.Error("Holder.postList is not [PostRow!]")
	}
	for _, absent := range []string{"VaultRow", "vaultArr", "type Vault "} {
		if strings.Contains(sdl, absent) {
			t.Errorf("SDL mentions %q", absent)
		}
	}
	for _, typ := range []string{"HolderFilter", "HolderCondition"} {
		if regexp.MustCompile(`(?s)input ` + typ + ` \{[^}]*\b(post|box)\b`).MatchString(sdl) {
			t.Errorf("%s offers a row-typed column", typ)
		}
	}
	for _, enum := range []string{"HoldersOrderBy", "HoldersDistinctOn", "HoldersGroupBy"} {
		if regexp.MustCompile(`(?s)enum ` + enum + ` \{[^}]*\b(POST|BOX|POST_LIST)\b`).MatchString(sdl) {
			t.Errorf("%s offers a row-typed column", enum)
		}
	}
}
