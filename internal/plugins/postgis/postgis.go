// Package postgis is a built-in plugin exposing PostGIS spatial columns as a
// full GraphQL query surface.
//
// Default generation already reads and writes spatial columns as GeoJSON (the
// core compiler handles the GeoJSON scalar). This plugin adds everything built
// on top of that:
//
//   - spatial filters: every geometry/geography column gets a
//     GeoJSONFilter operand set covering bounding-box operators
//     (bboxIntersects/&&, bboxContains/~, bboxContainedBy/@, the directional
//     bboxLeftOf/<< ... bboxOverlapsOrAbove/|&>, bboxEquals/~=,
//     bboxIntersectsND/&&&), the DE-9IM predicates (intersects, contains,
//     within, covers, crosses, touches, overlaps, equals, disjoint,
//     containsProperly, coveredBy, intersects3D), exact and ordering equality
//     (exactlyEquals/=, orderingEquals), and radius search (dwithin/beyond).
//     Bounding-box and dwithin operators are GiST-index-usable, which is what
//     keeps viewport and "near me" queries fast on large tables.
//
//   - derived accessors: each spatial column gains sibling fields for the
//     scalar properties PostGIS can compute in the same statement —
//     <field>Area, <field>Length, <field>Perimeter, <field>Centroid,
//     <field>Envelope, <field>Srid, <field>GeometryType, <field>IsValid,
//     <field>X/Y/Z/M, <field>NumPoints, <field>NumGeometries,
//     <field>StartPoint/EndPoint, <field>PointOnSurface, <field>Wkt/Ewkt,
//     <field>IsEmpty, <field>IsSimple and <field>Xmin/Ymin/Xmax/Ymax.
//     Accessors that only answer for some geometry types are omitted on a
//     column whose typmod rules them out (no locationX on a LineString
//     column, no locationZ on a 2D Point column).
//
//   - projection arguments: spatial fields accept buffer: (ST_Buffer
//     distance, metres on geography), simplify: (ST_SimplifyPreserveTopology
//     tolerance), transform: (reproject to an SRID) and precision: (decimal
//     digits of the GeoJSON coordinates), so clients can pull buffered,
//     web-mercator, low-vertex or compact geometry without a second query.
//
//   - distance ordering: <COLUMN>_DISTANCE_ASC/_DESC orderBy values plus a
//     `near:` argument on list and connection fields, compiled to the
//     index-assisted KNN operator (<->), and a <field>Distance: Float score
//     selecting the distance itself (ST_Distance; metres on geography, SRID
//     units on geometry unless useSpheroid: true).
//
//   - rasters: postgis_raster columns get no output field of their own (the
//     pixel data is never returned; a computed column encoding it is the
//     opt-in), but header-only metadata accessors (<field>Width,
//     <field>Height, <field>NumBands, <field>Srid, <field>PixelWidth,
//     <field>PixelHeight, <field>Envelope as GeoJSON), the pixel value
//     <field>Value(point:, band:) (ST_Value), per-band
//     <field>SummaryStats(band:) (ST_SummaryStats) with the derived
//     setting, and a RasterFilter { isNull intersects } filter field with
//     the filters setting.
//
//   - typed geometry (opt-in, typed_geometry: true): spatial column fields
//     return `@graphile/postgis`-style objects (GeometryInterface,
//     GeometryPoint {x y}, GeometryPolygon {exterior interiors}, ...; see
//     package geotype) instead of the GeoJSON scalar, typed from the column's
//     typmod; computed columns, function results and composite attributes
//     too, with GeometryInterface when no typmod is known. Inputs stay GeoJSON.
//
// Geometry and geography are both supported. They share the operator
// vocabulary but differ in units: geography measures in metres, geometry in
// SRID units. dwithin takes a `useSpheroid` flag so lat/lon data stored as
// plain geometry can still be searched in metres.
//
// Like advanced_filters, the plugin only adds schema surface plus catalog
// bindings; the compiler reads the bindings and stays inert when they are
// absent, so disabling the plugin (plugins.disabled: [postgis]) removes the
// surface completely while plain GeoJSON read/write keeps working.
//
// Per-object control follows the same smart-comment conventions as the rest of
// the codebase (package omit): "@omit filter" on a spatial column skips its
// filter surface, "@omit order" its distance ordering, and "@omit read" its
// derived accessors, distance field and distance ordering (measuring the
// distance from a client-chosen point reads the value back); on a raster
// column "@omit read" removes every raster field and its filter.
package postgis

