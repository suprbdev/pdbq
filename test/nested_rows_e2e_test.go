package e2e

import (
	"context"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
)

const nestedRowsSchema = "nestrow_e2e"

// nestedRowLeaks are values only hidden columns and the omitted table hold:
// docs.secret (@omit), docs.rast (raster) and every column of vault (@omit).
var nestedRowLeaks = []string{"TOPSECRET", "vault note", "[9,9]", `"rast"`, `"secret"`, `"secret_geom"`}

// setupNestedRows creates nestrow_e2e: docs with an @omit column, a column
// hidden from the public contract and a raster, the fully omitted vault,
// composites holding rows of both (and one nesting those), functions
// returning rows inside arrays, composites and RETURNS TABLE columns, and
// holder, a table whose columns hold all of these.
func setupNestedRows(t *testing.T) {
	t.Helper()
	requireExtensions(t, "postgis_raster")
	conn := testConn(t)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+nestedRowsSchema+` CASCADE`)
	})
	execAll(t, conn,
		`DROP SCHEMA IF EXISTS `+nestedRowsSchema+` CASCADE`,
		`CREATE SCHEMA `+nestedRowsSchema,
		`SET search_path TO `+nestedRowsSchema+`, public`,
		`CREATE TABLE docs (id int PRIMARY KEY, owner int, title text, secret text, rast raster)`,
		`COMMENT ON COLUMN docs.secret IS '@omit'`,
		`COMMENT ON COLUMN docs.title IS '@behavior -contract:public'`,
		`CREATE TABLE vault (id int PRIMARY KEY, doc_id int REFERENCES docs (id), secret_geom geometry(Point, 4326), note text)`,
		`COMMENT ON TABLE vault IS '@omit'`,
		`INSERT INTO docs VALUES
			(1, 1, 'title 1', 'TOPSECRET password1', ST_AddBand(ST_MakeEmptyRaster(2, 2, 0, 2, 1, -1, 0, 0, 4326), '8BUI'::text, 1, NULL)),
			(2, 2, 'title 2', 'TOPSECRET password2', NULL)`,
		`INSERT INTO vault VALUES (1, 1, 'SRID=4326;POINT(9 9)', 'vault note')`,

		`CREATE TYPE vaultbox AS (label text, v vault)`,
		`CREATE TYPE docbox AS (label text, d docs, r raster)`,
		`CREATE TYPE crate AS (vb vaultbox, dbs docbox[])`,
		`CREATE FUNCTION doc_arr() RETURNS docs[] STABLE LANGUAGE sql SET search_path FROM CURRENT AS $$ SELECT array_agg(d ORDER BY d.id) FROM docs d $$`,
		`CREATE FUNCTION doc_tab() RETURNS TABLE (n int, d docs) STABLE LANGUAGE sql SET search_path FROM CURRENT AS $$ SELECT d.id, d FROM docs d ORDER BY d.id $$`,
		`CREATE FUNCTION doc_box() RETURNS SETOF docbox STABLE LANGUAGE sql SET search_path FROM CURRENT AS $$ SELECT 'x', d, d.rast FROM docs d ORDER BY d.id $$`,
		`CREATE FUNCTION vault_box() RETURNS SETOF vaultbox STABLE LANGUAGE sql SET search_path FROM CURRENT AS $$ SELECT 'x', v FROM vault v $$`,
		`CREATE FUNCTION vault_rows() RETURNS SETOF vault STABLE LANGUAGE sql SET search_path FROM CURRENT AS $$ SELECT * FROM vault $$`,
		`CREATE FUNCTION vault_arr() RETURNS vault[] STABLE LANGUAGE sql SET search_path FROM CURRENT AS $$ SELECT array_agg(v) FROM vault v $$`,
		`CREATE FUNCTION docs_selfarr(r docs) RETURNS docs[] STABLE LANGUAGE sql SET search_path FROM CURRENT AS $$ SELECT ARRAY[r] $$`,
		`CREATE FUNCTION grab_docs() RETURNS docs[] VOLATILE LANGUAGE sql SET search_path FROM CURRENT AS $$ SELECT array_agg(d ORDER BY d.id) FROM docs d $$`,
		`CREATE FUNCTION grab_box() RETURNS docbox VOLATILE LANGUAGE sql SET search_path FROM CURRENT AS $$ SELECT 'g', d, d.rast FROM docs d WHERE d.id = 1 $$`,

		`CREATE TABLE holder (id int PRIMARY KEY, box vaultbox, dbox docbox, darr docs[], drow docs, crate crate)`,
		`INSERT INTO holder
			SELECT 1, ROW('x', v)::vaultbox, ROW('y', d, d.rast)::docbox,
				ARRAY(SELECT dd FROM docs dd ORDER BY dd.id), d2,
				ROW(ROW('x', v)::vaultbox, ARRAY[ROW('z', d, d.rast)::docbox])::crate
			FROM vault v, docs d, docs d2 WHERE v.id = 1 AND d.id = 1 AND d2.id = 2`,
		`RESET search_path`,
	)
}

