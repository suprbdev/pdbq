// Package contribfixture extends pdbcore's shared test fixture with a table
// covering every contrib column type (tsvector, citext, hstore, ltree,
// ltree[], the range and multirange types, inet, cidr and pg_trgm-indexed
// text) and a table of pgvector columns for golden and schema tests. The
// extensions (with unaccent) are recorded as installed in the "ext" schema,
// so compiled SQL shows the schema-qualified spelling that works when that
// schema is not on the search_path.
//
// It is a test helper: pdbcore's fixture (testutil.FixtureCatalog) carries
// no contrib columns, and the e2e suite creates the matching table itself.
package contribfixture

import (
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
)

// ExtSchema is the schema the fixture's extensions are installed in.
const ExtSchema = "ext"

// Extensions lists the contrib extensions the fixture records.
func Extensions() []*introspect.Extension {
	return []*introspect.Extension{
		{Name: "citext", Version: "1.6", Schema: ExtSchema},
		{Name: "hstore", Version: "1.8", Schema: ExtSchema},
		{Name: "ltree", Version: "1.3", Schema: ExtSchema},
		{Name: "pg_trgm", Version: "1.6", Schema: ExtSchema},
		{Name: "unaccent", Version: "1.1", Schema: ExtSchema},
		{Name: "vector", Version: "0.8.0", Schema: ExtSchema},
	}
}

