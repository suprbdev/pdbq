package e2e

import (
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/config"
)

// TestSpatialSRIDMismatchIsInvalidArgument: an EWKT reference or filter
// operand in another SRID than the column's typmod SRID is rejected as
// invalid_argument instead of failing in PostGIS (pg:XX000 invalid
// geometry); GeoJSON in another CRS is reprojected.
func TestSpatialSRIDMismatchIsInvalidArgument(t *testing.T) {
	url := testServer(t, func(c *config.Config) { c.Errors.Detail = "prod" }).URL
	for _, q := range []string{
		`{ allPlaces(near: {location: "SRID=3857;POINT(-14000 6700000)"}, orderBy: [LOCATION_DISTANCE_ASC]) { nodes { name } } }`,
		`{ allPlaces(filter: {location: {intersects: "SRID=3857;POINT(-14000 6700000)"}}) { nodes { name } } }`,
	} {
		got := postRaw(t, url, q, nil)
		if !strings.Contains(got, `"code":"invalid_argument"`) || !strings.Contains(got, "SRID 3857") || strings.Contains(got, "XX000") {
			t.Errorf("%s:\n%s\nwant invalid_argument naming SRID 3857", q, got)
		}
	}
	q := `{ allPlaces(near: {location: {type: "Point", coordinates: [-14000, 6700000], crs: {type: "name", properties: {name: "EPSG:3857"}}}}, orderBy: [LOCATION_DISTANCE_ASC], first: 1) { nodes { name } } }`
	if got := postRaw(t, url, q, nil); strings.Contains(got, `"errors"`) {
		t.Errorf("reprojected GeoJSON reference: %s", got)
	}
}
