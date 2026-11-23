package contrib

import (
	"strings"

	pgcontrib "github.com/suprbdev/pdbcore/contrib"
	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/contribtype"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/omit"
	"github.com/suprbdev/pdbq/internal/schema"
)

// valueVocabs are the vocabularies whose accessors and derived expressions
// the plugin exposes as fields; inet, range and vector accessors stay
// unexposed.
var valueVocabs = []string{pgcontrib.VocabLtree, pgcontrib.VocabLtreeArray, pgcontrib.VocabHstore, pgcontrib.VocabIntarray}

// derivedArgs names the single argument of a derived field taking a text
// operand.
var derivedArgs = map[string]string{"value": "key", "lca": "path"}

// hstoreKeyArg is the list argument holding the key per hstore column for
// the <COLUMN>_VALUE_ASC/_DESC orderBy values.
const hstoreKeyArg = "hstoreKey"

// valueVocabsOf returns the column's vocabularies among valueVocabs.
func valueVocabsOf(ci contribtype.Column) []pgcontrib.Vocabulary {
	var out []pgcontrib.Vocabulary
	for _, n := range valueVocabs {
		if v, ok := ci.Vocab(n); ok {
			out = append(out, v)
		}
	}
	return out
}

// comparable reports whether an accessor takes filter and orderBy surface
// (the integer depths and counts: ltree nlevel, intarray icount).
func comparable(a pgcontrib.Accessor) bool { return a.Type == "int4" }

// scalarFor maps a contrib value's pg type to its GraphQL scalar.
func scalarFor(pgType string) string {
	switch pgType {
	case "int4":
		return "Int"
	case "float4", "float8":
		return "Float"
	}
	return "String"
}

// valueDescription describes a value field of column col from the pdbcore
// description of its accessor or expression.
func valueDescription(col, desc string) string {
	if desc == "" {
		return col
	}
	return col + ": " + strings.ToLower(desc[:1]) + desc[1:]
}

// fieldName is the GraphQL field name of column c of t.
func (st *build) fieldName(t *introspect.Table, c *introspect.Column) string {
	return st.b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name})
}

// addValueFields adds the accessor fields (<field>Nlevel, <field>Parent,
// <field>Lca, <field>Icount) and the derived fields taking arguments
// (<field>Value(key:), <field>Lca(path:), <field>Subpath(offset:, length:))
// of t's readable ltree, ltree[], hstore and intarray-typed columns.
func (st *build) addValueFields(t *introspect.Table, cols []column) {
	obj := st.b.Objects[st.b.TypeForTable[t.Schema+"."+t.Name]]
	if obj == nil {
		return
	}
	for _, cc := range cols {
		if omit.Column(cc.col, omit.Read) {
			continue
		}
		field := st.fieldName(t, cc.col)
		add := func(f *schema.Field) {
			if obj.Field(f.Name) != nil {
				st.b.Options.Logger.Warn("contrib: field name already taken, value field skipped", "type", obj.Name, "field", f.Name)
				return
			}
			obj.AddField(f)
		}
		for _, v := range valueVocabsOf(cc.ci) {
			for _, a := range v.Accessors {
				add(&schema.Field{
					Name:        field + coreinflect.UpperCamel(a.Name),
					Type:        scalarFor(a.Type),
					Description: valueDescription(cc.col.Name, a.Description),
					Meta: &schema.FieldMeta{Kind: schema.KindContribAccessor, Table: t, Column: cc.col,
						Contrib: &schema.ContribValue{Column: cc.col.Name, Vocab: v.Name, Name: a.Name}},
				})
			}
			for _, e := range v.Exprs {
				if !e.Derived {
					continue
				}
				var args []schema.Arg
				arg := ""
				switch e.Arg {
				case pgcontrib.ArgSubpath:
					args = []schema.Arg{
						{Name: "offset", Type: "Int!", Description: "First label, counted from 0; from the end when negative."},
						{Name: "length", Type: "Int", Description: "Number of labels; the rest of the path when omitted."},
					}
				case pgcontrib.ArgText:
					arg = derivedArgs[e.Name]
					if arg == "" {
						continue
					}
					args = []schema.Arg{{Name: arg, Type: "String!"}}
				default:
					continue
				}
				add(&schema.Field{
					Name:        field + coreinflect.UpperCamel(e.Name),
					Type:        scalarFor(e.Type),
					Description: valueDescription(cc.col.Name, e.Description),
					Args:        args,
					Meta: &schema.FieldMeta{Kind: schema.KindContribDerived, Table: t, Column: cc.col,
						Contrib: &schema.ContribValue{Column: cc.col.Name, Vocab: v.Name, Name: e.Name, Expr: true, Arg: arg}},
				})
			}
		}
	}
}

