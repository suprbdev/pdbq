package exec

import (
	"context"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	pgcontrib "github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/contribfixture"
	"github.com/suprbdev/pdbq/internal/inflect"
	contribplugin "github.com/suprbdev/pdbq/internal/plugins/contrib"
	"github.com/suprbdev/pdbq/internal/schema"
)

// TestCostSpatialBuffer charges postgis.BufferCost per row for buffer:
// (given a non-null value) on top of the column read; the other projection
// arguments cost nothing extra.
func TestCostSpatialBuffer(t *testing.T) {
	cat := testutil.FixtureCatalog()
	plain := costFor(t, cat, map[string]any{}, `{ allPlaces(first: 10) { nodes { location } } }`, nil)
	for _, tc := range []struct {
		query string
		vars  map[string]any
		extra int
	}{
		{`{ allPlaces(first: 10) { nodes { location(precision: 6, simplify: 0.1, transform: 3857) } } }`, nil, 0},
		{`{ allPlaces(first: 10) { nodes { location(buffer: 100) } } }`, nil, postgis.BufferCost},
		{`query ($d: Float) { allPlaces(first: 10) { nodes { location(buffer: $d) } } }`, map[string]any{"d": 5.0}, postgis.BufferCost},
		{`query ($d: Float) { allPlaces(first: 10) { nodes { location(buffer: $d) } } }`, map[string]any{"d": nil}, 0},
	} {
		got := costFor(t, cat, map[string]any{}, tc.query, tc.vars)
		if want := plain + 10*tc.extra; got != want {
			t.Errorf("%s: cost %d, want %d", tc.query, got, want)
		}
	}
	typed := costFor(t, cat, map[string]any{"typed_geometry": true}, `{ allPlaces(first: 10) { nodes { location { x } } } }`, nil)
	buffered := costFor(t, cat, map[string]any{"typed_geometry": true}, `{ allPlaces(first: 10) { nodes { location(buffer: 1) { x } } } }`, nil)
	if buffered != typed+10*postgis.BufferCost {
		t.Errorf("typed geometry buffer cost %d, want %d", buffered, typed+10*postgis.BufferCost)
	}
}

// TestCostHeadline charges contrib.HeadlineCost per row for a headline.
func TestCostHeadline(t *testing.T) {
	b := schema.New(contribfixture.Catalog(), inflect.Default, schema.Options{FilterIndexedOnly: true})
	if err := contribplugin.New(nil).TransformSchema(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	measureQ := func(q string) int {
		doc, perr := parser.ParseQuery(&ast.Source{Input: q})
		if perr != nil {
			t.Fatal(perr)
		}
		if errs := validator.ValidateWithRules(built.Schema, doc, nil); len(errs) > 0 {
			t.Fatal(errs)
		}
		_, c := newMeasurer(doc.Fragments, nil, 100, built, 0, 0).measure(doc.Operations[0].SelectionSet)
		return c
	}
	rank := measureQ(`{ allDocuments(first: 10, filter: {search: {matches: "a"}}) { nodes { searchRank } } }`)
	headline := measureQ(`{ allDocuments(first: 10, filter: {search: {matches: "a"}}) { nodes { searchHeadline(document: NOTES) } } }`)
	if headline != rank+10*pgcontrib.HeadlineCost {
		t.Errorf("headline cost %d, want %d", headline, rank+10*pgcontrib.HeadlineCost)
	}
}
