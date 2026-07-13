package introspect

import (
	"strings"
	"testing"
)

func diffFixture() *Catalog {
	return &Catalog{
		ServerVersion: "16.0",
		Schemas:       []string{"public"},
		Tables: []*Table{{
			Schema: "public",
			Name:   "users",
			Kind:   RelTable,
			Columns: []*Column{
				{Name: "id", PGType: "int4", NotNull: true},
				{Name: "email", PGType: "text", NotNull: true},
			},
			PrimaryKey: &Constraint{Name: "users_pkey", Columns: []string{"id"}},
			Privileges: Privileges{Select: true},
		}},
		Enums:     []*Enum{{Schema: "public", Name: "mood", Values: []string{"HAPPY", "SAD"}}},
		Functions: []*Function{{Schema: "public", Name: "f", Args: []FuncArg{{Name: "q", PGType: "text"}}, ReturnType: "int4"}},
	}
}

func TestDiffEqual(t *testing.T) {
	if d := Diff(diffFixture(), diffFixture()); len(d) != 0 {
		t.Fatalf("expected no diff, got %v", d)
	}
}

func TestDiff(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Catalog)
		want   string
	}{
		{"table added", func(c *Catalog) {
			c.Tables = append(c.Tables, &Table{Schema: "public", Name: "posts", Kind: RelTable})
		}, "table public.posts added"},
		{"table removed", func(c *Catalog) { c.Tables = nil }, "table public.users removed"},
		{"column added", func(c *Catalog) {
			c.Tables[0].Columns = append(c.Tables[0].Columns, &Column{Name: "bio", PGType: "text"})
		}, "table public.users: column bio added (text)"},
		{"column removed", func(c *Catalog) {
			c.Tables[0].Columns = c.Tables[0].Columns[:1]
		}, "table public.users: column email removed"},
		{"column type changed", func(c *Catalog) {
			c.Tables[0].Columns[1].PGType = "varchar"
		}, "table public.users: column email type changed (text not null -> varchar not null)"},
		{"column attr changed", func(c *Catalog) {
			c.Tables[0].Columns[1].HasDefault = true
		}, "table public.users: column email changed"},
		{"index/key change", func(c *Catalog) {
			c.Tables[0].Uniques = []*Constraint{{Name: "users_email_key", Columns: []string{"email"}}}
		}, "table public.users changed (unique constraints)"},
		{"privilege change", func(c *Catalog) {
			c.Tables[0].Privileges.Insert = true
		}, "table public.users changed (privileges)"},
		{"enum changed", func(c *Catalog) {
			c.Enums[0].Values = append(c.Enums[0].Values, "ANGRY")
		}, "enum public.mood changed"},
		{"function removed", func(c *Catalog) { c.Functions = nil }, "function public.f(text) removed"},
		{"schema added", func(c *Catalog) {
			c.Schemas = append(c.Schemas, "app")
		}, "schema app added"},
		{"server version", func(c *Catalog) {
			c.ServerVersion = "17.0"
		}, "server version changed: 16.0 -> 17.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			live := diffFixture()
			tc.mutate(live)
			got := Diff(diffFixture(), live)
			if len(got) == 0 {
				t.Fatalf("expected diff containing %q, got none", tc.want)
			}
			joined := strings.Join(got, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("diff %q does not contain %q", joined, tc.want)
			}
			if len(got) != 1 {
				t.Fatalf("expected exactly one diff line, got %v", got)
			}
		})
	}
}
