package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/config"
)

// aggregatesServer serves the fixture with the aggregates plugin enabled;
// users.created_at is made orderable so its timestamp group-by derivatives
// exist under filters.indexed_only.
func aggregatesServer(t *testing.T, mutate func(*config.Config)) string {
	t.Helper()
	return testServer(t, func(cfg *config.Config) {
		cfg.Plugins.Settings = map[string]map[string]any{"aggregates": {"enabled": true, "spatial_union_collect": true}}
		cfg.Filters.AllowColumns = map[string][]string{"public.users": {"created_at"}}
		if mutate != nil {
			mutate(cfg)
		}
	}).URL
}

// num parses a JSON number or numeric string (BigInt/BigFloat) result.
func num(t *testing.T, v any) float64 {
	t.Helper()
	switch n := v.(type) {
	case float64:
		return n
	case string:
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			t.Fatalf("not a number: %q", n)
		}
		return f
	}
	t.Fatalf("not a number: %#v", v)
	return 0
}

// dig walks a decoded JSON value along object keys and list indexes.
func dig(t *testing.T, v any, path ...string) any {
	t.Helper()
	for _, p := range path {
		switch c := v.(type) {
		case map[string]any:
			v = c[p]
		case []any:
			i, err := strconv.Atoi(p)
			if err != nil || i >= len(c) {
				t.Fatalf("bad index %q into %v", p, c)
			}
			v = c[i]
		default:
			t.Fatalf("cannot take %q of %#v", p, v)
		}
	}
	return v
}

func decode(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var v any
	mustUnmarshal(t, raw, &v)
	return v
}

