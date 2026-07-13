// Package introspect reads pg_catalog and produces a Catalog — the single
// serializable model that drives schema building, caching, and plugins.
package introspect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// CatalogFormatVersion is bumped whenever the Catalog wire format changes
// incompatibly; the cache layer refuses to load mismatched versions.
const CatalogFormatVersion = 2

// Catalog is the complete introspected model of the database. It is the only
// input to GraphQL schema building, which keeps caching trivial and plugin
// transforms deterministic.
type Catalog struct {
	FormatVersion int          `json:"format_version"`
	ServerVersion string       `json:"server_version"`
	Schemas       []string     `json:"schemas"`
	Tables        []*Table     `json:"tables"`
	Enums         []*Enum      `json:"enums"`
	Composites    []*Composite `json:"composites"`
	Functions     []*Function  `json:"functions"`
}

// RelKind distinguishes tables from the view family.
type RelKind string

const (
	RelTable            RelKind = "table"
	RelView             RelKind = "view"
	RelMaterializedView RelKind = "matview"
)

type Table struct {
	Schema      string        `json:"schema"`
	Name        string        `json:"name"`
	Kind        RelKind       `json:"kind"`
	Comment     string        `json:"comment,omitempty"`
	RLSEnabled  bool          `json:"rls_enabled"`
	Columns     []*Column     `json:"columns"`
	PrimaryKey  *Constraint   `json:"primary_key,omitempty"`
	Uniques     []*Constraint `json:"uniques,omitempty"`
	ForeignKeys []*ForeignKey `json:"foreign_keys,omitempty"`
	Indexes     []*Index      `json:"indexes,omitempty"`
	Privileges  Privileges    `json:"privileges"`
}

// Insertable reports whether generated mutations may INSERT into this relation.
func (t *Table) Insertable() bool { return t.Kind == RelTable && t.Privileges.Insert }

// Updatable reports whether generated mutations may UPDATE this relation.
func (t *Table) Updatable() bool { return t.Kind == RelTable && t.Privileges.Update }

// Deletable reports whether generated mutations may DELETE from this relation.
func (t *Table) Deletable() bool { return t.Kind == RelTable && t.Privileges.Delete }

// Column returns the column with the given name, or nil.
func (t *Table) Column(name string) *Column {
	for _, c := range t.Columns {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// IndexedColumns returns the set of column names that are the leading column
// of at least one index (the filterable set under the indexed-only policy).
func (t *Table) IndexedColumns() map[string]bool {
	out := map[string]bool{}
	for _, ix := range t.Indexes {
		if len(ix.Columns) > 0 {
			out[ix.Columns[0]] = true
		}
	}
	if t.PrimaryKey != nil && len(t.PrimaryKey.Columns) > 0 {
		out[t.PrimaryKey.Columns[0]] = true
	}
	for _, u := range t.Uniques {
		if len(u.Columns) > 0 {
			out[u.Columns[0]] = true
		}
	}
	return out
}

type Column struct {
	Name       string `json:"name"`
	Position   int    `json:"position"`
	PGType     string `json:"pg_type"`     // canonical type name, e.g. int4, text, _text (array)
	TypeSchema string `json:"type_schema"` // pg schema of the type (for enums/domains/composites)
	IsArray    bool   `json:"is_array"`
	NotNull    bool   `json:"not_null"`
	HasDefault bool   `json:"has_default"`
	Generated  bool   `json:"generated"` // identity or generated column: excluded from create/update input
	Comment    string `json:"comment,omitempty"`
}

type Constraint struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Comment string   `json:"comment,omitempty"`
}

type ForeignKey struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"ref_schema"`
	RefTable   string   `json:"ref_table"`
	RefColumns []string `json:"ref_columns"`
	Comment    string   `json:"comment,omitempty"`
}

type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"` // expression indexes contribute no columns
	Unique  bool     `json:"unique"`
	Method  string   `json:"method"` // btree, gin, gist, ...
}

type Privileges struct {
	Select bool `json:"select"`
	Insert bool `json:"insert"`
	Update bool `json:"update"`
	Delete bool `json:"delete"`
}

type Enum struct {
	Schema  string   `json:"schema"`
	Name    string   `json:"name"`
	Values  []string `json:"values"`
	Comment string   `json:"comment,omitempty"`
}

type Composite struct {
	Schema string    `json:"schema"`
	Name   string    `json:"name"`
	Fields []*Column `json:"fields"`
}

// Volatility mirrors pg_proc.provolatile.
type Volatility string

const (
	VolatilityImmutable Volatility = "immutable"
	VolatilityStable    Volatility = "stable"
	VolatilityVolatile  Volatility = "volatile"
)

type Function struct {
	Schema           string     `json:"schema"`
	Name             string     `json:"name"`
	Args             []FuncArg  `json:"args"`
	ReturnType       string     `json:"return_type"`
	ReturnTypeSchema string     `json:"return_type_schema"`
	ReturnsSet       bool       `json:"returns_set"`
	Volatility       Volatility `json:"volatility"`
	Comment          string     `json:"comment,omitempty"`
}

type FuncArg struct {
	Name       string `json:"name"`
	PGType     string `json:"pg_type"`
	TypeSchema string `json:"type_schema"`
}

// Table returns the table with the given schema+name, or nil.
func (c *Catalog) Table(schema, name string) *Table {
	for _, t := range c.Tables {
		if t.Schema == schema && t.Name == name {
			return t
		}
	}
	return nil
}

// Enum returns the enum with the given schema+name, or nil.
func (c *Catalog) Enum(schema, name string) *Enum {
	for _, e := range c.Enums {
		if e.Schema == schema && e.Name == name {
			return e
		}
	}
	return nil
}

// Composite returns the composite type with the given schema+name, or nil.
func (c *Catalog) Composite(schema, name string) *Composite {
	for _, ct := range c.Composites {
		if ct.Schema == schema && ct.Name == name {
			return ct
		}
	}
	return nil
}

// Hash returns a stable content hash of the catalog, used by the schema
// cache and the watch-mode poll fallback to detect drift.
func (c *Catalog) Hash() (string, error) {
	c.sortStable()
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func (c *Catalog) sortStable() {
	sort.Strings(c.Schemas)
	sort.Slice(c.Tables, func(i, j int) bool {
		if c.Tables[i].Schema != c.Tables[j].Schema {
			return c.Tables[i].Schema < c.Tables[j].Schema
		}
		return c.Tables[i].Name < c.Tables[j].Name
	})
	sort.Slice(c.Enums, func(i, j int) bool {
		if c.Enums[i].Schema != c.Enums[j].Schema {
			return c.Enums[i].Schema < c.Enums[j].Schema
		}
		return c.Enums[i].Name < c.Enums[j].Name
	})
	sort.Slice(c.Composites, func(i, j int) bool {
		if c.Composites[i].Schema != c.Composites[j].Schema {
			return c.Composites[i].Schema < c.Composites[j].Schema
		}
		return c.Composites[i].Name < c.Composites[j].Name
	})
	sort.Slice(c.Functions, func(i, j int) bool {
		if c.Functions[i].Schema != c.Functions[j].Schema {
			return c.Functions[i].Schema < c.Functions[j].Schema
		}
		return c.Functions[i].Name < c.Functions[j].Name
	})
}