import (
	"context"
	"slices"
	"strings"

	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbq/internal/geotype"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/omit"
	"github.com/suprbdev/pdbq/internal/schema"
)

// Plugin implements the SchemaHook adding the spatial surface.
type Plugin struct {
	// filters generates the spatial operator inputs.
	filters bool
	// derived generates the <field>Area/<field>Centroid/... accessors.
	derived bool
	// ordering generates distance orderBy values, the <field>Distance
	// fields and the `near:` argument.
	ordering bool
	// projection generates the transform:/simplify: field arguments.
	projection bool
	// typedGeometry types spatial column fields as geometry objects instead
	// of the GeoJSON scalar.
	typedGeometry bool
}

// Name is the plugin's name in plugins.disabled and plugins.settings.
const Name = "postgis"

// Settings is the plugins.settings.postgis section.
type Settings struct {
	Filters       bool `koanf:"filters" doc:"GeoJSONFilter on spatial columns and RasterFilter on raster columns."`
	Derived       bool `koanf:"derived" doc:"Derived accessors (<field>Area, <field>Centroid, ...) and the raster accessors, pixel value and statistics."`
	Ordering      bool `koanf:"ordering" doc:"Distance ordering (<COLUMN>_DISTANCE_ASC/_DESC), the <field>Distance fields and the near: argument."`
	Projection    bool `koanf:"projection" doc:"The transform:, simplify:, precision: and buffer: projection arguments of spatial column fields."`
	TypedGeometry bool `koanf:"typed_geometry" doc:"Type spatial column fields as @graphile/postgis-style geometry objects instead of the GeoJSON scalar."`
}

// DefaultSettings returns the documented defaults: every surface on, typed
// geometry off.
func DefaultSettings() Settings {
	return Settings{Filters: true, Derived: true, Ordering: true, Projection: true}
}

// New builds the plugin from its settings.
func New(s Settings) *Plugin {
	return &Plugin{
		filters:       s.Filters,
		derived:       s.Derived,
		ordering:      s.Ordering,
		projection:    s.Projection,
		typedGeometry: s.TypedGeometry,
	}
}

func (p *Plugin) Name() string { return Name }

// Priority 160 places the plugin just after advanced_filters (150), so the
// filter inputs it extends already exist and relation filters are in place.
func (p *Plugin) Priority() int { return 160 }

// ---- SchemaHook ----

func (p *Plugin) TransformSchema(_ context.Context, b *schema.Builder) error {
	spatial := hasSpatialColumn(b.Catalog)
	if !spatial && !hasRasterColumn(b.Catalog) {
		return nil
	}
	if spatial {
		p.addSharedInputs(b)
	}
	rb := &rasterBuild{p: p, b: b}
	for _, t := range b.Catalog.Tables {
		if rc := rasterColumns(t); len(rc) > 0 {
			rb.add(t, rc)
		}
		cols := spatialColumns(t)
		if len(cols) == 0 {
			continue
		}
		if p.filters {
			p.addSpatialFilters(b, t, cols)
		}
		if p.derived {
			p.addDerivedFields(b, t, cols)
		}
		if p.projection {
			p.addProjectionArgs(b, t, cols)
		}
		if p.ordering {
			p.addDistanceOrdering(b, t, cols)
		}
	}
	if p.typedGeometry {
		p.addTypedGeometry(b)
	}
	return nil
}

// hasSpatialColumn reports whether any table carries a PostGIS column.
func hasSpatialColumn(cat *introspect.Catalog) bool {
	for _, t := range cat.Tables {
		if len(spatialColumns(t)) > 0 {
			return true
		}
	}
	return false
}

// spatialColumns returns the geometry/geography columns of a table.
func spatialColumns(t *introspect.Table) []*introspect.Column {
	var out []*introspect.Column
	for _, c := range t.Columns {
		if postgis.IsSpatial(c.PGType) {
			out = append(out, c)
		}
	}
	return out
}

// hasRasterColumn reports whether any table carries a postgis_raster column.
func hasRasterColumn(cat *introspect.Catalog) bool {
	for _, t := range cat.Tables {
		if len(rasterColumns(t)) > 0 {
			return true
		}
	}
	return false
}

