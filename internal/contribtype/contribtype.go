// Package contribtype maps pdbcore's contrib vocabularies (full-text search,
// pg_trgm, hstore, ltree, citext, ranges and multiranges, inet/cidr) onto
// GraphQL: which type a column of such a type surfaces as, which operator
// input filters it, and the PostGraphile-compatible names of operators and
// range types.
//
// The schema builder, the contrib plugin and the SQL compiler all derive
// their names from this package, so the generated SDL and the compiled SQL
// cannot drift.
//
// Type mapping (PostGraphile names where PostGraphile has one):
//
//	tsvector                -> FullText scalar (text form), FullTextFilterOps
//	citext                  -> String, StringFilterOps (case-insensitive)
//	hstore                  -> KeyValueHash scalar (JSON object), KeyValueHashFilterOps
//	ltree                   -> String, LtreeFilterOps
//	ltree[]                 -> [String!], LtreeListFilterOps
//	int4range ... daterange -> IntRange { start: IntRangeBound { value inclusive } end },
//	                           input IntRangeInput, IntRangeFilterOps
//	int4multirange ...      -> [IntRange!], input [IntRangeInput!], IntMultirangeFilterOps
//	CREATE TYPE ... AS RANGE -> as the built-in range over the same element
//	                           (floatrange over float8 -> FloatRange); String
//	                           when the subtype is not a pg_catalog type
//	inet / cidr             -> InternetAddress / CidrAddress scalars,
//	                           InternetAddressFilterOps / CidrAddressFilterOps
//	interval                -> Interval { seconds minutes hours days months years },
//	                           input IntervalInput, IntervalFilterOps
//	interval[]              -> [Interval!], input [IntervalInput!], IntervalListFilterOps
//
// Range types are named after their element's scalar: Int (int4), BigInt
// (int8), BigFloat (numeric), Date (date) and Datetime (timestamp and
// timestamptz, which therefore share DatetimeRange). Custom range types
// share these (a range over int4 is an IntRange) and add the scalars of
// their other pg_catalog subtypes: Float (float4, float8), Time, Boolean,
// UUID, and String for text-like subtypes (text, inet, interval, ...).
//
// Interval is a pdbq-owned mapping with no pdbcore vocabulary behind it:
// [For] resolves it for every interval column, and the schema builder keeps
// the String fallback unless schema.interval_type is "object".
//
// Extension types (citext, hstore, ltree) map only when the catalog records
// the extension; a catalog without it keeps the String fallback, so schemas
// of databases that use none of these types are unchanged.
package contribtype

import (
	"strings"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
)

// Kind classifies how a column's own type surfaces in GraphQL.
type Kind int

const (
	// None: the column's type is not owned by a contrib vocabulary (plain
	// text columns still get the trigram vocabulary, see [Column.Vocabs]).
	None Kind = iota
	// FullText is a tsvector column.
	FullText
	// Citext is a citext column: String, with case-insensitive comparisons.
	Citext
	// Hstore is an hstore column (KeyValueHash).
	Hstore
	// Ltree is an ltree column (String).
	Ltree
	// LtreeArray is an ltree[] column ([String!]).
	LtreeArray
	// Range is a range column (<Elem>Range object).
	Range
	// Multirange is a multirange column ([<Elem>Range!]).
	Multirange
	// Inet is an inet or cidr column.
	Inet
	// Interval is an interval column or an array of them (Interval).
	Interval
)

// Scalar names declared when a column needs them.
const (
	ScalarFullText        = "FullText"
	ScalarKeyValueHash    = "KeyValueHash"
	ScalarInternetAddress = "InternetAddress"
	ScalarCidrAddress     = "CidrAddress"
)

// Names of the interval object and input types.
const (
	IntervalType  = "Interval"
	IntervalInput = "IntervalInput"
)

// IntervalFields lists the fields of Interval and IntervalInput in schema
// order, as PostGraphile declares them.
var IntervalFields = []string{"seconds", "minutes", "hours", "days", "months", "years"}

// StringTrgmOps is the operator input of text columns that also take the
// pg_trgm operators: every StringFilterOps operator plus similarTo,
// wordSimilarTo and strictWordSimilarTo.
const StringTrgmOps = "StringTrgmFilterOps"

// Column is the GraphQL view of one column (or composite attribute, or
// computed-column return value).
type Column struct {
	// Kind is how the column's own type maps; None for types no vocabulary
	// owns.
	Kind Kind
	// Vocabs are all vocabularies applying to the column, bound to their
	// extension schemas (see contrib.ForColumn).
	Vocabs []contrib.Vocabulary
	// Own is the vocabulary owning the column type (with a Codec); zero
	// when Kind is None.
	Own contrib.Vocabulary
	// Scalar is the GraphQL element output type: a scalar name, or the
	// range object name for Range and Multirange.
	Scalar string
	// Input is the GraphQL element input type: Scalar, or the range input
	// object name for Range and Multirange.
	Input string
	// OpsInput is the operator input type filtering the column; empty for
	// Citext, which shares StringFilterOps.
	OpsInput string
	// Elem is a range's element pg type ("int4", "timestamptz").
	Elem string
	// ElemScalar is a range's element GraphQL scalar ("Int", "Datetime").
	ElemScalar string
	// Array marks an Interval column that is an array.
	Array bool
}

