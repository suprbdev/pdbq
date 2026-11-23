package contrib

import (
	"slices"
	"testing"

	"github.com/suprbdev/pdbq/internal/contribfixture"
	"github.com/suprbdev/pdbq/internal/schema"
)

// scoreFields lists the score fields of an object type with their bindings.
func scoreFields(b *schema.Builder, typeName string) map[string]schema.Score {
	out := map[string]schema.Score{}
	for _, f := range b.Objects[typeName].Fields {
		if f.Meta != nil && f.Meta.Kind == schema.KindScore && f.Meta.Score != nil {
			out[f.Name] = *f.Meta.Score
		}
	}
	return out
}

func TestScoreFields(t *testing.T) {
	b := buildWith(t, contribfixture.Catalog(), nil, true, true)
	docs := scoreFields(b, "Document")
	for name, want := range map[string]schema.Score{
		"labelSimilarity":           {Arg: "similarTo", Expr: "similarity"},
		"labelWordSimilarity":       {Arg: "similarTo", Expr: "wordSimilarity"},
		"labelStrictWordSimilarity": {Arg: "similarTo", Expr: "strictWordSimilarity"},
		"titleSimilarity":           {Arg: "similarTo", Expr: "similarity"},
		"titleStrictWordSimilarity": {Arg: "similarTo", Expr: "strictWordSimilarity"},
		"notesSimilarity":           {},
		"searchSimilarity":          {},
		"slugSimilarity":            {},
	} {
		got, ok := docs[name]
		if (want != schema.Score{}) != ok || got != want {
			t.Errorf("Document.%s = %+v (present %v), want %+v", name, got, ok, want)
		}
	}
	// title has only a GIN trigram index: similarity fields but no
	// similarity ordering; similarTo: still names it.
	if hasEnumValue(b, "DocumentsOrderBy", "TITLE_SIMILARITY_DESC") {
		t.Error("similarity ordering offered without a GiST index")
	}
	if got := fieldType(t, b, "DocumentSimilarToInput", "title"); got != "String" {
		t.Errorf("DocumentSimilarToInput.title = %q", got)
	}
	if f := b.Objects["Document"].Field("labelSimilarity"); f.Type != "Float" {
		t.Errorf("labelSimilarity type = %s", f.Type)
	}

	emb := scoreFields(b, "Embedding")
	var names []string
	for n := range emb {
		names = append(names, n)
	}
	slices.Sort(names)
	want := []string{
		"codeHammingDistance", "codeJaccardDistance",
		"embeddingCosineDistance", "embeddingL1Distance", "embeddingL2Distance", "embeddingNegativeInnerProduct",
		"halfCosineDistance", "halfL1Distance", "halfL2Distance", "halfNegativeInnerProduct",
		"sparseCosineDistance", "sparseL1Distance", "sparseL2Distance", "sparseNegativeInnerProduct",
	}
	if !slices.Equal(names, want) {
		t.Errorf("Embedding scores = %v\nwant %v", names, want)
	}
	if emb["codeHammingDistance"] != (schema.Score{Arg: "nearVector", Expr: "hammingDistance"}) {
		t.Errorf("codeHammingDistance = %+v", emb["codeHammingDistance"])
	}
	for field, typ := range map[string]string{"embedding": "[Float!]", "half": "[Float!]", "sparse": "String", "code": "String", "flags": ""} {
		if got := fieldType(t, b, "EmbeddingNearVectorInput", field); got != typ {
			t.Errorf("EmbeddingNearVectorInput.%s = %q, want %q", field, got, typ)
		}
	}
	if got := enumValues(b, "EmbeddingsOrderBy", "CODE_"); !slices.Equal(got, []string{
		"CODE_ASC", "CODE_DESC", "CODE_HAMMING_DISTANCE_ASC", "CODE_HAMMING_DISTANCE_DESC", "CODE_JACCARD_DISTANCE_ASC", "CODE_JACCARD_DISTANCE_DESC",
	}) {
		t.Errorf("bit orderings = %v", got)
	}
	if got := enumValues(b, "EmbeddingsOrderBy", "FLAGS_"); len(got) != 0 {
		t.Errorf("unindexed varbit orderings = %v", got)
	}

	all := buildWith(t, contribfixture.Catalog(), nil, false, true)
	if s := scoreFields(all, "Embedding"); s["flagsHammingDistance"] == (schema.Score{}) {
		t.Error("indexed_only off: varbit lacks distance fields")
	}
	if s := scoreFields(all, "Document"); s["notesSimilarity"] == (schema.Score{}) {
		t.Error("indexed_only off: notes lacks similarity fields")
	}
}

