package schema

import (
	"slices"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbcore/smarttags"
	"github.com/suprbdev/pdbq/internal/contribtype"
	"github.com/suprbdev/pdbq/internal/inflect"
)

// builtinScalars are the GraphQL scalars every schema has without declaring
// them.
var builtinScalars = []string{"Int", "Float", "String", "Boolean", "ID"}

// addDomains declares a custom scalar per domain that has a scalar
// representation (see domainScalarEligible) when Options.DomainScalars is
// set, the way PostGraphile aliases a domain's base type: the scalar reads
// and writes exactly like the base type and is described by the domain's
// comment plus the constraints of its chain. A domain whose name is
// already taken by another type keeps its base type.
func (b *Builder) addDomains() {
	if !b.Options.DomainScalars {
		return
	}
	taken := map[string]bool{}
	for _, t := range b.Catalog.Tables {
		taken[b.Inflect(inflect.KindTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})] = true
	}
	for _, ct := range b.Catalog.Composites {
		name := b.Inflect(inflect.KindTypeName, inflect.Input{Schema: ct.Schema, Table: ct.Name})
		taken[name], taken[name+"Input"] = true, true
	}
	for _, d := range b.Catalog.Domains {
		if !b.domainScalarEligible(d) {
			continue
		}
		name := b.Inflect(inflect.KindDomainTypeName, inflect.Input{Schema: d.Schema, Domain: d.Name})
		_, obj := b.Objects[name]
		_, in := b.Inputs[name]
		_, en := b.Enums[name]
		if name == "" || obj || in || en || taken[name] || slices.Contains(b.Scalars, name) || slices.Contains(builtinScalars, name) {
			b.log.Warn("domain scalar name already taken, domain keeps its base type", "domain", d.Ref(), "type", name)
			continue
		}
		b.Scalars = append(b.Scalars, name)
		b.ScalarDescriptions[name] = b.domainDescription(d)
		b.domainScalars[d.Ref()] = name
	}
}

// domainScalarEligible reports whether d maps to a custom scalar: its base
// type is a built-in (pg_catalog) type with a plain scalar mapping. Domains
// over arrays, enums, composites, ranges, PostGIS and contrib types
// (citext, hstore, ltree, tsvector, inet, ...) keep their base type's
// GraphQL type, which carries the representation and operators of that
// type.
func (b *Builder) domainScalarEligible(d *introspect.Domain) bool {
	if d.IsArrayBase || d.BaseTypeSchema != "pg_catalog" || postgis.IsSpatial(d.BaseType) {
		return false
	}
	base := &introspect.Column{PGType: d.BaseType, TypeSchema: d.BaseTypeSchema, BaseTypeSchema: d.BaseTypeSchema}
	return contribtype.For(b.Catalog, base).Kind == contribtype.None
}

// domainDescription is the scalar description of d: its comment without
// smart tags, then the NOT NULL and CHECK constraints of d and its parent
// domains.
func (b *Builder) domainDescription(d *introspect.Domain) string {
	desc := strings.TrimSpace(smarttags.Strip(d.Comment))
	var notNull bool
	var cons []string
	for _, cd := range b.Catalog.DomainChain(d) {
		notNull = notNull || cd.NotNull
		cons = append(cons, cd.Checks...)
	}
	if notNull {
		cons = append([]string{"NOT NULL"}, cons...)
	}
	if len(cons) == 0 {
		return desc
	}
	var out strings.Builder
	if desc != "" {
		out.WriteString(desc + "\n\n")
	}
	out.WriteString("Constraints:")
	for _, c := range cons {
		out.WriteString("\n- `" + c + "`")
	}
	return out.String()
}

// DomainScalar returns the custom scalar declared for a column-shaped
// value's domain (introspect.Column.Domain), or "" when it has none.
func (b *Builder) DomainScalar(c *introspect.Column) string {
	return b.domainScalars[c.Domain]
}

// FuncArgColumn describes a function parameter as a column of its resolved
// type: a parameter declared as a domain (or an array of one) takes the
// domain's base type, as a domain column does, so values are shaped and
// typed like the base type while the call still casts to the declared type.
func FuncArgColumn(cat *introspect.Catalog, a introspect.FuncArg) *introspect.Column {
	c := typeColumn(cat, a.PGType, a.TypeSchema, a.Domain)
	c.Name = a.Name
	return c
}

// FuncReturnColumn describes the type a function returns as a column, with
// a domain resolved as in FuncArgColumn.
func FuncReturnColumn(cat *introspect.Catalog, f *introspect.Function) *introspect.Column {
	c := typeColumn(cat, f.ReturnType, f.ReturnTypeSchema, f.ReturnDomain)
	c.Name = f.Name
	return c
}

func typeColumn(cat *introspect.Catalog, pgType, typeSchema, domain string) *introspect.Column {
	isArray := strings.HasPrefix(pgType, "_")
	d := cat.DomainByRef(domain)
	switch {
	case d == nil:
	case d.IsArrayBase && !isArray:
		return &introspect.Column{PGType: d.BaseType, TypeSchema: d.BaseTypeSchema, BaseTypeSchema: d.BaseTypeSchema, IsArray: true, Domain: domain}
	case !d.IsArrayBase && isArray:
		return &introspect.Column{PGType: "_" + d.BaseType, TypeSchema: d.BaseTypeSchema, BaseTypeSchema: d.BaseTypeSchema, IsArray: true, Domain: domain}
	case !d.IsArrayBase:
		return &introspect.Column{PGType: d.BaseType, TypeSchema: d.BaseTypeSchema, BaseTypeSchema: d.BaseTypeSchema, Domain: domain}
	}
	return &introspect.Column{PGType: pgType, TypeSchema: typeSchema, IsArray: isArray}
}
