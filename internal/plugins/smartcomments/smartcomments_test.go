package smartcomments_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/introspect"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/advancedfilters"
	"github.com/suprbdev/pdbq/internal/plugins/nestedmutations"
	"github.com/suprbdev/pdbq/internal/plugins/simplenames"
	"github.com/suprbdev/pdbq/internal/plugins/smartcomments"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/testutil"
)

// build runs the full pipeline (catalog hooks -> inflection -> schema hooks)
// over a fixture catalog mutated by prep, with the smart-comments plugin plus
// any extras registered.
func build(t *testing.T, prep func(*introspect.Catalog), extra ...plugin.Plugin) *schema.Built {
	t.Helper()
	cat := testutil.FixtureCatalog()
	if prep != nil {
		prep(cat)
	}
	reg := plugin.NewRegistry(append([]plugin.Plugin{smartcomments.New()}, extra...)...)
	if err := reg.TransformCatalog(context.Background(), nil, cat); err != nil {
		t.Fatal(err)
	}
	b := schema.New(cat, reg.Inflector(nil), schema.Options{
		FilterIndexedOnly: true,
		Functions:         true,
	})
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func wantSDL(t *testing.T, sdl string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(sdl, w) {
			t.Errorf("SDL missing %q", w)
		}
	}
}

func rejectSDL(t *testing.T, sdl string, reject ...string) {
	t.Helper()
	for _, r := range reject {
		if strings.Contains(sdl, r) {
			t.Errorf("SDL must not contain %q", r)
		}
	}
}

func TestOmitTable(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		c.Table("public", "metrics").Comment = "@omit"
	})
	rejectSDL(t, built.SDL, "Metric", "allMetrics")
}

func TestOmitTableActions(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		c.Table("public", "users").Comment = "@omit update,delete"
		c.Table("public", "posts").Comment = "@omit all"
	})
	sdl := built.SDL
	wantSDL(t, sdl, "createUser(", "postById(")
	rejectSDL(t, sdl, "updateUserById(", "deleteUserById(", "allPosts(")
	// Backward relation survives @omit all.
	wantSDL(t, sdl, "postsByAuthorId(")
}

func TestOmitTableReadAndMany(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		c.Table("public", "posts").Comment = "@omit read,many"
	})
	sdl := built.SDL
	// No root entry points, no relation lists — but the type remains for the
	// forward relation and mutations.
	rejectSDL(t, sdl, "allPosts(", "postById(", "postsByAuthorId")
	wantSDL(t, sdl, "type Post ", "createPost(")
}

func TestOmitTableFilterAndOrder(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		c.Table("public", "posts").Comment = "@omit filter,order"
	}, advancedfilters.New(nil))
	sdl := built.SDL
	rejectSDL(t, sdl, "PostFilter", "PostsOrderBy")
	// advanced-filters must not resurrect relation filters into a deleted input.
	rejectSDL(t, sdl, "PostToManyFilter")
	wantSDL(t, sdl, "UserFilter")
}

func TestOmitColumn(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		u := c.Table("public", "users")
		u.Column("settings").Comment = "@omit"
		u.Column("email").Comment = "@omit update"
		u.Column("balance").Comment = "@omit create,read"
	})
	sdl := built.SDL
	rejectSDL(t, sdl, "settings: JSON")
	// email still readable/creatable/filterable, not updatable.
	wantSDL(t, sdl, "email: String!", "email: StringFilterOps", "EMAIL_ASC")
	typ := sdlBlock(t, sdl, "input UserUpdateInput {")
	if strings.Contains(typ, "email") {
		t.Errorf("UserUpdateInput must not contain email:\n%s", typ)
	}
	create := sdlBlock(t, sdl, "input UserCreateInput {")
	if strings.Contains(create, "balance") {
		t.Errorf("UserCreateInput must not contain balance:\n%s", create)
	}
	user := sdlBlock(t, sdl, "type User implements Node {")
	if strings.Contains(user, "balance") {
		t.Errorf("type User must not contain balance:\n%s", user)
	}
}

func TestNameTable(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		c.Table("public", "users").Comment = "@name customers\nA customer."
	})
	sdl := built.SDL
	wantSDL(t, sdl,
		"type Customer implements Node", "allCustomers(", "customerById(", "customerByEmail(",
		"CustomerFilter", "CustomersOrderBy", "createCustomer(", "CustomerCreateInput",
		"author: Customer",  // forward relation retargets the renamed type
		`"""A customer."""`, // tag line stripped from the description
	)
	rejectSDL(t, sdl, "type User ", "allUsers(", "@name")
}

func TestNameColumn(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		c.Table("public", "users").Column("mood").Comment = "@name vibe"
	})
	sdl := built.SDL
	wantSDL(t, sdl, "vibe: Mood", "vibe: MoodFilterOps", "VIBE_ASC", "VIBE_DESC")
	rejectSDL(t, sdl, "mood: MoodFilterOps", "MOOD_ASC")
	if strings.Contains(sdlBlock(t, sdl, "type User implements Node {"), "mood:") {
		t.Error("type User must not keep the old column name")
	}
}

func TestNameEnumAndFunction(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		c.Enums[0].Comment = "@name feeling"
		c.Functions[0].Comment = "@name findPosts" // search_posts
	})
	wantSDL(t, built.SDL, "enum Feeling", "mood: Feeling", "findPosts(")
	rejectSDL(t, built.SDL, "enum Mood", "searchPosts(")
}

