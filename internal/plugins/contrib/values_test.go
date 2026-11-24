package contrib

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/contribfixture"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// valuesAllow allowlists the documents columns carrying value filters and
// orderings and the fuzzystrmatch operators.
var valuesAllow = map[string][]string{"public.documents": {"notes", "path", "attrs", "tag_ids"}}

// buildValues applies the plugin (configured by settings) to cat under the
// given policy and allowlist.
func buildValues(t *testing.T, cat *introspect.Catalog, settings func(*Settings), indexedOnly bool, allow map[string][]string) *schema.Builder {
	t.Helper()
	b := schema.New(cat, inflect.Next(inflect.Default), schema.Options{
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Functions:         true,
		FilterIndexedOnly: indexedOnly,
		AllowColumns:      allow,
	})
	s := DefaultSettings()
	if settings != nil {
		settings(&s)
	}
	if err := New(s).TransformSchema(context.Background(), b); err != nil {
		t.Fatalf("TransformSchema: %v", err)
	}
	if _, err := b.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return b
}

func objectField(b *schema.Builder, obj, field string) *schema.Field {
	o := b.Objects[obj]
	if o == nil {
		return nil
	}
	return o.Field(field)
}

func TestValueFields(t *testing.T) {
	b := buildValues(t, contribfixture.Catalog(), nil, true, nil)
	for field, want := range map[string]string{
		"pathNlevel": "Int", "pathParent": "String", "pathLca": "String", "pathSubpath": "String",
		"pathsLca": "String", "attrsValue": "String", "tagIdsIcount": "Int",
	} {
		f := objectField(b, "Document", field)
		if f == nil {
			t.Errorf("Document.%s missing", field)
			continue
		}
		if f.Type != want {
			t.Errorf("Document.%s = %s, want %s", field, f.Type, want)
		}
	}
	args := func(field string) string {
		var out []string
		for _, a := range objectField(b, "Document", field).Args {
			out = append(out, a.Name+":"+a.Type)
		}
		return strings.Join(out, ",")
	}
	for field, want := range map[string]string{
		"attrsValue": "key:String!", "pathLca": "path:String!", "pathSubpath": "offset:Int!,length:Int", "pathsLca": "", "pathNlevel": "",
	} {
		if got := args(field); got != want {
			t.Errorf("Document.%s args = %q, want %q", field, got, want)
		}
	}
	for _, field := range []string{"ipFamily", "spanStart", "slugNlevel", "titleLevenshteinDistance"} {
		if objectField(b, "Document", field) != nil {
			t.Errorf("Document.%s exposed", field)
		}
	}

	cat := contribfixture.Catalog()
	cat.Table("public", "documents").Column("path").Comment = "@omit read"
	cat.Table("public", "documents").Column("tag_ids").Comment = "@omit filter,order"
	omitted := buildValues(t, cat, nil, false, nil)
	for _, field := range []string{"pathNlevel", "pathParent", "pathLca", "pathSubpath"} {
		if objectField(omitted, "Document", field) != nil {
			t.Errorf("@omit read kept Document.%s", field)
		}
	}
	if objectField(omitted, "Document", "tagIdsIcount") == nil {
		t.Error("@omit filter,order removed the icount field")
	}

	noLtree := buildValues(t, withoutExtension(contribfixture.Catalog(), "ltree"), nil, false, nil)
	noIntarray := buildValues(t, withoutExtension(contribfixture.Catalog(), "intarray"), nil, false, nil)
	if objectField(noLtree, "Document", "pathNlevel") != nil || objectField(noIntarray, "Document", "tagIdsIcount") != nil {
		t.Error("value fields without their extension")
	}
	if strings.Contains(noIntarray.SDL(), "Intarray") {
		t.Error("intarray surface without the extension")
	}
}

