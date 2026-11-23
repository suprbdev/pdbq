package contrib

import (
	"testing"

	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbq/internal/contribfixture"
)

// withOpClass returns the contrib fixture with the named index of table
// recorded as built with opClass.
func withOpClass(t *testing.T, table, index, opClass string) *introspect.Catalog {
	t.Helper()
	cat := contribfixture.Catalog()
	for _, ix := range cat.Table("public", table).Indexes {
		if ix.Name == index {
			ix.OpClasses = []string{opClass}
			return cat
		}
	}
	t.Fatalf("index %s.%s missing", table, index)
	return nil
}

func TestIndexedOnlyMatchesOpClass(t *testing.T) {
	for opClass, want := range map[string]string{"gist_trgm_ops": "StringTrgmFilterOps", "gist_text_ops": "StringFilterOps"} {
		b := buildWith(t, withOpClass(t, "documents", "documents_label_idx", opClass), nil, true, true)
		if got := fieldType(t, b, "DocumentFilter", "label"); got != want {
			t.Errorf("label led by a %s index: DocumentFilter.label = %s, want %s", opClass, got, want)
		}
	}

	b := buildWith(t, withOpClass(t, "embeddings", "embeddings_embedding_idx", "vector_l2_ops"), nil, true, true)
	for value, want := range map[string]bool{"EMBEDDING_L2_DISTANCE_ASC": true, "EMBEDDING_COSINE_DISTANCE_ASC": false, "EMBEDDING_L1_DISTANCE_ASC": false} {
		if got := hasEnumValue(b, "EmbeddingsOrderBy", value); got != want {
			t.Errorf("vector_l2_ops index: EmbeddingsOrderBy.%s = %v, want %v", value, got, want)
		}
	}
}
