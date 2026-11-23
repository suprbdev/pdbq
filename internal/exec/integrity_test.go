package exec

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/suprbdev/pdbcore/introspect"
)

func integrityCatalog() *introspect.Catalog {
	users := &introspect.Table{
		Schema: "app", Name: "users",
		Columns: []*introspect.Column{
			{Name: "id"},
			{Name: "email", Comment: "@omit"},
			{Name: "full_name"},
		},
		PrimaryKey: &introspect.Constraint{Name: "users_pkey", Columns: []string{"id"}},
		Uniques: []*introspect.Constraint{
			{Name: "users_email_key", Columns: []string{"email"}},
			{Name: "users_full_name_key", Columns: []string{"full_name"}},
		},
		Indexes: []*introspect.Index{
			{Name: "users_id_email_lower_key", Columns: []string{"id"}, ExpressionColumns: []string{"email"}, Unique: true},
			{Name: "users_full_name_partial_key", Columns: []string{"full_name"}, ExpressionColumns: []string{"email"}, Unique: true},
			{Name: "users_full_name_lower_key", Columns: []string{"id"}, ExpressionColumns: []string{"full_name"}, Unique: true},
			{Name: "users_no_overlap", Columns: []string{"id"}, ExpressionColumns: []string{"email"}, Method: "gist"},
		},
		Checks: []*introspect.Constraint{
			{Name: "users_email_len", Columns: []string{"email"}},
			{Name: "users_full_name_len", Columns: []string{"full_name"}},
		},
	}
	posts := &introspect.Table{
		Schema: "app", Name: "posts",
		Columns: []*introspect.Column{{Name: "id"}, {Name: "author_id"}, {Name: "title"}},
		ForeignKeys: []*introspect.ForeignKey{
			{Name: "posts_author_id_fkey", Columns: []string{"author_id"}, RefSchema: "app", RefTable: "users", RefColumns: []string{"id"}},
		},
	}
	events := &introspect.Table{Schema: "app", Name: "events", Partitioned: true, Columns: []*introspect.Column{{Name: "id"}, {Name: "at"}}}
	events2026 := &introspect.Table{Schema: "app", Name: "events_2026", PartitionOf: "app.events", Columns: []*introspect.Column{{Name: "id"}, {Name: "at"}}}
	return &introspect.Catalog{Tables: []*introspect.Table{users, posts, events, events2026}}
}

func TestIntegrityMessageHidesNamesOutsideTheSchema(t *testing.T) {
	cat := integrityCatalog()
	const orig = "original message"
	for name, tc := range map[string]struct {
		err  pgconn.PgError
		want string
	}{
		"visible not-null column": {pgconn.PgError{Code: "23502", SchemaName: "app", TableName: "users", ColumnName: "full_name"}, orig},
		"hidden not-null column":  {pgconn.PgError{Code: "23502", SchemaName: "app", TableName: "users", ColumnName: "email"}, "not-null violation"},
		"unknown column":          {pgconn.PgError{Code: "23502", SchemaName: "app", TableName: "users", ColumnName: "ssn"}, "not-null violation"},
		"visible foreign key":     {pgconn.PgError{Code: "23503", SchemaName: "app", TableName: "posts", ConstraintName: "posts_author_id_fkey"}, orig},
		"foreign key of a hidden table": {pgconn.PgError{Code: "23503", SchemaName: "app", TableName: "secret_notes", ConstraintName: "secret_notes_user_id_fkey"},
			"foreign key violation"},
		"foreign key the schema left out": {pgconn.PgError{Code: "23503", SchemaName: "app", TableName: "posts", ConstraintName: "posts_editor_id_fkey"},
			"foreign key violation"},
		"visible unique key":       {pgconn.PgError{Code: "23505", SchemaName: "app", TableName: "users", ConstraintName: "users_full_name_key"}, orig},
		"unique key on hidden col": {pgconn.PgError{Code: "23505", SchemaName: "app", TableName: "users", ConstraintName: "users_email_key"}, "unique violation"},
		"check on a table with a hidden column": {pgconn.PgError{Code: "23514", SchemaName: "app", TableName: "users", ConstraintName: "users_email_check"},
			"check violation"},
		"expression index over a hidden column": {pgconn.PgError{Code: "23505", SchemaName: "app", TableName: "users", ConstraintName: "users_id_email_lower_key"},
			"unique violation"},
		"partial index predicate on a hidden column": {pgconn.PgError{Code: "23505", SchemaName: "app", TableName: "users", ConstraintName: "users_full_name_partial_key"},
			"unique violation"},
		"expression index over visible columns": {pgconn.PgError{Code: "23505", SchemaName: "app", TableName: "users", ConstraintName: "users_full_name_lower_key"}, orig},
		"recorded check on a hidden column": {pgconn.PgError{Code: "23514", SchemaName: "app", TableName: "users", ConstraintName: "users_email_len"},
			"check violation"},
		"recorded check on a visible column": {pgconn.PgError{Code: "23514", SchemaName: "app", TableName: "users", ConstraintName: "users_full_name_len"}, orig},
		"exclusion over a hidden column": {pgconn.PgError{Code: "23P01", SchemaName: "app", TableName: "users", ConstraintName: "users_no_overlap"},
			"exclusion violation"},
		"check on a fully visible table":   {pgconn.PgError{Code: "23514", SchemaName: "app", TableName: "posts", ConstraintName: "posts_title_check"}, orig},
		"partition index of visible table": {pgconn.PgError{Code: "23505", SchemaName: "app", TableName: "events_2026", ConstraintName: "events_2026_pkey"}, orig},
		"exclusion on a hidden table":      {pgconn.PgError{Code: "23P01", SchemaName: "app", TableName: "rooms", ConstraintName: "no_overlap"}, "exclusion violation"},
		"no table":                         {pgconn.PgError{Code: "23514", DataTypeName: "positive", ConstraintName: "positive_check"}, orig},
		"not an integrity error":           {pgconn.PgError{Code: "22003", SchemaName: "app", TableName: "secret_notes"}, orig},
	} {
		t.Run(name, func(t *testing.T) {
			if got := integrityMessage(cat, &tc.err, orig); got != tc.want {
				t.Fatalf("integrityMessage = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIntegrityMessageWithoutCatalog(t *testing.T) {
	err := &pgconn.PgError{Code: "23503", SchemaName: "app", TableName: "secret_notes"}
	if got := integrityMessage(nil, err, "m"); got != "m" {
		t.Fatalf("integrityMessage = %q, want the message unchanged", got)
	}
}
