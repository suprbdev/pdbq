// Package testutil provides a hand-built fixture Catalog mirroring the
// docker fixture schema, so schema-builder and compiler tests run without a
// database.
package testutil

import "github.com/suprbdev/pdbq/internal/introspect"

// FixtureCatalog models: users (pk id, unique email, enum mood, jsonb,
// text[], composite address + address[]), posts (pk id, fk author_id ->
// users.id), plus a stable function.
func FixtureCatalog() *introspect.Catalog {
	users := &introspect.Table{
		Schema: "public", Name: "users", Kind: introspect.RelTable,
		RLSEnabled: true,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true, HasDefault: true, Generated: true},
			{Name: "email", Position: 2, PGType: "text", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "full_name", Position: 3, PGType: "text", TypeSchema: "pg_catalog"},
			{Name: "mood", Position: 4, PGType: "mood", TypeSchema: "public"},
			{Name: "settings", Position: 5, PGType: "jsonb", TypeSchema: "pg_catalog"},
			{Name: "tags", Position: 6, PGType: "_text", TypeSchema: "pg_catalog", IsArray: true},
			{Name: "balance", Position: 7, PGType: "int8", TypeSchema: "pg_catalog", NotNull: true, HasDefault: true},
			{Name: "created_at", Position: 8, PGType: "timestamptz", TypeSchema: "pg_catalog", NotNull: true, HasDefault: true},
			{Name: "address", Position: 9, PGType: "address", TypeSchema: "public"},
			{Name: "prev_addresses", Position: 10, PGType: "_address", TypeSchema: "public", IsArray: true},
		},
		PrimaryKey: &introspect.Constraint{Name: "users_pkey", Columns: []string{"id"}},
		Uniques:    []*introspect.Constraint{{Name: "users_email_key", Columns: []string{"email"}}},
		Indexes: []*introspect.Index{
			{Name: "users_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
			{Name: "users_email_key", Columns: []string{"email"}, Unique: true, Method: "btree"},
			{Name: "users_mood_idx", Columns: []string{"mood"}, Method: "btree"},
			{Name: "users_settings_idx", Columns: []string{"settings"}, Method: "gin"},
			{Name: "users_tags_idx", Columns: []string{"tags"}, Method: "gin"},
		},
	}
	posts := &introspect.Table{
		Schema: "public", Name: "posts", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true, HasDefault: true, Generated: true},
			{Name: "author_id", Position: 2, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "title", Position: 3, PGType: "text", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "body", Position: 4, PGType: "text", TypeSchema: "pg_catalog"},
			{Name: "published", Position: 5, PGType: "bool", TypeSchema: "pg_catalog", NotNull: true, HasDefault: true},
		},
		PrimaryKey: &introspect.Constraint{Name: "posts_pkey", Columns: []string{"id"}},
		ForeignKeys: []*introspect.ForeignKey{{
			Name: "posts_author_id_fkey", Columns: []string{"author_id"},
			RefSchema: "public", RefTable: "users", RefColumns: []string{"id"},
		}},
		Indexes: []*introspect.Index{
			{Name: "posts_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
			{Name: "posts_author_id_idx", Columns: []string{"author_id"}, Method: "btree"},
			{Name: "posts_title_idx", Columns: []string{"title"}, Method: "btree"},
		},
	}
	// metrics is a PK-less view: no nodeId, offset-cursor pagination only.
	metrics := &introspect.Table{
		Schema: "public", Name: "metrics", Kind: introspect.RelView,
		Privileges: introspect.Privileges{Select: true},
		Columns: []*introspect.Column{
			{Name: "name", Position: 1, PGType: "text", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "value", Position: 2, PGType: "int4", TypeSchema: "pg_catalog"},
		},
	}
	return &introspect.Catalog{
		FormatVersion: introspect.CatalogFormatVersion,
		ServerVersion: "16.0",
		Schemas:       []string{"public"},
		Tables:        []*introspect.Table{metrics, posts, users},
		Enums: []*introspect.Enum{{
			Schema: "public", Name: "mood", Values: []string{"sad", "ok", "happy"},
		}},
		Composites: []*introspect.Composite{{
			Schema: "public", Name: "address",
			Fields: []*introspect.Column{
				{Name: "street", Position: 1, PGType: "text", TypeSchema: "pg_catalog"},
				{Name: "city", Position: 2, PGType: "text", TypeSchema: "pg_catalog"},
				{Name: "mood", Position: 3, PGType: "mood", TypeSchema: "public"},
			},
		}},
		Functions: []*introspect.Function{{
			Schema: "public", Name: "search_posts",
			Args:       []introspect.FuncArg{{Name: "term", PGType: "text", TypeSchema: "pg_catalog"}},
			ReturnType: "posts", ReturnTypeSchema: "public", ReturnsSet: true,
			Volatility: introspect.VolatilityStable,
		}, {
			// Computed column: first arg is users' row type -> User.postCount.
			Schema: "public", Name: "users_post_count",
			Args:       []introspect.FuncArg{{Name: "u", PGType: "users", TypeSchema: "public"}},
			ReturnType: "int8", ReturnTypeSchema: "pg_catalog",
			Volatility: introspect.VolatilityStable,
		}, {
			// Set-returning computed column over a table -> User.recentPosts(n:).
			Schema: "public", Name: "users_recent_posts",
			Args: []introspect.FuncArg{
				{Name: "u", PGType: "users", TypeSchema: "public"},
				{Name: "n", PGType: "int4", TypeSchema: "pg_catalog"},
			},
			ReturnType: "posts", ReturnTypeSchema: "public", ReturnsSet: true,
			Volatility: introspect.VolatilityStable,
		}, {
			// Set-returning computed column over a scalar -> User.tagWords.
			Schema: "public", Name: "users_tag_words",
			Args:       []introspect.FuncArg{{Name: "u", PGType: "users", TypeSchema: "public"}},
			ReturnType: "text", ReturnTypeSchema: "pg_catalog", ReturnsSet: true,
			Volatility: introspect.VolatilityStable,
		}, {
			// Volatile probe raising SQLSTATE 40001 on odd calls (retry e2e).
			Schema: "public", Name: "retry_probe",
			ReturnType: "int4", ReturnTypeSchema: "pg_catalog",
			Volatility: introspect.VolatilityVolatile,
		}, {
			// Volatile scalar function -> Mutation.addNumbers(input:){result}.
			Schema: "public", Name: "add_numbers",
			Args: []introspect.FuncArg{
				{Name: "a", PGType: "int4", TypeSchema: "pg_catalog"},
				{Name: "b", PGType: "int4", TypeSchema: "pg_catalog"},
			},
			ReturnType: "int4", ReturnTypeSchema: "pg_catalog",
			Volatility: introspect.VolatilityVolatile,
		}, {
			// Volatile table-returning function -> Mutation.publishPost(input:){result{...}}.
			Schema: "public", Name: "publish_post",
			Args:       []introspect.FuncArg{{Name: "post_id", PGType: "int4", TypeSchema: "pg_catalog"}},
			ReturnType: "posts", ReturnTypeSchema: "public",
			Volatility: introspect.VolatilityVolatile,
		}, {
			// Computed column with an extra argument -> Post.excerpt(maxChars:).
			Schema: "public", Name: "posts_excerpt",
			Args: []introspect.FuncArg{
				{Name: "p", PGType: "posts", TypeSchema: "public"},
				{Name: "max_chars", PGType: "int4", TypeSchema: "pg_catalog"},
			},
			ReturnType: "text", ReturnTypeSchema: "pg_catalog",
			Volatility: introspect.VolatilityStable,
		}},
	}
}
