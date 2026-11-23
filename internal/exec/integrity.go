package exec

import (
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/omit"
)

// integrityMessages are the generic messages of the integrity violations
// whose PostgreSQL message names an object the serving schema hides.
var integrityMessages = map[string]string{
	"23000": "integrity constraint violation",
	"23001": "restrict violation",
	"23502": "not-null violation",
	"23503": "foreign key violation",
	"23505": "unique violation",
	"23514": "check violation",
	"23P01": "exclusion violation",
}

// integrityMessage returns msg, the client message of the integrity
// violation pgErr (class 23), unless PostgreSQL's message may name a
// table, column or constraint that cat, the catalog of the serving schema,
// hides: a contract-hidden or omitted table, a column hidden from reading,
// or a foreign key the schema left out. Such a message is replaced by a
// generic one naming only the kind of violation; the SQLSTATE is kept.
func integrityMessage(cat *introspect.Catalog, pgErr *pgconn.PgError, msg string) string {
	if cat == nil || !strings.HasPrefix(pgErr.Code, "23") || !namesHidden(cat, pgErr) {
		return msg
	}
	if m, ok := integrityMessages[pgErr.Code]; ok {
		return m
	}
	return "constraint violation"
}

// namesHidden reports whether pgErr's table, column or constraint is not
// part of the schema built from cat. A table outside cat is hidden, and so
// is a partition of one; a constraint cat does not record (a check or
// exclusion constraint, an expression index, a partition's own index) is
// treated as hidden when its table has a hidden column it could be on.
func namesHidden(cat *introspect.Catalog, pgErr *pgconn.PgError) bool {
	if pgErr.TableName == "" {
		return false
	}
	t := cat.Table(pgErr.SchemaName, pgErr.TableName)
	if t == nil {
		return true
	}
	owner := t
	if t.PartitionOf != "" {
		s, n, _ := strings.Cut(t.PartitionOf, ".")
		if owner = cat.Table(s, n); owner == nil {
			return true
		}
	}
	if pgErr.ColumnName != "" && hiddenColumn(owner, pgErr.ColumnName) {
		return true
	}
	if pgErr.ConstraintName == "" {
		return false
	}
	if pgErr.Code == "23503" {
		fk := foreignKey(t, pgErr.ConstraintName)
		if fk == nil {
			fk = foreignKey(owner, pgErr.ConstraintName)
		}
		if fk == nil {
			return true
		}
		ref := cat.Table(fk.RefSchema, fk.RefTable)
		return ref == nil || anyHidden(owner, fk.Columns) || anyHidden(ref, fk.RefColumns)
	}
	if cols, ok := keyColumns(t, pgErr.ConstraintName); ok && len(cols) > 0 {
		return anyHidden(owner, cols)
	}
	if cols, ok := keyColumns(owner, pgErr.ConstraintName); ok && len(cols) > 0 {
		return anyHidden(owner, cols)
	}
	for _, c := range owner.Columns {
		if omit.Column(c, omit.Read) {
			return true
		}
	}
	return false
}

func hiddenColumn(t *introspect.Table, name string) bool {
	c := t.Column(name)
	return c == nil || omit.Column(c, omit.Read)
}

func anyHidden(t *introspect.Table, cols []string) bool {
	for _, n := range cols {
		if hiddenColumn(t, n) {
			return true
		}
	}
	return false
}

func foreignKey(t *introspect.Table, name string) *introspect.ForeignKey {
	for _, fk := range t.ForeignKeys {
		if fk.Name == name {
			return fk
		}
	}
	return nil
}

// keyColumns returns the columns of t's primary key, unique constraint or
// index named name.
func keyColumns(t *introspect.Table, name string) ([]string, bool) {
	if pk := t.PrimaryKey; pk != nil && pk.Name == name {
		return pk.Columns, true
	}
	for _, u := range t.Uniques {
		if u.Name == name {
			return u.Columns, true
		}
	}
	for _, ix := range t.Indexes {
		if ix.Name == name {
			return ix.Columns, true
		}
	}
	return nil, false
}
