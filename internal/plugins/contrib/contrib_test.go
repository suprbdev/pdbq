package contrib

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/contribfixture"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// buildWith applies the plugin (configured by settings, or not at all when
// apply is false) to cat and returns the builder for inspection.
func buildWith(t *testing.T, cat *introspect.Catalog, settings func(*Settings), indexedOnly, apply bool) *schema.Builder {
	t.Helper()
	b := schema.New(cat, inflect.Next(inflect.Default), schema.Options{
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Functions:         true,
		FilterIndexedOnly: indexedOnly,
	})
	if apply {
		s := DefaultSettings()
		if settings != nil {
			settings(&s)
		}
		if err := New(s).TransformSchema(context.Background(), b); err != nil {
			t.Fatalf("TransformSchema: %v", err)
		}
	}
	if _, err := b.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return b
}

func fieldType(t *testing.T, b *schema.Builder, input, field string) string {
	t.Helper()
	in := b.Inputs[input]
	if in == nil {
		t.Fatalf("input %s missing", input)
	}
	f := in.Field(field)
	if f == nil {
		return ""
	}
	return f.Type
}

func hasEnumValue(b *schema.Builder, enum, value string) bool {
	for _, v := range b.Enums[enum].Values {
		if v.Name == value {
			return true
		}
	}
	return false
}

func TestInertWithoutContribColumns(t *testing.T) {
	for _, indexedOnly := range []bool{true, false} {
		want := buildWith(t, testutil.FixtureCatalog(), nil, indexedOnly, false).SDL()
		if got := buildWith(t, testutil.FixtureCatalog(), nil, indexedOnly, true).SDL(); got != want {
			t.Errorf("indexed_only=%v: plugin changed a schema without contrib columns", indexedOnly)
		}
	}
	// Installed extensions alone change nothing while no text column has a
	// trigram-capable index.
	cat := testutil.FixtureCatalog()
	cat.Extensions = append(cat.Extensions, contribfixture.Extensions()...)
	want := buildWith(t, testutil.FixtureCatalog(), nil, true, false).SDL()
	if got := buildWith(t, cat, nil, true, true).SDL(); got != want {
		t.Error("installed extensions changed a schema without contrib columns")
	}
}

func TestTypeMapping(t *testing.T) {
	b := buildWith(t, contribfixture.Catalog(), nil, true, true)
	obj := b.Objects["Document"]
	for field, want := range map[string]string{
		"slug": "String", "search": "FullText", "attrs": "KeyValueHash", "path": "String",
		"paths": "[String!]", "during": "DatetimeRange", "span": "IntRange", "avail": "[IntRange!]",
		"price": "BigFloatRange", "ip": "InternetAddress", "net": "CidrAddress", "window": "DatetimeRange",
		"searchRank": "Float",
	} {
		f := obj.Field(field)
		if f == nil || f.Type != want {
			t.Errorf("Document.%s: got %v, want %s", field, f, want)
		}
	}
	for input, want := range map[string]string{"span": "IntRangeInput", "avail": "[IntRangeInput!]", "attrs": "KeyValueHash"} {
		if got := fieldType(t, b, "DocumentInput", input); got != want {
			t.Errorf("DocumentInput.%s = %q, want %q", input, got, want)
		}
	}
	if f := b.Objects["IntRange"].Field("isEmpty"); f == nil || f.Type != "Boolean!" {
		t.Errorf("IntRange.isEmpty: %+v", f)
	}
	if got := fieldType(t, b, "IntRangeInput", "isEmpty"); got != "Boolean" {
		t.Errorf("IntRangeInput.isEmpty = %q", got)
	}
	if got := fieldType(t, b, "StayInput", "nights"); got != "IntRangeInput" {
		t.Errorf("StayInput.nights = %q", got)
	}
	bound := b.Objects["IntRangeBound"]
	if bound == nil || bound.Field("value").Type != "Int!" || bound.Field("inclusive").Type != "Boolean!" {
		t.Errorf("IntRangeBound shape: %+v", bound)
	}
	if b.Objects["BigIntRange"] != nil {
		t.Error("unused range types must not be declared")
	}
}

