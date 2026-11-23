package schema_test

import (
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// domainCatalog is the fixture catalog plus domains: email (text, with a
// comment and a CHECK), percent over positive_int (inherited CHECK), a
// domain over int[], one over hstore (not built in), one named after an
// existing type, and columns, a composite attribute and functions using
// them.
func domainCatalog() *introspect.Catalog {
	cat := testutil.FixtureCatalog()
	cat.Domains = []*introspect.Domain{
		{Schema: "public", Name: "email", BaseType: "text", BaseTypeSchema: "pg_catalog",
			Checks: []string{"CHECK ((VALUE ~ '@'::text))"}, Comment: "@name EmailAddress\nAn email address."},
		{Schema: "public", Name: "positive_int", BaseType: "int4", BaseTypeSchema: "pg_catalog", NotNull: true,
			Checks: []string{"CHECK ((VALUE > 0))"}},
		{Schema: "public", Name: "percent", BaseType: "int4", BaseTypeSchema: "pg_catalog", Parent: "public.positive_int",
			Checks: []string{"CHECK (((VALUE)::integer <= 100))"}},
		{Schema: "public", Name: "int_list", BaseType: "_int4", BaseTypeSchema: "pg_catalog", IsArrayBase: true},
		{Schema: "public", Name: "attrs", BaseType: "hstore", BaseTypeSchema: "extensions"},
		{Schema: "public", Name: "user", BaseType: "text", BaseTypeSchema: "pg_catalog"},
	}
	users := cat.Table("public", "users")
	users.Columns = append(users.Columns,
		&introspect.Column{Name: "work_email", Position: 12, PGType: "text", TypeSchema: "public", BaseTypeSchema: "pg_catalog", Domain: "public.email"},
		&introspect.Column{Name: "aliases", Position: 13, PGType: "_text", TypeSchema: "public", BaseTypeSchema: "pg_catalog", IsArray: true, Domain: "public.email"},
		&introspect.Column{Name: "score", Position: 14, PGType: "int4", TypeSchema: "public", BaseTypeSchema: "pg_catalog", Domain: "public.percent"},
		&introspect.Column{Name: "lucky", Position: 15, PGType: "_int4", TypeSchema: "public", BaseTypeSchema: "pg_catalog", IsArray: true, Domain: "public.int_list"},
		&introspect.Column{Name: "nick", Position: 16, PGType: "text", TypeSchema: "public", BaseTypeSchema: "pg_catalog", Domain: "public.user"},
	)
	users.Indexes = append(users.Indexes, &introspect.Index{Name: "users_work_email_idx", Columns: []string{"work_email"}, Method: "btree"})
	cat.Composite("public", "address").Fields = append(cat.Composite("public", "address").Fields,
		&introspect.Column{Name: "contact", Position: 9, PGType: "text", TypeSchema: "public", BaseTypeSchema: "pg_catalog", Domain: "public.email"})
	cat.Functions = append(cat.Functions,
		&introspect.Function{
			Schema: "public", Name: "rate",
			Args: []introspect.FuncArg{
				{Name: "who", PGType: "email", TypeSchema: "public", Mode: introspect.ArgModeIn, Domain: "public.email"},
				{Name: "cc", PGType: "_email", TypeSchema: "public", Mode: introspect.ArgModeIn, Domain: "public.email"},
				{Name: "picks", PGType: "int_list", TypeSchema: "public", Mode: introspect.ArgModeIn, Domain: "public.int_list"},
			},
			ReturnType: "percent", ReturnTypeSchema: "public", ReturnDomain: "public.percent",
			Volatility: introspect.VolatilityStable,
		},
		&introspect.Function{
			Schema: "public", Name: "lucky_numbers",
			ReturnType: "int_list", ReturnTypeSchema: "public", ReturnDomain: "public.int_list",
			Volatility: introspect.VolatilityStable,
		},
	)
	return cat
}

func TestDomainScalars(t *testing.T) {
	b := schema.New(domainCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, Functions: true, DomainScalars: true})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	user := sdlType(built.SDL, "type User ")
	for _, want := range []string{
		"workEmail: Email\n", "aliases: [Email!]\n", "score: Percent\n", "lucky: [Int!]\n", "nick: String\n",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("User missing %q:\n%s", want, user)
		}
	}
	for _, want := range []string{
		"\"\"\"Constraints:\n- `NOT NULL`\n- `CHECK (((VALUE)::integer <= 100))`\n- `CHECK ((VALUE > 0))`\"\"\"\nscalar Percent\n",
		"\"\"\"Constraints:\n- `NOT NULL`\n- `CHECK ((VALUE > 0))`\"\"\"\nscalar PositiveInt\n",
		"rate(who: Email, cc: [Email], picks: [Int]): Percent",
		"luckyNumbers: [Int!]",
	} {
		if !strings.Contains(built.SDL, want) {
			t.Errorf("SDL missing %q", want)
		}
	}
	if !strings.Contains(built.SDL, "\"\"\"An email address.\n\nConstraints:\n- `CHECK ((VALUE ~ '@'::text))`\"\"\"\nscalar Email\n") {
		t.Error("Email scalar description missing")
	}
	for _, absent := range []string{"scalar IntList", "scalar Attrs", "scalar User\n", "@name"} {
		if strings.Contains(built.SDL, absent) {
			t.Errorf("SDL contains %q", absent)
		}
	}
	if f := sdlType(built.SDL, "input UserFilter "); !strings.Contains(f, "workEmail: StringFilter") {
		t.Errorf("domain column filter must use the base type's operators:\n%s", f)
	}
	if c := sdlType(built.SDL, "input UserCondition "); !strings.Contains(c, "workEmail: Email") {
		t.Errorf("condition should take the domain scalar:\n%s", c)
	}
	if a := sdlType(built.SDL, "type Address "); !strings.Contains(a, "contact: Email") {
		t.Errorf("composite attribute should take the domain scalar:\n%s", a)
	}
	if in := sdlType(built.SDL, "input UserInput "); !strings.Contains(in, "workEmail: Email") || !strings.Contains(in, "aliases: [Email!]") {
		t.Errorf("create input should take the domain scalar:\n%s", in)
	}
}

func TestDomainScalarsOff(t *testing.T) {
	b := schema.New(domainCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, Functions: true})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	user := sdlType(built.SDL, "type User ")
	for _, want := range []string{"workEmail: String\n", "aliases: [String!]\n", "score: Int\n", "lucky: [Int!]\n"} {
		if !strings.Contains(user, want) {
			t.Errorf("User missing %q:\n%s", want, user)
		}
	}
	if !strings.Contains(built.SDL, "rate(who: String, cc: [String], picks: [Int]): Int") {
		t.Error("function over domains should take the base types")
	}
	if strings.Contains(built.SDL, "scalar Email") || strings.Contains(built.SDL, "scalar Percent") {
		t.Error("domain scalars declared with DomainScalars off")
	}
}

func TestDomainScalarInflection(t *testing.T) {
	renamed := func(kind inflect.Kind, in inflect.Input) string {
		if kind == inflect.KindDomainTypeName && in.Domain == "email" {
			return "EmailAddress"
		}
		return inflect.Default(kind, in)
	}
	built, err := schema.New(domainCatalog(), renamed, schema.Options{Functions: true, DomainScalars: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(built.SDL, "scalar EmailAddress\n") || !strings.Contains(sdlType(built.SDL, "type User "), "workEmail: EmailAddress") {
		t.Error("domain scalar name must come from KindDomainTypeName")
	}
}