func TestDeprecated(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		c.Table("public", "users").Column("balance").Comment = "@deprecated use credits\nLegacy balance."
		for _, f := range c.Functions {
			if f.Name == "users_post_count" {
				f.Comment = "@deprecated"
			}
		}
	})
	sdl := built.SDL
	wantSDL(t, sdl,
		`balance: BigInt! @deprecated(reason: "use credits")`,
		`postCount: BigInt @deprecated(reason: "No longer supported")`,
		`"""Legacy balance."""`,
	)
	rejectSDL(t, sdl, "@deprecated use credits")
}

func TestOmitFunction(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		c.Functions[0].Comment = "@omit" // search_posts
	})
	rejectSDL(t, built.SDL, "searchPosts")
}

func TestViewKeysAndRelations(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		m := c.Table("public", "metrics")
		m.Comment = "@primaryKey name\n@unique value\n@foreignKey (value) references users (id)"
		m.Column("value").Comment = "@notNull"
	})
	sdl := built.SDL
	wantSDL(t, sdl,
		"metricByName(", "metricByValue(", // logical PK + unique lookups
		"value: Int!",     // @notNull
		"user: User",      // forward relation from the logical FK
		"metricsByValue(", // backward relation on User
	)
	// nodeId requires the logical PK.
	if !strings.Contains(sdlBlock(t, sdl, "type Metric implements Node {"), "nodeId: ID!") {
		t.Error("Metric must implement Node via @primaryKey")
	}

	// The logical PK must compile: single-row lookup by key.
	stmt := compileRoot(t, built, `{ metricByName(name: "rps") { name value } }`)
	if !strings.Contains(stmt.SQL, `"name" = $1`) {
		t.Errorf("lookup must key on the logical PK:\n%s", stmt.SQL)
	}
}

func TestFieldNameOnForeignKey(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		c.Table("public", "posts").ForeignKeys[0].Comment = "@fieldName writer\n@foreignFieldName authored"
	}, advancedfilters.New(nil))
	sdl := built.SDL
	wantSDL(t, sdl, "writer: User", "authored(", "writer: UserFilter", "authored: PostToManyFilter")
	rejectSDL(t, sdl, "author: User", "postsByAuthorId")
}

func TestOmitManyOnForeignKey(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		c.Table("public", "posts").ForeignKeys[0].Comment = "@omit many"
	})
	wantSDL(t, built.SDL, "author: User")
	rejectSDL(t, built.SDL, "postsByAuthorId(")
}

func TestOmitForeignKey(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		c.Table("public", "posts").ForeignKeys[0].Comment = "@omit"
	}, advancedfilters.New(nil))
	rejectSDL(t, built.SDL, "author: User", "postsByAuthorId", "PostToManyFilter")
}

func TestOmitUniqueConstraint(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		c.Table("public", "users").Uniques[0].Comment = "@omit"
	})
	rejectSDL(t, built.SDL, "userByEmail(")
	// Filterability persists via the backing unique index.
	wantSDL(t, built.SDL, "email: StringFilterOps")
}

func TestFilterableColumn(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		u := c.Table("public", "users")
		u.Column("full_name").Comment = "@filterable"
		u.Column("created_at").Comment = "@sortable\n@omit filter"
	})
	sdl := built.SDL
	wantSDL(t, sdl, "fullName: StringFilterOps", "FULL_NAME_ASC", "CREATED_AT_DESC")
	rejectSDL(t, sdl, "createdAt: DatetimeFilterOps")
}

func TestAdvancedFilterOmits(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		c.Table("public", "posts").ForeignKeys[0].Comment = "@omit filter"
		for _, f := range c.Functions {
			if f.Name == "users_post_count" {
				f.Comment = "@omit order"
			}
		}
	}, advancedfilters.New(nil))
	sdl := built.SDL
	// Selection fields stay; only the filter surface goes.
	wantSDL(t, sdl, "author: User", "postsByAuthorId(", "postCount: BigIntFilterOps")
	rejectSDL(t, sdl, "author: UserFilter", "postsByAuthorId: PostToManyFilter", "POST_COUNT_ASC")
}

func TestAdvancedFilterOptIn(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		for _, f := range c.Functions {
			if f.Name == "users_post_count" {
				f.Comment = "@filterable"
			}
		}
	}, advancedfilters.New(map[string]any{"relations_opt_in": true, "computed_opt_in": true}))
	sdl := built.SDL
	// Only the tagged computed column gets filter surface; untagged FKs none.
	wantSDL(t, sdl, "postCount: BigIntFilterOps", "POST_COUNT_ASC")
	rejectSDL(t, sdl, "author: UserFilter", "PostToManyFilter")
}

func TestComposesWithOtherBuiltins(t *testing.T) {
	built := build(t, func(c *introspect.Catalog) {
		c.Table("public", "users").Comment = "@name customers"
		c.Table("public", "posts").ForeignKeys[0].Comment = "@fieldName writer\n@foreignFieldName authored"
	}, simplenames.New(), nestedmutations.New(nil))
	sdl := built.SDL
	// @name feeds simple-names: short root fields derive from the new name.
	wantSDL(t, sdl, "customers(", "customer(", "updateCustomer(")
	rejectSDL(t, sdl, "allCustomers(", "users(")
	// @fieldName flows through the shared inflection, so the nested-mutations
	// input field aligns with the renamed selection field.
	if !strings.Contains(sdlBlock(t, sdl, "input PostCreateInput {"), "writer:") {
		t.Error("nested-mutations input must use the @fieldName name")
	}
	wantSDL(t, sdl, "authored(")
}

// sdlBlock extracts one type/input block from the SDL for scoped assertions.
func sdlBlock(t *testing.T, sdl, header string) string {
	t.Helper()
	i := strings.Index(sdl, header)
	if i < 0 {
		t.Fatalf("SDL missing block %q", header)
	}
	rest := sdl[i:]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		t.Fatalf("unterminated block %q", header)
	}
	return rest[:end]
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
