package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/config"
)

// TestFunctionRowArguments calls the fixture functions taking a table row
// (user_greeting), an array of rows (total_title_length), an array of
// composites (address_cities) and a volatile function with a leading row
// argument (rename_user, a custom mutation).
func TestFunctionRowArguments(t *testing.T) {
	hs := testServer(t, nil)
	conn := testConn(t)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `UPDATE users SET full_name = 'Grace Hopper' WHERE id = 2`)
	})

	res := post(t, hs.URL, `query ($u: UserRowInput) {
	  userGreeting(greeting: "Hello", u: $u)
	  anon: userGreeting(greeting: "Hi", u: {email: "x@example.com"})
	  totalTitleLength(ps: [{title: "abc"}, null, {title: "de", published: true}])
	  empty: totalTitleLength(ps: [])
	  addressCities(addrs: [{city: "Paris", mood: HAPPY}, null, {street: "Main"}])
	}`, map[string]any{"u": map[string]any{"id": 1, "email": "ada@example.com", "fullName": "Ada", "mood": "HAPPY",
		"tags": []string{"a"}, "address": map[string]any{"city": "London"}, "createdAt": "2020-01-01T00:00:00Z", "balance": "12"}})
	requireNoErrors(t, res)
	for field, want := range map[string]string{
		"userGreeting":     `"Hello, Ada!"`,
		"anon":             `"Hi, x@example.com!"`,
		"totalTitleLength": `5`,
		"empty":            `0`,
		"addressCities":    `["Paris",null,null]`,
	} {
		if got := normalize(t, res.Data[field]); got != want {
			t.Errorf("%s = %s, want %s", field, got, want)
		}
	}

	res = post(t, hs.URL, `mutation {
	  renameUser(input: {u: {id: 2, email: "ignored@example.com"}, newName: "Grace B. Hopper", clientMutationId: "r1"}) {
	    clientMutationId
	    user { id fullName postsByAuthorId { totalCount } }
	  }
	}`, nil)
	requireNoErrors(t, res)
	if got := normalize(t, res.Data["renameUser"]); got != `{"clientMutationId":"r1","user":{"fullName":"Grace B. Hopper","id":2,"postsByAuthorId":{"totalCount":1}}}` {
		t.Errorf("renameUser = %s", got)
	}

	res = post(t, hs.URL, `{ userGreeting(greeting: "Hi", u: {nope: 1}) }`, nil)
	if len(res.Errors) == 0 {
		t.Error("unknown row field must be rejected")
	}
	res = post(t, hs.URL, `{ computed: allUsers(first: 1) { nodes { postCount } } }`, nil)
	requireNoErrors(t, res)
}

// TestFunctionPolymorphicArguments calls the fixture functions whose
// polymorphic signatures smart comments resolve, and checks the untagged
// one, and every one with smart comments disabled, stays out of the schema.
func TestFunctionPolymorphicArguments(t *testing.T) {
	hs := testServer(t, nil)
	res := post(t, hs.URL, `{
	  pickFirst(a: null, b: "fallback")
	  same: pickFirst(a: "first", b: "second")
	  largerOf(a: "1.5", b: "2")
	  rangeLower(r: {start: {value: 3, inclusive: true}, end: {value: 9, inclusive: false}})
	  exclusive: rangeLower(r: {start: {value: 3, inclusive: false}})
	}`, nil)
	requireNoErrors(t, res)
	for field, want := range map[string]string{
		"pickFirst":  `"fallback"`,
		"same":       `"first"`,
		"largerOf":   `"2"`,
		"rangeLower": `3`,
		"exclusive":  `4`,
	} {
		if got := normalize(t, res.Data[field]); got != want {
			t.Errorf("%s = %s, want %s", field, got, want)
		}
	}
	res = post(t, hs.URL, `{ arrayFirst(arr: "{1}") }`, nil)
	if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "arrayFirst") {
		t.Errorf("untagged polymorphic function must not be exposed: %+v", res.Errors)
	}

	off := testServer(t, func(c *config.Config) {
		c.Plugins.Disabled = append(c.Plugins.Disabled, "smart_comments")
	})
	res = post(t, off.URL, `{ pickFirst(a: "x", b: "y") }`, nil)
	if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "pickFirst") {
		t.Errorf("polymorphic functions need smart comments: %+v", res.Errors)
	}
}

// TestFunctionRowArgumentOmit checks that a row argument's input type
// leaves out the columns tagged @omit read, and keeps generated and
// @omit create/update columns.
func TestFunctionRowArgumentOmit(t *testing.T) {
	const schema = "fnargs_e2e"
	conn := testConn(t)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})
	execAll(t, conn,
		`CREATE SCHEMA `+schema,
		`SET search_path TO `+schema,
		`CREATE TABLE vaults (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY, label text, secret text, frozen text)`,
		`COMMENT ON COLUMN vaults.secret IS '@omit read'`,
		`COMMENT ON COLUMN vaults.frozen IS '@omit create,update'`,
		`CREATE FUNCTION describe_vault(note text, v vaults) RETURNS text LANGUAGE sql STABLE
			AS $$ SELECT note || ':' || v.id || ':' || coalesce(v.label, '-') || ':' || coalesce(v.secret, '-') || ':' || coalesce(v.frozen, '-') $$`,
	)
	hs := testServer(t, func(c *config.Config) { c.Schema.Schemas = []string{schema} })

	res := post(t, hs.URL, `{ describeVault(note: "n", v: {id: 7, label: "L", frozen: "F"}) }`, nil)
	requireNoErrors(t, res)
	if got := normalize(t, res.Data["describeVault"]); got != `"n:7:L:-:F"` {
		t.Errorf("describeVault = %s", got)
	}
	res = post(t, hs.URL, `{ describeVault(note: "n", v: {secret: "S"}) }`, nil)
	if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "secret") {
		t.Errorf("@omit read column must not be accepted: %+v", res.Errors)
	}
}
