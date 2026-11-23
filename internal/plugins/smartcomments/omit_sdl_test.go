package smartcomments_test

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/contribfixture"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/advancedfilters"
	"github.com/suprbdev/pdbq/internal/plugins/aggregates"
	contribplugin "github.com/suprbdev/pdbq/internal/plugins/contrib"
	"github.com/suprbdev/pdbq/internal/plugins/nestedmutations"
	postgisplugin "github.com/suprbdev/pdbq/internal/plugins/postgis"
	"github.com/suprbdev/pdbq/internal/plugins/smartcomments"
	"github.com/suprbdev/pdbq/internal/schema"
)

// vaultCatalog is the contrib fixture plus public.vaults, whose secret_*
// columns of different types each reach every surface their type gets
// (unique lookups and by-key mutations, nested-mutation keys, PostGIS
// accessors and distance ordering, full-text rank, trigram similarity,
// aggregates, group-by, distinctOn, relational ordering), and the tables
// around it: vault_entries references vaults.secret_code, vault_logs
// references vaults.id, and vaults references users.
func vaultCatalog() *introspect.Catalog {
	cat := contribfixture.Catalog()
	col := func(pos int, name, typ, schema string) *introspect.Column {
		return &introspect.Column{Name: name, Position: pos, PGType: typ, TypeSchema: schema, BaseTypeSchema: schema}
	}
	identity := func(c *introspect.Column) *introspect.Column {
		c.NotNull, c.Generated, c.GeneratedKind = true, true, introspect.GeneratedIdentity
		return c
	}
	all := introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true}
	spot := col(4, "secret_spot", "geometry", "public")
	spot.Spatial = &introspect.SpatialType{Subtype: introspect.SpatialPoint, SRID: 4326}
	vaults := &introspect.Table{
		Schema: "public", Name: "vaults", Kind: introspect.RelTable, Privileges: all,
		Columns: []*introspect.Column{
			identity(col(1, "id", "int4", "pg_catalog")),
			col(2, "name", "text", "pg_catalog"),
			col(3, "secret_code", "text", "pg_catalog"),
			spot,
			col(5, "secret_doc", "tsvector", "pg_catalog"),
			col(6, "secret_amount", "int4", "pg_catalog"),
			col(7, "secret_stamp", "timestamptz", "pg_catalog"),
			col(8, "owner_id", "int4", "pg_catalog"),
		},
		PrimaryKey: &introspect.Constraint{Name: "vaults_pkey", Columns: []string{"id"}},
		Uniques:    []*introspect.Constraint{{Name: "vaults_secret_code_key", Columns: []string{"secret_code"}}},
		ForeignKeys: []*introspect.ForeignKey{{
			Name: "vaults_owner_id_fkey", Columns: []string{"owner_id"},
			RefSchema: "public", RefTable: "users", RefColumns: []string{"id"},
		}},
		Indexes: []*introspect.Index{
			{Name: "vaults_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
			{Name: "vaults_secret_code_key", Columns: []string{"secret_code"}, Unique: true, Method: "btree"},
			{Name: "vaults_secret_code_trgm", Columns: []string{"secret_code"}, Method: "gist"},
			{Name: "vaults_secret_spot_idx", Columns: []string{"secret_spot"}, Method: "gist"},
			{Name: "vaults_secret_doc_idx", Columns: []string{"secret_doc"}, Method: "gin"},
			{Name: "vaults_secret_amount_idx", Columns: []string{"secret_amount"}, Method: "btree"},
			{Name: "vaults_secret_stamp_idx", Columns: []string{"secret_stamp"}, Method: "btree"},
			{Name: "vaults_owner_id_idx", Columns: []string{"owner_id"}, Method: "btree"},
		},
	}
	entries := &introspect.Table{
		Schema: "public", Name: "vault_entries", Kind: introspect.RelTable, Privileges: all,
		Columns: []*introspect.Column{
			identity(col(1, "id", "int4", "pg_catalog")),
			col(2, "vault_code", "text", "pg_catalog"),
			col(3, "note", "text", "pg_catalog"),
		},
		PrimaryKey: &introspect.Constraint{Name: "vault_entries_pkey", Columns: []string{"id"}},
		ForeignKeys: []*introspect.ForeignKey{{
			Name: "vault_entries_vault_code_fkey", Columns: []string{"vault_code"},
			RefSchema: "public", RefTable: "vaults", RefColumns: []string{"secret_code"},
		}},
		Indexes: []*introspect.Index{
			{Name: "vault_entries_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
			{Name: "vault_entries_vault_code_idx", Columns: []string{"vault_code"}, Method: "btree"},
		},
	}
	logs := &introspect.Table{
		Schema: "public", Name: "vault_logs", Kind: introspect.RelTable, Privileges: all,
		Columns: []*introspect.Column{
			identity(col(1, "id", "int4", "pg_catalog")),
			col(2, "vault_id", "int4", "pg_catalog"),
			col(3, "line", "text", "pg_catalog"),
			col(4, "snapshot", "vaults", "public"),
		},
		PrimaryKey: &introspect.Constraint{Name: "vault_logs_pkey", Columns: []string{"id"}},
		ForeignKeys: []*introspect.ForeignKey{{
			Name: "vault_logs_vault_id_fkey", Columns: []string{"vault_id"},
			RefSchema: "public", RefTable: "vaults", RefColumns: []string{"id"},
		}},
		Indexes: []*introspect.Index{
			{Name: "vault_logs_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
			{Name: "vault_logs_vault_id_idx", Columns: []string{"vault_id"}, Method: "btree"},
		},
	}
	cat.Tables = append(cat.Tables, vaults, entries, logs)
	vaultArg := introspect.FuncArg{Name: "v", PGType: "vaults", TypeSchema: "public", Mode: introspect.ArgModeIn}
	cat.Functions = append(cat.Functions, &introspect.Function{
		Schema: "public", Name: "every_vault",
		ReturnType: "vaults", ReturnTypeSchema: "public", ReturnsSet: true, Volatility: introspect.VolatilityStable,
	}, &introspect.Function{
		Schema: "public", Name: "open_vault",
		Args:       []introspect.FuncArg{{Name: "vault_id", PGType: "int4", TypeSchema: "pg_catalog", Mode: introspect.ArgModeIn}},
		ReturnType: "vaults", ReturnTypeSchema: "public", Volatility: introspect.VolatilityVolatile,
	}, &introspect.Function{
		Schema: "public", Name: "vaults_label",
		Args:       []introspect.FuncArg{vaultArg},
		ReturnType: "text", ReturnTypeSchema: "pg_catalog", Volatility: introspect.VolatilityStable,
	}, &introspect.Function{
		Schema: "public", Name: "vault_digest",
		Args:       []introspect.FuncArg{{Name: "target", PGType: "vaults", TypeSchema: "public", Mode: introspect.ArgModeIn}, {Name: "salt", PGType: "text", TypeSchema: "pg_catalog", Mode: introspect.ArgModeIn}},
		ReturnType: "text", ReturnTypeSchema: "pg_catalog", Volatility: introspect.VolatilityStable,
	})
	return cat
}

// buildAll runs the vault catalog, mutated by prep, through every built-in
// plugin that adds surface (aggregates enabled everywhere).
func buildAll(t *testing.T, prep func(*introspect.Catalog)) *schema.Built {
	t.Helper()
	cat := vaultCatalog()
	if prep != nil {
		prep(cat)
	}
	reg := plugin.NewRegistry(
		smartcomments.New(),
		advancedfilters.New(nil),
		postgisplugin.New(nil),
		contribplugin.New(nil),
		aggregates.New(map[string]any{"enabled": true}, nil),
		nestedmutations.New(nil),
	)
	if err := reg.TransformCatalog(context.Background(), nil, cat); err != nil {
		t.Fatal(err)
	}
	b := schema.New(cat, reg.Inflector(nil), schema.Options{FilterIndexedOnly: true, Functions: true})
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return built
}

// Surfaces an occurrence of a column's name in the SDL is classified into.
const (
	sOutput    = "output"     // a field of an output type (column, accessor, rank, aggregate)
	sKey       = "key"        // a lookup, by-key mutation, nested key or key-named type
	sUpsert    = "upsert"     // an upsert keyed by the column
	sFilter    = "filter"     // a filter or condition field
	sAggFilter = "agg-filter" // a having or relation-aggregate filter field
	sOrder     = "order"      // a plain or relational orderBy value
	sRefOrder  = "ref-order"  // a distance or similarity orderBy value or its reference input
	sRank      = "rank"       // a full-text rank field, scored against the column's filter
	sRankOrder = "rank-order" // a full-text rank orderBy value
	sAggOrder  = "agg-order"  // a relational aggregate orderBy value
	sDistinct  = "distinct"   // a distinctOn value
	sGroupBy   = "group-by"   // a groupBy value
	sCreate    = "create"     // a create input field
	sUpdate    = "update"     // an update patch field
)

// occurrences returns every place the SDL names column (snake_case), keyed
// "surface|location".
func occurrences(t *testing.T, s *ast.Schema, column string) map[string]bool {
	t.Helper()
	token := strings.ReplaceAll(column, "_", "")
	has := func(name string) bool {
		return strings.Contains(strings.ToLower(strings.ReplaceAll(name, "_", "")), token)
	}
	upper := strings.ToUpper(column)
	out := map[string]bool{}
	add := func(surface, loc string) { out[surface+"|"+loc] = true }
	root := map[string]bool{"Query": true, "Mutation": true, "Subscription": true}
	for _, def := range s.Types {
		if def.BuiltIn {
			continue
		}
		if has(def.Name) {
			add(sKey, "type "+def.Name)
		}
		switch def.Kind {
		case ast.Object, ast.Interface:
			for _, f := range def.Fields {
				loc := def.Name + "." + f.Name
				switch {
				case root[def.Name] && strings.HasPrefix(f.Name, "upsert") && has(f.Name):
					add(sUpsert, loc)
				case root[def.Name] && has(f.Name):
					add(sKey, loc)
				case has(f.Name) && strings.HasSuffix(f.Name, "Rank"):
					add(sRank, loc)
				case has(f.Name):
					add(sOutput, loc)
				}
				for _, a := range f.Arguments {
					if has(a.Name) {
						add(sKey, loc+"("+a.Name+")")
					}
				}
			}
		case ast.InputObject:
			for _, f := range def.Fields {
				if !has(f.Name) {
					continue
				}
				loc := def.Name + "." + f.Name
				switch name := def.Name; {
				case has(name):
				case strings.Contains(f.Name, "By"):
					add(sKey, loc)
				case strings.Contains(name, "Having") || strings.HasSuffix(name, "AggregateFilter"):
					add(sAggFilter, loc)
				case strings.HasSuffix(name, "Filter") || strings.HasSuffix(name, "Condition"):
					add(sFilter, loc)
				case strings.HasSuffix(name, "SimilarToInput"):
					add(sRefOrder, loc)
				case strings.HasSuffix(name, "CreateInput"):
					add(sCreate, loc)
				case strings.HasSuffix(name, "UpdateInput"):
					add(sUpdate, loc)
				default:
					t.Errorf("unclassified input field %s", loc)
				}
			}
		case ast.Enum:
			for _, v := range def.EnumValues {
				i := strings.Index(v.Name, upper)
				if i < 0 {
					continue
				}
				loc := def.Name + "." + v.Name
				prefix, rest := v.Name[:i], v.Name[i+len(upper):]
				switch {
				case strings.HasSuffix(def.Name, "DistinctOn"):
					add(sDistinct, loc)
				case strings.HasSuffix(def.Name, "GroupBy"):
					add(sGroupBy, loc)
				case !strings.HasSuffix(def.Name, "OrderBy"):
					t.Errorf("unclassified enum value %s", loc)
				case strings.HasSuffix(prefix, "__"):
					add(sOrder, loc)
				case prefix != "":
					add(sAggOrder, loc)
				case rest == "_ASC" || rest == "_DESC":
					add(sOrder, loc)
				case strings.HasPrefix(rest, "_RANK_"):
					add(sRankOrder, loc)
				default:
					add(sRefOrder, loc)
				}
			}
		}
	}
	return out
}

func surfacesOf(occ map[string]bool) []string {
	seen := map[string]bool{}
	for k := range occ {
		seen[strings.SplitN(k, "|", 2)[0]] = true
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// TestOmitColumnEverywhere tags each secret column of vaults with every
// column @omit variant and checks the whole SDL: each occurrence of the
// column's name the untagged schema has must survive exactly when its
// surface is not one the variant hides, and no new occurrence may appear.
func TestOmitColumnEverywhere(t *testing.T) {
	everything := []string{sOutput, sKey, sUpsert, sFilter, sAggFilter, sOrder, sRefOrder, sAggOrder, sRank, sRankOrder, sDistinct, sGroupBy, sCreate, sUpdate}
	read := []string{sOutput, sKey, sUpsert, sFilter, sAggFilter, sOrder, sRefOrder, sAggOrder, sRank, sRankOrder, sDistinct, sGroupBy}
	variants := []struct {
		tag    string
		hidden []string
	}{
		{"@omit", everything},
		{"@omit read", read},
		{"@behavior -select", read},
		{"@omit filter", []string{sFilter, sAggFilter, sRank, sRankOrder, sDistinct}},
		{"@omit order", []string{sOrder, sRefOrder, sAggOrder, sRankOrder, sDistinct, sGroupBy}},
		{"@omit create", []string{sCreate, sUpsert}},
		{"@omit update", []string{sUpdate}},
		{"@omit filter,order", []string{sFilter, sAggFilter, sOrder, sRefOrder, sAggOrder, sRank, sRankOrder, sDistinct, sGroupBy}},
	}
	columns := map[string][]string{
		"secret_code":   {sOutput, sKey, sUpsert, sFilter, sAggFilter, sOrder, sRefOrder, sAggOrder, sDistinct, sCreate, sUpdate},
		"secret_spot":   {sOutput, sFilter, sAggFilter, sRefOrder, sCreate, sUpdate},
		"secret_doc":    {sOutput, sFilter, sAggFilter, sOrder, sAggOrder, sRank, sRankOrder, sDistinct, sGroupBy, sCreate, sUpdate},
		"secret_amount": {sOutput, sFilter, sAggFilter, sOrder, sAggOrder, sDistinct, sGroupBy, sCreate, sUpdate},
	}
	for column, wantSurfaces := range columns {
		base := occurrences(t, buildAll(t, nil).Schema, column)
		if got := surfacesOf(base); !slices.Equal(got, sorted(wantSurfaces)) {
			t.Fatalf("%s: untagged surfaces = %v, want %v (fixture drift)", column, got, sorted(wantSurfaces))
		}
		for _, v := range variants {
			t.Run(column+"/"+v.tag, func(t *testing.T) {
				built := buildAll(t, func(c *introspect.Catalog) {
					c.Table("public", "vaults").Column(column).Comment = v.tag
				})
				got := occurrences(t, built.Schema, column)
				for occ := range got {
					if !base[occ] {
						t.Errorf("new occurrence %s", occ)
					}
				}
				for occ := range base {
					surface := strings.SplitN(occ, "|", 2)[0]
					switch hidden := slices.Contains(v.hidden, surface); {
					case hidden && got[occ]:
						t.Errorf("exposed: %s", occ)
					case !hidden && !got[occ]:
						t.Errorf("lost: %s", occ)
					}
				}
			})
		}
	}
}

func sorted(in []string) []string {
	out := slices.Clone(in)
	sort.Strings(out)
	return out
}

// fieldNames lists the fields of an object or input type, nil when the type
// does not exist.
func fieldNames(s *ast.Schema, typeName string) []string {
	def := s.Types[typeName]
	if def == nil {
		return nil
	}
	out := []string{}
	for _, f := range def.Fields {
		out = append(out, f.Name)
	}
	return out
}

func wantFields(t *testing.T, s *ast.Schema, typeName string, fields ...string) {
	t.Helper()
	have := fieldNames(s, typeName)
	for _, f := range fields {
		if !slices.Contains(have, f) {
			t.Errorf("%s.%s missing (fields: %v)", typeName, f, have)
		}
	}
}

func rejectFields(t *testing.T, s *ast.Schema, typeName string, fields ...string) {
	t.Helper()
	have := fieldNames(s, typeName)
	for _, f := range fields {
		if slices.Contains(have, f) {
			t.Errorf("%s.%s must not exist", typeName, f)
		}
	}
}

// TestOmitTableEverywhere covers the table and foreign key @omit actions
// across every plugin's surface.
func TestOmitTableEverywhere(t *testing.T) {
	setTable := func(tag string) func(*introspect.Catalog) {
		return func(c *introspect.Catalog) { c.Table("public", "vaults").Comment = tag }
	}
	base := buildAll(t, nil).Schema
	wantFields(t, base, "Query", "allVaults", "vaultById", "vaultBySecretCode", "everyVault")
	wantFields(t, base, "Vault", "label", "vaultDigest")
	wantFields(t, base, "Mutation", "openVault", "updateVaults", "deleteVaults")
	wantFields(t, base, "VaultLog", "snapshot")
	if len(occurrences(t, base, "vaults_by_owner_id")) == 0 {
		t.Fatal("fixture drift: no vaultsByOwnerId surface")
	}

	t.Run("bare", func(t *testing.T) {
		s := buildAll(t, setTable("@omit")).Schema
		for _, name := range []string{"Vault", "VaultFilter", "VaultsOrderBy", "VaultCreateInput"} {
			if s.Types[name] != nil {
				t.Errorf("type %s must not exist", name)
			}
		}
		rejectFields(t, s, "Query", "everyVault", "vaultDigest")
		rejectFields(t, s, "Mutation", "openVault")
		rejectFields(t, s, "VaultLog", "snapshot", "vault")
		rejectFields(t, s, "VaultEntry", "vaultByVaultCode")
	})
	t.Run("read", func(t *testing.T) {
		s := buildAll(t, setTable("@omit read")).Schema
		rejectFields(t, s, "Query", "allVaults", "vaultById", "vaultBySecretCode", "everyVault")
		wantFields(t, s, "Mutation", "openVault", "createVault")
		wantFields(t, s, "User", "vaultsByOwnerId")
		wantFields(t, s, "Vault", "label", "vaultDigest")
	})
	for _, tc := range []struct {
		name string
		prep func(*introspect.Catalog)
	}{
		{"many on the table", setTable("@omit many")},
		{"many on the foreign key", func(c *introspect.Catalog) {
			c.Table("public", "vaults").ForeignKeys[0].Comment = "@omit many"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := buildAll(t, tc.prep).Schema
			if occ := occurrences(t, s, "vaults_by_owner_id"); len(occ) != 0 {
				t.Errorf("one-to-many surface left: %v", surfacesOf(occ))
			}
			wantFields(t, s, "Vault", "owner")
			wantFields(t, s, "VaultFilter", "owner")
		})
	}
	t.Run("filter", func(t *testing.T) {
		s := buildAll(t, setTable("@omit filter")).Schema
		for _, name := range []string{"VaultFilter", "VaultCondition", "VaultsDistinctOn"} {
			if s.Types[name] != nil {
				t.Errorf("type %s must not exist", name)
			}
		}
		rejectFields(t, s, "Mutation", "updateVaults", "deleteVaults")
		wantFields(t, s, "Mutation", "updateVaultById", "deleteVaultById")
	})
	t.Run("order", func(t *testing.T) {
		s := buildAll(t, setTable("@omit order")).Schema
		for _, name := range []string{"VaultsOrderBy", "VaultsDistinctOn"} {
			if s.Types[name] != nil {
				t.Errorf("type %s must not exist", name)
			}
		}
		wantFields(t, s, "Query", "allVaults")
	})
}

// TestOmitPrimaryKeyColumn: a primary key over a hidden column identifies
// no row through the API — no nodeId, by-key fields, nested key operations
// or payload edges, and nested operations copying it into vault_logs'
// foreign key are gone — and connection cursors fall back to offsets
// instead of encoding the key. The column is no longer filterable.
func TestOmitPrimaryKeyColumn(t *testing.T) {
	built := buildAll(t, func(c *introspect.Catalog) {
		c.Table("public", "vaults").Column("id").Comment = "@omit read"
	})
	s := built.Schema
	if def := s.Types["Vault"]; def == nil || slices.Contains(def.Interfaces, "Node") {
		t.Fatal("Vault must exist and not implement Node")
	}
	rejectFields(t, s, "Vault", "id", "nodeId")
	rejectFields(t, s, "Query", "vaultById")
	rejectFields(t, s, "Mutation", "updateVaultById", "deleteVaultById")
	wantFields(t, s, "Query", "vaultBySecretCode")
	rejectFields(t, s, "UpdateVaultBySecretCodePayload", "vaultEdge")
	wantFields(t, s, "UserVaultsByOwnerIdNestedPatchInput", "connectBySecretCode")
	rejectFields(t, s, "UserVaultsByOwnerIdNestedPatchInput", "connectById", "connectByNodeId")
	rejectFields(t, s, "VaultFilter", "id")
	rejectFields(t, s, "VaultCondition", "id")
	wantFields(t, s, "VaultLog", "vault")

	sql := compileRoot(t, built, `{ allVaults(first: 1) { edges { cursor } } }`).SQL
	if strings.Contains(sql, `"id"`) {
		t.Errorf("connection over a hidden primary key must not read it:\n%s", sql)
	}
	node := compileRoot(t, built, `{ node(nodeId: "WyJWYXVsdCIsMV0=") { __typename } }`).SQL
	if !strings.Contains(node, "WHERE false") {
		t.Errorf("node() must not look up a hidden primary key:\n%s", node)
	}
}

// TestOmitReferencedKey: nested operations that copy a hidden referenced
// key into the referencing row's readable foreign key column are not
// generated (#67), in either direction.
func TestOmitReferencedKey(t *testing.T) {
	base := buildAll(t, nil).Schema
	wantFields(t, base, "VaultEntryVaultByVaultCodeNestedInput", "connectById", "create")
	wantFields(t, base, "VaultVaultEntriesByVaultCodeNestedInput", "connectById", "create")
	s := buildAll(t, func(c *introspect.Catalog) {
		c.Table("public", "vaults").Column("secret_code").Comment = "@omit read"
	}).Schema
	if s.Types["VaultEntryVaultByVaultCodeNestedInput"] != nil {
		t.Error("forward nested input over a hidden referenced key must not exist")
	}
	rejectFields(t, s, "VaultEntryVaultByVaultCodeNestedPatchInput", "connectById", "connectByNodeId", "create", "disconnectById")
	wantFields(t, s, "VaultEntryVaultByVaultCodeNestedPatchInput", "updateById", "deleteById")
	rejectFields(t, s, "VaultVaultEntriesByVaultCodeNestedInput", "connectById", "create")
	wantFields(t, s, "VaultVaultEntriesByVaultCodeNestedPatchInput", "updateById", "deleteById")
	wantFields(t, s, "VaultEntry", "vaultByVaultCode")
}

// TestOmitFunctionsOverHiddenRows: functions returning or taking rows of an
// omitted table, or of a type from an unexposed schema, are not exposed
// (#63); functions over exposed and extension types stay.
func TestOmitFunctionsOverHiddenRows(t *testing.T) {
	private := func(c *introspect.Catalog) {
		c.Functions = append(c.Functions, &introspect.Function{
			Schema: "public", Name: "all_priv_keys",
			ReturnType: "keys", ReturnTypeSchema: "private", ReturnsSet: true, Volatility: introspect.VolatilityStable,
		}, &introspect.Function{
			Schema: "public", Name: "check_key",
			Args:       []introspect.FuncArg{{Name: "k", PGType: "keys", TypeSchema: "private", Mode: introspect.ArgModeIn}},
			ReturnType: "bool", ReturnTypeSchema: "pg_catalog", Volatility: introspect.VolatilityStable,
		})
	}
	s := buildAll(t, private).Schema
	rejectFields(t, s, "Query", "allPrivKeys", "checkKey")
	wantFields(t, s, "Query", "everyVault", "withFlag", "widen", "totalNights", "searchPosts")

	s = buildAll(t, func(c *introspect.Catalog) {
		private(c)
		c.Table("public", "vaults").Comment = "@omit"
	}).Schema
	rejectFields(t, s, "Query", "everyVault", "vaultDigest", "allPrivKeys")
	rejectFields(t, s, "Mutation", "openVault")
	wantFields(t, s, "Query", "withFlag", "searchPosts")
}

// TestOmitCompositeAttribute: @omit on a composite type's attribute hides
// it from the composite's object (read) and input (create/update) types.
func TestOmitCompositeAttribute(t *testing.T) {
	s := buildAll(t, func(c *introspect.Catalog) {
		for _, ct := range c.Composites {
			if ct.Name == "address" {
				ct.Fields[0].Comment = "@omit"
				ct.Fields[1].Comment = "@omit read"
			}
		}
	}).Schema
	rejectFields(t, s, "Address", "street", "city")
	rejectFields(t, s, "AddressInput", "street")
	wantFields(t, s, "AddressInput", "city", "mood")
	wantFields(t, s, "Address", "mood")
}

// TestOmitEveryField: a type whose every field is hidden keeps a null
// placeholder rather than one of the hidden fields.
func TestOmitEveryField(t *testing.T) {
	built := buildAll(t, func(c *introspect.Catalog) {
		for _, col := range c.Table("public", "metrics").Columns {
			col.Comment = "@omit"
		}
	})
	if got := fieldNames(built.Schema, "Metric"); !slices.Equal(got, []string{schema.PlaceholderField}) {
		t.Fatalf("Metric fields = %v", got)
	}
	sql := compileRoot(t, built, `{ allMetrics { nodes { _empty } } }`).SQL
	if !strings.Contains(sql, "'_empty', NULL::json") || strings.Contains(sql, `"value"`) {
		t.Errorf("placeholder must read null without touching the columns:\n%s", sql)
	}
}

// TestOmitManyOnLogicalForeignKey: tags after | on an @foreignKey smart
// comment apply like those on a real constraint.
func TestOmitManyOnLogicalForeignKey(t *testing.T) {
	s := buildAll(t, func(c *introspect.Catalog) {
		c.Table("public", "metrics").Comment = "@foreignKey (value) references users (id)|@omit many"
	}).Schema
	wantFields(t, s, "Metric", "userByValue")
	if occ := occurrences(t, s, "metrics_by_value"); len(occ) != 0 {
		t.Errorf("one-to-many surface left: %v", occ)
	}
}
