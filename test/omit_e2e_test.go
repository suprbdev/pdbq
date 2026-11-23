package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/config"
)

const omitSchema = "omit_e2e"

// setupOmit creates the omit_e2e schema reproducing the @omit enforcement
// issues: hidden spatial columns (#62), functions over an omitted table and
// over a table in an unexposed schema (#63), distinctOn over omitted columns
// (#64), a hidden unique key (#65), a column tagged @omit update reached by
// an upsert (#66), and nested mutations copying a hidden referenced key or
// ignoring @omit many (#67). Everything is dropped when the test ends.
func setupOmit(t *testing.T) {
	t.Helper()
	conn := testConn(t)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+omitSchema+` CASCADE; DROP SCHEMA IF EXISTS `+omitSchema+`_private CASCADE`)
	})
	execAll(t, conn,
		`CREATE SCHEMA `+omitSchema,
		`CREATE SCHEMA `+omitSchema+`_private`,
		`SET search_path TO `+omitSchema+`, public`,
		`CREATE TABLE hideouts (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text,
			secret_location geometry(Point,4326), private_area geography)`,
		`COMMENT ON COLUMN hideouts.secret_location IS '@omit'`,
		`COMMENT ON COLUMN hideouts.private_area IS '@omit read'`,
		`INSERT INTO hideouts (name, secret_location, private_area) VALUES
			('x', 'SRID=4326;POINT(12.345678 45.678901)', 'SRID=4326;POLYGON((1 1,2 1,2 2,1 2,1 1))')`,

		`CREATE TABLE gone (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY, account_id int, val text)`,
		`COMMENT ON TABLE gone IS '@omit'`,
		`INSERT INTO gone (account_id, val) VALUES (1, 'g1')`,
		`CREATE FUNCTION all_gone() RETURNS SETOF gone LANGUAGE sql STABLE AS $$ SELECT * FROM gone $$`,
		`CREATE FUNCTION gone_label(g gone) RETURNS text LANGUAGE sql STABLE AS $$ SELECT g.val $$`,
		`CREATE TABLE `+omitSchema+`_private.keys (id int PRIMARY KEY, k text)`,
		`INSERT INTO `+omitSchema+`_private.keys VALUES (1, 'PRIVKEY')`,
		`CREATE FUNCTION all_priv_keys() RETURNS SETOF `+omitSchema+`_private.keys LANGUAGE sql STABLE AS $$ SELECT * FROM `+omitSchema+`_private.keys $$`,

		`CREATE TABLE accounts (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text NOT NULL,
			secret_note text, score int, api_token text UNIQUE)`,
		`COMMENT ON COLUMN accounts.secret_note IS '@omit'`,
		`COMMENT ON COLUMN accounts.score IS '@omit filter,order'`,
		`COMMENT ON COLUMN accounts.api_token IS '@omit'`,
		`INSERT INTO accounts (name, secret_note, score, api_token) VALUES
			('acme', 'acme note', 5, 'tok-acme-SECRET'), ('globex', 'globex note', 7, 'tok-globex-SECRET'),
			('initech', 'zzz-last', 1, NULL), ('umbrella', 'acme note', 9, NULL)`,

		`CREATE TABLE contracts (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			code text UNIQUE NOT NULL, signed_by text, amount int)`,
		`COMMENT ON COLUMN contracts.signed_by IS '@omit update'`,
		`INSERT INTO contracts (code, signed_by, amount) VALUES ('c1', 'alice', 100)`,

		`CREATE TABLE tok_refs (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			account_token text REFERENCES accounts (api_token), note text)`,
		`CREATE TABLE approvals (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			account_id int NOT NULL REFERENCES accounts (id), note text)`,
		`COMMENT ON CONSTRAINT approvals_account_id_fkey ON approvals IS '@omit many'`,
		`INSERT INTO approvals (account_id, note) VALUES (1, 'first')`,
		`RESET search_path`,
	)
}

func omitServer(t *testing.T) string {
	hs := testServer(t, func(c *config.Config) {
		c.Schema.Schemas = []string{omitSchema}
		c.Filters.IndexedOnly = false
	})
	return hs.URL
}

// requireRejected asserts the query fails validation on the field name.
func requireRejected(t *testing.T, url, query, field string) {
	t.Helper()
	res := post(t, url, query, nil)
	if len(res.Errors) == 0 {
		t.Fatalf("%s: expected a validation error, got data %s", query, res.Data)
	}
	if !strings.Contains(res.Errors[0].Message, field) {
		t.Fatalf("%s: error %q does not name %s", query, res.Errors[0].Message, field)
	}
}

