package contrib

import (
	"testing"

	"github.com/suprbdev/pdbcore/contract"
	pgcontrib "github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/contribfixture"
	"github.com/suprbdev/pdbq/internal/schema"
)

func enumNames(b *schema.Builder, enum string) []string {
	var out []string
	for _, v := range b.Enums[enum].Values {
		out = append(out, v.Name)
	}
	return out
}

func TestHeadlineFields(t *testing.T) {
	b := buildWith(t, contribfixture.Catalog(), nil, true, true)
	obj := b.Objects["Document"]
	for _, name := range []string{"searchHeadline", "keywordsHeadline"} {
		f := obj.Field(name)
		if f == nil {
			t.Fatalf("%s missing", name)
		}
		if f.Type != "String" || len(f.Args) != 2 || f.Args[0].Type != "DocumentHeadlineDocument!" || f.Args[1].Type != headlineOptionsInput {
			t.Errorf("%s: type %s args %+v", name, f.Type, f.Args)
		}
		if f.Meta.Kind != schema.KindFullTextHeadline || f.Meta.RowCost != pgcontrib.HeadlineDocumentCost(pgcontrib.MaxHeadlineDocument) ||
			f.Meta.Nested["max_document"] != pgcontrib.MaxHeadlineDocument {
			t.Errorf("%s meta: %+v", name, f.Meta)
		}
	}
	if got, want := enumNames(b, "DocumentHeadlineDocument"), []string{"TITLE", "SLUG", "LABEL", "NOTES"}; !equal(got, want) {
		t.Errorf("documents = %v, want %v", got, want)
	}
	in := b.Inputs[headlineOptionsInput]
	if in == nil {
		t.Fatal("FullTextHeadlineOptions missing")
	}
	keys := pgcontrib.HeadlineOptionKeys()
	if len(in.Fields) != len(keys) {
		t.Fatalf("options fields %d, want %d", len(in.Fields), len(keys))
	}
	for i, k := range keys {
		if in.Fields[i].Name != k {
			t.Errorf("option field %d = %s, want %s", i, in.Fields[i].Name, k)
		}
	}
	if b.Objects["User"].Field("bioHeadline") != nil || b.Enums["UserHeadlineDocument"] != nil {
		t.Error("headline surface on a table without tsvector columns")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestHeadlineOmitAndContracts(t *testing.T) {
	cat := contribfixture.Catalog()
	docs := cat.Table("public", "documents")
	docs.Column("search").Comment = "@omit filter"
	docs.Column("notes").Comment = "@omit read"
	b := buildWith(t, cat, nil, true, true)
	if b.Objects["Document"].Field("searchHeadline") != nil {
		t.Error("@omit filter kept the headline")
	}
	if b.Objects["Document"].Field("keywordsHeadline") == nil {
		t.Fatal("keywordsHeadline missing")
	}
	for _, v := range enumNames(b, "DocumentHeadlineDocument") {
		if v == "NOTES" {
			t.Error("@omit read column is a headline document")
		}
	}

	cat = contribfixture.Catalog()
	cat.Table("public", "documents").Column("title").Comment = "@behavior -contract:public"
	cat.Table("public", "documents").Column("keywords").Comment = "@behavior -contract:public"
	view, _, err := contract.Contract{Name: "public"}.View(cat)
	if err != nil {
		t.Fatal(err)
	}
	b = buildWith(t, view, nil, true, true)
	if b.Objects["Document"].Field("keywordsHeadline") != nil {
		t.Error("a column hidden by the contract kept its headline")
	}
	for _, v := range enumNames(b, "DocumentHeadlineDocument") {
		if v == "TITLE" {
			t.Error("a column hidden by the contract is a headline document")
		}
	}

	cat = contribfixture.Catalog()
	docs = cat.Table("public", "documents")
	var keep []*introspect.Column
	for _, c := range docs.Columns {
		if c.Name == "id" || c.Name == "search" {
			keep = append(keep, c)
		}
	}
	docs.Columns = keep
	docs.Uniques = nil
	b = buildWith(t, cat, nil, false, true)
	if b.Objects["Document"].Field("searchRank") == nil || b.Objects["Document"].Field("searchHeadline") != nil {
		t.Error("a table without text columns: want rank, no headline")
	}
}

func TestHeadlineNameTaken(t *testing.T) {
	cat := contribfixture.Catalog()
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "document_headline_documents", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true},
		Columns:    []*introspect.Column{{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog"}},
	})
	b := buildWith(t, cat, nil, true, true)
	if b.Objects["Document"].Field("searchHeadline") != nil {
		t.Error("headline added although DocumentHeadlineDocument is taken")
	}
	if b.Objects["Document"].Field("searchRank") == nil {
		t.Error("rank lost with the headline")
	}
}
