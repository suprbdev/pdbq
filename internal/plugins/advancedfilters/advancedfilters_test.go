package advancedfilters_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/introspect"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/advancedfilters"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/testutil"
)

// setup builds the fixture schema with the plugin applied.
func setup(t *testing.T, settings map[string]any) *schema.Built {
	return setupCatalog(t, settings, testutil.FixtureCatalog())
}

func setupCatalog(t *testing.T, settings map[string]any, cat *introspect.Catalog) *schema.Built {
	t.Helper()
	b := schema.New(cat, inflect.Default, schema.Options{
		FilterIndexedOnly: true,
		Functions:         true,
	})
	reg := plugin.NewRegistry(advancedfilters.New(settings))
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func compileRoot(t *testing.T, built *schema.Built, query string) *compile.Statement {
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
	stmt, err := compile.New(built).Compile(context.Background(), &compile.Request{
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

func TestSchemaSurface(t *testing.T) {
	built := setup(t, nil)
	sdl := built.SDL

	for _, want := range []string{
		"author: UserFilter",                // forward relation on PostFilter
		"postsByAuthorId: PostToManyFilter", // reverse relation on UserFilter
		"some: PostFilter",                  // quantifier wrapper
		"postCount: BigIntFilterOps",        // computed filter (int8 -> BigInt)
		"POST_COUNT_ASC",                    // computed ordering
		"POST_COUNT_DESC",
	} {
		if !strings.Contains(sdl, want) {
			t.Errorf("SDL missing %q", want)
		}
	}
	// posts_excerpt takes an extra argument: not filterable/orderable.
	for _, reject := range []string{"excerpt: StringFilterOps", "EXCERPT_ASC"} {
		if strings.Contains(sdl, reject) {
			t.Errorf("SDL must not contain %q", reject)
		}
	}
}

func TestSettingsToggles(t *testing.T) {
	built := setup(t, map[string]any{"relations": false})
	if strings.Contains(built.SDL, "PostToManyFilter") {
		t.Error("relations: false must drop relation filter fields")
	}
	if !strings.Contains(built.SDL, "postCount: BigIntFilterOps") {
		t.Error("relations: false must keep computed filters")
	}

	built = setup(t, map[string]any{"computed": false})
	if strings.Contains(built.SDL, "POST_COUNT_ASC") {
		t.Error("computed: false must drop computed orderBy values")
	}
	if !strings.Contains(built.SDL, "author: UserFilter") {
		t.Error("computed: false must keep relation filters")
	}
}

// Smart comments are honored straight off the catalog, independent of the
// smart-comments plugin.
func TestSmartCommentTags(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Table("public", "posts").ForeignKeys[0].Comment = "@omit filter"
	built := setupCatalog(t, nil, cat)
	if strings.Contains(built.SDL, "author: UserFilter") || strings.Contains(built.SDL, "PostToManyFilter") {
		t.Error("@omit filter on a FK must drop both relation filter directions")
	}

	cat = testutil.FixtureCatalog()
	built = setupCatalog(t, map[string]any{"relations_opt_in": true, "computed_opt_in": true}, cat)
	for _, reject := range []string{"author: UserFilter", "postCount: BigIntFilterOps", "POST_COUNT_ASC"} {
		if strings.Contains(built.SDL, reject) {
			t.Errorf("opt-in mode must suppress untagged surface %q", reject)
		}
	}

	cat = testutil.FixtureCatalog()
	cat.Table("public", "posts").ForeignKeys[0].Comment = "@filterable"
	built = setupCatalog(t, map[string]any{"relations_opt_in": true}, cat)
	if !strings.Contains(built.SDL, "author: UserFilter") {
		t.Error("@filterable must opt a FK into relations_opt_in mode")
	}
}

func TestForwardRelationFilter(t *testing.T) {
	built := setup(t, nil)
	stmt := compileRoot(t, built, `{
		allPosts(filter: {author: {email: {equalTo: "ada@example.com"}}}) { nodes { id } }
	}`)
	sql := stmt.SQL
	for _, want := range []string{
		`EXISTS (SELECT 1 FROM "public"."users" AS`,
		`."id" = `, // join on FK ref column
		`."email" = $`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "ada@example.com") {
		t.Errorf("literal leaked into SQL:\n%s", sql)
	}
}

func TestBackwardRelationQuantifiers(t *testing.T) {
	built := setup(t, nil)
	stmt := compileRoot(t, built, `{
		allUsers(filter: {postsByAuthorId: {
			some:  {title: {startsWith: "A"}}
			none:  {title: {equalTo: "x"}}
			every: {title: {notEqualTo: "draft"}}
		}}) { nodes { id } }
	}`)
	sql := stmt.SQL
	if got := strings.Count(sql, `EXISTS (SELECT 1 FROM "public"."posts" AS`); got != 3 {
		t.Errorf("want 3 EXISTS subqueries, got %d:\n%s", got, sql)
	}
	if got := strings.Count(sql, "NOT EXISTS"); got != 2 {
		t.Errorf("want 2 NOT EXISTS (none + every), got %d:\n%s", got, sql)
	}
	if !strings.Contains(sql, "AND NOT (") {
		t.Errorf("every must negate the inner condition:\n%s", sql)
	}
}

func TestNestedRelationFilter(t *testing.T) {
	built := setup(t, nil)
	// Two levels: users having a post whose author (self-join back) matches.
	stmt := compileRoot(t, built, `{
		allUsers(filter: {postsByAuthorId: {some: {author: {mood: {equalTo: HAPPY}}}}}) { nodes { id } }
	}`)
	if got := strings.Count(stmt.SQL, "EXISTS (SELECT 1 FROM"); got != 2 {
		t.Errorf("want 2 nested EXISTS, got %d:\n%s", got, stmt.SQL)
	}
	// Enum coerced to its PG label as a parameter.
	found := false
	for _, a := range stmt.Args {
		if a == "happy" {
			found = true
		}
	}
	if !found {
		t.Errorf("enum arg not coerced: %v", stmt.Args)
	}
}

func TestComputedFilterAndOrder(t *testing.T) {
	built := setup(t, nil)
	stmt := compileRoot(t, built, `{
		allUsers(filter: {postCount: {greaterThan: "1"}}, orderBy: [POST_COUNT_DESC], first: 2) {
			nodes { id }
		}
	}`)
	sql := stmt.SQL
	call := `"public"."users_post_count"(ROW(`
	if !strings.Contains(sql, call+"") {
		t.Errorf("SQL missing computed call:\n%s", sql)
	}
	if !strings.Contains(sql, ") > $") {
		t.Errorf("SQL missing computed comparison:\n%s", sql)
	}
	if !strings.Contains(sql, "DESC") || !strings.Contains(sql, `ORDER BY "public"."users_post_count"(ROW(`) {
		t.Errorf("SQL missing computed ORDER BY:\n%s", sql)
	}
	// PK tiebreaker still appended.
	if !strings.Contains(sql, `."id" ASC`) {
		t.Errorf("SQL missing PK tiebreaker:\n%s", sql)
	}
}

func TestComputedOrderWithKeysetCursor(t *testing.T) {
	built := setup(t, nil)
	cursor := base64.StdEncoding.EncodeToString([]byte(`["User",1]`))
	stmt := compileRoot(t, built, fmt.Sprintf(`{
		allUsers(orderBy: [POST_COUNT_ASC], first: 2, after: %q) {
			nodes { id }
			pageInfo { hasNextPage endCursor }
		}
	}`, cursor))
	sql := stmt.SQL
	// The anchor subquery must evaluate the computed expression for the
	// cursor row, and the keyset predicate must compare it (nullable form).
	if !strings.Contains(sql, `"public"."users_post_count"(ROW(__a.`) {
		t.Errorf("anchor missing computed expression:\n%s", sql)
	}
	if !strings.Contains(sql, "IS NOT DISTINCT FROM") {
		t.Errorf("computed keyset predicate must use null-safe equality:\n%s", sql)
	}
}

func TestRelationFilterOnBackwardRelationField(t *testing.T) {
	built := setup(t, nil)
	// Relation filter applied inside a nested child connection.
	stmt := compileRoot(t, built, `{
		userById(id: 1) {
			postsByAuthorId(filter: {author: {email: {equalTo: "x"}}}) { nodes { id } }
		}
	}`)
	if !strings.Contains(stmt.SQL, `EXISTS (SELECT 1 FROM "public"."users" AS`) {
		t.Errorf("nested connection filter missing EXISTS:\n%s", stmt.SQL)
	}
}