// TestOmitEnforcement runs the reproductions of the @omit enforcement
// issues against a real database.
func TestOmitEnforcement(t *testing.T) {
	setupOmit(t)
	url := omitServer(t)
	sdl := sdlOf(t, url)

	t.Run("postgis accessors of hidden columns (#62)", func(t *testing.T) {
		requireRejected(t, url, `{ allHideouts { nodes { secretLocationWkt } } }`, "secretLocationWkt")
		requireRejected(t, url, `{ allHideouts { nodes { secretLocationX } } }`, "secretLocationX")
		requireRejected(t, url, `{ allHideouts { nodes { privateAreaEwkt } } }`, "privateAreaEwkt")
		for _, absent := range []string{"secretLocation", "SECRET_LOCATION", "privateAreaWkt", "PRIVATE_AREA_DISTANCE"} {
			if strings.Contains(sdl, absent) {
				t.Errorf("SDL exposes %s", absent)
			}
		}
		res := post(t, url, `{ allHideouts { nodes { name } } }`, nil)
		requireNoErrors(t, res)
	})

	t.Run("functions over hidden rows (#63)", func(t *testing.T) {
		requireRejected(t, url, `{ allGone }`, "allGone")
		requireRejected(t, url, `{ allPrivKeys }`, "allPrivKeys")
		requireRejected(t, url, `{ goneLabel(g: "{}") }`, "goneLabel")
		for _, absent := range []string{"allGone", "allPrivKeys", "goneLabel", "PRIVKEY"} {
			if strings.Contains(sdl, absent) {
				t.Errorf("SDL exposes %s", absent)
			}
		}
	})

	t.Run("distinctOn over hidden columns (#64)", func(t *testing.T) {
		requireRejected(t, url, `{ allAccounts(distinctOn: [SECRET_NOTE]) { nodes { id name } } }`, "SECRET_NOTE")
		requireRejected(t, url, `{ allAccounts(distinctOn: [SCORE]) { nodes { name } } }`, "SCORE")
		res := post(t, url, `{ allAccounts(distinctOn: [NAME], orderBy: [NAME_ASC]) { nodes { name } } }`, nil)
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["allAccounts"]); got != `{"nodes":[{"name":"acme"},{"name":"globex"},{"name":"initech"},{"name":"umbrella"}]}` {
			t.Fatalf("distinctOn NAME = %s", got)
		}
	})

	t.Run("lookups by a hidden unique key (#65)", func(t *testing.T) {
		requireRejected(t, url, `{ hit: accountByApiToken(apiToken: "tok-acme-SECRET") { id name } }`, "accountByApiToken")
		requireRejected(t, url, `mutation { updateAccountByApiToken(apiToken: "tok-acme-SECRET", patch: {name: "x"}) { account { id } } }`, "updateAccountByApiToken")
		requireRejected(t, url, `mutation { upsertAccountByApiToken(input: {name: "x"}) { account { id } } }`, "upsertAccountByApiToken")
		for _, absent := range []string{"ByApiToken", "apiToken"} {
			if strings.Contains(sdl, absent) {
				t.Errorf("SDL exposes %s", absent)
			}
		}
	})

	t.Run("upsert keeps columns tagged @omit update (#66)", func(t *testing.T) {
		requireRejected(t, url, `mutation { updateContractById(id: 1, patch: {signedBy: "mallory"}) { contract { id } } }`, "signedBy")
		res := post(t, url, `mutation { upsertContractByCode(input: {code: "c1", signedBy: "mallory", amount: 200}) { contract { id signedBy amount } } }`, nil)
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["upsertContractByCode"]); got != `{"contract":{"amount":200,"id":1,"signedBy":"alice"}}` {
			t.Fatalf("upsert = %s", got)
		}
		res = post(t, url, `mutation { upsertContractByCode(input: {code: "c2", signedBy: "bob"}) { contract { code signedBy } } }`, nil)
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["upsertContractByCode"]); got != `{"contract":{"code":"c2","signedBy":"bob"}}` {
			t.Fatalf("upsert insert = %s", got)
		}
	})

	t.Run("nested mutations (#67)", func(t *testing.T) {
		requireRejected(t, url,
			`mutation { createTokRef(input: {note: "leak", accountByAccountToken: {connectById: {id: 2}}}) { tokRef { accountToken } } }`,
			"accountByAccountToken")
		requireRejected(t, url,
			`mutation { updateAccountById(id: 1, patch: {approvalsByAccountId: {create: [{note: "x"}]}}) { account { id } } }`,
			"approvalsByAccountId")
		requireRejected(t, url,
			`{ allAccounts(filter: {approvalsByAccountId: {some: {note: {startsWith: "f"}}}}) { nodes { id } } }`,
			"approvalsByAccountId")
		if strings.Contains(sdl, "approvalsByAccountId") {
			t.Error("SDL exposes approvalsByAccountId")
		}
		res := post(t, url, `{ allApprovals { nodes { note account { name } } } }`, nil)
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["allApprovals"]); got != `{"nodes":[{"account":{"name":"acme"},"note":"first"}]}` {
			t.Fatalf("forward relation = %s", got)
		}
	})
}
