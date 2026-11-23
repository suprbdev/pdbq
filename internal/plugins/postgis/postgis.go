// Package postgis is a built-in plugin exposing PostGIS spatial columns as a
// full GraphQL query surface.
//
// Default generation already reads and writes spatial columns as GeoJSON (the
// core compiler handles the GeoJSON scalar). This plugin adds everything built
// on top of that:
//
//   - spatial filters: every geometry/geography column gets a
//     GeometryFilterOps operand set covering bounding-box operators
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
//   - projection arguments: spatial fields accept transform: (reproject to an
//     SRID) and simplify: (ST_SimplifyPreserveTopology tolerance), so clients
//     can pull web-mercator or low-vertex geometry without a second query.
//
//   - distance ordering: <COLUMN>_DISTANCE_ASC/_DESC orderBy values plus a
//     `near:` argument on list and connection fields, compiled to the
//     index-assisted KNN operator (<->).
//
//   - typed geometry (opt-in, typed_geometry: true): spatial column fields
//     return `@graphile/postgis`-style objects (GeometryInterface,
//     GeometryPoint {x y}, GeometryPolygon {exterior interiors}, ...; see
//     package geotype) instead of the GeoJSON scalar, typed from the column's
//     typmod. Inputs stay GeoJSON.
//
// Geometry and geography are both supported. They share the operator
// vocabulary but differ in units: geography measures in metres, geometry in
// SRID units. dwithin takes a `useSpheroid` flag so lat/lon data stored as
// plain geometry can still be searched in metres.
//
// Like advanced-filters, the plugin only adds schema surface plus catalog
// bindings; the compiler reads the bindings and stays inert when they are
// absent, so disabling the plugin (plugins.disabled: [postgis]) removes the
// surface completely while plain GeoJSON read/write keeps working.
//
// Per-object control follows the same smart-comment conventions as the rest of
// the codebase: "@omit filter" / "@omit order" on a spatial column skips its
// filter / ordering surface.
package postgis