func TestExtensionTypesNeedTheExtension(t *testing.T) {
	cat := contribfixture.Catalog()
	cat.Extensions = cat.Extensions[:2] // plpgsql, postgis
	b := buildWith(t, cat, nil, true, true)
	obj := b.Objects["Document"]
	for _, field := range []string{"slug", "attrs", "path"} {
		if got := obj.Field(field).Type; got != "String" {
			t.Errorf("Document.%s without its extension = %s, want String", field, got)
		}
	}
	if got := obj.Field("paths").Type; got != "[String!]" {
		t.Errorf("Document.paths = %s", got)
	}
	if got := fieldType(t, b, "DocumentFilter", "title"); got != "StringFilterOps" {
		t.Errorf("title without pg_trgm = %s, want StringFilterOps", got)
	}
	if strings.Contains(b.SDL(), "KeyValueHash") {
		t.Error("KeyValueHash declared without hstore")
	}
	if got := obj.Field("span").Type; got != "IntRange" {
		t.Errorf("core range type must not depend on extensions: %s", got)
	}
}

func TestFilterSurface(t *testing.T) {
	b := buildWith(t, contribfixture.Catalog(), nil, true, true)
	for field, want := range map[string]string{
		"title": "StringTrgmFilterOps", "label": "StringTrgmFilterOps",
		"notes": "StringFilterOps", "slug": "StringFilterOps",
		"search": "FullTextFilterOps", "attrs": "KeyValueHashFilterOps", "path": "LtreeFilterOps",
		"paths": "LtreeListFilterOps", "span": "IntRangeFilterOps", "avail": "IntMultirangeFilterOps",
		"during": "DatetimeRangeFilterOps", "ip": "InternetAddressFilterOps", "net": "CidrAddressFilterOps",
		"price": "",
	} {
		if got := fieldType(t, b, "DocumentFilter", field); got != want {
			t.Errorf("DocumentFilter.%s = %q, want %q", field, got, want)
		}
	}
	for input, ops := range map[string][]string{
		"FullTextFilterOps":        {"isNull", "matches", "matchesPlain", "matchesPhrase", "matchesWebsearch", "config"},
		"KeyValueHashFilterOps":    {"equalTo", "containsKey", "containsAllKeys", "containsAnyKeys", "contains"},
		"LtreeFilterOps":           {"in", "lessThan", "ancestorOf", "descendantOf", "matchesLquery", "matchesAnyLquery", "matchesLtxtquery"},
		"IntRangeFilterOps":        {"equalTo", "overlaps", "contains", "containsElement", "containedBy", "adjacentTo"},
		"InternetAddressFilterOps": {"in", "contains", "containsOrEqualTo", "containedBy", "containedByOrEqualTo", "containsOrContainedBy"},
		"StringTrgmFilterOps":      {"equalTo", "like", "startsWith", "similarTo", "wordSimilarTo", "strictWordSimilarTo"},
	} {
		for _, op := range ops {
			if fieldType(t, b, input, op) == "" {
				t.Errorf("%s.%s missing", input, op)
			}
		}
	}
	if got := fieldType(t, b, "IntRangeFilterOps", "containsElement"); got != "Int" {
		t.Errorf("containsElement operand = %s, want Int", got)
	}
	// Under indexed_only, operators no index serves are left out.
	for input, op := range map[string]string{
		"KeyValueHashFilterOps": "containedBy", "LtreeFilterOps": "ancestorOfAny", "LtreeListFilterOps": "ancestorOf",
	} {
		if fieldType(t, b, input, op) != "" {
			t.Errorf("%s.%s exposed under indexed_only", input, op)
		}
	}
	all := buildWith(t, contribfixture.Catalog(), nil, false, true)
	for input, op := range map[string]string{
		"KeyValueHashFilterOps": "containedBy", "LtreeFilterOps": "descendantOfAny", "LtreeListFilterOps": "ancestorOf",
	} {
		if fieldType(t, all, input, op) == "" {
			t.Errorf("%s.%s missing without indexed_only", input, op)
		}
	}
	for _, field := range []string{"notes", "slug", "email"} {
		input := "DocumentFilter"
		if field == "email" {
			input = "UserFilter"
		}
		if got := fieldType(t, all, input, field); got != "StringTrgmUnaccentFuzzyFilterOps" {
			t.Errorf("%s.%s without indexed_only = %s, want StringTrgmUnaccentFuzzyFilterOps", input, field, got)
		}
	}
}