// addValueFilters adds the table-filter fields comparing contrib values of
// column cc: <field>Nlevel / <field>Icount (IntFilterOps) and <field>Value
// (KeyValueHashValueFilter). The caller checked that the column is
// filterable and that the policy admits unindexed surface on it.
func (st *build) addValueFilters(t *introspect.Table, cc column) {
	in := st.b.Inputs[st.b.Inflect(inflect.KindFilterTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})]
	if in == nil {
		return
	}
	field := st.fieldName(t, cc.col)
	add := func(f *schema.InputField) {
		if in.Field(f.Name) != nil {
			st.b.Options.Logger.Warn("contrib: filter field name already taken, value filter skipped", "type", in.Name, "field", f.Name)
			return
		}
		in.AddField(f)
	}
	for _, v := range valueVocabsOf(cc.ci) {
		for _, a := range v.Accessors {
			if !comparable(a) {
				continue
			}
			add(&schema.InputField{
				Name:        field + coreinflect.UpperCamel(a.Name),
				Type:        st.b.FilterOpsInputFor(&introspect.Column{Name: a.Name, PGType: "int4", TypeSchema: "pg_catalog"}),
				Description: "Filter by " + strings.ToLower(strings.TrimSuffix(a.Description, ".")) + " of " + cc.col.Name + ". No index serves it.",
				Contrib:     &schema.ContribValue{Column: cc.col.Name, Vocab: v.Name, Name: a.Name},
			})
		}
		if e, ok := v.Expr("value"); ok && v.Name == pgcontrib.VocabHstore && st.ensureValueFilter() {
			add(&schema.InputField{
				Name:        field + coreinflect.UpperCamel(e.Name),
				Type:        contribtype.KeyValueHashValueFilter,
				Description: "Filter by the value of a key of " + cc.col.Name + " (->). No index serves it.",
				Contrib:     &schema.ContribValue{Column: cc.col.Name, Vocab: v.Name, Name: e.Name, Expr: true},
			})
		}
	}
}

// ensureValueFilter declares KeyValueHashValueFilter, {key: String!} plus
// the StringFilterOps operators, reporting false when the name is taken.
func (st *build) ensureValueFilter() bool {
	name := contribtype.KeyValueHashValueFilter
	if st.extended[name] {
		return true
	}
	if st.nameTaken(name) {
		st.b.Options.Logger.Warn("contrib: type name already taken, hstore value filters disabled", "type", name)
		return false
	}
	base := st.b.Inputs[st.b.FilterOpsInputFor(&introspect.Column{Name: "value", PGType: "text", TypeSchema: "pg_catalog"})]
	if base == nil {
		return false
	}
	st.extended[name] = true
	in := &schema.Input{
		Name:        name,
		Description: "The value of one key of an hstore column (null when the key is absent or its value is null) and the String comparisons it must pass.",
		Fields:      []*schema.InputField{{Name: "key", Type: "String!", Description: "The key whose value is compared."}},
	}
	for _, f := range base.Fields {
		cp := *f
		in.Fields = append(in.Fields, &cp)
	}
	st.b.Inputs[name] = in
	return true
}

