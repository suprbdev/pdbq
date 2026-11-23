package e2e

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbq/internal/config"
)

const intervalSchema = "interval_e2e"

// setupIntervals creates the interval_e2e schema: a table with interval and
// interval[] columns, a composite with interval attributes, and functions
// taking and returning intervals. Everything is dropped when the test ends.
func setupIntervals(t *testing.T) {
	t.Helper()
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	t.Cleanup(func() { _, _ = conn.Exec(ctx, `DROP SCHEMA IF EXISTS `+intervalSchema+` CASCADE`) })
	execAll(t, conn,
		`CREATE SCHEMA `+intervalSchema,
		`SET search_path TO `+intervalSchema+`, public`,
		`CREATE TYPE timing AS (label text, span interval, spans interval[])`,
		`CREATE TABLE durations (
			id int PRIMARY KEY,
			span interval,
			spans interval[],
			timing timing
		)`,
		`INSERT INTO durations VALUES
			(1, '-1 year -2 mons +3 days -04:05:06.789', ARRAY['1 day', '90 minutes']::interval[], ROW('a', '2 hours', ARRAY['1 mon'])::timing),
			(2, '14 months', '{}', NULL),
			(3, '100 hours', NULL, NULL),
			(4, NULL, NULL, NULL)`,
		`CREATE FUNCTION double_span(s interval) RETURNS interval LANGUAGE sql STABLE AS $$ SELECT s * 2 $$`,
		`CREATE FUNCTION span_list(s interval[]) RETURNS interval[] LANGUAGE sql STABLE AS $$ SELECT s $$`,
		`CREATE FUNCTION durations_twice(d durations) RETURNS interval LANGUAGE sql STABLE AS $$ SELECT d.span * 2 $$`,
		`RESET search_path`,
	)
}

func intervalServer(t *testing.T) string {
	return testServer(t, func(c *config.Config) {
		c.Schema.Schemas = []string{intervalSchema}
		c.Filters.IndexedOnly = false
		c.Aggregates.Enabled = true
	}).URL
}

func TestInterval(t *testing.T) {
	setupIntervals(t)
	url := intervalServer(t)

	const all = `{ seconds minutes hours days months years }`
	cases := []struct{ name, query, want string }{
		{"decomposition", `{ durationById(id: 1) { span ` + all + ` } d2: durationById(id: 2) { span ` + all + ` } d3: durationById(id: 3) { span ` + all + ` } d4: durationById(id: 4) { span { years } } }`,
			`{"d2":{"span":{"days":0,"hours":0,"minutes":0,"months":2,"seconds":0,"years":1}},"d3":{"span":{"days":0,"hours":100,"minutes":0,"months":0,"seconds":0,"years":0}},"d4":{"span":null},"durationById":{"span":{"days":3,"hours":-4,"minutes":-5,"months":-2,"seconds":-6.789,"years":-1}}}`},
		{"arrays and composites", `{ durationById(id: 1) { spans { days minutes hours } timing { label span { hours } spans { months } } twice { hours minutes } } }`,
			`{"durationById":{"spans":[{"days":1,"hours":0,"minutes":0},{"days":0,"hours":1,"minutes":30}],"timing":{"label":"a","span":{"hours":2},"spans":[{"months":1}]},"twice":{"hours":-8,"minutes":-10}}}`},
		{"empty and null arrays", `{ a: durationById(id: 2) { spans { days } } b: durationById(id: 3) { spans { days } } }`,
			`{"a":{"spans":[]},"b":{"spans":null}}`},
		{"comparison", `{ allDurations(filter: {span: {greaterThan: {hours: 50}}}) { nodes { id } } }`,
			`{"allDurations":{"nodes":[{"id":2},{"id":3}]}}`},
		{"equality and in", `{ allDurations(filter: {or: [{span: {equalTo: {years: 1, months: 2}}}, {span: {in: [{hours: 100}], notIn: [{days: 1}]}}]}) { nodes { id } } }`,
			`{"allDurations":{"nodes":[{"id":2},{"id":3}]}}`},
		{"array filter", `{ allDurations(filter: {spans: {contains: [{days: 1}]}}) { nodes { id } } }`,
			`{"allDurations":{"nodes":[{"id":1}]}}`},
		{"condition and order", `{ allDurations(condition: {span: {hours: 100}}, orderBy: SPAN_DESC) { nodes { id } } }`,
			`{"allDurations":{"nodes":[{"id":3}]}}`},
		{"order", `{ allDurations(orderBy: SPAN_DESC) { nodes { id } } }`,
			`{"allDurations":{"nodes":[{"id":2},{"id":3},{"id":1},{"id":4}]}}`},
		{"function arguments and results", `{ doubleSpan(s: {hours: 1, minutes: 30, seconds: 1.5}) { hours minutes seconds } spanList(s: [{days: 1}, {hours: 2}]) { days hours } }`,
			`{"doubleSpan":{"hours":3,"minutes":0,"seconds":3},"spanList":[{"days":1,"hours":0},{"days":0,"hours":2}]}`},
		{"aggregates", `{ allDurations(filter: {id: {in: [2, 3]}}) { aggregates { min { span { hours } } max { span { months years hours } } sum { span { hours months years } } } } }`,
			`{"allDurations":{"aggregates":{"max":{"span":{"hours":0,"months":2,"years":1}},"min":{"span":{"hours":100}},"sum":{"span":{"hours":100,"months":2,"years":1}}}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := post(t, url, tc.query, nil)
			requireNoErrors(t, res)
			b, _ := json.Marshal(res.Data)
			if got := normalize(t, b); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}

	t.Run("round trip", func(t *testing.T) {
		res := post(t, url, `mutation($t: TimingInput) {
			createDuration(input: {id: 10, span: {hours: 1, minutes: 90, seconds: 0.25}, spans: [{days: 2}, {months: 14}], timing: $t}) {
				duration { span { hours minutes seconds } spans { days months years } timing { label span { days } spans { hours } } }
			}
		}`, map[string]any{"t": map[string]any{"label": "t", "span": map[string]any{"days": 3}, "spans": []any{map[string]any{"hours": 4}}}})
		requireNoErrors(t, res)
		want := `{"duration":{"span":{"hours":2,"minutes":30,"seconds":0.25},"spans":[{"days":2,"months":0,"years":0},{"days":0,"months":2,"years":1}],"timing":{"label":"t","span":{"days":3},"spans":[{"hours":4}]}}}`
		if got := normalize(t, res.Data["createDuration"]); got != want {
			t.Errorf("createDuration = %s\nwant %s", got, want)
		}
		res = post(t, url, `mutation { updateDurationById(id: 10, patch: {span: {years: -1, seconds: -1.5}}) { duration { span { years seconds } } } }`, nil)
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["updateDurationById"]); got != `{"duration":{"span":{"seconds":-1.5,"years":-1}}}` {
			t.Errorf("updateDurationById = %s", got)
		}
		res = post(t, url, `mutation { deleteDurationById(id: 10) { duration { id } } }`, nil)
		requireNoErrors(t, res)
	})

	t.Run("schema", func(t *testing.T) {
		sdl := sdlOfServer(t)
		for _, want := range []string{"type Interval {", "input IntervalInput {", "  span: Interval\n", "  spans: [Interval!]\n", "doubleSpan(s: IntervalInput): Interval"} {
			if !strings.Contains(sdl, want) {
				t.Errorf("SDL missing %q", want)
			}
		}
	})
}

func sdlOfServer(t *testing.T) string {
	hs := testServer(t, func(c *config.Config) {
		c.Schema.Schemas = []string{intervalSchema}
	})
	return sdlOf(t, hs.URL)
}
