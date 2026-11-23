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

const domainSchema = "domains_e2e"

// setupDomains creates the domains_e2e schema: domains over text, int (one
// layered over another, renamed by @name), bigint, int[], PostGIS geometry
// and citext, used by table columns, an array column, a composite
// attribute and function parameters and results. Everything is dropped
// when the test ends.
func setupDomains(t *testing.T) {
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
	var citextSchema string
	installed := false
	if err := conn.QueryRow(ctx, `SELECT COALESCE((SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'citext'), '')`).Scan(&citextSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, `DROP SCHEMA IF EXISTS `+domainSchema+` CASCADE`)
		if installed {
			_, _ = conn.Exec(ctx, `DROP EXTENSION IF EXISTS citext CASCADE`)
			_, _ = conn.Exec(ctx, `DROP SCHEMA IF EXISTS domains_e2e_ext CASCADE`)
		}
	})
	if citextSchema == "" {
		installed, citextSchema = true, "domains_e2e_ext"
		execAll(t, conn, `CREATE SCHEMA domains_e2e_ext`, `CREATE EXTENSION citext SCHEMA domains_e2e_ext`)
	}
	execAll(t, conn,
		`CREATE SCHEMA `+domainSchema,
		`SET search_path TO `+domainSchema+`, public`,
		`CREATE DOMAIN email AS text CHECK (VALUE ~ '^[^@]+@[^@]+$')`,
		`COMMENT ON DOMAIN email IS 'An email address.'`,
		`CREATE DOMAIN positive_int AS int CHECK (VALUE > 0)`,
		`CREATE DOMAIN small_positive AS positive_int CHECK (VALUE < 100)`,
		`COMMENT ON DOMAIN small_positive IS E'@name Percent\nA percentage.'`,
		`CREATE DOMAIN big_count AS bigint`,
		`CREATE DOMAIN int_list AS int[]`,
		`CREATE DOMAIN spot AS public.geometry(Point,4326)`,
		`CREATE DOMAIN handle AS "`+citextSchema+`".citext`,
		`CREATE TYPE contact AS (email email, rank positive_int)`,
		`CREATE TABLE people (
			id int PRIMARY KEY,
			email email NOT NULL,
			aliases email[],
			score small_positive,
			visits big_count,
			lucky int_list,
			home spot,
			handle handle,
			contact contact
		)`,
		`INSERT INTO people VALUES
			(1, 'ada@example.com', '{ada@x.org,countess@x.org}', 42, 12345678901234, '{3,7}',
			 'SRID=4326;POINT(1 2)', 'Ada', ROW('ada@example.com', 1)),
			(2, 'alan@example.com', '{}', 7, 1, '{}', NULL, 'Alan', NULL)`,
		`CREATE FUNCTION double_score(s small_positive) RETURNS positive_int
			LANGUAGE sql STABLE AS $$ SELECT (s * 2)::domains_e2e.positive_int $$`,
		`CREATE FUNCTION first_alias(a email[]) RETURNS email
			LANGUAGE sql STABLE AS $$ SELECT a[1] $$`,
		`CREATE FUNCTION people_score_twice(p people) RETURNS positive_int
			LANGUAGE sql STABLE AS $$ SELECT (p.score * 2)::domains_e2e.positive_int $$`,
		`CREATE FUNCTION spot_x(s spot) RETURNS float8
			LANGUAGE sql STABLE AS $$ SELECT public.ST_X(s) $$`,
		`RESET search_path`,
	)
}

func domainServer(t *testing.T, scalars bool) string {
	hs := testServer(t, func(c *config.Config) {
		c.Schema.Schemas = []string{domainSchema}
		c.Schema.DomainScalars = scalars
		c.Server.ExposeSchema = true
		c.Filters.IndexedOnly = false
	})
	return hs.URL
}