// addValueOrdering adds the orderBy values sorting by contrib values of
// t's columns the policy admits unindexed ordering on:
// <COLUMN>_NLEVEL_ASC/_DESC, <COLUMN>_ICOUNT_ASC/_DESC and, against the
// hstoreKey: argument it declares, <COLUMN>_VALUE_ASC/_DESC.
func (st *build) addValueOrdering(t *introspect.Table, cols []column) {
	b := st.b
	enumName := b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
	en, ok := b.Enums[enumName]
	if !ok {
		return
	}
	var keyed []column
	for _, cc := range cols {
		if omit.Column(cc.col, omit.Read) || omit.Column(cc.col, omit.Order) || !orderable(en, cc.col.Name) || !st.served(t, cc.col, nil) {
			continue
		}
		base := coreinflect.EnumValue(cc.col.Name)
		for _, v := range valueVocabsOf(cc.ci) {
			for _, a := range v.Accessors {
				if !comparable(a) {
					continue
				}
				cv := &schema.ContribValue{Column: cc.col.Name, Vocab: v.Name, Name: a.Name}
				infix := "_" + coreinflect.EnumValue(a.Name)
				desc := "Order by " + strings.ToLower(strings.TrimSuffix(a.Description, ".")) + " of " + cc.col.Name
				addValue(en, base+infix+"_ASC", schema.EnumValue{Column: cc.col.Name, Contrib: cv, Description: desc + " (ascending)."})
				addValue(en, base+infix+"_DESC", schema.EnumValue{Column: cc.col.Name, Contrib: cv, Desc: true, Description: desc + " (descending)."})
			}
			if _, ok := v.Expr("value"); ok && v.Name == pgcontrib.VocabHstore {
				keyed = append(keyed, cc)
			}
		}
	}
	if len(keyed) == 0 || !st.addHstoreKeyArg(t, enumName, keyed) {
		return
	}
	for _, cc := range keyed {
		cv := &schema.ContribValue{Column: cc.col.Name, Vocab: pgcontrib.VocabHstore, Name: "value", Expr: true, Arg: hstoreKeyArg}
		base := coreinflect.EnumValue(cc.col.Name) + "_VALUE"
		desc := "Order by the value of the hstoreKey: key of " + cc.col.Name
		addValue(en, base+"_ASC", schema.EnumValue{Column: cc.col.Name, Contrib: cv, Description: desc + " (ascending; null values last). Requires hstoreKey:."})
		addValue(en, base+"_DESC", schema.EnumValue{Column: cc.col.Name, Contrib: cv, Desc: true, Description: desc + " (descending; null values last). Requires hstoreKey:."})
	}
}

// addHstoreKeyArg declares <Type>HstoreKeyInput (one String per hstore
// column with value ordering) and adds hstoreKey: to every field taking
// the table's orderBy enum, reporting false when the input's name is taken.
func (st *build) addHstoreKeyArg(t *introspect.Table, enumName string, cols []column) bool {
	b := st.b
	typeName := b.TypeForTable[t.Schema+"."+t.Name]
	inputName := typeName + "HstoreKeyInput"
	if st.nameTaken(inputName) {
		b.Options.Logger.Warn("contrib: type name already taken, hstore value ordering skipped", "type", inputName)
		return false
	}
	input := &schema.Input{
		Name:        inputName,
		Description: "Keys for the hstore value orderBy values of " + typeName + ", per column.",
	}
	for _, cc := range cols {
		input.AddField(&schema.InputField{
			Name:        st.fieldName(t, cc.col),
			Type:        "String",
			Description: "Key whose value the " + cc.col.Name + " value orderBy values sort by.",
			Column:      cc.col.Name,
		})
	}
	b.Inputs[inputName] = input
	for _, f := range st.orderBy[enumName] {
		if !hasArg(f, hstoreKeyArg) {
			f.Args = append(f.Args, schema.Arg{Name: hstoreKeyArg, Type: inputName})
		}
	}
	return true
}
