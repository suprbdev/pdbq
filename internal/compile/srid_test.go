package compile_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/errcode"
)

// TestSpatialOperandSRIDMismatch rejects an EWKT operand whose SRID differs
// from the column's typmod SRID, in filters and in near:, as invalid_argument
// before PostGIS fails on mixed SRIDs. GeoJSON in another CRS is reprojected
// and EWKT on an unconstrained column keeps its SRID.
func TestSpatialOperandSRIDMismatch(t *testing.T) {
	for _, q := range []string{
		`{ allPlaces(filter: {location: {intersects: "SRID=3857;POINT(-14000 6700000)"}}) { nodes { id } } }`,
		`{ allPlaces(filter: {location: {dwithin: {geometry: "SRID=3857;POINT(0 0)", distance: 1}}}) { nodes { id } } }`,
		`{ allPlaces(near: {location: "SRID=3857;POINT(-14000 6700000)"}, orderBy: [LOCATION_DISTANCE_ASC]) { nodes { id } } }`,
	} {
		_, err := compileOne(t, q, 0)
		var uerr *compile.UserError
		if !errors.As(err, &uerr) || uerr.Code() != errcode.InvalidArgument || !strings.Contains(err.Error(), "SRID 3857") {
			t.Errorf("%s: err = %v, want invalid_argument naming the SRID", q, err)
		}
	}
	for _, q := range []string{
		`{ allPlaces(near: {location: "SRID=4326;POINT(0 0)"}, orderBy: [LOCATION_DISTANCE_ASC]) { nodes { id } } }`,
		`{ allPlaces(near: {location: "POINT(0 0)"}, orderBy: [LOCATION_DISTANCE_ASC]) { nodes { id } } }`,
		`{ allPlaces(near: {location: {type: "Point", coordinates: [0, 0], crs: {type: "name", properties: {name: "EPSG:3857"}}}}, orderBy: [LOCATION_DISTANCE_ASC]) { nodes { id } } }`,
		`{ allPlaces(near: {area: "SRID=4326;POINT(0 0)"}, orderBy: [AREA_DISTANCE_ASC]) { nodes { id } } }`,
	} {
		if _, err := compileOne(t, q, 0); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
}

// TestSetReturningFunctionListsLimited cuts every list a set-returning
// function returns, at the root and as a computed column, to the page-size
// cap.
func TestSetReturningFunctionListsLimited(t *testing.T) {
	for _, q := range []string{
		`{ searchPosts(term: "") { id } }`,
		`{ searchPosts(term: "") { id userByAuthorId { email } } }`,
		`{ userById(id: 1) { recentPosts(n: 3) { id } } }`,
		`{ userById(id: 1) { tagWords } }`,
	} {
		stmt, err := compileOne(t, q, 7)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if !hasLimit(stmt, 7) {
			t.Errorf("%s: no LIMIT 7 in\n%s", q, stmt.SQL)
		}
	}
}
