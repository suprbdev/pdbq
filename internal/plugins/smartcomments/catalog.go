package smartcomments

import (
	"context"
	"sort"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/smarttags"
	"github.com/suprbdev/pdbq/internal/omit"
)

// notice is a catalog-time decision logged once the schema build provides
// the configured logger.
type notice struct {
	msg  string
	args []any
}

// TransformCatalog runs pdbcore's catalog half (tag index, structural tags,
// bare @omit on tables, constraints and functions), then removes what an
// omitted object would otherwise leave reachable:
//
//   - primary keys and unique constraints over a column hidden from read,
//     so no lookup, by-key mutation, upsert, nested-mutation target, nodeId
//     or keyset cursor is keyed by a hidden value (the columns stay
//     filterable through a synthetic index);
//   - functions returning or taking rows of an omitted table, which would
//     otherwise be typed String and return whole rows as JSON;
//   - columns typed as an omitted table's row type, for the same reason.
//
// It also copies the tags of @foreignKey constraints into their Comment,
// so per-constraint decisions read them like those of real constraints.
func (p *Plugin) TransformCatalog(ctx context.Context, c *introspect.Catalog) error {
	before := make(map[string]bool, len(c.Tables))
	for _, t := range c.Tables {
		before[tableKey(t.Schema, t.Name)] = true
	}
	if err := p.Hook.TransformCatalog(ctx, c); err != nil {
		return err
	}
	p.notices = nil
	removed := before
	for _, t := range c.Tables {
		delete(removed, tableKey(t.Schema, t.Name))
	}
	for _, t := range c.Tables {
		p.syntheticConstraintComments(t)
		p.hideKeys(t)
		p.hideRowTypedColumns(t, removed)
	}
	p.dropFunctions(c, removed)
	return nil
}

func (p *Plugin) note(msg string, args ...any) {
	p.notices = append(p.notices, notice{msg: msg, args: args})
}

// syntheticConstraintComments renders the index tags of t's @foreignKey
// constraints (the |@tag value parts) as their Comment.
func (p *Plugin) syntheticConstraintComments(t *introspect.Table) {
	for _, fk := range t.ForeignKeys {
		if fk.Comment != "" || !strings.HasPrefix(fk.Name, "@foreignKey") {
			continue
		}
		fk.Comment = renderTags(p.Index().Constraint(t.Schema, fk.Name))
	}
}

func renderTags(tags smarttags.Tags) string {
	names := make([]string, 0, len(tags))
	for name := range tags {
		names = append(names, name)
	}
	sort.Strings(names)
	var lines []string
	for _, name := range names {
		for _, v := range tags[name] {
			lines = append(lines, strings.TrimSpace("@"+name+" "+v))
		}
	}
	return strings.Join(lines, "\n")
}

// hideKeys drops t's primary key and unique constraints that include a
// column hidden from read.
func (p *Plugin) hideKeys(t *introspect.Table) {
	if pk := t.PrimaryKey; pk != nil && omit.HiddenKey(t, pk.Columns) {
		p.note("smart_comments: primary key includes an omitted column; no nodeId, by-key lookups or keyset cursors for the table",
			"table", tableKey(t.Schema, t.Name), "constraint", pk.Name)
		keepIndexed(t, pk)
		t.PrimaryKey = nil
	}
	uniques := t.Uniques[:0]
	for _, u := range t.Uniques {
		if omit.HiddenKey(t, u.Columns) {
			p.note("smart_comments: unique constraint includes an omitted column; no by-key lookups or mutations for it",
				"table", tableKey(t.Schema, t.Name), "constraint", u.Name)
			keepIndexed(t, u)
			continue
		}
		uniques = append(uniques, u)
	}
	t.Uniques = uniques
}

// keepIndexed records a synthetic index over the columns of key, which
// [introspect.Table.IndexedColumns] counted through the key itself, so
// filters.indexed_only keeps admitting them once the key is dropped.
func keepIndexed(t *introspect.Table, key *introspect.Constraint) {
	if len(key.Columns) == 0 || t.IndexedColumns()[key.Columns[0]] {
		return
	}
	t.Indexes = append(t.Indexes, &introspect.Index{
		Name: "@omit " + key.Name, Columns: key.Columns, Unique: true, Method: "btree",
	})
}

// hideRowTypedColumns marks the columns of t typed as the row type of a
// removed table omitted.
func (p *Plugin) hideRowTypedColumns(t *introspect.Table, removed map[string]bool) {
	for _, c := range t.Columns {
		if !removed[tableKey(c.TypeSchema, strings.TrimPrefix(c.PGType, "_"))] || omit.Everything(c.Comment) {
			continue
		}
		p.note("smart_comments: column typed as an omitted table's row type is omitted",
			"table", tableKey(t.Schema, t.Name), "column", c.Name)
		c.Comment = strings.TrimRight("@omit\n"+c.Comment, "\n")
	}
}

// dropFunctions removes the functions whose result, an argument or an output
// parameter is the row type of a removed table.
func (p *Plugin) dropFunctions(c *introspect.Catalog, removed map[string]bool) {
	if len(removed) == 0 {
		return
	}
	kept := c.Functions[:0]
	for _, f := range c.Functions {
		if ref := rowTypeOf(f, removed); ref != "" {
			p.note("smart_comments: function not exposed: it returns or takes rows of an omitted table",
				"function", f.Schema+"."+f.Name, "table", ref)
			continue
		}
		kept = append(kept, f)
	}
	c.Functions = kept
}

// rowTypeOf returns the key of the removed table whose row type f returns or
// takes, or "".
func rowTypeOf(f *introspect.Function, removed map[string]bool) string {
	if key := tableKey(f.ReturnTypeSchema, strings.TrimPrefix(f.ReturnType, "_")); removed[key] {
		return key
	}
	for _, a := range append(append([]introspect.FuncArg{}, f.Args...), f.ReturnColumns...) {
		if key := tableKey(a.TypeSchema, strings.TrimPrefix(a.PGType, "_")); removed[key] {
			return key
		}
	}
	return ""
}
