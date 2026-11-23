package compile_test

import (
	"context"
	"encoding/base64"
	"math"
	"strconv"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/schema"
)

// FuzzFilter drives arbitrary values through the filter-input -> SQL path
// and asserts the invariant that makes injection impossible: user values
// never appear in the SQL text, only in the parameter list. It runs against
// the contrib fixture so the contrib vocabulary operators (full-text,
// pg_trgm, unaccent, hstore, ltree, citext, ranges, inet, cube,
// earthdistance), the codec-typed generic comparisons, rank, similarity,
// vector, cube and earth distance ordering, the similarity and distance
// score fields, the vector, bit, cube, earth and point distance thresholds
// and vector, cube and earth inputs are covered too, and against
// the aggregates schema for having and relation-aggregate filters, through
// offset and offset-cursor pagination (with and without distinctOn), and
// through a custom mutation whose payload compiles to a Then statement.
func FuzzFilter(f *testing.F) {
	built := buildContrib(f)
	aggBuilt := buildAggregates(f)

	const query = `query ($e: String, $tag: [String!], $n: Int!, $h: KeyValueHash, $b: Boolean!, $d: Datetime, $ip: InternetAddress, $ips: [InternetAddress!], $cfg: String, $vec: [Float!], $fl: Float!, $sv: String!, $vv: [Float!]!, $k: String!) {
		allUsers(filter: {
			and: [
				{email: {like: $e, startsWith: $e}}
				{or: [{id: {greaterThan: $n}}, {tags: {contains: $tag}}]}
				{settings: {pathExists: $e, pathMatch: $e}}
			]
		}) { nodes { id } }
		allDocuments(
			filter: {
				search: {matches: $e, matchesWebsearch: $e, config: $cfg}
				title: {similarTo: $e, wordSimilarTo: $e, strictWordSimilarTo: $e}
				slug: {equalTo: $e, in: $tag, likeInsensitive: $e, startsWith: $e}
				attrs: {containsKey: $e, containsAllKeys: $tag, contains: $h, equalTo: $h}
				path: {ancestorOf: $e, matchesLquery: $e, matchesAnyLquery: $tag, in: $tag}
				paths: {descendantOf: $e, equalTo: $tag}
				span: {overlaps: {start: {value: $n, inclusive: $b}, end: {value: $n, inclusive: true}}, containsElement: $n}
				avail: {equalTo: [{start: {value: $n, inclusive: $b}}], containsElement: $n}
				during: {containsElement: $d}
				ip: {containedBy: $ip, in: $ips}
				notes: {unaccentEqualTo: $e, unaccentLike: $e, unaccentIncludes: $e, unaccentStartsWith: $e}
			}
			condition: {slug: $e, attrs: $h}
			near: {label: $e, title: $e}
			orderBy: [SEARCH_RANK_DESC, LABEL_SIMILARITY_DESC]
		) { nodes { id searchRank attrs span { start { value inclusive } } labelSimilarity titleWordSimilarity } }
		values: allDocuments(
			filter: {
				pathNlevel: {greaterThan: $n, in: [$n]}
				attrsValue: {key: $k, equalTo: $e, in: $tag}
				tagIds: {matchesQueryInt: $e, overlaps: [$n]}
				notes: {soundexEqualTo: $e, doubleMetaphoneEqualTo: $e}
				or: [{attrsValue: {key: $k, isNull: true}}, {pathNlevel: {lessThan: $n}}]
			}
			near: {attrs: $e}
			orderBy: [ATTRS_VALUE_ASC, PATH_NLEVEL_DESC]
		) { nodes { id pathNlevel pathParent pathsLca tagIdsIcount attrsValue(key: $k) pathLca(path: $k) } }
		allEmbeddings(
			filter: {
				embedding: {cosineDistanceLessThan: {vector: $vv, distance: $fl}, l2DistanceLessThan: {vector: $vv, distance: $fl}}
				sparse: {negativeInnerProductLessThan: {vector: $sv, distance: $fl}}
				code: {hammingDistanceLessThan: {vector: "101", distance: $fl}}
			}
			near: {embedding: $vec, half: $vec, sparse: $cfg, code: "011"}
			orderBy: [EMBEDDING_COSINE_DISTANCE_ASC, HALF_L2_DISTANCE_DESC, SPARSE_NEGATIVE_INNER_PRODUCT_ASC, EMBEDDING_L1_DISTANCE_ASC, CODE_HAMMING_DISTANCE_ASC]
			first: 3
		) { nodes { id embedding half sparse embeddingCosineDistance sparseL2Distance codeJaccardDistance } }
		allPlaces(near: {location: "POINT(0 0)", area: "POINT(0 0)"}, orderBy: [LOCATION_DISTANCE_ASC]) {
			nodes { locationDistance(useSpheroid: true) areaDistance }
		}
	}`
	const aggQuery = `query ($e: String, $tag: [String!], $n: Int!, $big: BigInt, $bigs: [BigInt!], $bf: BigFloat, $d: Datetime) {
		allPosts(filter: {title: {startsWith: $e}}) {
			aggregates { sum { id } }
			groupedAggregates(
				groupBy: [AUTHOR_ID, TITLE]
				having: {
					sum: {id: {greaterThan: $big, lessThanOrEqualTo: $big}}
					or: [{max: {id: {equalTo: $n}}}, {average: {authorId: {notEqualTo: $bf}}}]
				}
			) { keys distinctCount { title } }
		}
		allUsers(
			filter: {postsByAuthorId: {aggregates: {
				filter: {title: {in: $tag, likeInsensitive: $e}}
				sum: {id: {greaterThan: $big, in: $bigs}}
				distinctCount: {title: {equalTo: $big, isNull: false}}
				max: {id: {lessThan: $n}}
			}}}
			orderBy: [POSTS_BY_AUTHOR_ID__SUM_ID_DESC]
		) {
			groupedAggregates(groupBy: [CREATED_AT_TRUNCATED_TO_DAY], having: {max: {createdAt: {greaterThan: $d}}}) { keys }
		}
	}`
	const fuzzyQuery = `query ($e: String!, $n: Int!, $len: Int, $b: Boolean!) {
		allDocuments(
			filter: {notes: {levenshteinDistanceLessThan: {text: $e, distance: $n}, metaphoneEqualTo: $e}, search: {matchesPlain: $e}}
			near: {notes: $e}
			orderBy: [NOTES_LEVENSHTEIN_DISTANCE_ASC]
		) { nodes { id notesLevenshteinDistance pathSubpath(offset: $n, length: $len) searchHeadline(document: NOTES, options: {maxWords: $n, minWords: 1, startSel: $e, highlightAll: $b}) } }
	}`
	const pageQuery = `query ($n: Int!, $after: Cursor) {
		allUsers(distinctOn: [MOOD], orderBy: [MOOD_DESC], first: 2, offset: $n) {
			totalCount edges { cursor node { id } } pageInfo { hasNextPage hasPreviousPage endCursor }
		}
		afterUsers: allUsers(distinctOn: [MOOD], orderBy: [MOOD_DESC], first: 2, after: $after) {
			nodes { id } edges { cursor } pageInfo { hasNextPage hasPreviousPage startCursor }
		}
		allPosts(first: 3, offset: $n, orderBy: [TITLE_ASC]) { edges { cursor } pageInfo { hasNextPage hasPreviousPage } }
		allMetrics(first: 2, offset: $n, after: $after) { totalCount edges { cursor } pageInfo { hasNextPage hasPreviousPage } }
	}`
	const cubeQuery = `query ($cu: Cube!, $cs: Cube!, $fl: Float!, $lat: Float!, $lon: Float!) {
		allLocations(
			filter: {
				pos: {contains: $cu, overlaps: $cs, l1DistanceLessThan: {cube: $cu, distance: $fl}}
				box: {equalTo: $cs, containedBy: $cu}
				spot: {earthDistanceLessThan: {lat: $lat, lon: $lon, distance: $fl}}
				here: {earthDistanceMilesLessThan: {lat: $lat, lon: $lon, distance: $fl}}
			}
			near: {pos: $cu, box: $cs, spot: {lat: $lat, lon: $lon}, home: {lat: $lat, lon: $lon}, here: {lat: $lon, lon: $lat}}
			orderBy: [POS_CHEBYSHEV_DISTANCE_ASC, SPOT_EARTH_DISTANCE_DESC, HERE_EARTH_DISTANCE_MILES_ASC]
			first: 3
		) { nodes { id pos spot { lat lon } posL2Distance boxL1Distance spotEarthDistance homeEarthDistance hereEarthDistanceMiles } }
	}`
	const cubeMutation = `mutation ($cu: Cube!, $lat: Float!, $lon: Float!) {
		createLocation(input: {location: {pos: $cu, box: $cu, spot: {lat: $lat, lon: $lon}}}) { location { id pos spot { lon } } }
	}`
	const mutationQuery = `mutation ($e: String, $n: Int!, $vec: [Float!], $cfg: String) {
		createEmbedding(input: {embedding: {embedding: $vec, half: $vec, sparse: $cfg}}) { embedding { id embedding } }
		publishPost(input: {postId: $n, clientMutationId: $e}) {
			clientMutationId
			post { title userByAuthorId { email } }
			query { allUsers(filter: {email: {startsWith: $e}}) { totalCount } }
		}
	}`
	type target struct {
		built *schema.Built
		op    *ast.OperationDefinition
		// lenient targets carry operands with value limits (fuzzystrmatch
		// lengths, subpath positions): a compile error on them is a refused
		// value, not a failure, and the statement is skipped.
		lenient bool
	}
	var targets []target
	for _, tc := range []struct {
		built   *schema.Built
		query   string
		lenient bool
	}{{built, query, false}, {aggBuilt, aggQuery, false}, {built, pageQuery, true}, {built, cubeQuery, true}, {built, cubeMutation, true},
		{built, mutationQuery, false}, {built, fuzzyQuery, true}} {
		doc, perr := parser.ParseQuery(&ast.Source{Input: tc.query})
		if perr != nil {
			f.Fatal(perr)
		}
		if errs := validator.ValidateWithRules(tc.built.Schema, doc, nil); len(errs) > 0 {
			f.Fatal(errs)
		}
		targets = append(targets, target{tc.built, doc.Operations[0], tc.lenient})
	}

	compileWith := func(t testing.TB, email, tag string, n int) []*compile.Statement {
		t.Helper()
		var length any
		if n%2 != 0 {
			length = n
		}
		var out []*compile.Statement
		for _, tg := range targets {
			vars, verr := validator.VariableValues(tg.built.Schema, tg.op, map[string]any{
				"e": email, "tag": []any{tag}, "n": n, "h": map[string]any{tag: email}, "b": n%2 == 0,
				"d": email, "ip": email, "ips": []any{tag}, "cfg": "c" + email, "big": tag, "bigs": []any{tag}, "bf": email,
				"vec": []any{float64(n), 0.5, -float64(n)}, "fl": float64(n) / 4, "sv": "c" + email, "vv": []any{0.5, float64(n)},
				"len": length, "k": email, "after": base64.StdEncoding.EncodeToString([]byte("o:" + strconv.Itoa(n))),
				"cu": []any{float64(n), 0.5}, "cs": "(" + tag + ")", "lat": float64(n % 91), "lon": float64(n) / 7,
			})
			if verr != nil {
				t.Skipf("rejected variables: %v", verr)
			}
			for _, sel := range tg.op.SelectionSet {
				stmt, err := compile.New(tg.built).Compile(context.Background(), &compile.Request{
					Field:    sel.(*ast.Field),
					Vars:     vars,
					Built:    tg.built,
					MaxDepth: 15,
				})
				if err != nil && tg.lenient {
					out = append(out, nil)
					continue
				}
				if err != nil {
					t.Fatalf("compile error on valid input: %v", err)
				}
				out = append(out, stmt)
			}
		}
		return out
	}
	// The invariant that makes injection impossible: the SQL text is a pure
	// function of the query shape — values only move through the parameter
	// list, so any two value sets must produce byte-identical SQL.
	baseline := compileWith(f, "a@example.com", "admin", 5)
	for i, stmt := range baseline {
		if stmt == nil {
			f.Fatalf("baseline statement %d failed to compile", i)
		}
	}
	if baseline[len(baseline)-2].Then == nil {
		f.Fatal("the custom mutation compiled without a Then statement")
	}

	f.Add("a@example.com", "admin", 5)
	f.Add(`'; DROP TABLE users; --`, `"]}`, -1)
	f.Add("%_\\", "'", 0)
	f.Add("", "", 1<<30)

	f.Fuzz(func(t *testing.T, email, tag string, n int) {
		for i, stmt := range compileWith(t, email, tag, n) {
			if stmt == nil {
				continue
			}
			if stmt.SQL != baseline[i].SQL {
				t.Fatalf("SQL text varied with input values:\n--- baseline ---\n%s\n--- got ---\n%s", baseline[i].SQL, stmt.SQL)
			}
			if len(stmt.Args) != len(baseline[i].Args) {
				t.Fatalf("parameter count varied: %d != %d", len(stmt.Args), len(baseline[i].Args))
			}
			then, want := stmt.Then, baseline[i].Then
			if (then == nil) != (want == nil) || then != nil && (then.SQL != want.SQL || len(then.Args) != len(want.Args)) {
				t.Fatalf("Then statement varied with input values:\n--- baseline ---\n%+v\n--- got ---\n%+v", want, then)
			}
		}
	})
}