func TestValueFiltersAndOrdering(t *testing.T) {
	b := buildValues(t, contribfixture.Catalog(), nil, true, nil)
	for _, field := range []string{"pathNlevel", "attrsValue", "tagIdsIcount"} {
		if fieldType(t, b, "DocumentFilter", field) != "" {
			t.Errorf("indexed_only without allowlist: DocumentFilter.%s exposed", field)
		}
	}
	for _, v := range []string{"PATH_NLEVEL_ASC", "ATTRS_VALUE_ASC", "TAG_IDS_ICOUNT_DESC"} {
		if hasEnumValue(b, "DocumentsOrderBy", v) {
			t.Errorf("indexed_only without allowlist: %s exposed", v)
		}
	}
	if in := b.Inputs["DocumentNearInput"]; in != nil && in.Field("attrs") != nil {
		t.Error("hstore key reference declared without value ordering")
	}

	allowed := buildValues(t, contribfixture.Catalog(), nil, true, valuesAllow)
	for field, want := range map[string]string{
		"pathNlevel": "IntFilter", "tagIdsIcount": "IntFilter", "attrsValue": "KeyValueHashValueFilter",
	} {
		if got := fieldType(t, allowed, "DocumentFilter", field); got != want {
			t.Errorf("DocumentFilter.%s = %q, want %s", field, got, want)
		}
	}
	for _, op := range []string{"key", "equalTo", "in", "likeInsensitive", "isNull"} {
		if fieldType(t, allowed, "KeyValueHashValueFilter", op) == "" {
			t.Errorf("KeyValueHashValueFilter.%s missing", op)
		}
	}
	for _, v := range []string{"PATH_NLEVEL_ASC", "PATH_NLEVEL_DESC", "TAG_IDS_ICOUNT_ASC", "ATTRS_VALUE_ASC", "ATTRS_VALUE_DESC"} {
		if !hasEnumValue(allowed, "DocumentsOrderBy", v) {
			t.Errorf("%s missing", v)
		}
	}
	if fieldType(t, allowed, "DocumentNearInput", "attrs") != "String" {
		t.Error("DocumentNearInput.attrs missing")
	}
	if !strings.Contains(allowed.SDL(), "near: DocumentNearInput") {
		t.Error("list fields lack near:")
	}

	cat := contribfixture.Catalog()
	cat.Table("public", "documents").Column("path").Comment = "@omit filter"
	cat.Table("public", "documents").Column("attrs").Comment = "@omit order"
	omitted := buildValues(t, cat, nil, true, valuesAllow)
	if fieldType(t, omitted, "DocumentFilter", "pathNlevel") != "" || !hasEnumValue(omitted, "DocumentsOrderBy", "PATH_NLEVEL_ASC") {
		t.Error("@omit filter on path: want no nlevel filter, nlevel ordering kept")
	}
	if fieldType(t, omitted, "DocumentFilter", "attrsValue") == "" || hasEnumValue(omitted, "DocumentsOrderBy", "ATTRS_VALUE_ASC") {
		t.Error("@omit order on attrs: want the value filter kept, no value ordering")
	}

	noFilters := buildValues(t, contribfixture.Catalog(), func(s *Settings) { s.Filters = false }, true, valuesAllow)
	if fieldType(t, noFilters, "DocumentFilter", "pathNlevel") != "" || !hasEnumValue(noFilters, "DocumentsOrderBy", "PATH_NLEVEL_ASC") {
		t.Error("filters: false must drop value filters only")
	}
	noOrdering := buildValues(t, contribfixture.Catalog(), func(s *Settings) { s.Ordering = false }, true, valuesAllow)
	if hasEnumValue(noOrdering, "DocumentsOrderBy", "PATH_NLEVEL_ASC") || noOrdering.Inputs["DocumentNearInput"] != nil ||
		fieldType(t, noOrdering, "DocumentFilter", "pathNlevel") == "" {
		t.Error("ordering: false must drop value orderings only")
	}
	if objectField(noOrdering, "Document", "pathNlevel") == nil {
		t.Error("ordering: false dropped the value fields")
	}

	off := buildValues(t, contribfixture.Catalog(), nil, false, nil)
	if fieldType(t, off, "DocumentFilter", "pathNlevel") == "" || !hasEnumValue(off, "DocumentsOrderBy", "ATTRS_VALUE_DESC") {
		t.Error("indexed_only off: value surface missing")
	}
}

func TestDefinedKeyOperator(t *testing.T) {
	if got := fieldType(t, buildValues(t, contribfixture.Catalog(), nil, true, valuesAllow), "KeyValueHashFilter", "containsDefinedKey"); got != "" {
		t.Error("containsDefinedKey exposed under indexed_only")
	}
	if got := fieldType(t, buildValues(t, contribfixture.Catalog(), nil, false, nil), "KeyValueHashFilter", "containsDefinedKey"); got != "String" {
		t.Errorf("containsDefinedKey = %q without indexed_only", got)
	}
}