// rasterColumns returns the raster columns of a table.
func rasterColumns(t *introspect.Table) []*introspect.Column {
	var out []*introspect.Column
	for _, c := range t.Columns {
		if postgis.IsRaster(c.PGType) && !c.IsArray {
			out = append(out, c)
		}
	}
	return out
}

// Shared input type names.
const (
	bboxInput    = "BBoxInput"
	dwithinInput = "DWithinInput"
)

// addSharedInputs declares the operand types every spatial column reuses.
func (p *Plugin) addSharedInputs(b *schema.Builder) {
	if _, ok := b.Inputs[bboxInput]; !ok {
		in := &schema.Input{
			Name: bboxInput,
			Description: "An axis-aligned bounding box. Corners are in the coordinate system " +
				"identified by srid (WGS84 lon/lat when omitted).",
			Fields: []*schema.InputField{
				{Name: "minX", Type: "Float!", Description: "Minimum X (longitude)."},
				{Name: "minY", Type: "Float!", Description: "Minimum Y (latitude)."},
				{Name: "maxX", Type: "Float!", Description: "Maximum X (longitude)."},
				{Name: "maxY", Type: "Float!", Description: "Maximum Y (latitude)."},
				{Name: "srid", Type: "Int", Default: "4326", Description: "Spatial reference identifier of the corners."},
			},
		}
		b.Inputs[bboxInput] = in
	}
	if _, ok := b.Inputs[dwithinInput]; !ok {
		b.Inputs[dwithinInput] = &schema.Input{
			Name: dwithinInput,
			Description: "A radius search: a geometry plus a distance. The distance is in metres " +
				"for geography columns and in SRID units for geometry columns, unless useSpheroid is set.",
			Fields: []*schema.InputField{
				{Name: "geometry", Type: postgis.TypeName + "!", Description: "Centre geometry, as GeoJSON or WKT."},
				{Name: "distance", Type: "Float!", Description: "Search radius."},
				{Name: "useSpheroid", Type: "Boolean", Default: "false",
					Description: "Measure in metres on a geometry column by casting both sides to geography."},
			},
		}
	}
}

// opsInputName is the shared operator input for spatial columns. Geometry and
// geography share one type: the operator set is identical and the unit
// difference is a property of the column, not of the input.
const opsInputName = "GeoJSONFilter"

// addSpatialFilters gives each spatial column a filter field bound to the
// shared spatial operator input.
func (p *Plugin) addSpatialFilters(b *schema.Builder, t *introspect.Table, cols []*introspect.Column) {
	filterName := b.Inflect(inflect.KindFilterTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
	in, ok := b.Inputs[filterName]
	if !ok {
		return
	}
	p.ensureOpsInput(b)
	allowed := filterableSet(b, t)
	for _, c := range cols {
		if omit.Column(c, omit.Filter) {
			continue
		}
		// Respect the indexed-only policy exactly like scalar columns: a
		// spatial filter without a GiST index is a sequential scan.
		if !allowed[c.Name] {
			continue
		}
		name := b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name})
		in.AddField(&schema.InputField{
			Name:        name,
			Type:        opsInputName,
			Description: "Spatial filters on " + c.Name + ".",
			Column:      c.Name,
		})
	}
}

// filterableSet reports which of the table's columns the indexed-only policy
// admits, as a set for cheap lookup.
func filterableSet(b *schema.Builder, t *introspect.Table) map[string]bool {
	out := map[string]bool{}
	for _, c := range b.FilterableColumns(t) {
		out[c.Name] = true
	}
	return out
}

// ensureOpsInput declares GeoJSONFilter once per schema.
func (p *Plugin) ensureOpsInput(b *schema.Builder) {
	b.ClaimOperatorInput(opsInputName)
	if _, ok := b.Inputs[opsInputName]; ok {
		return
	}
	in := &schema.Input{
		Name: opsInputName,
		Description: "Spatial operators for a PostGIS geometry or geography column. " +
			"Bounding-box operators and dwithin can use a GiST index; the exact " +
			"predicates are precise but scan more rows.",
	}
	for _, op := range postgis.Ops() {
		desc := op.Description
		if op.Indexable {
			desc += " Index-usable."
		}
		in.AddField(&schema.InputField{Name: op.Name, Type: op.OperandType, Description: desc})
	}
	b.Inputs[opsInputName] = in
}

