package introspect

import (
	"context"
	"fmt"
	"strings"

	"github.com/suprbdev/pdbq/internal/smarttags"
)

// convertEnumTables implements the PostGraphile-style @enum smart comment on
// tables: a table commented @enum is read at introspection time and its rows
// become a table-backed Enum in the catalog — the table itself disappears from
// the API surface, and every single-column FK referencing its value column is
// rebound (Column.EnumRef) so the referencing columns are typed as the enum.
//
// The value column is the table's single-column primary key, or the column of
// a single-column unique constraint whose comment carries @enum. It must be a
// text-family column: the values become GraphQL enum value names. Per-value
// descriptions come from a column commented @enumDescription, falling back to
// a column named "description".
//
// This runs in the introspect package (not the smart-comments plugin) because
// it needs the table's rows, which only introspection can read; like
// @costMultiplier, the tag therefore works with the plugin disabled. Tables
// that do not qualify (multi-column or non-text key, or no rows) are left
// untouched.
func convertEnumTables(ctx context.Context, db Querier, cat *Catalog) error {
	valueCols := map[string]string{} // "schema.table" -> value column
	var converted []*Enum
	for _, t := range cat.Tables {
		tags, _ := smarttags.Parse(t.Comment)
		if !tags.Has("enum") {
			continue
		}
		vc := enumValueColumn(t)
		if vc == "" {
			continue
		}
		e, err := readEnumTable(ctx, db, t, vc)
		if err != nil {
			return err
		}
		if e == nil {
			continue // no rows: an empty GraphQL enum is invalid SDL
		}
		valueCols[tableKey(t.Schema, t.Name)] = vc
		converted = append(converted, e)
	}
	if len(converted) == 0 {
		return nil
	}
	cat.Enums = append(cat.Enums, converted...)

	tables := cat.Tables[:0]
	for _, t := range cat.Tables {
		if _, ok := valueCols[tableKey(t.Schema, t.Name)]; !ok {
			tables = append(tables, t)
		}
	}
	cat.Tables = tables

	// Rebind or drop FKs pointing at converted tables: a single-column FK on
	// the value column types its local column as the enum; anything else just
	// loses the (now dangling) relation.
	for _, t := range cat.Tables {
		fks := t.ForeignKeys[:0]
		for _, fk := range t.ForeignKeys {
			ref := tableKey(fk.RefSchema, fk.RefTable)
			vc, isEnum := valueCols[ref]
			if !isEnum {
				fks = append(fks, fk)
				continue
			}
			if len(fk.Columns) == 1 && len(fk.RefColumns) == 1 && fk.RefColumns[0] == vc {
				if c := t.Column(fk.Columns[0]); c != nil {
					c.EnumRef = ref
				}
			}
		}
		t.ForeignKeys = fks
	}
	return nil
}

// enumValueColumn picks the column whose values become the enum: a
// single-column unique constraint commented @enum wins, then a single-column
// primary key. Returns "" when the table does not qualify.
func enumValueColumn(t *Table) string {
	pick := func(con *Constraint) string {
		if con == nil || len(con.Columns) != 1 {
			return ""
		}
		c := t.Column(con.Columns[0])
		if c == nil || c.IsArray || !isTextFamily(c.PGType) {
			return ""
		}
		return c.Name
	}
	for _, u := range t.Uniques {
		tags, _ := smarttags.Parse(u.Comment)
		if tags.Has("enum") {
			if vc := pick(u); vc != "" {
				return vc
			}
		}
	}
	return pick(t.PrimaryKey)
}

func isTextFamily(pgType string) bool {
	switch pgType {
	case "text", "varchar", "bpchar", "citext", "name":
		return true
	}
	return false
}

// enumDescriptionColumn returns the column supplying per-value descriptions:
// one commented @enumDescription, else one named "description"; "" when
// neither exists (the value column never describes itself).
func enumDescriptionColumn(t *Table, valueCol string) string {
	for _, c := range t.Columns {
		tags, _ := smarttags.Parse(c.Comment)
		if tags.Has("enumDescription") && c.Name != valueCol {
			return c.Name
		}
	}
	if c := t.Column("description"); c != nil && c.Name != valueCol {
		return c.Name
	}
	return ""
}

// readEnumTable reads the table's rows into a table-backed Enum, ordered by
// the value column. Returns nil when the table is empty.
func readEnumTable(ctx context.Context, db Querier, t *Table, valueCol string) (*Enum, error) {
	descExpr := "''"
	if dc := enumDescriptionColumn(t, valueCol); dc != "" {
		descExpr = "COALESCE(" + quoteIdent(dc) + "::text, '')"
	}
	q := fmt.Sprintf("SELECT %s::text, %s FROM %s.%s ORDER BY 1",
		quoteIdent(valueCol), descExpr, quoteIdent(t.Schema), quoteIdent(t.Name))
	rows, err := db.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect: @enum table %s.%s: %w", t.Schema, t.Name, err)
	}
	defer rows.Close()
	e := &Enum{Schema: t.Schema, Name: t.Name, Comment: t.Comment, TableBacked: true}
	for rows.Next() {
		var v, d string
		if err := rows.Scan(&v, &d); err != nil {
			return nil, err
		}
		e.Values = append(e.Values, v)
		e.ValueDescriptions = append(e.ValueDescriptions, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(e.Values) == 0 {
		return nil, nil
	}
	return e, nil
}

func tableKey(schema, name string) string { return schema + "." + name }

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