func sdlOf(t *testing.T, url string) string {
	t.Helper()
	sdl, err := httpGet(url + "/schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	return string(sdl)
}

// TestDomainScalars covers schema.domain_scalars (on by default): one
// custom scalar per domain over a built-in scalar, named after the domain
// (or its @name), described by its comment and constraints, used by
// columns, arrays, composite attributes and function parameters and
// results, reading and writing like the base type, with the base type's
// filter operators; domains over arrays, PostGIS and contrib types keep
// their base type.
func TestDomainScalars(t *testing.T) {
	setupDomains(t)
	url := domainServer(t, true)
	sdl := sdlOf(t, url)
	for _, want := range []string{
		"\"\"\"An email address.\n\nConstraints:\n- `CHECK ((VALUE ~ '^[^@]+@[^@]+$'::text))`\"\"\"\nscalar Email\n",
		"\"\"\"A percentage.\n\nConstraints:\n- `CHECK (((VALUE)::integer < 100))`\n- `CHECK ((VALUE > 0))`\"\"\"\nscalar Percent\n",
		"scalar PositiveInt\n", "scalar BigCount\n",
		"  email: Email!\n", "  aliases: [Email!]\n", "  score: Percent\n", "  visits: BigCount\n",
		"  lucky: [Int!]\n", "  home: GeoJSON\n", "  handle: String\n",
		"  rank: PositiveInt\n", "  scoreTwice: PositiveInt\n",
		"doubleScore(s: Percent): PositiveInt", "firstAlias(a: [Email]): Email", "spotX(s: GeoJSON): Float",
		"  email: StringFilterOps\n", "  score: IntFilterOps\n",
	} {
		if !strings.Contains(sdl, want) {
			t.Errorf("SDL missing %q", want)
		}
	}
	for _, absent := range []string{"scalar IntList", "scalar Spot", "scalar Handle", "scalar SmallPositive", "@name"} {
		if strings.Contains(sdl, absent) {
			t.Errorf("SDL contains %q", absent)
		}
	}

	cases := []struct{ name, query, want string }{
		{"read", `{ personById(id: 1) { email aliases score visits lucky home handle contact { email rank } scoreTwice } }`,
			`{"personById":{"aliases":["ada@x.org","countess@x.org"],"contact":{"email":"ada@example.com","rank":1},"email":"ada@example.com","handle":"Ada","home":{"coordinates":[1,2],"type":"Point"},"lucky":[3,7],"score":42,"scoreTwice":84,"visits":"12345678901234"}}`},
		{"filter with base operators", `{ allPeople(filter: {email: {like: "a%@example.com"}, score: {greaterThan: 10}, handle: {equalTo: "ADA"}}) { nodes { id } } }`,
			`{"allPeople":{"nodes":[{"id":1}]}}`},
		{"array filter", `{ allPeople(filter: {aliases: {contains: ["ada@x.org"]}}) { nodes { id } } }`,
			`{"allPeople":{"nodes":[{"id":1}]}}`},
		{"in list and domain over an array", `{ allPeople(filter: {email: {in: ["alan@example.com", "x@y.z"]}, lucky: {equalTo: []}}) { nodes { id } } }`,
			`{"allPeople":{"nodes":[{"id":2}]}}`},
		{"domain over an array contains", `{ allPeople(filter: {lucky: {contains: [7]}}) { nodes { id } } }`,
			`{"allPeople":{"nodes":[{"id":1}]}}`},
		{"condition", `{ allPeople(condition: {score: 7}) { nodes { id } } }`,
			`{"allPeople":{"nodes":[{"id":2}]}}`},
		{"function arguments and results", `{
			doubleScore(s: 21)
			firstAlias(a: ["x@y.z", "q@r.s"])
			spotX(s: {type: "Point", coordinates: [5, 6]})
		}`, `{"doubleScore":42,"firstAlias":"x@y.z","spotX":5}`},
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

	t.Run("mutations take base type literals", func(t *testing.T) {
		res := post(t, url, `mutation($e: Email!, $s: Percent) {
			createPerson(input: {id: 3, email: $e, aliases: ["c@x.org"], score: $s, visits: "9", lucky: [1]}) { person { email aliases score visits lucky } }
		}`, map[string]any{"e": "cy@example.com", "s": 5})
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["createPerson"]); got != `{"person":{"aliases":["c@x.org"],"email":"cy@example.com","lucky":[1],"score":5,"visits":"9"}}` {
			t.Errorf("createPerson = %s", got)
		}
		res = post(t, url, `mutation { updatePersonById(id: 3, patch: {score: 500}) { person { score } } }`, nil)
		if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "small_positive") {
			t.Errorf("CHECK violation not reported: %+v", res.Errors)
		}
	})
}

// TestDomainScalarsOff checks that schema.domain_scalars: false types
// domains as their base types.
func TestDomainScalarsOff(t *testing.T) {
	setupDomains(t)
	url := domainServer(t, false)
	sdl := sdlOf(t, url)
	for _, want := range []string{
		"  email: String!\n", "  aliases: [String!]\n", "  score: Int\n", "  visits: BigInt\n", "  rank: Int\n",
		"doubleScore(s: Int): Int", "firstAlias(a: [String]): String",
	} {
		if !strings.Contains(sdl, want) {
			t.Errorf("SDL missing %q", want)
		}
	}
	if strings.Contains(sdl, "scalar Email") || strings.Contains(sdl, "scalar Percent") {
		t.Error("domain scalars declared with domain_scalars off")
	}
	res := post(t, url, `{ doubleScore(s: 21) personById(id: 1) { score visits } }`, nil)
	requireNoErrors(t, res)
	b, _ := json.Marshal(res.Data)
	if got := normalize(t, b); got != `{"doubleScore":42,"personById":{"score":42,"visits":"12345678901234"}}` {
		t.Errorf("got %s", got)
	}
}