// addDerivedFields adds the computed spatial accessors as sibling fields of
// each spatial column (location -> locationArea, locationCentroid, ...),
// skipping accessors the column's typmod makes always-null.
func (p *Plugin) addDerivedFields(b *schema.Builder, t *introspect.Table, cols []*introspect.Column) {
	typeName := b.TypeForTable[t.Schema+"."+t.Name]
	obj, ok := b.Objects[typeName]
	if !ok {
		return
	}
	for _, c := range cols {
		if omit.Column(c, omit.Read) {
			continue
		}
		base := b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name})
		for _, d := range postgis.Deriveds() {
			if !derivedApplies(d, c) {
				continue
			}
			name := base + d.Suffix
			if obj.Field(name) != nil {
				// A real column already owns the name; leave it alone.
				continue
			}
			obj.AddField(&schema.Field{
				Name:        name,
				Type:        d.Type,
				Description: d.Description,
				Meta: &schema.FieldMeta{
					Kind:   schema.KindSpatialDerived,
					Table:  t,
					Column: c,
					Nested: map[string]any{"suffix": d.Suffix},
				},
			})
		}
	}
}

// derivedApplies reports whether accessor d can be non-null on column c. A
// column without typmod information (an old schema cache, or a hand-built
// catalog) is treated as unconstrained.
func derivedApplies(d postgis.Derived, c *introspect.Column) bool {
	if c.Spatial == nil {
		return true
	}
	return d.AppliesTo(string(c.Spatial.Subtype), c.Spatial.HasZ, c.Spatial.HasM)
}

// addTypedGeometry declares the geometry object types of each flavour in use
// and retypes every non-array spatial output field to the type its typmod
// implies: table column fields, composite attributes, the output parameters
// of record-returning functions, computed columns, and the results of query
// and mutation functions. A function result has no typmod, so it takes the
// top-level interface. A flavour whose type names collide with an existing
// type is left on the GeoJSON scalar.
func (p *Plugin) addTypedGeometry(b *schema.Builder) {
	type target struct {
		field *schema.Field
		typ   geotype.Type
	}
	byFlavour := map[postgis.Flavour][]target{}
	for _, obj := range b.Objects {
		for _, f := range obj.Fields {
			c := spatialOutput(b, f)
			if c == nil {
				continue
			}
			gt, ok := geotype.ForColumn(c)
			if !ok {
				continue
			}
			byFlavour[gt.Flavour] = append(byFlavour[gt.Flavour], target{f, gt})
		}
	}
	for _, flavour := range []postgis.Flavour{postgis.Geometry, postgis.Geography} {
		targets := byFlavour[flavour]
		if len(targets) == 0 {
			continue
		}
		types := geotype.All(flavour)
		if clash := typeClash(b, types); clash != "" {
			b.Options.Logger.Warn("postgis: typed geometry disabled, type name already taken",
				"flavour", flavour.String(), "type", clash)
			continue
		}
		for _, gt := range types {
			b.Objects[gt.Name()] = geometryObject(gt)
		}
		for _, tg := range targets {
			tg.field.Type = strings.Replace(tg.field.Type, postgis.TypeName, tg.typ.Name(), 1)
		}
	}
}

// spatialOutput returns the column describing the spatial value f reads when
// f is a non-array output field currently typed as the GeoJSON scalar, or
// nil.
func spatialOutput(b *schema.Builder, f *schema.Field) *introspect.Column {
	if f.Meta == nil || strings.Trim(f.Type, "[]!") != postgis.TypeName {
		return nil
	}
	var c *introspect.Column
	switch f.Meta.Kind {
	case schema.KindColumn, schema.KindCompositeField:
		c = f.Meta.Column
	case schema.KindFunction, schema.KindComputed, schema.KindPayloadResult:
		if f.Meta.Function != nil {
			c = schema.FuncReturnColumn(b.Catalog, f.Meta.Function)
		}
	}
	if c == nil || c.IsArray || !postgis.IsSpatial(c.PGType) {
		return nil
	}
	return c
}