// Documents is the contrib table: public.documents.
func Documents() *introspect.Table {
	col := func(pos int, name, typ, schema string) *introspect.Column {
		return &introspect.Column{Name: name, Position: pos, PGType: typ, TypeSchema: schema}
	}
	id := col(1, "id", "int4", "pg_catalog")
	id.NotNull, id.Generated, id.GeneratedKind = true, true, introspect.GeneratedIdentity
	title := col(2, "title", "text", "pg_catalog")
	title.NotNull = true
	paths := col(8, "paths", "_ltree", ExtSchema)
	paths.IsArray = true
	return &introspect.Table{
		Schema: "public", Name: "documents", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		Columns: []*introspect.Column{
			id,
			title,
			col(3, "slug", "citext", ExtSchema),
			col(4, "search", "tsvector", "pg_catalog"),
			col(5, "attrs", "hstore", ExtSchema),
			col(6, "path", "ltree", ExtSchema),
			col(7, "label", "varchar", "pg_catalog"),
			paths,
			col(9, "during", "tstzrange", "pg_catalog"),
			col(10, "span", "int4range", "pg_catalog"),
			col(11, "avail", "int4multirange", "pg_catalog"),
			col(12, "price", "numrange", "pg_catalog"),
			col(13, "ip", "inet", "pg_catalog"),
			col(14, "net", "cidr", "pg_catalog"),
			col(15, "notes", "text", "pg_catalog"),
			col(16, "keywords", "tsvector", "pg_catalog"),
			col(17, "stay", "stay", "public"),
		},
		PrimaryKey: &introspect.Constraint{Name: "documents_pkey", Columns: []string{"id"}},
		Uniques:    []*introspect.Constraint{{Name: "documents_slug_key", Columns: []string{"slug"}}},
		Indexes: []*introspect.Index{
			{Name: "documents_attrs_idx", Columns: []string{"attrs"}, Method: "gin"},
			{Name: "documents_avail_idx", Columns: []string{"avail"}, Method: "gist"},
			{Name: "documents_during_idx", Columns: []string{"during"}, Method: "gist"},
			{Name: "documents_ip_idx", Columns: []string{"ip"}, Method: "gist"},
			{Name: "documents_keywords_idx", Columns: []string{"keywords"}, Method: "gin"},
			{Name: "documents_label_idx", Columns: []string{"label"}, Method: "gist"},
			{Name: "documents_net_idx", Columns: []string{"net"}, Method: "btree"},
			{Name: "documents_notes_idx", Columns: []string{"notes"}, Method: "btree"},
			{Name: "documents_path_idx", Columns: []string{"path"}, Method: "gist"},
			{Name: "documents_paths_idx", Columns: []string{"paths"}, Method: "gist"},
			{Name: "documents_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
			{Name: "documents_search_idx", Columns: []string{"search"}, Method: "gin"},
			{Name: "documents_slug_key", Columns: []string{"slug"}, Unique: true, Method: "btree"},
			{Name: "documents_span_idx", Columns: []string{"span"}, Method: "gist"},
			{Name: "documents_title_idx", Columns: []string{"title"}, Method: "gin"},
		},
	}
}

// Embeddings is the pgvector table: public.embeddings with a vector column
// led by an HNSW index, a halfvec column led by an IVFFlat index and an
// unindexed sparsevec column.
func Embeddings() *introspect.Table {
	col := func(pos int, name, typ, schema string) *introspect.Column {
		return &introspect.Column{Name: name, Position: pos, PGType: typ, TypeSchema: schema}
	}
	id := col(1, "id", "int4", "pg_catalog")
	id.NotNull, id.Generated, id.GeneratedKind = true, true, introspect.GeneratedIdentity
	return &introspect.Table{
		Schema: "public", Name: "embeddings", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		Columns: []*introspect.Column{
			id,
			col(2, "embedding", "vector", ExtSchema),
			col(3, "half", "halfvec", ExtSchema),
			col(4, "sparse", "sparsevec", ExtSchema),
		},
		PrimaryKey: &introspect.Constraint{Name: "embeddings_pkey", Columns: []string{"id"}},
		Indexes: []*introspect.Index{
			{Name: "embeddings_embedding_idx", Columns: []string{"embedding"}, Method: "hnsw"},
			{Name: "embeddings_half_idx", Columns: []string{"half"}, Method: "ivfflat"},
			{Name: "embeddings_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
		},
	}
}

// Catalog returns testutil.FixtureCatalog plus the documents and embeddings
// tables, their extensions, the composite type stay (nights int4range,
// extras hstore), a computed column (documents_window(documents) returns
// tstzrange) and functions taking and returning contrib types (widen(r
// int4range, by int4) returns int4range, with_flag(h hstore) returns hstore,
// total_nights(VARIADIC rs int4range[]) returns int4, scale_vector(v vector,
// by float8) returns vector).
func Catalog() *introspect.Catalog {
	cat := testutil.FixtureCatalog()
	cat.Tables = append(cat.Tables, Documents(), Embeddings())
	cat.Extensions = append(cat.Extensions, Extensions()...)
	cat.Composites = append(cat.Composites, &introspect.Composite{
		Schema: "public", Name: "stay",
		Fields: []*introspect.Column{
			{Name: "nights", Position: 1, PGType: "int4range", TypeSchema: "pg_catalog"},
			{Name: "extras", Position: 2, PGType: "hstore", TypeSchema: ExtSchema},
		},
	})
	cat.Functions = append(cat.Functions, &introspect.Function{
		Schema: "public", Name: "documents_window",
		Args:       []introspect.FuncArg{{Name: "d", PGType: "documents", TypeSchema: "public", Mode: introspect.ArgModeIn}},
		ReturnType: "tstzrange", ReturnTypeSchema: "pg_catalog",
		Volatility: introspect.VolatilityStable,
	}, &introspect.Function{
		Schema: "public", Name: "widen",
		Args: []introspect.FuncArg{
			{Name: "r", PGType: "int4range", TypeSchema: "pg_catalog", Mode: introspect.ArgModeIn},
			{Name: "by", PGType: "int4", TypeSchema: "pg_catalog", Mode: introspect.ArgModeIn},
		},
		ReturnType: "int4range", ReturnTypeSchema: "pg_catalog",
		Volatility: introspect.VolatilityImmutable,
	}, &introspect.Function{
		Schema: "public", Name: "with_flag",
		Args:       []introspect.FuncArg{{Name: "h", PGType: "hstore", TypeSchema: ExtSchema, Mode: introspect.ArgModeIn}},
		ReturnType: "hstore", ReturnTypeSchema: ExtSchema,
		Volatility: introspect.VolatilityImmutable,
	}, &introspect.Function{
		Schema: "public", Name: "total_nights",
		Args:       []introspect.FuncArg{{Name: "rs", PGType: "_int4range", TypeSchema: "pg_catalog", Mode: introspect.ArgModeVariadic}},
		ReturnType: "int4", ReturnTypeSchema: "pg_catalog",
		Volatility: introspect.VolatilityImmutable,
	}, &introspect.Function{
		Schema: "public", Name: "scale_vector",
		Args: []introspect.FuncArg{
			{Name: "v", PGType: "vector", TypeSchema: ExtSchema, Mode: introspect.ArgModeIn},
			{Name: "by", PGType: "float8", TypeSchema: "pg_catalog", Mode: introspect.ArgModeIn},
		},
		ReturnType: "vector", ReturnTypeSchema: ExtSchema,
		Volatility: introspect.VolatilityImmutable,
	})
	return cat
}
