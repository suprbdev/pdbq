package contrib

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/contribfixture"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// buildAllowing applies the plugin to cat under indexed_only with the given
// allow_columns, returning the builder and the log output.
func buildAllowing(t *testing.T, cat *introspect.Catalog, allow map[string][]string) (*schema.Builder, string) {
	t.Helper()
	var logs bytes.Buffer
	b := schema.New(cat, inflect.Next(inflect.Default), schema.Options{
		Logger:            slog.New(slog.NewTextHandler(&logs, nil)),
		Functions:         true,
		FilterIndexedOnly: true,
		AllowColumns:      allow,
	})
	if err := New(DefaultSettings()).TransformSchema(context.Background(), b); err != nil {
		t.Fatalf("TransformSchema: %v", err)
	}
	if _, err := b.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return b, logs.String()
}

func withoutExtension(cat *introspect.Catalog, name string) *introspect.Catalog {
	cat.Extensions = slices.DeleteFunc(cat.Extensions, func(e *introspect.Extension) bool { return e.Name == name })
	return cat
}

func enumValues(b *schema.Builder, enum, prefix string) []string {
	var out []string
	for _, v := range b.Enums[enum].Values {
		if strings.HasPrefix(v.Name, prefix) {
			out = append(out, v.Name)
		}
	}
	return out
}

func TestUnaccentSurface(t *testing.T) {
	ops := []string{"unaccentEqualTo", "unaccentLike", "unaccentIncludes", "unaccentStartsWith"}

	b := buildWith(t, withoutExtension(contribfixture.Catalog(), "fuzzystrmatch"), nil, true, true)
	for _, field := range []string{"title", "label"} {
		if got := fieldType(t, b, "DocumentFilter", field); got != "StringTrgmFilter" {
			t.Errorf("indexed_only: DocumentFilter.%s = %s, want StringTrgmFilter", field, got)
		}
	}
	if b.Inputs["StringUnaccentFilter"] != nil || b.Inputs["StringTrgmUnaccentFilter"] != nil {
		t.Error("indexed_only declared an unaccent input without an allowlisted column")
	}

	b, _ = buildAllowing(t, withoutExtension(contribfixture.Catalog(), "fuzzystrmatch"), map[string][]string{"public.documents": {"notes"}})
	if got := fieldType(t, b, "DocumentFilter", "notes"); got != "StringTrgmUnaccentFilter" {
		t.Errorf("allowlisted notes = %s", got)
	}
	if got := fieldType(t, b, "DocumentFilter", "title"); got != "StringTrgmFilter" {
		t.Errorf("title = %s, want StringTrgmFilter", got)
	}
	for _, op := range append([]string{"equalTo", "startsWith", "similarTo", "strictWordSimilarTo"}, ops...) {
		if fieldType(t, b, "StringTrgmUnaccentFilter", op) == "" {
			t.Errorf("StringTrgmUnaccentFilter.%s missing", op)
		}
	}

	noTrgm := buildWith(t, withoutExtension(withoutExtension(contribfixture.Catalog(), "fuzzystrmatch"), "pg_trgm"), nil, false, true)
	if got := fieldType(t, noTrgm, "DocumentFilter", "notes"); got != "StringUnaccentFilter" {
		t.Errorf("notes without pg_trgm = %s, want StringUnaccentFilter", got)
	}
	for _, op := range append([]string{"equalTo", "likeInsensitive"}, ops...) {
		if fieldType(t, noTrgm, "StringUnaccentFilter", op) == "" {
			t.Errorf("StringUnaccentFilter.%s missing", op)
		}
	}
	if fieldType(t, noTrgm, "StringUnaccentFilter", "similarTo") != "" {
		t.Error("StringUnaccentFilter carries pg_trgm operators")
	}

	noUnaccent := buildWith(t, withoutExtension(withoutExtension(contribfixture.Catalog(), "fuzzystrmatch"), "unaccent"), nil, false, true)
	if strings.Contains(noUnaccent.SDL(), "unaccent") {
		t.Error("unaccent surface without the extension")
	}

	off := buildWith(t, withoutExtension(contribfixture.Catalog(), "fuzzystrmatch"), func(s *Settings) { s.Filters = false }, false, true)
	if strings.Contains(off.SDL(), "Unaccent") {
		t.Error("filters: false kept the unaccent operators")
	}
}