// typeClash returns the first of types whose name an existing output, input
// or enum type already uses.
func typeClash(b *schema.Builder, types []geotype.Type) string {
	for _, gt := range types {
		n := gt.Name()
		_, obj := b.Objects[n]
		_, in := b.Inputs[n]
		_, en := b.Enums[n]
		if obj || in || en {
			return n
		}
	}
	return ""
}

// geometryObject renders one geotype as a schema object or interface.
func geometryObject(gt geotype.Type) *schema.Object {
	obj := &schema.Object{
		Name:        gt.Name(),
		Description: gt.Description(),
		IsInterface: !gt.Concrete(),
	}
	for _, iface := range gt.Interfaces() {
		obj.Interfaces = append(obj.Interfaces, iface.Name())
	}
	for _, f := range gt.Fields() {
		obj.Fields = append(obj.Fields, &schema.Field{
			Name:        f.Name,
			Type:        f.Type,
			Description: f.Description,
			Meta:        &schema.FieldMeta{Kind: schema.KindSynthetic, CostMultiplier: 1},
		})
	}
	return obj
}

// addProjectionArgs adds transform:, simplify:, buffer: and precision: to
// each spatial column field, and prices buffer: in the cost estimate.
func (p *Plugin) addProjectionArgs(b *schema.Builder, t *introspect.Table, cols []*introspect.Column) {
	typeName := b.TypeForTable[t.Schema+"."+t.Name]
	obj, ok := b.Objects[typeName]
	if !ok {
		return
	}
	for _, c := range cols {
		name := b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name})
		f := obj.Field(name)
		if f == nil || f.Meta == nil || f.Meta.Kind != schema.KindColumn || f.Meta.Column != c {
			continue
		}
		f.Args = append(f.Args,
			schema.Arg{Name: "transform", Type: "Int"},
			schema.Arg{Name: "simplify", Type: "Float"},
			schema.Arg{Name: "buffer", Type: "Float", Description: "Return the area within this distance of the value (ST_Buffer): metres on geography, SRID units on geometry; negative distances shrink polygons. Applied before simplify: and transform:."},
			schema.Arg{Name: "precision", Type: "Int", Description: "Round the GeoJSON coordinates to this many decimal places (0 to 15; PostGIS's default is 9, 6 is about 10 cm on lon/lat data)."},
		)
		if f.Meta.ArgCost == nil {
			f.Meta.ArgCost = map[string]int{}
		}
		f.Meta.ArgCost["buffer"] = postgis.BufferCost
	}
}

// addDistanceOrdering adds <COLUMN>_DISTANCE_ASC/_DESC values to the table's
// orderBy enum and a <field>Distance score field per readable spatial
// column, and the column's reference geometry to the table's
// <Type>NearInput (see [schema.Builder.NearInput]).
//
// The enum values and score fields carry the column binding; the reference
// geometry comes from `near:` at query time, which is why they are generated
// together. Scores are projections of the returned rows, so unlike the
// ordering they ignore the indexed-only policy and @omit order.
func (p *Plugin) addDistanceOrdering(b *schema.Builder, t *introspect.Table, cols []*introspect.Column) {
	enumName := b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
	en, ok := b.Enums[enumName]
	obj := b.Objects[b.TypeForTable[t.Schema+"."+t.Name]]
	if !ok || obj == nil {
		return
	}
	allowed := filterableSet(b, t)
	for _, c := range cols {
		if omit.Column(c, omit.Read) {
			continue
		}
		field := b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name})
		scored := obj.Field(field+"Distance") == nil
		ordered := !omit.Column(c, omit.Order) && allowed[c.Name]
		if !scored && !ordered {
			continue
		}
		near := b.NearInput(t)
		if near == nil {
			return
		}
		near.AddField(&schema.InputField{
			Name:        field,
			Type:        postgis.TypeName,
			Description: "Reference geometry for the " + c.Name + " distance orderBy values and distance field, as GeoJSON or WKT.",
			Column:      c.Name,
		})
		if scored {
			addDistanceField(obj, t, c, field)
		}
		if !ordered {
			continue
		}
		base := coreinflect.EnumValue(c.Name)
		for _, dir := range []struct {
			suffix string
			desc   bool
		}{{"_DISTANCE_ASC", false}, {"_DISTANCE_DESC", true}} {
			value := base + dir.suffix
			if en.HasValue(value) {
				continue
			}
			en.Values = append(en.Values, schema.EnumValue{
				Name: value,
				Desc: dir.desc,
				Description: "Order by distance from the " + field + " geometry of the near: argument " +
					"(nearest first for _ASC). Requires it.",
				Column:   c.Name,
				Distance: true,
			})
		}
	}
}