func TestAggregates(t *testing.T) {
	url := aggregatesServer(t, nil)

	t.Run("connection aggregates ignore pagination", func(t *testing.T) {
		res := post(t, url, `{
			allPosts(first: 1, filter: {id: {lessThanOrEqualTo: 4}}) {
				totalCount
				nodes { id }
				aggregates {
					sum { id authorId }
					distinctCount { authorId title }
					min { id } max { id }
					average { id }
					stddevSample { id } stddevPopulation { id }
					varianceSample { id } variancePopulation { id }
				}
			}
		}`, nil)
		requireNoErrors(t, res)
		agg := dig(t, decode(t, res.Data["allPosts"]), "aggregates")
		for _, c := range []struct {
			path []string
			want float64
		}{
			{[]string{"sum", "id"}, 10},
			{[]string{"sum", "authorId"}, 7},
			{[]string{"distinctCount", "authorId"}, 3},
			{[]string{"distinctCount", "title"}, 4},
			{[]string{"min", "id"}, 1},
			{[]string{"max", "id"}, 4},
			{[]string{"average", "id"}, 2.5},
			{[]string{"stddevSample", "id"}, math.Sqrt(5.0 / 3)},
			{[]string{"stddevPopulation", "id"}, math.Sqrt(1.25)},
			{[]string{"varianceSample", "id"}, 5.0 / 3},
			{[]string{"variancePopulation", "id"}, 1.25},
		} {
			if got := num(t, dig(t, agg, c.path...)); math.Abs(got-c.want) > 1e-9 {
				t.Errorf("%s = %v, want %v", strings.Join(c.path, "."), got, c.want)
			}
		}
		if _, ok := dig(t, agg, "sum", "id").(string); !ok {
			t.Errorf("sum of an int column must serialize as a BigInt string, got %#v", dig(t, agg, "sum", "id"))
		}
	})

	t.Run("nested relation aggregates", func(t *testing.T) {
		res := post(t, url, `{
			allUsers(orderBy: [ID_ASC], filter: {id: {lessThanOrEqualTo: 3}}) {
				nodes { id postsByAuthorId { totalCount aggregates { sum { id } } } }
			}
		}`, nil)
		requireNoErrors(t, res)
		want := normalizeStr(t, `{"nodes": [
			{"id": 1, "postsByAuthorId": {"totalCount": 2, "aggregates": {"sum": {"id": "3"}}}},
			{"id": 2, "postsByAuthorId": {"totalCount": 1, "aggregates": {"sum": {"id": "3"}}}},
			{"id": 3, "postsByAuthorId": {"totalCount": 1, "aggregates": {"sum": {"id": "4"}}}}]}`)
		if got := normalize(t, res.Data["allUsers"]); got != want {
			t.Errorf("got %s want %s", got, want)
		}
	})

	t.Run("grouped aggregates with having", func(t *testing.T) {
		res := post(t, url, `{
			allPosts(filter: {id: {lessThanOrEqualTo: 4}}) {
				all: groupedAggregates(groupBy: [AUTHOR_ID]) { keys sum { id } }
				big: groupedAggregates(groupBy: [AUTHOR_ID], having: {sum: {id: {greaterThan: "3"}}}) { keys }
				either: groupedAggregates(groupBy: [AUTHOR_ID], having: {OR: [{max: {id: {equalTo: 1}}}, {max: {id: {equalTo: 2}}}]}) { keys }
				any: groupedAggregates(groupBy: [AUTHOR_ID], having: {OR: [{}, {max: {id: {equalTo: 2}}}]}) { keys }
			}
		}`, nil)
		requireNoErrors(t, res)
		want := normalizeStr(t, `{
			"all": [{"keys": ["1"], "sum": {"id": "3"}}, {"keys": ["2"], "sum": {"id": "3"}}, {"keys": ["3"], "sum": {"id": "4"}}],
			"big": [{"keys": ["3"]}],
			"either": [{"keys": ["1"]}],
			"any": [{"keys": ["1"]}, {"keys": ["2"]}, {"keys": ["3"]}]}`)
		if got := normalize(t, res.Data["allPosts"]); got != want {
			t.Errorf("got %s want %s", got, want)
		}
	})

	t.Run("group by timestamp derivative and enum", func(t *testing.T) {
		res := post(t, url, `{
			allUsers(filter: {id: {lessThanOrEqualTo: 3}}) {
				byDay: groupedAggregates(groupBy: [CREATED_AT_TRUNCATED_TO_DAY]) { keys distinctCount { id } }
				byMood: groupedAggregates(groupBy: [MOOD]) { keys distinctCount { id } }
				byMoods: groupedAggregates(groupBy: [MOODS], having: {distinctCount: {id: {greaterThan: "0"}}}) { keys }
				bySettings: groupedAggregates(groupBy: [SETTINGS]) { keys }
			}
		}`, nil)
		requireNoErrors(t, res)
		v := decode(t, res.Data["allUsers"])
		days, _ := dig(t, v, "byDay").([]any)
		if len(days) != 1 || num(t, dig(t, days[0], "distinctCount", "id")) != 3 {
			t.Fatalf("all fixture users share a creation day, got %v", days)
		}
		if key, _ := dig(t, days[0], "keys", "0").(string); !strings.Contains(key, "T00:00:00") {
			t.Errorf("day key should be an ISO timestamp truncated to midnight, got %q", key)
		}
		if got, want := normalize(t, mustJSON(t, dig(t, v, "byMood"))), normalizeStr(t, `[
			{"keys": ["OK"], "distinctCount": {"id": "1"}},
			{"keys": ["HAPPY"], "distinctCount": {"id": "2"}}]`); got != want {
			t.Errorf("byMood: got %s want %s", got, want)
		}
		if got, want := normalize(t, mustJSON(t, dig(t, v, "byMoods"))), normalizeStr(t, `[
			{"keys": ["[\"SAD\", \"HAPPY\"]"]}, {"keys": ["[\"OK\"]"]}, {"keys": [null]}]`); got != want {
			t.Errorf("byMoods: got %s want %s", got, want)
		}
		if got, want := normalize(t, mustJSON(t, dig(t, v, "bySettings"))), normalizeStr(t, `[
			{"keys": ["{\"theme\": \"dark\"}"]}, {"keys": ["{\"theme\": \"light\"}"]}, {"keys": [null]}]`); got != want {
			t.Errorf("bySettings: got %s want %s", got, want)
		}
	})

	t.Run("order by relation aggregates", func(t *testing.T) {
		ids := fetchIDs(t, url, `{allUsers(filter: {id: {lessThanOrEqualTo: 3}}, orderBy: [POSTS_BY_AUTHOR_ID_COUNT_DESC, ID_ASC]) { nodes { id } }}`)
		if !equalInts(ids, []int{1, 2, 3}) {
			t.Errorf("count desc: got %v", ids)
		}
		ids = fetchIDs(t, url, `{allUsers(filter: {id: {lessThanOrEqualTo: 3}}, orderBy: [POSTS_BY_AUTHOR_ID_SUM_ID_DESC, ID_DESC]) { nodes { id } }}`)
		if !equalInts(ids, []int{3, 2, 1}) {
			t.Errorf("sum desc: got %v", ids)
		}
		res := post(t, url, `{allUsers(first: 1, filter: {id: {lessThanOrEqualTo: 3}}, orderBy: [POSTS_BY_AUTHOR_ID_MAX_ID_DESC]) { pageInfo { endCursor } }}`, nil)
		requireNoErrors(t, res)
		cursor, _ := dig(t, decode(t, res.Data["allUsers"]), "pageInfo", "endCursor").(string)
		ids = fetchIDs(t, url, `{allUsers(first: 5, after: "`+cursor+`", filter: {id: {lessThanOrEqualTo: 3}}, orderBy: [POSTS_BY_AUTHOR_ID_MAX_ID_DESC]) { nodes { id } }}`)
		if !equalInts(ids, []int{2, 1}) {
			t.Errorf("keyset after the max-id leader: got %v", ids)
		}
	})

	t.Run("filter by relation aggregates", func(t *testing.T) {
		ids := fetchIDs(t, url, `{allUsers(orderBy: [ID_ASC], filter: {postsByAuthorId: {aggregates: {sum: {id: {greaterThan: "3"}}}}}) { nodes { id } }}`)
		if !equalInts(ids, []int{3}) {
			t.Errorf("sum > 3: got %v", ids)
		}
		ids = fetchIDs(t, url, `{allUsers(orderBy: [ID_ASC], filter: {postsByAuthorId: {aggregates: {
			filter: {title: {startsWith: "N"}}
			distinctCount: {id: {equalTo: "1"}}
		}}}) { nodes { id } }}`)
		if !equalInts(ids, []int{1}) {
			t.Errorf("filtered distinct count: got %v", ids)
		}
		empty := `postsByAuthorId: {aggregates: {filter: {title: {equalTo: "none"}}, max: {id: {greaterThan: 0}}}}`
		if ids = fetchIDs(t, url, `{allUsers(orderBy: [ID_ASC], filter: {id: {lessThanOrEqualTo: 3}, `+empty+`}) { nodes { id } }}`); len(ids) != 0 {
			t.Errorf("max over no rows must not match: got %v", ids)
		}
		if ids = fetchIDs(t, url, `{allUsers(orderBy: [ID_ASC], filter: {id: {lessThanOrEqualTo: 3}, not: {`+empty+`}}) { nodes { id } }}`); !equalInts(ids, []int{1, 2, 3}) {
			t.Errorf("not must complement a NULL aggregate comparison: got %v", ids)
		}
	})

	t.Run("spatial aggregates", func(t *testing.T) {
		res := post(t, url, `{
			allPlaces(filter: {ownerId: {equalTo: 1}}) {
				aggregates {
					stExtent { location }
					stCentroid { location }
					stCollect { location }
					stUnion { location }
					distinctCount { location }
				}
			}
		}`, nil)
		requireNoErrors(t, res)
		agg := dig(t, decode(t, res.Data["allPlaces"]), "aggregates")
		if typ := dig(t, agg, "stExtent", "location", "type"); typ != "Polygon" {
			t.Errorf("extent should be a polygon, got %v", typ)
		}
		ring, _ := dig(t, agg, "stExtent", "location", "coordinates", "0").([]any)
		minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
		for _, pt := range ring {
			x, y := num(t, dig(t, pt, "0")), num(t, dig(t, pt, "1"))
			minX, maxX, minY, maxY = math.Min(minX, x), math.Max(maxX, x), math.Min(minY, y), math.Max(maxY, y)
		}
		if math.Abs(minX+0.1281) > 1e-6 || math.Abs(maxX+0.1270) > 1e-6 || math.Abs(minY-51.5080) > 1e-6 || math.Abs(maxY-51.5194) > 1e-6 {
			t.Errorf("extent bounds = (%v %v, %v %v)", minX, minY, maxX, maxY)
		}
		cx, cy := num(t, dig(t, agg, "stCentroid", "location", "coordinates", "0")), num(t, dig(t, agg, "stCentroid", "location", "coordinates", "1"))
		if math.Abs(cx+0.12755) > 1e-6 || math.Abs(cy-51.5137) > 1e-6 {
			t.Errorf("centroid = (%v, %v)", cx, cy)
		}
		if typ := dig(t, agg, "stCollect", "location", "type"); typ != "MultiPoint" {
			t.Errorf("collect should be a MultiPoint, got %v", typ)
		}
		if typ := dig(t, agg, "stUnion", "location", "type"); typ != "MultiPoint" {
			t.Errorf("union should be a MultiPoint, got %v", typ)
		}
		if n := num(t, dig(t, agg, "distinctCount", "location")); n != 2 {
			t.Errorf("distinct locations = %v, want 2", n)
		}
	})

	t.Run("or with an empty branch matches everything", func(t *testing.T) {
		ids := fetchIDs(t, url, `{allUsers(orderBy: [ID_ASC], filter: {id: {lessThanOrEqualTo: 3}, or: [{}, {email: {equalTo: "nobody"}}]}) { nodes { id } }}`)
		if !equalInts(ids, []int{1, 2, 3}) {
			t.Errorf("got %v", ids)
		}
	})

	t.Run("distinctOn is rejected", func(t *testing.T) {
		res := post(t, url, `{allPosts(distinctOn: [AUTHOR_ID]) { aggregates { sum { id } } }}`, nil)
		if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "distinctOn") {
			t.Errorf("want a distinctOn error, got %+v", res.Errors)
		}
	})
}