func TestUnaccentInputNameTaken(t *testing.T) {
	b := schema.New(withoutExtension(contribfixture.Catalog(), "fuzzystrmatch"), inflect.Next(inflect.Default), schema.Options{
		Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	b.Inputs["StringTrgmUnaccentFilter"] = &schema.Input{Name: "StringTrgmUnaccentFilter"}
	if err := New(DefaultSettings()).TransformSchema(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if got := fieldType(t, b, "DocumentFilter", "notes"); got != "StringTrgmFilter" {
		t.Errorf("notes with the unaccent input name taken = %s, want StringTrgmFilter", got)
	}
}

func TestVectorTypeMapping(t *testing.T) {
	b := buildWith(t, contribfixture.Catalog(), nil, true, true)
	obj := b.Objects["Embedding"]
	for field, want := range map[string]string{"embedding": "[Float!]", "half": "[Float!]", "sparse": "String"} {
		if got := obj.Field(field).Type; got != want {
			t.Errorf("Embedding.%s = %s, want %s", field, got, want)
		}
		if got := fieldType(t, b, "EmbeddingInput", field); got != want {
			t.Errorf("EmbeddingInput.%s = %s, want %s", field, got, want)
		}
	}
	fn := b.Query.Field("scaleVector")
	if fn == nil || fn.Type != "[Float!]" {
		t.Fatalf("scaleVector: %+v", fn)
	}
	for _, a := range fn.Args {
		if a.Name == "v" && a.Type != "[Float!]" {
			t.Errorf("scaleVector(v:) = %s", a.Type)
		}
	}
	if strings.Contains(b.SDL(), "scalar Float") {
		t.Error("Float declared as a custom scalar")
	}

	if got := fieldType(t, b, "EmbeddingFilter", "embedding"); got != "VectorFilter" {
		t.Errorf("EmbeddingFilter.embedding = %s", got)
	}
	if fields := b.Inputs["VectorFilter"].Fields; len(fields) != 1 || fields[0].Name != "isNull" {
		t.Errorf("VectorFilter fields: %+v", fields)
	}
	if c := b.Inputs["EmbeddingCondition"]; c != nil && c.Field("embedding") != nil {
		t.Error("vector column in the condition input")
	}
	if en := b.Enums["EmbeddingsDistinctOn"]; en != nil && en.HasValue("EMBEDDING") {
		t.Error("vector column in distinctOn")
	}
	if hasEnumValue(b, "EmbeddingsOrderBy", "EMBEDDING_ASC") {
		t.Error("plain ordering on a vector column")
	}

	noExt := buildWith(t, withoutExtension(contribfixture.Catalog(), "vector"), nil, true, true)
	if got := noExt.Objects["Embedding"].Field("embedding").Type; got != "String" {
		t.Errorf("embedding without pgvector = %s, want String", got)
	}
}

func TestVectorOrdering(t *testing.T) {
	b := buildWith(t, contribfixture.Catalog(), nil, true, true)
	want := []string{
		"EMBEDDING_L2_DISTANCE_ASC", "EMBEDDING_L2_DISTANCE_DESC",
		"EMBEDDING_NEGATIVE_INNER_PRODUCT_ASC", "EMBEDDING_NEGATIVE_INNER_PRODUCT_DESC",
		"EMBEDDING_COSINE_DISTANCE_ASC", "EMBEDDING_COSINE_DISTANCE_DESC",
		"EMBEDDING_L1_DISTANCE_ASC", "EMBEDDING_L1_DISTANCE_DESC",
	}
	if got := enumValues(b, "EmbeddingsOrderBy", "EMBEDDING_"); !slices.Equal(got, want) {
		t.Errorf("embedding (HNSW) orderings = %v", got)
	}
	if got := enumValues(b, "EmbeddingsOrderBy", "HALF_"); len(got) != 6 || slices.Contains(got, "HALF_L1_DISTANCE_ASC") {
		t.Errorf("half (IVFFlat) orderings = %v", got)
	}
	if got := enumValues(b, "EmbeddingsOrderBy", "SPARSE_"); len(got) != 0 {
		t.Errorf("unindexed sparse orderings = %v", got)
	}
	if got := fieldType(t, b, "EmbeddingNearInput", "embedding"); got != "[Float!]" {
		t.Errorf("EmbeddingNearInput.embedding = %s", got)
	}
	if got := fieldType(t, b, "EmbeddingNearInput", "sparse"); got != "String" {
		t.Errorf("EmbeddingNearInput.sparse (distance fields only) = %q", got)
	}
	if !hasArg(b.Query.Field("allEmbeddings"), "near") {
		t.Error("allEmbeddings lacks near:")
	}
	for _, f := range b.Inputs["DocumentNearInput"].Fields {
		if f.Type != "String" {
			t.Errorf("vector reference %s on a table without vector columns", f.Name)
		}
	}

	allowed, _ := buildAllowing(t, contribfixture.Catalog(), map[string][]string{"public.embeddings": {"sparse"}})
	if got := enumValues(allowed, "EmbeddingsOrderBy", "SPARSE_"); len(got) != 8 {
		t.Errorf("allowlisted sparse orderings = %v", got)
	}
	if got := fieldType(t, allowed, "EmbeddingNearInput", "sparse"); got != "String" {
		t.Errorf("EmbeddingNearInput.sparse = %s", got)
	}
	all := buildWith(t, contribfixture.Catalog(), nil, false, true)
	if got := enumValues(all, "EmbeddingsOrderBy", "HALF_"); len(got) != 8 {
		t.Errorf("half orderings without indexed_only = %v", got)
	}

	cat := contribfixture.Catalog()
	for _, e := range cat.Extensions {
		if e.Name == "vector" {
			e.Version = "0.6.2"
		}
	}
	old := buildWith(t, cat, nil, true, true)
	if hasEnumValue(old, "EmbeddingsOrderBy", "EMBEDDING_L1_DISTANCE_ASC") {
		t.Error("L1 ordering for pgvector 0.6")
	}

	noOrder := buildWith(t, contribfixture.Catalog(), func(s *Settings) { s.Ordering = false }, true, true)
	if len(enumValues(noOrder, "EmbeddingsOrderBy", "EMBEDDING_")) != 0 || hasArg(noOrder.Query.Field("allEmbeddings"), "near") {
		t.Error("ordering: false kept vector ordering")
	}
}

func TestVectorOrderingOmit(t *testing.T) {
	for _, tag := range []string{"@omit order", "@omit read"} {
		cat := contribfixture.Catalog()
		cat.Table("public", "embeddings").Column("embedding").Comment = tag
		b := buildWith(t, cat, nil, true, true)
		if got := enumValues(b, "EmbeddingsOrderBy", "EMBEDDING_"); len(got) != 0 {
			t.Errorf("%s kept %v", tag, got)
		}
		read := tag == "@omit read"
		if got := fieldType(t, b, "EmbeddingNearInput", "embedding") != ""; got == read {
			t.Errorf("%s: near: field present = %v", tag, got)
		}
		if got := b.Objects["Embedding"].Field("embeddingCosineDistance") != nil; got == read {
			t.Errorf("%s: distance field present = %v", tag, got)
		}
	}
}

func TestVectorNearInputNameTaken(t *testing.T) {
	cat := contribfixture.Catalog()
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "embedding_near_input", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true},
		Columns:    []*introspect.Column{{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog"}},
	})
	b, logs := buildAllowing(t, cat, nil)
	if b.Objects["EmbeddingNearInput"] == nil {
		t.Fatal("clashing table type missing")
	}
	if len(enumValues(b, "EmbeddingsOrderBy", "EMBEDDING_")) != 0 || hasArg(b.Query.Field("allEmbeddings"), "near") {
		t.Error("vector ordering added although its input name is taken")
	}
	if !strings.Contains(logs, "EmbeddingNearInput") {
		t.Errorf("no warning: %s", logs)
	}
}