// addDistanceField adds <field>Distance: Float to obj, the distance of
// column c from its geometry in the list's near: argument. Geometry columns
// take useSpheroid: to measure in metres.
func addDistanceField(obj *schema.Object, t *introspect.Table, c *introspect.Column, field string) {
	desc := "Distance of " + c.Name + " from the " + field + " geometry in the near: argument of the list this row was read " +
		"through (required there); null for rows read elsewhere. "
	var args []schema.Arg
	if postgis.FlavourOf(c.PGType) == postgis.Geography {
		desc += "In metres."
	} else {
		desc += "In the units of the column's SRID, or in metres on the spheroid with useSpheroid: true " +
			"(lon/lat SRIDs only)."
		args = []schema.Arg{{
			Name: "useSpheroid", Type: "Boolean", Default: "false",
			Description: "Measure in metres on the WGS 84 spheroid by casting both geometries to geography.",
		}}
	}
	obj.AddField(&schema.Field{
		Name:        field + "Distance",
		Type:        "Float",
		Args:        args,
		Description: desc,
		Meta:        &schema.FieldMeta{Kind: schema.KindScore, Table: t, Column: c, Score: &schema.Score{}},
	})
}

// Raster type names.
const (
	rasterOpsInput = "RasterFilter"
	rasterStats    = "RasterSummaryStats"
)

// rasterBuild is the per-TransformSchema state of the raster surface: the
// shared RasterFilter and RasterSummaryStats types are declared on first
// use, or found taken.
type rasterBuild struct {
	p *Plugin
	b *schema.Builder
	// ops and stats are "", "declared" or "taken".
	ops, stats string
}

// add adds the raster columns' filter fields (filters setting) and
// metadata, pixel value and summary statistics fields (derived setting),
// declaring the GeoJSON scalar they take when no spatial column did.
func (rb *rasterBuild) add(t *introspect.Table, cols []*introspect.Column) {
	added := false
	if rb.p.filters {
		added = rb.addFilters(t, cols)
	}
	if rb.p.derived && rb.addFields(t, cols) {
		added = true
	}
	if added && !slices.Contains(rb.b.Scalars, postgis.TypeName) {
		rb.b.Scalars = append(rb.b.Scalars, postgis.TypeName)
	}
}

// addFilters gives each raster column the indexed-only policy admits (an
// allowlisted column under filters.indexed_only: the GiST index serving
// intersects is an expression index on ST_ConvexHull, which leads no
// column) a filter field typed RasterFilter, reporting whether any was
// added.
func (rb *rasterBuild) addFilters(t *introspect.Table, cols []*introspect.Column) bool {
	b := rb.b
	in := b.Inputs[b.Inflect(inflect.KindFilterTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})]
	if in == nil {
		return false
	}
	allowed := filterableSet(b, t)
	added := false
	for _, c := range cols {
		if omit.Column(c, omit.Filter) || !allowed[c.Name] || !rb.ensureOps() {
			continue
		}
		in.AddField(&schema.InputField{
			Name:        b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name}),
			Type:        rasterOpsInput,
			Description: "Raster filters on " + c.Name + ".",
			Column:      c.Name,
		})
		added = true
	}
	return added
}

// ensureOps declares RasterFilter once per schema, reporting false when
// another type took the name.
func (rb *rasterBuild) ensureOps() bool {
	if rb.ops == "" {
		rb.ops = "taken"
		rb.b.ClaimOperatorInput(rasterOpsInput)
		if typeTaken(rb.b, rasterOpsInput) {
			rb.b.Options.Logger.Warn("postgis: type name already taken, raster filters disabled", "type", rasterOpsInput)
			return false
		}
		rb.ops = "declared"
		rb.b.Inputs[rasterOpsInput] = &schema.Input{
			Name:        rasterOpsInput,
			Description: "Operators for a PostGIS raster column.",
			Fields: []*schema.InputField{
				{Name: "isNull", Type: "Boolean", Description: "Is null (if true) or is not null (if false)."},
				{Name: "intersects", Type: postgis.TypeName, Description: "The raster's convex hull and the given geometry share any " +
					"space (ST_Intersects). The geometry must be in the raster's SRID: give it as EWKT (SRID=n;...) unless the raster " +
					"is in WGS 84. A GiST index on ST_ConvexHull(column) can serve it."},
			},
		}
	}
	return rb.ops == "declared"
}