// FuzzSpatial is FuzzFilter for the PostGIS path: WKT strings and bounding-box
// coordinates are user data, so they must move through the parameter list
// exactly like scalars. A WKT string is the interesting case because it is
// free text that ends up inside a PostGIS constructor. Every input form (bare
// WKT, EWKT) compiles to the same SQL text, so one baseline covers them all.
// The raster intersects filter, pixel value and summary statistics take the
// same geometry and a band number.
func FuzzSpatial(f *testing.F) {
	built := build(f)
	compiler := compile.New(built)

	const query = `query ($wkt: GeoJSON!, $x: Float!, $d: Float!, $p: Int!, $band: Int!) {
		allPlaces(filter: {
			location: {
				intersects: $wkt
				bboxIntersects: {minX: $x, minY: $x, maxX: $x, maxY: $x}
				dwithin: {geometry: $wkt, distance: $d}
				bboxLeftOf: $wkt
				bboxIntersectsND: $wkt
				bboxIntersects2D: $wkt
			}
		}, near: {location: $wkt, area: $wkt}, orderBy: [AREA_DISTANCE_ASC]) { nodes { id locationDistance areaDistance location(buffer: $d, precision: $p) area(buffer: $x, simplify: $d, transform: 3857, precision: $p) } }
		allTiles(filter: {rast: {intersects: $wkt}}) {
			nodes { rastEnvelope rastValue(point: $wkt, band: $band) rastSummaryStats(band: $band) { count mean } }
		}
	}`
	doc, perr := parser.ParseQuery(&ast.Source{Input: query})
	if perr != nil {
		f.Fatal(perr)
	}
	if errs := validator.ValidateWithRules(built.Schema, doc, nil); len(errs) > 0 {
		f.Fatal(errs)
	}
	op := doc.Operations[0]

	compileWith := func(t testing.TB, wkt string, x, d float64) ([]*compile.Statement, bool) {
		t.Helper()
		vars, verr := validator.VariableValues(built.Schema, op, map[string]any{
			"wkt": wkt, "x": x, "d": d, "p": len(wkt) % 16, "band": 1 + int(math.Mod(math.Abs(x), 4)),
		})
		if verr != nil {
			return nil, false
		}
		var out []*compile.Statement
		for _, sel := range op.SelectionSet {
			stmt, err := compiler.Compile(context.Background(), &compile.Request{
				Field:    sel.(*ast.Field),
				Vars:     vars,
				Built:    built,
				MaxDepth: 15,
			})
			if err != nil {
				// Malformed geometry is rejected at compile time by design; only a
				// successful compile is subject to the invariant.
				return nil, false
			}
			out = append(out, stmt)
		}
		return out, true
	}

	baseline, ok := compileWith(f, "POINT(1 2)", 1.5, 100)
	if !ok {
		f.Fatal("baseline spatial query failed to compile")
	}

	f.Add("POINT(1 2)", 1.5, 100.0)
	f.Add("SRID=4326;POLYGON((0 0,1 0,1 1,0 0))", -0.5, 0.0)
	f.Add("SRID=3857;POINT(1 2)", 1.5, 100.0)
	f.Add(`POINT(1 2)'); DROP TABLE places; --`, 0.0, 1.0)
	f.Add(`'; DROP TABLE places; --`, 1e30, -1.0)
	f.Add("", 0.0, 0.0)

	f.Fuzz(func(t *testing.T, wkt string, x, d float64) {
		stmts, ok := compileWith(t, wkt, x, d)
		if !ok {
			return
		}
		for i, stmt := range stmts {
			if stmt.SQL != baseline[i].SQL {
				t.Fatalf("spatial SQL text varied with input values:\n--- baseline ---\n%s\n--- got ---\n%s", baseline[i].SQL, stmt.SQL)
			}
			if len(stmt.Args) != len(baseline[i].Args) {
				t.Fatalf("parameter count varied: %d != %d", len(stmt.Args), len(baseline[i].Args))
			}
		}
	})
}