func TestIntarraySurface(t *testing.T) {
	b := buildValues(t, contribfixture.Catalog(), nil, true, nil)
	if got := fieldType(t, b, "DocumentFilter", "tagIds"); got != "IntListIntarrayFilter" {
		t.Errorf("GIN-indexed tag_ids = %s, want IntListIntarrayFilter", got)
	}
	for _, op := range []string{"contains", "overlaps", "matchesQueryInt"} {
		if fieldType(t, b, "IntListIntarrayFilter", op) == "" {
			t.Errorf("IntListIntarrayFilter.%s missing", op)
		}
	}
	cat := contribfixture.Catalog()
	doc := cat.Table("public", "documents")
	for _, ix := range doc.Indexes {
		if ix.Name == "documents_tag_ids_idx" {
			ix.Method = "btree"
		}
	}
	btree := buildValues(t, cat, nil, true, nil)
	if got := fieldType(t, btree, "DocumentFilter", "tagIds"); got != "IntListFilter" {
		t.Errorf("btree-indexed tag_ids = %s, want IntListFilter", got)
	}
	if got := fieldType(t, buildValues(t, cat, nil, true, valuesAllow), "DocumentFilter", "tagIds"); got != "IntListIntarrayFilter" {
		t.Errorf("allowlisted tag_ids = %s", got)
	}
	if strings.Contains(buildValues(t, contribfixture.Catalog(), func(s *Settings) { s.Filters = false }, true, nil).SDL(), "matchesQueryInt") {
		t.Error("filters: false kept matchesQueryInt")
	}
}

func TestFuzzySurface(t *testing.T) {
	ops := []string{"levenshteinDistanceLessThan", "soundexEqualTo", "metaphoneEqualTo", "doubleMetaphoneEqualTo"}
	b := buildValues(t, contribfixture.Catalog(), nil, true, valuesAllow)
	if got := fieldType(t, b, "DocumentFilter", "notes"); got != "StringTrgmUnaccentFuzzyFilter" {
		t.Errorf("allowlisted notes = %s", got)
	}
	if got := fieldType(t, b, "DocumentFilter", "title"); got != "StringTrgmFilter" {
		t.Errorf("title = %s, want StringTrgmFilter", got)
	}
	for _, op := range ops {
		if fieldType(t, b, "StringTrgmUnaccentFuzzyFilter", op) == "" {
			t.Errorf("StringTrgmUnaccentFuzzyFilter.%s missing", op)
		}
	}
	if got := fieldType(t, b, "StringTrgmUnaccentFuzzyFilter", "levenshteinDistanceLessThan"); got != "TextDistanceInput" {
		t.Errorf("levenshteinDistanceLessThan operand = %s", got)
	}
	if !hasEnumValue(b, "DocumentsOrderBy", "NOTES_LEVENSHTEIN_DISTANCE_ASC") || hasEnumValue(b, "DocumentsOrderBy", "TITLE_LEVENSHTEIN_DISTANCE_ASC") {
		t.Error("Levenshtein ordering: want it on notes only")
	}
	if f := objectField(b, "Document", "notesLevenshteinDistance"); f == nil || f.Type != "Int" {
		t.Errorf("notesLevenshteinDistance = %+v", f)
	}
	if fieldType(t, b, "DocumentNearInput", "notes") != "String" {
		t.Error("similarTo: lacks notes")
	}

	noTrgm := buildValues(t, withoutExtension(withoutExtension(contribfixture.Catalog(), "pg_trgm"), "unaccent"), nil, false, nil)
	if got := fieldType(t, noTrgm, "DocumentFilter", "notes"); got != "StringFuzzyFilter" {
		t.Errorf("notes without pg_trgm and unaccent = %s, want StringFuzzyFilter", got)
	}
	if fieldType(t, noTrgm, "DocumentNearInput", "notes") != "String" || !hasEnumValue(noTrgm, "DocumentsOrderBy", "NOTES_LEVENSHTEIN_DISTANCE_DESC") {
		t.Error("Levenshtein ordering without pg_trgm missing")
	}
	if strings.Contains(buildValues(t, withoutExtension(contribfixture.Catalog(), "fuzzystrmatch"), nil, false, nil).SDL(), "evenshtein") {
		t.Error("fuzzystrmatch surface without the extension")
	}

	cat := contribfixture.Catalog()
	cat.Table("public", "documents").Column("notes").Comment = "@omit order"
	omitted := buildValues(t, cat, nil, true, valuesAllow)
	if hasEnumValue(omitted, "DocumentsOrderBy", "NOTES_LEVENSHTEIN_DISTANCE_ASC") || objectField(omitted, "Document", "notesLevenshteinDistance") == nil {
		t.Error("@omit order: want no Levenshtein ordering, the score kept")
	}
}