// For resolves a column. Its Kind is None (and Vocabs possibly non-empty)
// for types that keep their default mapping.
func For(cat *introspect.Catalog, col *introspect.Column) Column {
	if isInterval(col) {
		return intervalColumn(col)
	}
	vs := contrib.ForColumn(cat, col)
	out := Column{Vocabs: vs}
	for _, v := range vs {
		if v.Codec == nil {
			continue
		}
		out.Own = v
		switch v.Name {
		case contrib.VocabFullText:
			out.Kind, out.Scalar, out.OpsInput = FullText, ScalarFullText, "FullTextFilterOps"
		case contrib.VocabCitext:
			out.Kind, out.Scalar = Citext, "String"
		case contrib.VocabHstore:
			out.Kind, out.Scalar, out.OpsInput = Hstore, ScalarKeyValueHash, "KeyValueHashFilterOps"
		case contrib.VocabLtree:
			out.Kind, out.Scalar, out.OpsInput = Ltree, "String", "LtreeFilterOps"
		case contrib.VocabLtreeArray:
			out.Kind, out.Scalar, out.OpsInput = LtreeArray, "String", "LtreeListFilterOps"
		case contrib.VocabInet:
			out.Kind, out.Scalar = Inet, ScalarInternetAddress
			if v.Type == "cidr" {
				out.Scalar = ScalarCidrAddress
			}
			out.OpsInput = out.Scalar + "FilterOps"
		case contrib.VocabRange, contrib.VocabMultirange:
			if r := catalogRange(cat, col, v); r != nil && r.SubtypeSchema != "pg_catalog" {
				out.Own = contrib.Vocabulary{}
				continue
			}
			start, _ := v.Accessor("start")
			out.Elem = start.Type
			out.ElemScalar = elemScalar(start.Type)
			out.Scalar = out.ElemScalar + "Range"
			out.Kind, out.OpsInput = Range, out.Scalar+"FilterOps"
			if v.Name == contrib.VocabMultirange {
				out.Kind, out.OpsInput = Multirange, out.ElemScalar+"MultirangeFilterOps"
			}
		default:
			out.Own = contrib.Vocabulary{}
			continue
		}
		out.Input = out.Scalar
		if out.Kind == Range || out.Kind == Multirange {
			out.Input = RangeInput(out.Scalar)
		}
		break
	}
	return out
}

// isInterval reports whether col is the built-in interval type or an array
// of it.
func isInterval(col *introspect.Column) bool {
	if col == nil || col.Spatial != nil || col.EnumRef != "" || strings.TrimPrefix(col.PGType, "_") != "interval" {
		return false
	}
	schema := col.BaseTypeSchema
	if schema == "" {
		schema = col.TypeSchema
	}
	return schema == "" || schema == "pg_catalog"
}

func intervalColumn(col *introspect.Column) Column {
	opsInput := IntervalType + "FilterOps"
	if col.IsArray {
		opsInput = IntervalType + "ListFilterOps"
	}
	return Column{
		Kind: Interval, Scalar: IntervalType, Input: IntervalInput,
		OpsInput: opsInput, Array: col.IsArray,
	}
}

// catalogRange returns the entry of [introspect.Catalog.Ranges] that the
// range or multirange vocabulary v of col was built from, or nil for a
// built-in range type.
func catalogRange(cat *introspect.Catalog, col *introspect.Column, v contrib.Vocabulary) *introspect.RangeType {
	if cat == nil || col == nil {
		return nil
	}
	for _, r := range cat.Ranges {
		schema, name := r.Schema, r.Name
		if v.Name == contrib.VocabMultirange {
			schema, name = r.MultirangeSchema, r.Multirange
		}
		if name == v.Type && (col.BaseTypeSchema == "" || col.BaseTypeSchema == schema) {
			return r
		}
	}
	return nil
}

// elemScalar is the GraphQL scalar of a range element type; it mirrors the
// schema builder's scalar mapping for the pg_catalog types ranges can be
// built over.
func elemScalar(pg string) string {
	switch pg {
	case "int2", "int4":
		return "Int"
	case "int8", "oid":
		return "BigInt"
	case "float4", "float8":
		return "Float"
	case "numeric", "money":
		return "BigFloat"
	case "bool":
		return "Boolean"
	case "uuid":
		return "UUID"
	case "date":
		return "Date"
	case "timestamp", "timestamptz":
		return "Datetime"
	case "time", "timetz":
		return "Time"
	}
	return "String"
}

// RangeBound names the bound object of a range object type (IntRangeBound).
func RangeBound(rangeType string) string { return rangeType + "Bound" }

// RangeInput names the input object of a range object type (IntRangeInput).
func RangeInput(rangeType string) string { return rangeType + "Input" }

// RangeBoundInput names the bound input object of a range object type
// (IntRangeBoundInput).
func RangeBoundInput(rangeType string) string { return rangeType + "BoundInput" }

// Owned reports whether the column's own type is mapped by a vocabulary.
func (c Column) Owned() bool { return c.Kind != None }