func TestAllowColumnsGetEverything(t *testing.T) {
	b := schema.New(contribfixture.Catalog(), inflect.Next(inflect.Default), schema.Options{
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		FilterIndexedOnly: true,
		AllowColumns:      map[string][]string{"public.documents": {"notes"}},
	})
	if err := New(DefaultSettings()).TransformSchema(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if got := fieldType(t, b, "DocumentFilter", "notes"); got != "StringTrgmUnaccentFuzzyFilterOps" {
		t.Errorf("allowlisted notes = %s, want StringTrgmUnaccentFuzzyFilterOps", got)
	}
	if !hasEnumValue(b, "DocumentsOrderBy", "NOTES_SIMILARITY_DESC") {
		t.Error("allowlisted notes lacks similarity ordering")
	}
}

func TestOrderingSurface(t *testing.T) {
	b := buildWith(t, contribfixture.Catalog(), nil, true, true)
	for _, v := range []string{
		"SEARCH_RANK_ASC", "SEARCH_RANK_DESC",
		"LABEL_SIMILARITY_ASC", "LABEL_SIMILARITY_DESC", "LABEL_WORD_SIMILARITY_DESC", "LABEL_STRICT_WORD_SIMILARITY_ASC",
	} {
		if !hasEnumValue(b, "DocumentsOrderBy", v) {
			t.Errorf("DocumentsOrderBy.%s missing", v)
		}
	}
	// title has only a GIN trigram index, which cannot serve KNN ordering.
	if hasEnumValue(b, "DocumentsOrderBy", "TITLE_SIMILARITY_DESC") {
		t.Error("similarity ordering offered without a GiST index")
	}
	if got := fieldType(t, b, "DocumentSimilarToInput", "label"); got != "String" {
		t.Errorf("DocumentSimilarToInput.label = %q", got)
	}
	f := b.Query.Field("allDocuments")
	if !hasArg(f, "similarTo") {
		t.Error("allDocuments lacks similarTo:")
	}
	if hasArg(b.Query.Field("allUsers"), "similarTo") {
		t.Error("similarTo: added to an unrelated table")
	}
}

func TestOldTrgmVersionLimitsOperators(t *testing.T) {
	cat := contribfixture.Catalog()
	for _, e := range cat.Extensions {
		if e.Name == "pg_trgm" {
			e.Version = "1.1"
		}
	}
	b := buildWith(t, cat, nil, true, true)
	if fieldType(t, b, "StringTrgmFilterOps", "similarTo") == "" {
		t.Error("similarTo missing")
	}
	if fieldType(t, b, "StringTrgmFilterOps", "wordSimilarTo") != "" {
		t.Error("wordSimilarTo exposed for pg_trgm 1.1")
	}
	if hasEnumValue(b, "DocumentsOrderBy", "LABEL_WORD_SIMILARITY_DESC") {
		t.Error("word similarity ordering exposed for pg_trgm 1.1")
	}
}

func TestSettingsToggleEachHalf(t *testing.T) {
	noFilters := buildWith(t, contribfixture.Catalog(), func(s *Settings) { s.Filters = false }, true, true)
	if fieldType(t, noFilters, "KeyValueHashFilterOps", "containsKey") != "" {
		t.Error("filters: false kept vocabulary operators")
	}
	if fieldType(t, noFilters, "KeyValueHashFilterOps", "equalTo") == "" {
		t.Error("filters: false removed the generic comparisons")
	}
	if noFilters.Objects["Document"].Field("searchRank") != nil || hasEnumValue(noFilters, "DocumentsOrderBy", "SEARCH_RANK_DESC") {
		t.Error("filters: false kept the full-text rank")
	}
	if !hasEnumValue(noFilters, "DocumentsOrderBy", "LABEL_SIMILARITY_DESC") {
		t.Error("filters: false removed similarity ordering")
	}
	noOrder := buildWith(t, contribfixture.Catalog(), func(s *Settings) { s.Ordering = false }, true, true)
	if hasEnumValue(noOrder, "DocumentsOrderBy", "SEARCH_RANK_DESC") || hasEnumValue(noOrder, "DocumentsOrderBy", "LABEL_SIMILARITY_DESC") {
		t.Error("ordering: false kept contrib orderBy values")
	}
	if hasArg(noOrder.Query.Field("allDocuments"), "similarTo") {
		t.Error("ordering: false kept similarTo:")
	}
	if noOrder.Objects["Document"].Field("searchRank") == nil {
		t.Error("ordering: false removed the rank field")
	}
}

func TestSmartCommentOmitsSurface(t *testing.T) {
	cat := contribfixture.Catalog()
	docs := cat.Table("public", "documents")
	docs.Column("search").Comment = "@omit filter"
	docs.Column("label").Comment = "@omit order"
	docs.Column("keywords").Comment = "@omit read"
	b := buildWith(t, cat, nil, true, true)
	if b.Objects["Document"].Field("keywordsRank") != nil || hasEnumValue(b, "DocumentsOrderBy", "KEYWORDS_RANK_DESC") {
		t.Error("@omit read kept the rank field or rank ordering")
	}
	if b.Objects["Document"].Field("searchRank") != nil {
		t.Error("@omit filter kept the rank field")
	}
	if fieldType(t, b, "DocumentFilter", "label") != "StringTrgmFilterOps" {
		t.Error("@omit order removed trigram filters")
	}
	if hasEnumValue(b, "DocumentsOrderBy", "LABEL_SIMILARITY_DESC") {
		t.Error("@omit order kept similarity ordering")
	}
}

func TestTypeNameClashFallsBackToString(t *testing.T) {
	cat := contribfixture.Catalog()
	clash := &introspect.Table{
		Schema: "public", Name: "key_value_hash", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true},
		Columns:    []*introspect.Column{{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog"}},
	}
	cat.Tables = append([]*introspect.Table{clash}, cat.Tables...)
	docs := cat.Table("public", "documents")
	docs.Columns = docs.Columns[:len(docs.Columns)-1]
	cat.Composites = cat.Composites[:len(cat.Composites)-1]
	b := buildWith(t, cat, nil, true, true)
	if got := b.Objects["Document"].Field("attrs").Type; got != "String" {
		t.Errorf("attrs with KeyValueHash taken = %s, want String", got)
	}
	if got := fieldType(t, b, "DocumentFilter", "attrs"); got != "StringFilterOps" {
		t.Errorf("attrs filter = %s, want StringFilterOps", got)
	}
	// Declared first (by a composite attribute), the scalar wins and the
	// table type takes the suffix.
	b = buildWith(t, func() *introspect.Catalog {
		cat := contribfixture.Catalog()
		cat.Tables = append([]*introspect.Table{clash}, cat.Tables...)
		return cat
	}(), nil, true, true)
	if got := b.Objects["Document"].Field("attrs").Type; got != "KeyValueHash" {
		t.Errorf("attrs = %s, want KeyValueHash", got)
	}
	if b.Objects["KeyValueHash_"] == nil {
		t.Error("clashing table type not suffixed")
	}
}
