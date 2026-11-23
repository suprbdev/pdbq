// Package contribtype maps pdbcore's contrib vocabularies (full-text search,
// pg_trgm, unaccent, fuzzystrmatch, hstore, ltree, citext, intarray, ranges
// and multiranges, inet/cidr, pgvector, cube, earthdistance) onto
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
//	vector / halfvec        -> [Float!] (input [Float!]), VectorFilterOps
//	                           (VectorDistanceFilterOps with distance thresholds)
//	sparsevec               -> String (pgvector's text form), VectorFilterOps
//	cube                    -> Cube scalar (JSON list of coordinates, or a pair
//	                           of corner lists), CubeFilterOps
//	                           (CubeDistanceFilterOps with distance thresholds)
//	earth (earthdistance)   -> LatLon { lat lon }, input LatLonInput,
//	                           LatLonFilterOps (LatLonDistanceFilterOps with
//	                           the radius filter)
//	interval                -> Interval { seconds minutes hours days months years },
//	                           input IntervalInput, IntervalFilterOps
//	interval[]              -> [Interval!], input [IntervalInput!], IntervalListFilterOps
//
// Text columns keep String but take StringFilterOps variants adding pg_trgm,
// unaccent and fuzzystrmatch operators ([StringOps]); int4[] columns with
// intarray keep [Int!] and may take [IntListIntarrayOps]; point columns with
// earthdistance keep their mapping and may take [PointDistanceOps].
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
// Extension types (citext, hstore, ltree, the pgvector types, cube, earth) map only when
// the catalog records the extension; a catalog without it keeps the String
// fallback, so schemas of databases that use none of these types are
// unchanged.
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
	// Vector is a pgvector vector, halfvec ([Float!]) or sparsevec (String)
	// column.
	Vector
	// Cube is a cube column (the Cube JSON scalar).
	Cube
	// Earth is a column declared as earthdistance's earth domain (LatLon).
	Earth
)

// Scalar names declared when a column needs them.
const (
	ScalarFullText        = "FullText"
	ScalarKeyValueHash    = "KeyValueHash"
	ScalarInternetAddress = "InternetAddress"
	ScalarCidrAddress     = "CidrAddress"
	ScalarCube            = "Cube"
)

// ScalarCubeDescription describes the Cube scalar.
const ScalarCubeDescription = "A cube (cube extension): a point as a list of coordinates ([1, 2, 3]) or a box as the pair of " +
	"its opposite corners ([[1, 2], [3, 4]]). Input also accepts the cube text form as a string (\"(1, 2), (3, 4)\")."

