package compile_test

import (
	"context"
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
// pg_trgm, unaccent, hstore, ltree, citext, ranges, inet), the codec-typed
// generic comparisons, rank, similarity and vector distance ordering, the
// similarity and distance score fields, the vector and bit distance
// thresholds and vector inputs are covered too, and against
// the aggregates schema for having and relation-aggregate filters, and
// through a custom mutation whose payload compiles to a Then statement.
func FuzzFilter(f *testing.F) {
	built := buildContrib(f)
	aggBuilt := buildAggregates(f)

	const query = `query ($e: String, $tag: [String!], $n: Int!, $h: KeyValueHash, $b: Boolean!, $d: Datetime, $ip: InternetAddress, $ips: [InternetAddress!], $cfg: String, $vec: [Float!], $fl: Float!, $sv: String!, $vv: [Float!]!) {
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
			similarTo: {label: $e, title: $e}
			orderBy: [SEARCH_RANK_DESC, LABEL_SIMILARITY_DESC]
		) { nodes { id searchRank attrs span { start { value inclusive } } labelSimilarity titleWordSimilarity } }
		allEmbeddings(
			filter: {
				embedding: {cosineDistanceLessThan: {vector: $vv, distance: $fl}, l2DistanceLessThan: {vector: $vv, distance: $fl}}
				sparse: {negativeInnerProductLessThan: {vector: $sv, distance: $fl}}
				code: {hammingDistanceLessThan: {vector: "101", distance: $fl}}
			}
			nearVector: {embedding: $vec, half: $vec, sparse: $cfg, code: "011"}
			orderBy: [EMBEDDING_COSINE_DISTANCE_ASC, HALF_L2_DISTANCE_DESC, SPARSE_NEGATIVE_INNER_PRODUCT_ASC, EMBEDDING_L1_DISTANCE_ASC, CODE_HAMMING_DISTANCE_ASC]
			first: 3
		) { nodes { id embedding half sparse embeddingCosineDistance sparseL2Distance codeJaccardDistance } }
		allPlaces(near: {geometry: "POINT(0 0)"}, orderBy: [LOCATION_DISTANCE_ASC]) {
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
					OR: [{max: {id: {equalTo: $n}}}, {average: {authorId: {notEqualTo: $bf}}}]
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
			orderBy: [POSTS_BY_AUTHOR_ID_SUM_ID_DESC]
		) {
			groupedAggregates(groupBy: [CREATED_AT_TRUNCATED_TO_DAY], having: {max: {createdAt: {greaterThan: $d}}}) { keys }
		}
	}`
	const mutationQuery = `mutation ($e: String, $n: Int!, $vec: [Float!], $cfg: String) {
		createEmbedding(input: {embedding: $vec, half: $vec, sparse: $cfg}) { embedding { id embedding } }
		publishPost(input: {postId: $n, clientMutationId: $e}) {
			clientMutationId
			post { title author { email } }
			query { allUsers(filter: {email: {startsWith: $e}}) { totalCount } }
		}
	}`
	type target struct {
		built *schema.Built
		op    *ast.OperationDefinition
	}
	var targets []target
	for _, tc := range []struct {
		built *schema.Built
		query string
	}{{built, query}, {aggBuilt, aggQuery}, {built, mutationQuery}} {
		doc, perr := parser.ParseQuery(&ast.Source{Input: tc.query})
		if perr != nil {
			f.Fatal(perr)
		}
		if errs := validator.ValidateWithRules(tc.built.Schema, doc, nil); len(errs) > 0 {
			f.Fatal(errs)
		}
		targets = append(targets, target{tc.built, doc.Operations[0]})
	}

	compileWith := func(t testing.TB, email, tag string, n int) []*compile.Statement {
		t.Helper()
		var out []*compile.Statement
		for _, tg := range targets {
			vars, verr := validator.VariableValues(tg.built.Schema, tg.op, map[string]any{
				"e": email, "tag": []any{tag}, "n": n, "h": map[string]any{tag: email}, "b": n%2 == 0,
				"d": email, "ip": email, "ips": []any{tag}, "cfg": "c" + email, "big": tag, "bigs": []any{tag}, "bf": email,
				"vec": []any{float64(n), 0.5, -float64(n)}, "fl": float64(n) / 4, "sv": "c" + email, "vv": []any{0.5, float64(n)},
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
	if baseline[len(baseline)-1].Then == nil {
		f.Fatal("the custom mutation compiled without a Then statement")
	}

	f.Add("a@example.com", "admin", 5)
	f.Add(`'; DROP TABLE users; --`, `"]}`, -1)
	f.Add("%_\\", "'", 0)
	f.Add("", "", 1<<30)

	f.Fuzz(func(t *testing.T, email, tag string, n int) {
		for i, stmt := range compileWith(t, email, tag, n) {
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
func FuzzSpatial(f *testing.F) {
	built := build(f)
	compiler := compile.New(built)

	const query = `query ($wkt: GeoJSON!, $x: Float!, $d: Float!) {
		allPlaces(filter: {
			location: {
				intersects: $wkt
				bboxIntersects: {minX: $x, minY: $x, maxX: $x, maxY: $x}
				dwithin: {geometry: $wkt, distance: $d}
				bboxLeftOf: $wkt
				bboxIntersectsND: $wkt
				bboxIntersects2D: $wkt
			}
		}, near: {geometry: $wkt}, orderBy: [AREA_DISTANCE_ASC]) { nodes { id locationDistance areaDistance } }
	}`
	doc, perr := parser.ParseQuery(&ast.Source{Input: query})
	if perr != nil {
		f.Fatal(perr)
	}
	if errs := validator.ValidateWithRules(built.Schema, doc, nil); len(errs) > 0 {
		f.Fatal(errs)
	}
	op := doc.Operations[0]

	compileWith := func(t testing.TB, wkt string, x, d float64) (*compile.Statement, bool) {
		t.Helper()
		vars, verr := validator.VariableValues(built.Schema, op, map[string]any{
			"wkt": wkt, "x": x, "d": d,
		})
		if verr != nil {
			return nil, false
		}
		stmt, err := compiler.Compile(context.Background(), &compile.Request{
			Field:    op.SelectionSet[0].(*ast.Field),
			Vars:     vars,
			Built:    built,
			MaxDepth: 15,
		})
		if err != nil {
			// Malformed geometry is rejected at compile time by design; only a
			// successful compile is subject to the invariant.
			return nil, false
		}
		return stmt, true
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
		stmt, ok := compileWith(t, wkt, x, d)
		if !ok {
			return
		}
		if stmt.SQL != baseline.SQL {
			t.Fatalf("spatial SQL text varied with input values:\n--- baseline ---\n%s\n--- got ---\n%s", baseline.SQL, stmt.SQL)
		}
		if len(stmt.Args) != len(baseline.Args) {
			t.Fatalf("parameter count varied: %d != %d", len(stmt.Args), len(baseline.Args))
		}
	})
}
