package compile_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/errcode"
)

func TestFunctionMissingArgumentsAreUserErrors(t *testing.T) {
	cases := map[string]struct {
		query, want string
	}{
		"required omitted": {`{ greet(greeting: "Hi") }`, "greet: missing required argument(s): name"},
		"variadic gap":     {`{ sumAll(nums: [1, 2]) }`, "sumAll: missing required argument(s): scale"},
		"mutation input":   {`mutation { recordVisit(input: {}) { clientMutationId } }`, "recordVisit: missing required argument(s): spot"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := compileOne(t, tc.query, 100)
			var uerr *compile.UserError
			if !errors.As(err, &uerr) {
				t.Fatalf("err = %v, want a *compile.UserError", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestFunctionSchemaSurface(t *testing.T) {
	sdl := build(t).SDL
	for _, want := range []string{
		`greet(name: String, """Optional; defaults to ` + "`'Hello'::text`" + ` when omitted.""" greeting: String): String`,
		`"""Optional; defaults to ` + "`'{}'::integer[]`" + ` when omitted. Variadic: pass the values as a list.""" nums: [Int]`,
		`postStats("""Optional; defaults to ` + "`0`" + ` when omitted.""" minId: Int): PostStatsRecord`,
		"postTitles: [PostTitlesRecord!]!",
		"type PostStatsRecord {\n  total: BigInt\n  maxTitle: String\n}",
		"makeAddress(street: String, city: String): Address",
		"formatAddress(a: AddressInput): String",
		"placesNear(pt: GeoJSON,",
		"result: RecordVisitRecord",
		"home: Address",
		"authenticate(input: AuthenticateInput!): AuthenticatePayload!",
	} {
		if !strings.Contains(sdl, want) {
			t.Errorf("SDL missing %q", want)
		}
	}
}

func TestRowArgumentSQLIsValueIndependent(t *testing.T) {
	var first string
	for i, q := range []string{
		`{ userGreeting(greeting: "a", u: {id: 1}) }`,
		`{ userGreeting(greeting: "b", u: {email: "x'); DROP TABLE users; --", mood: SAD, address: {city: "c"}, prevAddresses: [{street: "s"}]}) }`,
		`{ userGreeting(greeting: "c", u: {}) }`,
	} {
		stmt, err := compileOne(t, q, 0)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stmt.SQL, "DROP") || len(stmt.Args) != 2 {
			t.Fatalf("values must be bound: %s %v", stmt.SQL, stmt.Args)
		}
		if i == 0 {
			first = stmt.SQL
		} else if stmt.SQL != first {
			t.Errorf("SQL depends on the row's values:\n%s\n%s", first, stmt.SQL)
		}
	}
}

func TestMutationInputErrorsAreInvalidArgument(t *testing.T) {
	cases := map[string]struct {
		query, want string
	}{
		"empty patch":       {`mutation { updateUserById(input: {id: 1, userPatch: {}}) { clientMutationId } }`, "updateUserById: userPatch must set at least one column"},
		"empty bulk patch":  {`mutation { updateManyUsers(input: {filter: {id: {equalTo: 1}}, userPatch: {}}) { affectedCount } }`, "updateManyUsers: userPatch must set at least one column"},
		"empty bulk create": {`mutation { createManyUsers(input: {users: []}) { clientMutationId } }`, "createManyUsers: users must be a non-empty list"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := compileOne(t, tc.query, 100)
			var uerr *compile.UserError
			if !errors.As(err, &uerr) {
				t.Fatalf("err = %v, want a *compile.UserError", err)
			}
			if uerr.Code() != errcode.InvalidArgument {
				t.Errorf("code = %s, want %s", uerr.Code(), errcode.InvalidArgument)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}