func TestScoreFieldsGating(t *testing.T) {
	cat := contribfixture.Catalog()
	cat.Table("public", "documents").Column("label").Comment = "@omit read"
	cat.Table("public", "documents").Column("title").Comment = "@omit order"
	cat.Table("public", "embeddings").Column("embedding").Comment = "@omit read"
	b := buildWith(t, cat, nil, true, true)
	docs, emb := scoreFields(b, "Document"), scoreFields(b, "Embedding")
	if _, ok := docs["labelSimilarity"]; ok {
		t.Error("@omit read kept labelSimilarity")
	}
	if _, ok := docs["titleSimilarity"]; !ok {
		t.Error("@omit order removed titleSimilarity")
	}
	if _, ok := emb["embeddingL2Distance"]; ok {
		t.Error("@omit read kept embeddingL2Distance")
	}

	noOrder := buildWith(t, contribfixture.Catalog(), func(s *Settings) { s.Ordering = false }, true, true)
	if len(scoreFields(noOrder, "Document")) != 0 || len(scoreFields(noOrder, "Embedding")) != 0 {
		t.Error("ordering: false kept score fields")
	}
	if hasArg(noOrder.Query.Field("allDocuments"), "similarTo") {
		t.Error("ordering: false kept similarTo:")
	}

	noVector := buildWith(t, withoutExtension(contribfixture.Catalog(), "vector"), nil, true, true)
	if len(scoreFields(noVector, "Embedding")) != 0 {
		t.Error("score fields without pgvector")
	}
}

func TestVectorThresholdFilters(t *testing.T) {
	b := buildWith(t, contribfixture.Catalog(), nil, true, true)
	for field, want := range map[string]string{"embedding": "VectorFilterOps", "sparse": "", "code": "StringFilterOps"} {
		if got := fieldType(t, b, "EmbeddingFilter", field); got != want {
			t.Errorf("indexed_only: EmbeddingFilter.%s = %s, want %s", field, got, want)
		}
	}
	if in := b.Inputs["VectorFilterOps"]; in.Field("cosineDistanceLessThan") != nil {
		t.Error("threshold operator on the shared VectorFilterOps")
	}

	allowed, _ := buildAllowing(t, contribfixture.Catalog(), map[string][]string{"public.embeddings": {"embedding", "sparse", "code"}})
	for field, want := range map[string]string{
		"embedding": "VectorDistanceFilterOps", "half": "VectorFilterOps",
		"sparse": "SparseVectorDistanceFilterOps", "code": "BitDistanceFilterOps", "flags": "",
	} {
		if got := fieldType(t, allowed, "EmbeddingFilter", field); got != want {
			t.Errorf("allowlisted: EmbeddingFilter.%s = %s, want %s", field, got, want)
		}
	}
	for input, ops := range map[string]map[string]string{
		"VectorDistanceFilterOps": {
			"isNull": "Boolean", "l2DistanceLessThan": "VectorDistanceInput", "negativeInnerProductLessThan": "VectorDistanceInput",
			"cosineDistanceLessThan": "VectorDistanceInput", "l1DistanceLessThan": "VectorDistanceInput",
		},
		"SparseVectorDistanceFilterOps": {"isNull": "Boolean", "cosineDistanceLessThan": "SparseVectorDistanceInput"},
		"BitDistanceFilterOps": {
			"equalTo": "String", "like": "String", "hammingDistanceLessThan": "BitDistanceInput", "jaccardDistanceLessThan": "BitDistanceInput",
		},
		"VectorDistanceInput":       {"vector": "[Float!]!", "distance": "Float!"},
		"SparseVectorDistanceInput": {"vector": "String!", "distance": "Float!"},
		"BitDistanceInput":          {"vector": "String!", "distance": "Float!"},
	} {
		for op, typ := range ops {
			if got := fieldType(t, allowed, input, op); got != typ {
				t.Errorf("%s.%s = %q, want %q", input, op, got, typ)
			}
		}
	}

	all := buildWith(t, contribfixture.Catalog(), nil, false, true)
	if got := fieldType(t, all, "EmbeddingFilter", "half"); got != "VectorDistanceFilterOps" {
		t.Errorf("indexed_only off: EmbeddingFilter.half = %s", got)
	}
	if got := fieldType(t, all, "EmbeddingFilter", "flags"); got != "BitDistanceFilterOps" {
		t.Errorf("indexed_only off: EmbeddingFilter.flags = %s", got)
	}
	noFilters := buildWith(t, contribfixture.Catalog(), func(s *Settings) { s.Filters = false }, false, true)
	if noFilters.Inputs["VectorDistanceFilterOps"] != nil || noFilters.Inputs["BitDistanceFilterOps"] != nil {
		t.Error("filters: false declared threshold inputs")
	}
}
