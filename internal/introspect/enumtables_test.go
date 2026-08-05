package introspect

import "testing"

func enumFixtureTable() *Table {
	return &Table{
		Schema: "public", Name: "event_type", Kind: RelTable,
		Comment: "@enum",
		Columns: []*Column{
			{Name: "code", PGType: "text", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "label", PGType: "text", TypeSchema: "pg_catalog"},
			{Name: "description", PGType: "text", TypeSchema: "pg_catalog"},
		},
		PrimaryKey: &Constraint{Name: "event_type_pkey", Columns: []string{"code"}},
	}
}

func TestEnumValueColumn(t *testing.T) {
	tbl := enumFixtureTable()
	if got := enumValueColumn(tbl); got != "code" {
		t.Errorf("single text PK: got %q, want code", got)
	}

	// A unique constraint tagged @enum takes precedence over the PK.
	tbl.Uniques = []*Constraint{{Name: "event_type_label_key", Columns: []string{"label"}, Comment: "@enum"}}
	if got := enumValueColumn(tbl); got != "label" {
		t.Errorf("@enum unique: got %q, want label", got)
	}

	// Non-text and multi-column keys do not qualify.
	intPK := enumFixtureTable()
	intPK.Columns[0].PGType = "int4"
	if got := enumValueColumn(intPK); got != "" {
		t.Errorf("int PK: got %q, want empty", got)
	}
	multi := enumFixtureTable()
	multi.PrimaryKey.Columns = []string{"code", "label"}
	if got := enumValueColumn(multi); got != "" {
		t.Errorf("multi-column PK: got %q, want empty", got)
	}
}

func TestEnumDescriptionColumn(t *testing.T) {
	tbl := enumFixtureTable()
	if got := enumDescriptionColumn(tbl, "code"); got != "description" {
		t.Errorf("named column: got %q, want description", got)
	}
	// @enumDescription beats the "description" naming convention.
	tbl.Column("label").Comment = "@enumDescription"
	if got := enumDescriptionColumn(tbl, "code"); got != "label" {
		t.Errorf("@enumDescription: got %q, want label", got)
	}
	// The value column never describes itself.
	bare := &Table{Columns: []*Column{{Name: "description", PGType: "text"}}}
	if got := enumDescriptionColumn(bare, "description"); got != "" {
		t.Errorf("self-description: got %q, want empty", got)
	}
}

func TestEnumByRef(t *testing.T) {
	cat := &Catalog{Enums: []*Enum{
		{Schema: "public", Name: "mood", Values: []string{"sad"}},
		{Schema: "public", Name: "event_type", TableBacked: true, Values: []string{"meetup"}},
	}}
	if e := cat.EnumByRef("public.event_type"); e == nil || !e.TableBacked {
		t.Fatal("EnumByRef must resolve the table-backed enum")
	}
	// Native enums are never resolved by ref, even with a matching key.
	if e := cat.EnumByRef("public.mood"); e != nil {
		t.Fatal("EnumByRef must ignore native enums")
	}
	if e := cat.EnumByRef(""); e != nil {
		t.Fatal("empty ref must resolve to nil")
	}
}
