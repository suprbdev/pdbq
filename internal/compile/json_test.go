package compile_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/compile"
)

func TestJSONObject(t *testing.T) {
	if got := compile.JSONObject(); got != "'{}'::json" {
		t.Errorf("empty: %s", got)
	}
	if got := compile.JSONObject("'a'", "1", "'b'", "2"); got != "json_build_object('a', 1, 'b', 2)" {
		t.Errorf("narrow: %s", got)
	}
	var pairs []string
	for i := range 90 {
		pairs = append(pairs, fmt.Sprintf("'k%d'", i), fmt.Sprint(i))
	}
	got := compile.JSONObject(pairs...)
	if n := strings.Count(got, "json_build_object("); n != 3 {
		t.Fatalf("90 pairs: %d json_build_object calls, want 3: %s", n, got)
	}
	if !strings.HasPrefix(got, "(left(json_build_object('k0', 0,") ||
		!strings.Contains(got, "::text, -1) || ', ' || substr(left(json_build_object('k40', 40,") ||
		!strings.Contains(got, "::text, -1), 2) || ', ' || substr(json_build_object('k80', 80,") ||
		!strings.HasSuffix(got, "'k89', 89)::text, 2))::json") {
		t.Errorf("90 pairs: %s", got)
	}
}