func TestAggregatesCostLimit(t *testing.T) {
	url := aggregatesServer(t, func(cfg *config.Config) { cfg.Server.MaxCost = 20 })
	res := post(t, url, `{allPosts(first: 1) { aggregates { sum { id } } }}`, nil)
	requireNoErrors(t, res)
	res = post(t, url, `{allPosts(first: 1) { groupedAggregates(groupBy: [AUTHOR_ID]) { keys } }}`, nil)
	if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "cost") {
		t.Errorf("grouped aggregates must exceed max_cost 20, got %+v", res.Errors)
	}
}

func TestAggregatesRLS(t *testing.T) {
	url := aggregatesServer(t, func(cfg *config.Config) {
		cfg.RLS.Enabled = true
		cfg.RLS.Auth.Mode = "headers"
		cfg.RLS.RoleClaim = "role"
	})
	query := func(headers map[string]string) float64 {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"query": `{allPosts(filter: {id: {lessThanOrEqualTo: 4}}) { aggregates { distinctCount { id } } }}`})
		req, err := http.NewRequest("POST", url+"/graphql", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		var out gqlResp
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("bad response: %s", raw)
		}
		requireNoErrors(t, out)
		return num(t, dig(t, decode(t, out.Data["allPosts"]), "aggregates", "distinctCount", "id"))
	}
	if n := query(nil); n != 3 {
		t.Errorf("anonymous aggregates should see 3 published posts, got %v", n)
	}
	if n := query(map[string]string{"X-Pdbq-Claim-Role": "app_user", "X-Pdbq-Claim-User-Id": "1"}); n != 4 {
		t.Errorf("author 1 aggregates should include the own draft (4), got %v", n)
	}
}

