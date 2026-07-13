package introspect

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Diff describes the structural differences between two catalogs as
// human-readable lines, phrased as the change from cached to live
// ("added" = present in live only). Empty means no drift.
func Diff(cached, live *Catalog) []string {
	var out []string
	if cached.ServerVersion != live.ServerVersion {
		out = append(out, fmt.Sprintf("server version changed: %s -> %s", cached.ServerVersion, live.ServerVersion))
	}
	out = append(out, diffStringSet("schema", cached.Schemas, live.Schemas)...)
	out = append(out, diffTables(cached.Tables, live.Tables)...)
	out = append(out, diffNamed("enum", enumMap(cached.Enums), enumMap(live.Enums))...)
	out = append(out, diffNamed("composite type", compositeMap(cached.Composites), compositeMap(live.Composites))...)
	out = append(out, diffNamed("function", functionMap(cached.Functions), functionMap(live.Functions))...)
	return out
}

func diffStringSet(kind string, cached, live []string) []string {
	var out []string
	c, l := map[string]bool{}, map[string]bool{}
	for _, s := range cached {
		c[s] = true
	}
	for _, s := range live {
		l[s] = true
	}
	for _, s := range sortedKeys(c) {
		if !l[s] {
			out = append(out, fmt.Sprintf("%s %s removed", kind, s))
		}
	}
	for _, s := range sortedKeys(l) {
		if !c[s] {
			out = append(out, fmt.Sprintf("%s %s added", kind, s))
		}
	}
	return out
}

// diffNamed reports added/removed/changed objects, comparing by canonical
// JSON. The maps are name -> object.
func diffNamed(kind string, cached, live map[string]any) []string {
	var out []string
	for _, name := range sortedKeys(cached) {
		if _, ok := live[name]; !ok {
			out = append(out, fmt.Sprintf("%s %s removed", kind, name))
		}
	}
	for _, name := range sortedKeys(live) {
		cobj, ok := cached[name]
		if !ok {
			out = append(out, fmt.Sprintf("%s %s added", kind, name))
			continue
		}
		if canonJSON(cobj) != canonJSON(live[name]) {
			out = append(out, fmt.Sprintf("%s %s changed", kind, name))
		}
	}
	return out
}

func diffTables(cached, live []*Table) []string {
	var out []string
	c, l := map[string]*Table{}, map[string]*Table{}
	for _, t := range cached {
		c[t.Schema+"."+t.Name] = t
	}
	for _, t := range live {
		l[t.Schema+"."+t.Name] = t
	}
	for _, name := range sortedKeys(c) {
		if _, ok := l[name]; !ok {
			out = append(out, fmt.Sprintf("%s %s removed", c[name].Kind, name))
		}
	}
	for _, name := range sortedKeys(l) {
		lt := l[name]
		ct, ok := c[name]
		if !ok {
			out = append(out, fmt.Sprintf("%s %s added", lt.Kind, name))
			continue
		}
		out = append(out, diffTable(name, ct, lt)...)
	}
	return out
}

func diffTable(name string, cached, live *Table) []string {
	var out []string
	cc, lc := map[string]*Column{}, map[string]*Column{}
	for _, col := range cached.Columns {
		cc[col.Name] = col
	}
	for _, col := range live.Columns {
		lc[col.Name] = col
	}
	for _, cn := range sortedKeys(cc) {
		if _, ok := lc[cn]; !ok {
			out = append(out, fmt.Sprintf("%s %s: column %s removed", cached.Kind, name, cn))
		}
	}
	for _, cn := range sortedKeys(lc) {
		lcol := lc[cn]
		ccol, ok := cc[cn]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("%s %s: column %s added (%s)", live.Kind, name, cn, typeDesc(lcol)))
		case typeDesc(ccol) != typeDesc(lcol):
			out = append(out, fmt.Sprintf("%s %s: column %s type changed (%s -> %s)", live.Kind, name, cn, typeDesc(ccol), typeDesc(lcol)))
		case canonJSON(ccol) != canonJSON(lcol):
			out = append(out, fmt.Sprintf("%s %s: column %s changed", live.Kind, name, cn))
		}
	}
	// Columns equal but something else on the table drifted (keys, indexes,
	// privileges, RLS, comments): report it coarsely rather than staying silent.
	if len(out) == 0 && canonJSON(cached) != canonJSON(live) {
		out = append(out, fmt.Sprintf("%s %s changed (%s)", live.Kind, name,
			strings.Join(tableAspectChanges(cached, live), ", ")))
	}
	return out
}

func tableAspectChanges(cached, live *Table) []string {
	var parts []string
	aspect := func(label string, a, b any) {
		if canonJSON(a) != canonJSON(b) {
			parts = append(parts, label)
		}
	}
	aspect("kind", cached.Kind, live.Kind)
	aspect("comment", cached.Comment, live.Comment)
	aspect("rls", cached.RLSEnabled, live.RLSEnabled)
	aspect("primary key", cached.PrimaryKey, live.PrimaryKey)
	aspect("unique constraints", cached.Uniques, live.Uniques)
	aspect("foreign keys", cached.ForeignKeys, live.ForeignKeys)
	aspect("indexes", cached.Indexes, live.Indexes)
	aspect("privileges", cached.Privileges, live.Privileges)
	if len(parts) == 0 {
		parts = append(parts, "definition")
	}
	return parts
}

func typeDesc(c *Column) string {
	t := c.PGType
	if c.IsArray {
		t += "[]"
	}
	if c.NotNull {
		t += " not null"
	}
	return t
}

func enumMap(enums []*Enum) map[string]any {
	out := map[string]any{}
	for _, e := range enums {
		out[e.Schema+"."+e.Name] = e
	}
	return out
}

func compositeMap(comps []*Composite) map[string]any {
	out := map[string]any{}
	for _, c := range comps {
		out[c.Schema+"."+c.Name] = c
	}
	return out
}

func functionMap(funcs []*Function) map[string]any {
	out := map[string]any{}
	for _, f := range funcs {
		args := make([]string, len(f.Args))
		for i, a := range f.Args {
			args[i] = a.PGType
		}
		out[fmt.Sprintf("%s.%s(%s)", f.Schema, f.Name, strings.Join(args, ", "))] = f
	}
	return out
}

func canonJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("!%v", err)
	}
	return string(b)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