// Names of the earth location object and input types.
const (
	LatLonType  = "LatLon"
	LatLonInput = "LatLonInput"
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

// StringUnaccentOps and StringTrgmUnaccentOps are StringFilterOps and
// [StringTrgmOps] plus the unaccent operators (unaccentEqualTo,
// unaccentLike, unaccentIncludes, unaccentStartsWith).
const (
	StringUnaccentOps     = "StringUnaccentFilterOps"
	StringTrgmUnaccentOps = "StringTrgmUnaccentFilterOps"
)

// StringOps names the operator input of a text column from the vocabularies
// it takes on top of StringFilterOps: "String" + "Trgm" (pg_trgm) +
// "Unaccent" + "Fuzzy" (fuzzystrmatch) + "FilterOps", e.g.
// StringTrgmFuzzyFilterOps.
func StringOps(trgm, unaccent, fuzzy bool) string {
	name := "String"
	if trgm {
		name += "Trgm"
	}
	if unaccent {
		name += "Unaccent"
	}
	if fuzzy {
		name += "Fuzzy"
	}
	return name + "FilterOps"
}

// TextDistanceInput is the operand of levenshteinDistanceLessThan: a
// reference text and an exclusive edit-distance bound.
const TextDistanceInput = "TextDistanceInput"

// IntListIntarrayOps is IntListFilterOps plus intarray's matchesQueryInt,
// the operator input of int4[] columns the filter policy admits it on.
const IntListIntarrayOps = "IntListIntarrayFilterOps"

// KeyValueHashValueFilter is the operand of a <field>Value table-filter
// field: the key of an hstore column and the String comparisons its value
// must pass.
const KeyValueHashValueFilter = "KeyValueHashValueFilter"

// VectorOps is the operator input of pgvector columns: isNull only, since
// vectors have no meaningful scalar comparison.
const VectorOps = "VectorFilterOps"

// Distance-threshold filter inputs (contrib plugin): VectorOps plus the
// <distance>LessThan operators for vector and halfvec columns
// (VectorDistanceOps, operand VectorDistanceInput) and sparsevec columns
// (SparseVectorDistanceOps, SparseVectorDistanceInput), and StringFilterOps
// plus hammingDistanceLessThan / jaccardDistanceLessThan for bit columns
// (BitDistanceOps, BitDistanceInput). No index serves these operators.
const (
	VectorDistanceOps         = "VectorDistanceFilterOps"
	VectorDistanceInput       = "VectorDistanceInput"
	SparseVectorDistanceOps   = "SparseVectorDistanceFilterOps"
	SparseVectorDistanceInput = "SparseVectorDistanceInput"
	BitDistanceOps            = "BitDistanceFilterOps"
	BitDistanceInput          = "BitDistanceInput"
)

// Distance-threshold filter inputs of the cube and earthdistance
// vocabularies: CubeFilterOps plus the <distance>LessThan operators of cube
// columns (CubeDistanceOps, operand CubeDistanceInput {cube, distance}),
// LatLonFilterOps plus earthDistanceLessThan on earth columns
// (LatLonDistanceOps, operand LatLonDistanceInput {lat, lon, distance}), and
// the point column's StringFilterOps plus earthDistanceMilesLessThan
// (PointDistanceOps, operand LatLonDistanceInput). A GiST index serves the
// cube and earth thresholds; no index serves the point one.
const (
	CubeDistanceOps     = "CubeDistanceFilterOps"
	CubeDistanceInput   = "CubeDistanceInput"
	LatLonDistanceOps   = "LatLonDistanceFilterOps"
	LatLonDistanceInput = "LatLonDistanceInput"
	PointDistanceOps    = "PointDistanceFilterOps"
)

// Reference arguments of the distance orderings and scores: nearVector:
// holds a reference vector or cube per column, nearPoint: a reference
// location per earth or point column.
const (
	NearVectorArg = "nearVector"
	NearPointArg  = "nearPoint"
)

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
		case contrib.VocabVector:
			out.Kind, out.Scalar, out.OpsInput = Vector, "Float", VectorOps
			if v.Type == "sparsevec" {
				out.Scalar = "String"
			}
		case contrib.VocabCube:
			out.Kind, out.Scalar, out.OpsInput = Cube, ScalarCube, "CubeFilterOps"
		case contrib.VocabEarth:
			out.Kind, out.Scalar, out.OpsInput = Earth, LatLonType, "LatLonFilterOps"
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
		switch out.Kind {
		case Range, Multirange:
			out.Input = RangeInput(out.Scalar)
		case Earth:
			out.Input = LatLonInput
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

// List reports whether a single value of the column's own type is a
// GraphQL list of Scalar: a multirange, or a dense pgvector vector.
func (c Column) List() bool {
	return c.Kind == Multirange || (c.Kind == Vector && c.Scalar != "String")
}

// Declares reports whether the mapping needs a schema type of its own
// (Scalar): false for types mapped onto String or the built-in Float.
func (c Column) Declares() bool {
	return c.Owned() && c.Scalar != "String" && c.Kind != Vector
}

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
func (c Column) Trgm() (contrib.Vocabulary, bool) { return c.vocab(contrib.VocabTrgm) }

// Unaccent returns the unaccent vocabulary applying to the column, if any.
func (c Column) Unaccent() (contrib.Vocabulary, bool) { return c.vocab(contrib.VocabUnaccent) }

// Fuzzy returns the fuzzystrmatch vocabulary applying to the column, if any.
func (c Column) Fuzzy() (contrib.Vocabulary, bool) { return c.vocab(contrib.VocabFuzzy) }

// Intarray returns the intarray vocabulary applying to the column (an
// int4[] column with intarray installed), if any.
func (c Column) Intarray() (contrib.Vocabulary, bool) { return c.vocab(contrib.VocabIntarray) }

// Vocab returns the vocabulary of the given name applying to the column, if
// any.
func (c Column) Vocab(name string) (contrib.Vocabulary, bool) { return c.vocab(name) }

// Distances returns the vocabulary holding the column's distances to a
// per-request reference: its own for vector, halfvec, sparsevec, cube and
// earth columns, pgvector's bit vocabulary for bit and varbit columns and
// earthdistance's point vocabulary for point columns.
func (c Column) Distances() (contrib.Vocabulary, bool) {
	switch c.Kind {
	case Vector, Cube, Earth:
		return c.Own, true
	}
	if v, ok := c.vocab(contrib.VocabBit); ok {
		return v, true
	}
	return c.vocab(contrib.VocabEarthPoint)
}

// DistanceInput returns the GraphQL type of the column's reference (the
// nearVector: or nearPoint: field, the reference of distance-threshold
// operands): a list of floats for vector and halfvec, the text form for
// sparsevec and bit, Cube for cube columns and LatLonInput for earth and
// point columns.
func (c Column) DistanceInput() string {
	switch {
	case c.List():
		return "[" + c.Scalar + "!]"
	case c.Kind == Cube:
		return ScalarCube
	case c.ReferenceArg() == NearPointArg:
		return LatLonInput
	}
	return "String"
}

// ReferenceArg names the list argument holding the column's distance
// reference: [NearPointArg] for earth and point columns, [NearVectorArg]
// for the other columns with [Column.Distances].
func (c Column) ReferenceArg() string {
	if c.Kind == Earth {
		return NearPointArg
	}
	if _, ok := c.vocab(contrib.VocabEarthPoint); ok {
		return NearPointArg
	}
	return NearVectorArg
}

// IsDistanceOp reports whether o is a distance threshold against a
// per-operand reference (the operators of the distance-threshold inputs).
func IsDistanceOp(o contrib.Op) bool {
	switch o.Arg {
	case contrib.ArgVectorDistance, contrib.ArgCubeDistance, contrib.ArgLatLonDistance:
		return true
	}
	return false
}

// DistanceOps returns the distance thresholds of the column's
// [Column.Distances] vocabulary.
func (c Column) DistanceOps() []contrib.Op {
	dv, ok := c.Distances()
	if !ok {
		return nil
	}
	var out []contrib.Op
	for _, o := range dv.Ops {
		if IsDistanceOp(o) {
			out = append(out, o)
		}
	}
	return out
}

func (c Column) vocab(name string) (contrib.Vocabulary, bool) {
	for _, v := range c.Vocabs {
		if v.Name == name {
			return v, true
		}
	}
	return contrib.Vocabulary{}, false
}

// FilterOps returns the vocabulary operators the contrib plugin exposes on
// the column's own operator input (OpsInput): the owning vocabulary's
// operators. Text vocabularies (pg_trgm, unaccent) are exposed through
// [StringTrgmOps], [StringUnaccentOps] and [StringTrgmUnaccentOps] instead,
// the distance thresholds (pgvector, cube, earthdistance) through the
// distance-threshold inputs, and full-text search over text columns is not
// exposed.
func (c Column) FilterOps() []contrib.Op {
	if !c.Owned() || c.Kind == Vector {
		return nil
	}
	var out []contrib.Op
	for _, o := range c.Own.Ops {
		if !IsDistanceOp(o) {
			out = append(out, o)
		}
	}
	return out
}

// Op finds a filter operator by its GraphQL name among the column's owning,
// pg_trgm, unaccent, pgvector bit, fuzzystrmatch, intarray and earthdistance
// point vocabularies.
func (c Column) Op(name string) (contrib.Op, bool) {
	var vs []contrib.Vocabulary
	if c.Owned() {
		vs = append(vs, c.Own)
	}
	for _, n := range []string{contrib.VocabTrgm, contrib.VocabUnaccent, contrib.VocabBit, contrib.VocabFuzzy, contrib.VocabIntarray, contrib.VocabEarthPoint} {
		if v, ok := c.vocab(n); ok {
			vs = append(vs, v)
		}
	}
	for _, v := range vs {
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
		"hasKey":        "containsKey",
		"hasAllKeys":    "containsAllKeys",
		"hasAnyKeys":    "containsAnyKeys",
		"hasDefinedKey": "containsDefinedKey",
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

// IndexServes reports whether an index of t led by column col serves one
// of the hints: its access method matches and, when both the hint and the
// catalog name one, so does the leading column's operator class.
func IndexServes(t *introspect.Table, col string, hints []contrib.IndexHint) bool {
	for _, ix := range t.Indexes {
		if len(ix.Columns) == 0 || ix.Columns[0] != col {
			continue
		}
		for _, h := range hints {
			if h.Serves(ix.Method, ix.OpClass(0)) {
				return true
			}
		}
	}
	return false
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

// DistanceOrder describes one distance ordering of a column with
// [Column.Distances]: the orderBy value infix and the vocabulary expression
// it sorts by.
type DistanceOrder struct {
	// Infix sits between the column's enum name and the direction:
	// EMBEDDING_COSINE_DISTANCE_ASC.
	Infix string
	// Expr is the vocabulary's distance expression; nearest rows have the
	// smallest distance.
	Expr string
	// Label names the metric in descriptions.
	Label string
}

// DistanceOrders lists the distance ordering families in schema order: the
// pgvector vector distances, the bit distances, then cube's Chebyshev
// distance (cube shares L2 and L1 with pgvector) and the earthdistance
// distances. A column takes the families its vocabulary has an expression
// for.
var DistanceOrders = []DistanceOrder{
	{Infix: "_L2_DISTANCE", Expr: "l2Distance", Label: "Euclidean (L2) distance"},
	{Infix: "_NEGATIVE_INNER_PRODUCT", Expr: "negativeInnerProduct", Label: "negative inner product"},
	{Infix: "_COSINE_DISTANCE", Expr: "cosineDistance", Label: "cosine distance"},
	{Infix: "_L1_DISTANCE", Expr: "l1Distance", Label: "taxicab (L1) distance"},
	{Infix: "_HAMMING_DISTANCE", Expr: "hammingDistance", Label: "Hamming distance"},
	{Infix: "_JACCARD_DISTANCE", Expr: "jaccardDistance", Label: "Jaccard distance"},
	{Infix: "_CHEBYSHEV_DISTANCE", Expr: "chebyshevDistance", Label: "Chebyshev (L-infinity) distance"},
	{Infix: "_EARTH_DISTANCE", Expr: "earthDistance", Label: "great-circle distance in metres"},
	{Infix: "_EARTH_DISTANCE_MILES", Expr: "earthDistanceMiles", Label: "distance in statute miles"},
}