import (
	"context"
	"strings"

	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbcore/smarttags"
	"github.com/suprbdev/pdbq/internal/geotype"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// Plugin implements the SchemaHook adding the spatial surface.
type Plugin struct {
	// filters generates the spatial operator inputs.
	filters bool
	// derived generates the <field>Area/<field>Centroid/... accessors.
	derived bool
	// ordering generates distance orderBy values and the `near:` argument.
	ordering bool
	// projection generates the transform:/simplify: field arguments.
	projection bool
	// typedGeometry types spatial column fields as geometry objects instead
	// of the GeoJSON scalar.
	typedGeometry bool
}

// New builds the plugin from its `plugins.settings.postgis` map.
func New(settings map[string]any) *Plugin {
	p := &Plugin{filters: true, derived: true, ordering: true, projection: true}
	for key, target := range map[string]*bool{
		"filters":        &p.filters,
		"derived":        &p.derived,
		"ordering":       &p.ordering,
		"projection":     &p.projection,
		"typed_geometry": &p.typedGeometry,
	} {
		if v, ok := settings[key].(bool); ok {
			*target = v
		}
	}
	return p
}

func (p *Plugin) Name() string { return "postgis" }

// Priority 160 places the plugin just after advanced-filters (150), so the
// filter inputs it extends already exist and relation filters are in place.
func (p *Plugin) Priority() int { return 160 }

// ---- SchemaHook ----

func (p *Plugin) TransformSchema(_ context.Context, b *schema.Builder) error {
	if !hasSpatialColumn(b.Catalog) {
		return nil
	}
	p.addSharedInputs(b)
	orderBy := b.FieldsByOrderBy()
	for _, t := range b.Catalog.Tables {
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
			p.addDistanceOrdering(b, t, cols, orderBy)
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

// omits reports which halves of the surface a smart comment removes.
func omits(comment string) (skipFilter, skipOrder bool) {
	tags, _ := smarttags.Parse(comment)
	om, everything := tags.Omits()
	return everything || om["filter"], everything || om["order"]
}

// Shared input type names.
const (
	bboxInput    = "BBoxInput"
	dwithinInput = "DWithinInput"
	nearInput    = "NearInput"
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
	if _, ok := b.Inputs[nearInput]; !ok {
		b.Inputs[nearInput] = &schema.Input{
			Name:        nearInput,
			Description: "Reference geometry for distance ordering (<COLUMN>_DISTANCE_ASC/_DESC).",
			Fields: []*schema.InputField{
				{Name: "geometry", Type: postgis.TypeName + "!", Description: "Reference geometry, as GeoJSON or WKT."},
			},
		}
	}
}

// opsInputName is the shared operator input for spatial columns. Geometry and
// geography share one type: the operator set is identical and the unit
// difference is a property of the column, not of the input.
const opsInputName = "GeometryFilterOps"

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
		if skip, _ := omits(c.Comment); skip {
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

// ensureOpsInput declares GeometryFilterOps once per schema.
func (p *Plugin) ensureOpsInput(b *schema.Builder) {
	if _, ok := b.Inputs[opsInputName]; ok {
		return
	}
	in := &schema.Input{
		Name: opsInputName,
		Description: "Spatial operators for a PostGIS geometry or geography column. " +
			"Bounding-box operators and dwithin can use a GiST index; the exact " +
			"predicates are precise but scan more rows.",
	}
	for _, op := range postgis.Ops {
		desc := op.Description
		if op.Index {
			desc += " Index-usable."
		}
		in.AddField(&schema.InputField{Name: op.Name, Type: op.Arg, Description: desc})
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
		base := b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name})
		for _, d := range postgis.Deriveds {
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
// and retypes the spatial column fields of every table type to the type their
// typmod implies. A flavour whose type names collide with an existing type is
// left on the GeoJSON scalar.
func (p *Plugin) addTypedGeometry(b *schema.Builder) {
	type target struct {
		field *schema.Field
		typ   geotype.Type
	}
	byFlavour := map[postgis.Flavour][]target{}
	for _, t := range b.Catalog.Tables {
		obj, ok := b.Objects[b.TypeForTable[t.Schema+"."+t.Name]]
		if !ok {
			continue
		}
		for _, c := range spatialColumns(t) {
			if c.IsArray {
				continue
			}
			name := b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name})
			f := obj.Field(name)
			if f == nil || f.Meta == nil || f.Meta.Kind != schema.KindColumn || f.Meta.Column != c {
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

// addProjectionArgs adds transform:/simplify: to each spatial column field.
func (p *Plugin) addProjectionArgs(b *schema.Builder, t *introspect.Table, cols []*introspect.Column) {
	typeName := b.TypeForTable[t.Schema+"."+t.Name]
	obj, ok := b.Objects[typeName]
	if !ok {
		return
	}
	for _, c := range cols {
		name := b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name})
		f := obj.Field(name)
		if f == nil {
			continue
		}
		f.Args = append(f.Args,
			schema.Arg{Name: "transform", Type: "Int"},
			schema.Arg{Name: "simplify", Type: "Float"},
		)
	}
}

// addDistanceOrdering adds <COLUMN>_DISTANCE_ASC/_DESC values to the table's
// orderBy enum and a `near:` argument to the fields that accept orderBy.
//
// The enum value carries the column binding; the reference geometry comes from
// `near:` at query time, which is why the two are generated together.
func (p *Plugin) addDistanceOrdering(b *schema.Builder, t *introspect.Table, cols []*introspect.Column, orderBy map[string][]*schema.Field) {
	enumName := b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
	en, ok := b.Enums[enumName]
	if !ok {
		return
	}
	allowed := filterableSet(b, t)
	added := false
	for _, c := range cols {
		if _, skip := omits(c.Comment); skip {
			continue
		}
		if !allowed[c.Name] {
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
				Description: "Order by distance from the geometry given in the near: argument " +
					"(nearest first for _ASC). Requires near:.",
				Column:   c.Name,
				Distance: true,
			})
			added = true
		}
	}
	if added {
		addNearArg(orderBy[enumName])
	}
}

// addNearArg adds `near: NearInput` to every field that already takes the
// table's orderBy enum, so distance ordering has a reference geometry.
func addNearArg(fields []*schema.Field) {
	for _, f := range fields {
		if !hasArg(f, "near") {
			f.Args = append(f.Args, schema.Arg{Name: "near", Type: nearInput})
		}
	}
}

func hasArg(f *schema.Field, name string) bool {
	for _, a := range f.Args {
		if a.Name == name {
			return true
		}
	}
	return false
}
