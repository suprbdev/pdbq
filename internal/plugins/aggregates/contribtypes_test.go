package aggregates_test

import (
	"context"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/contribfixture"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/advancedfilters"
	"github.com/suprbdev/pdbq/internal/plugins/aggregates"
	contribplugin "github.com/suprbdev/pdbq/internal/plugins/contrib"
	postgisplugin "github.com/suprbdev/pdbq/internal/plugins/postgis"
	"github.com/suprbdev/pdbq/internal/schema"
)

// TestNoAggregatesOverCubeEarthRaster pins that cube, earth and raster
// columns take no aggregate, group-by or having surface: raster has no btree
// operator class, and cube's (and so earth's) btree order is a storage
// order without meaning for min, max or grouping.
func TestNoAggregatesOverCubeEarthRaster(t *testing.T) {
	cat := contribfixture.Catalog()
	locations := cat.Table("public", "locations")
	locations.Columns = append(locations.Columns, &introspect.Column{Name: "rast", Position: 8, PGType: "raster", TypeSchema: "public"})
	b := schema.New(cat, inflect.Default, schema.Options{Functions: true})
	reg := plugin.NewRegistry(advancedfilters.New(nil), postgisplugin.New(nil), contribplugin.New(nil),
		aggregates.New(map[string]any{"enabled": true}, nil))
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"LocationMinAggregates", "LocationMaxAggregates", "LocationDistinctCountAggregates"} {
		def := built.Schema.Types[typ]
		if def == nil {
			t.Fatalf("%s missing", typ)
		}
		for _, f := range def.Fields {
			if f.Name != "id" {
				t.Errorf("%s.%s aggregates a cube, earth or raster column", typ, f.Name)
			}
		}
	}
	if gb := built.Schema.Types["LocationGroupBy"]; gb != nil {
		for _, v := range gb.EnumValues {
			switch v.Name {
			case "POS", "BOX", "SPOT", "HOME", "RAST":
				t.Errorf("LocationGroupBy groups by %s", v.Name)
			}
		}
	}
}