// TestAggregatesOptIn covers the default-off switch: without settings the
// schema has no aggregates, and an @aggregates table comment opts one table
// in.
func TestAggregatesOptIn(t *testing.T) {
	hasType := func(url, name string) bool {
		t.Helper()
		res := post(t, url, `query ($n: String!) { __type(name: $n) { name } }`, map[string]any{"n": name})
		requireNoErrors(t, res)
		return string(res.Data["__type"]) != "null"
	}
	url := testServer(t, nil).URL
	if hasType(url, "PostAggregates") {
		t.Fatal("aggregates must be off by default")
	}

	conn := testConn(t)
	execAll(t, conn, `COMMENT ON TABLE posts IS '@aggregates'`)
	t.Cleanup(func() { execAll(t, conn, `COMMENT ON TABLE posts IS NULL`) })
	url = testServer(t, nil).URL
	if !hasType(url, "PostAggregates") || hasType(url, "UserAggregates") {
		t.Fatal("@aggregates must opt in exactly the tagged table")
	}
	res := post(t, url, `{allUsers(orderBy: [POSTS_BY_AUTHOR_ID_COUNT_DESC, ID_ASC], first: 1) { nodes { id } }}`, nil)
	requireNoErrors(t, res)

	url = testServer(t, func(cfg *config.Config) { cfg.Plugins.Disabled = append(cfg.Plugins.Disabled, "aggregates") }).URL
	if hasType(url, "PostAggregates") {
		t.Fatal("plugins.disabled must win over the @aggregates tag")
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
