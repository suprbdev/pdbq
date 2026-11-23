package omit_test

import (
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/omit"
)

func TestHides(t *testing.T) {
	actions := []omit.Action{omit.Read, omit.Create, omit.Update, omit.Delete, omit.Filter, omit.Order, omit.Many, omit.All, omit.Aggregates}
	cases := []struct {
		comment string
		hidden  []omit.Action
	}{
		{"", nil},
		{"Just a description.", nil},
		{"A description mentioning @omit later.", nil},
		{"@omit", actions},
		{"@omit\nThe description.", actions},
		{"@omit read", []omit.Action{omit.Read}},
		{"@omit read,filter", []omit.Action{omit.Read, omit.Filter}},
		{"@omit create, update", []omit.Action{omit.Create, omit.Update}},
		{"@omit READ", []omit.Action{omit.Read}},
		{"@omit order\n@omit many", []omit.Action{omit.Order, omit.Many}},
		{"@name other\n@omit all", []omit.Action{omit.All}},
		{"@behavior -select -insert", []omit.Action{omit.Read, omit.Create}},
		{"@behavior -update -delete -filter -order", []omit.Action{omit.Update, omit.Delete, omit.Filter, omit.Order}},
		{"@omit aggregates", []omit.Action{omit.Aggregates}},
	}
	for _, tc := range cases {
		hidden := map[omit.Action]bool{}
		for _, a := range tc.hidden {
			hidden[a] = true
		}
		for _, a := range actions {
			if got := omit.Hides(tc.comment, a); got != hidden[a] {
				t.Errorf("Hides(%q, %s) = %v, want %v", tc.comment, a, got, hidden[a])
			}
		}
		if got, want := omit.Everything(tc.comment), len(tc.hidden) == len(actions); got != want {
			t.Errorf("Everything(%q) = %v, want %v", tc.comment, got, want)
		}
	}
}

func TestDistinct(t *testing.T) {
	for comment, want := range map[string]bool{
		"":              false,
		"@omit create":  false,
		"@omit update":  false,
		"@omit":         true,
		"@omit read":    true,
		"@omit filter":  true,
		"@omit order":   true,
		"@omit filter ": true,
	} {
		if got := omit.Distinct(&introspect.Column{Comment: comment}); got != want {
			t.Errorf("Distinct(%q) = %v, want %v", comment, got, want)
		}
	}
	if omit.Column(nil, omit.Read) {
		t.Error("a missing column is not hidden")
	}
}

func TestKeys(t *testing.T) {
	parent := &introspect.Table{Name: "accounts", Columns: []*introspect.Column{
		{Name: "id"},
		{Name: "token", Comment: "@omit"},
		{Name: "pin", Comment: "@omit read"},
		{Name: "code", Comment: "@omit filter,order,create,update"},
	}}
	for _, tc := range []struct {
		cols []string
		want bool
	}{
		{[]string{"id"}, false},
		{[]string{"code"}, false},
		{[]string{"token"}, true},
		{[]string{"pin"}, true},
		{[]string{"id", "pin"}, true},
		{[]string{"missing"}, false},
	} {
		if got := omit.HiddenKey(parent, tc.cols); got != tc.want {
			t.Errorf("HiddenKey(%v) = %v, want %v", tc.cols, got, tc.want)
		}
		fk := &introspect.ForeignKey{RefColumns: tc.cols}
		if got := omit.ReferencesHidden(parent, fk); got != tc.want {
			t.Errorf("ReferencesHidden(%v) = %v, want %v", tc.cols, got, tc.want)
		}
	}
	if omit.ReferencesHidden(nil, &introspect.ForeignKey{RefColumns: []string{"token"}}) {
		t.Error("a missing parent hides nothing")
	}
}

func TestManyHidden(t *testing.T) {
	fk := &introspect.ForeignKey{}
	child := &introspect.Table{}
	if omit.ManyHidden(child, fk) {
		t.Error("untagged")
	}
	fk.Comment = "@omit many"
	if !omit.ManyHidden(child, fk) {
		t.Error("@omit many on the foreign key")
	}
	fk.Comment = "@omit filter"
	child.Comment = "@omit many"
	if !omit.ManyHidden(child, fk) {
		t.Error("@omit many on the referencing table")
	}
	child.Comment = "@omit read"
	if omit.ManyHidden(child, fk) {
		t.Error("@omit read on the table does not hide the one-to-many side")
	}
}