// Extension reports whether the owning vocabulary belongs to an extension
// (citext, hstore, ltree), whose operators must be spelled schema-qualified.
func (c Column) Extension() bool { return c.Owned() && c.Own.Extension != "" }

// Operator renders a generic comparison operator of the column's type,
// schema-qualified for extension types (see contrib.Vocabulary.Operator).
// Arrays of extension types compare with the core array operators, which
// live in pg_catalog and must stay unqualified.
func (c Column) Operator(op string) string {
	if !c.Owned() || c.Kind == LtreeArray {
		return op
	}
	return c.Own.Operator(op)
}

// hstoreOrder maps the ordering comparisons onto the operators of hstore's
// btree operator class, which has no < or >.
var hstoreOrder = map[string]string{"<": "#<#", "<=": "#<=#", ">": "#>#", ">=": "#>=#"}

// Compare renders a comparison operator (=, <>, <, <=, >, >=) consistent
// with the column type's default btree ordering, schema-qualified like
// Operator: hstore orders with #<# and friends.
func (c Column) Compare(op string) string {
	if c.Kind == Hstore {
		if o, ok := hstoreOrder[op]; ok {
			op = o
		}
	}
	return c.Operator(op)
}

// TypeSQL renders the column's own (element) type name, schema-qualified
// for extension types: `"ext".ltree`, `inet`.
func (c Column) TypeSQL() string {
	name := strings.TrimPrefix(c.Own.Type, "_")
	if c.Own.Schema == "" {
		return name
	}
	return `"` + strings.ReplaceAll(c.Own.Schema, `"`, `""`) + `".` + name
}

// Trgm returns the pg_trgm vocabulary applying to the column, if any.
func (c Column) Trgm() (contrib.Vocabulary, bool) {
	for _, v := range c.Vocabs {
		if v.Name == contrib.VocabTrgm {
			return v, true
		}
	}
	return contrib.Vocabulary{}, false
}

// FilterOps returns the vocabulary operators the contrib plugin exposes on
// the column's own operator input (OpsInput): the owning vocabulary's
// operators. Text vocabularies (pg_trgm) are exposed through
// [StringTrgmOps] instead, and full-text search over text columns is not
// exposed.
func (c Column) FilterOps() []contrib.Op {
	if !c.Owned() {
		return nil
	}
	return c.Own.Ops
}

// Op finds a filter operator by its GraphQL name among the column's owning
// and pg_trgm vocabularies.
func (c Column) Op(name string) (contrib.Op, bool) {
	if c.Owned() {
		for _, o := range c.Own.Ops {
			if OpName(c.Own, o) == name {
				return o, true
			}
		}
	}
	if v, ok := c.Trgm(); ok {
		for _, o := range v.Ops {
			if OpName(v, o) == name {
				return o, true
			}
		}
	}
	return contrib.Op{}, false
}

// opRenames gives contrib operators their postgraphile-plugin-connection-filter
// names where those differ.
var opRenames = map[string]map[string]string{
	contrib.VocabHstore: {
		"hasKey":     "containsKey",
		"hasAllKeys": "containsAllKeys",
		"hasAnyKeys": "containsAnyKeys",
	},
	contrib.VocabInet: {
		"containsOrEquals":    "containsOrEqualTo",
		"containedByOrEquals": "containedByOrEqualTo",
	},
}

// OpName is the GraphQL field name of a vocabulary operator.
func OpName(v contrib.Vocabulary, o contrib.Op) string {
	if n, ok := opRenames[v.Name][o.Name]; ok {
		return n
	}
	return o.Name
}

// IndexServes reports whether one of the column's index access methods
// (from the catalog's indexes led by the column) matches one of the hints.
// Operator classes are not introspected, so a matching method is taken to
// carry the hinted operator class.
func IndexServes(methods map[string]bool, hints []contrib.IndexHint) bool {
	for _, h := range hints {
		if methods[h.Method] {
			return true
		}
	}
	return false
}

// IndexMethods returns the access methods of the indexes whose leading
// column is col.
func IndexMethods(t *introspect.Table, col string) map[string]bool {
	out := map[string]bool{}
	for _, ix := range t.Indexes {
		if len(ix.Columns) > 0 && ix.Columns[0] == col {
			out[ix.Method] = true
		}
	}
	return out
}

// SimilarityOrder describes one trigram ordering family of a text column:
// the orderBy value infix and the pg_trgm distance expression it sorts by.
type SimilarityOrder struct {
	// Infix sits between the column's enum name and the direction:
	// NAME_SIMILARITY_DESC.
	Infix string
	// Expr is the pg_trgm distance expression (distance, wordDistance,
	// strictWordDistance); best matches have the smallest distance.
	Expr string
}

// SimilarityOrders lists the trigram ordering families in schema order.
var SimilarityOrders = []SimilarityOrder{
	{Infix: "_SIMILARITY", Expr: "distance"},
	{Infix: "_WORD_SIMILARITY", Expr: "wordDistance"},
	{Infix: "_STRICT_WORD_SIMILARITY", Expr: "strictWordDistance"},
}