// typeTaken reports whether an output, input or enum type uses name.
func typeTaken(b *schema.Builder, name string) bool {
	_, obj := b.Objects[name]
	_, in := b.Inputs[name]
	_, en := b.Enums[name]
	return obj || in || en
}

// addFields adds the metadata accessors, <field>Value(point:, band:) and
// <field>SummaryStats(band:) next to each readable raster column, skipping
// names a column or another field already owns, and reports whether any
// was added.
func (rb *rasterBuild) addFields(t *introspect.Table, cols []*introspect.Column) bool {
	b := rb.b
	obj := b.Objects[b.TypeForTable[t.Schema+"."+t.Name]]
	if obj == nil {
		return false
	}
	added := false
	add := func(f *schema.Field) {
		if obj.Field(f.Name) != nil {
			b.Options.Logger.Warn("postgis: field name already taken, raster field skipped", "type", obj.Name, "field", f.Name)
			return
		}
		obj.AddField(f)
		added = true
	}
	band := schema.Arg{Name: "band", Type: "Int", Default: "1", Description: "1-based band number."}
	for _, c := range cols {
		if omit.Column(c, omit.Read) {
			continue
		}
		base := b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name})
		for _, a := range postgis.RasterAccessors() {
			add(&schema.Field{
				Name: base + a.Suffix, Type: a.Type, Description: a.Description,
				Meta: &schema.FieldMeta{Kind: schema.KindRasterAccessor, Table: t, Column: c, Nested: map[string]any{"suffix": a.Suffix}},
			})
		}
		add(&schema.Field{
			Name: base + "Value", Type: "Float",
			Args: []schema.Arg{
				{Name: "point", Type: postgis.TypeName + "!", Description: "Point to sample, as GeoJSON or WKT; reprojected to the raster's SRID."},
				band,
			},
			Description: "Value of the pixel of " + c.Name + " at the given point in the given band (ST_Value); null for " +
				"NODATA, a point outside the raster or a missing band.",
			Meta: &schema.FieldMeta{Kind: schema.KindRasterValue, Table: t, Column: c, RowCost: rowCost(postgis.RasterValueCost)},
		})
		if rb.ensureStats() {
			add(&schema.Field{
				Name: base + "SummaryStats", Type: rasterStats, Args: []schema.Arg{band},
				Description: "Summary statistics of one band of " + c.Name + " (ST_SummaryStats, NODATA pixels excluded); " +
					"null for a missing band. Reads every pixel of the band.",
				Meta: &schema.FieldMeta{Kind: schema.KindRasterSummaryStats, Table: t, Column: c, RowCost: rowCost(postgis.RasterSummaryStatsCost)},
			})
		}
	}
	return added
}

// rowCost converts a cost weight in units of one column read (pdbcore's
// Raster*Cost) into the FieldMeta.RowCost surcharge on top of the unit cost
// every field is charged.
func rowCost(units int) int { return units - 1 }

// ensureStats declares the RasterSummaryStats object once per schema,
// reporting false when another type took the name.
func (rb *rasterBuild) ensureStats() bool {
	if rb.stats == "" {
		rb.stats = "taken"
		if typeTaken(rb.b, rasterStats) {
			rb.b.Options.Logger.Warn("postgis: type name already taken, raster summary statistics disabled", "type", rasterStats)
			return false
		}
		rb.stats = "declared"
		obj := &schema.Object{Name: rasterStats, Description: "Summary statistics of one band of a raster (ST_SummaryStats)."}
		for _, f := range postgis.RasterSummaryStatsFields {
			obj.Fields = append(obj.Fields, &schema.Field{
				Name: f.Name, Type: f.Type, Description: f.Description,
				Meta: &schema.FieldMeta{Kind: schema.KindSynthetic},
			})
		}
		rb.b.Objects[rasterStats] = obj
	}
	return rb.stats == "declared"
}