func nestedRowsConfig(cfg *config.Config) {
	cfg.Schema.Schemas = []string{nestedRowsSchema}
	cfg.Filters.IndexedOnly = false
}

// nestedRowsContracts serves nestrow_e2e with an internal contract at / and
// a public one at /public.
func nestedRowsContracts(t *testing.T) string {
	t.Helper()
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	cfg := config.DefaultConfig()
	cfg.Database.URL = url
	noIdentity(&cfg)
	cfg.Errors.Detail = "dev"
	nestedRowsConfig(&cfg)
	cfg.Contracts = []config.Contract{{Name: "internal", Path: "/"}, {Name: "public"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app := pdbq.New(cfg)
	t.Cleanup(app.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, err := app.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)
	return hs.URL
}

// requireNoLeak runs query and fails on errors or on any hidden value in
// the response.
func requireNoLeak(t *testing.T, url, query string) gqlResp {
	t.Helper()
	res := post(t, url, query, nil)
	requireNoErrors(t, res)
	for root, raw := range res.Data {
		for _, leak := range nestedRowLeaks {
			if strings.Contains(string(raw), leak) {
				t.Errorf("%s: %s leaks %s: %s", query, root, leak, raw)
			}
		}
	}
	return res
}

// TestNestedRowValues reproduces rows nested in arrays, composite
// attributes, RETURNS TABLE columns and table columns being rendered whole,
// with @omit and contract-hidden columns, raster bytes and the rows of an
// omitted table, and checks they render through the visible columns only.
func TestNestedRowValues(t *testing.T) {
	setupNestedRows(t)
	url := testServer(t, nestedRowsConfig).URL

	t.Run("schema", func(t *testing.T) {
		sdl := getSDL(t, url)
		for _, want := range []string{
			"docArr: [DocRow!]",
			"selfarr: [DocRow!]",
			"darr: [DocRow!]",
			"drow: DocRow",
		} {
			if !strings.Contains(sdl, want) {
				t.Errorf("SDL lacks %q", want)
			}
		}
		for typ, hidden := range map[string][]string{
			"DocRow":       {"secret", "rast"},
			"Docbox":       {"r"},
			"Vaultbox":     {"v"},
			"Crate":        {},
			"DocTabRecord": {},
		} {
			body := regexp.MustCompile(`(?s)type ` + typ + ` \{(.*?)\n\}`).FindStringSubmatch(sdl)
			if body == nil {
				t.Errorf("SDL lacks type %s", typ)
				continue
			}
			for _, f := range hidden {
				if regexp.MustCompile(`(?m)^\s+` + f + `\b`).MatchString(body[1]) {
					t.Errorf("type %s exposes %s", typ, f)
				}
			}
		}
		for _, absent := range []string{"vaultRows", "vaultArr", "VaultRow", "secretGeom"} {
			if strings.Contains(sdl, absent) {
				t.Errorf("SDL mentions %s", absent)
			}
		}
	})

	t.Run("nested rows render visible columns", func(t *testing.T) {
		res := requireNoLeak(t, url, `{ docArr { __typename id owner title } }`)
		if got := normalize(t, res.Data["docArr"]); got != `[{"__typename":"DocRow","id":1,"owner":1,"title":"title 1"},{"__typename":"DocRow","id":2,"owner":2,"title":"title 2"}]` {
			t.Errorf("docArr = %s", got)
		}
		res = requireNoLeak(t, url, `{ docTab { n d { id title } } docBox { label d { id } } vaultBox { label } }`)
		if got := normalize(t, res.Data["docTab"]); got != `[{"d":{"id":1,"title":"title 1"},"n":1},{"d":{"id":2,"title":"title 2"},"n":2}]` {
			t.Errorf("docTab = %s", got)
		}
		if got := normalize(t, res.Data["vaultBox"]); got != `[{"label":"x"}]` {
			t.Errorf("vaultBox = %s", got)
		}
		res = requireNoLeak(t, url, `{ allDocs(orderBy: ID_ASC) { nodes { selfarr { id title } } } }`)
		if got := normalize(t, res.Data["allDocs"]); got != `{"nodes":[{"selfarr":[{"id":1,"title":"title 1"}]},{"selfarr":[{"id":2,"title":"title 2"}]}]}` {
			t.Errorf("selfarr = %s", got)
		}
		res = requireNoLeak(t, url, `{ allHolders { nodes {
			box { label } dbox { label d { id title } } darr { id } drow { id title }
			crate { vb { label } dbs { label d { id owner } } } } } }`)
		want := `{"nodes":[{"box":{"label":"x"},"crate":{"dbs":[{"d":{"id":1,"owner":1},"label":"z"}],"vb":{"label":"x"}},` +
			`"darr":[{"id":1},{"id":2}],"dbox":{"d":{"id":1,"title":"title 1"},"label":"y"},"drow":{"id":2,"title":"title 2"}}]}`
		if got := normalize(t, res.Data["allHolders"]); got != want {
			t.Errorf("holders = %s", got)
		}
	})

	t.Run("mutation payloads", func(t *testing.T) {
		res := requireNoLeak(t, url, `mutation { grabDocs(input: {}) { docRows { id title } } grabBox(input: {}) { docbox { label d { id } } } }`)
		if got := normalize(t, res.Data["grabDocs"]); got != `{"docRows":[{"id":1,"title":"title 1"},{"id":2,"title":"title 2"}]}` {
			t.Errorf("grabDocs = %s", got)
		}
		if got := normalize(t, res.Data["grabBox"]); got != `{"docbox":{"d":{"id":1},"label":"g"}}` {
			t.Errorf("grabBox = %s", got)
		}
	})

	t.Run("hidden values are not queryable", func(t *testing.T) {
		for _, q := range []string{
			`{ docArr }`,
			`{ docArr { secret } }`,
			`{ docArr { rast } }`,
			`{ vaultBox { label v } }`,
			`{ docBox { r } }`,
			`{ docTab { d { secret } } }`,
			`{ allDocs { nodes { selfarr { secret } } } }`,
			`{ allHolders { nodes { box { v } } } }`,
			`{ allHolders { nodes { dbox { d { secret } } } } }`,
			`{ allHolders { nodes { crate { vb { v } } } } }`,
			`{ allHolders(filter: {drow: {isNull: false}}) { totalCount } }`,
			`{ allHolders(filter: {dbox: {isNull: false}}) { totalCount } }`,
			`{ allHolders(orderBy: DROW_ASC) { totalCount } }`,
			`{ vaultRows { id } }`,
			`{ vaultArr }`,
		} {
			if res := post(t, url, q, nil); len(res.Errors) == 0 {
				t.Errorf("accepted %s: %s", q, res.Data)
			}
		}
	})

	t.Run("contract", func(t *testing.T) {
		base := nestedRowsContracts(t)
		public := base + "/public"
		for _, q := range []string{
			`{ docArr { title } }`,
			`{ docTab { d { title } } }`,
			`{ allHolders { nodes { dbox { d { title } } } } }`,
			`{ allDocs { nodes { title } } }`,
		} {
			res := post(t, public, q, nil)
			if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, `"title"`) {
				t.Errorf("public accepted %s: %+v %s", q, res.Errors, res.Data)
			}
		}
		res := requireNoLeak(t, public, `{ docArr { id owner } allHolders { nodes { dbox { d { id } } darr { owner } } } }`)
		for root, raw := range res.Data {
			if strings.Contains(string(raw), "title 1") {
				t.Errorf("public %s leaks the hidden title: %s", root, raw)
			}
		}
		res = requireNoLeak(t, base, `{ docArr { title } }`)
		if got := normalize(t, res.Data["docArr"]); got != `[{"title":"title 1"},{"title":"title 2"}]` {
			t.Errorf("internal docArr = %s", got)
		}
	})
}
