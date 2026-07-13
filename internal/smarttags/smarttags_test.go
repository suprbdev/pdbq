package smarttags

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name     string
		comment  string
		wantTags Tags
		wantDesc string
	}{
		{"empty", "", nil, ""},
		{"plain comment", "A user of the app.", nil, "A user of the app."},
		{"bare tag", "@omit", Tags{"omit": {""}}, ""},
		{"tag with value", "@name people", Tags{"name": {"people"}}, ""},
		{
			"tags then description",
			"@name people\n@omit create,delete\nA user of the app.\nSecond line.",
			Tags{"name": {"people"}, "omit": {"create,delete"}},
			"A user of the app.\nSecond line.",
		},
		{
			"blank line inside tag block",
			"@omit\n\n@name people\nDesc.",
			Tags{"omit": {""}, "name": {"people"}},
			"Desc.",
		},
		{
			"repeated tags accumulate",
			"@unique email\n@unique full_name, mood",
			Tags{"unique": {"email", "full_name, mood"}},
			"",
		},
		{
			"description with @ mid-text is not a tag",
			"Contact us @support.",
			nil,
			"Contact us @support.",
		},
		{
			"tag lines after description are description",
			"Desc first.\n@omit",
			nil,
			"Desc first.\n@omit",
		},
		{
			"multi-word value",
			"@deprecated use fullName instead",
			Tags{"deprecated": {"use fullName instead"}},
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tags, desc := Parse(tc.comment)
			if !reflect.DeepEqual(tags, tc.wantTags) {
				t.Errorf("tags = %#v, want %#v", tags, tc.wantTags)
			}
			if desc != tc.wantDesc {
				t.Errorf("desc = %q, want %q", desc, tc.wantDesc)
			}
		})
	}
}

func TestOmits(t *testing.T) {
	tags, _ := Parse("@omit create, update\n@omit filter")
	actions, everything := tags.Omits()
	if everything {
		t.Error("everything = true, want false")
	}
	want := map[string]bool{"create": true, "update": true, "filter": true}
	if !reflect.DeepEqual(actions, want) {
		t.Errorf("actions = %v, want %v", actions, want)
	}

	tags, _ = Parse("@omit")
	if _, everything := tags.Omits(); !everything {
		t.Error("bare @omit must omit everything")
	}

	// PostGraphile-V5 @behavior negative tokens map onto omit actions.
	tags, _ = Parse("@behavior -insert -update -delete +select\nDescription.")
	actions, everything = tags.Omits()
	if everything {
		t.Error("@behavior must not omit everything")
	}
	for _, want := range []string{"create", "update", "delete"} {
		if !actions[want] {
			t.Errorf("@behavior must omit %q, got %v", want, actions)
		}
	}
	if actions["read"] {
		t.Error("+select must not omit read")
	}

	var nilTags Tags
	if actions, everything := nilTags.Omits(); everything || len(actions) != 0 {
		t.Error("nil Tags must omit nothing")
	}
}

func TestHelpers(t *testing.T) {
	tags, _ := Parse("@filterable\n@name people")
	if !tags.Has("filterable") || tags.Has("sortable") {
		t.Error("Has wrong")
	}
	if tags.First("filterable") != "" || tags.First("name") != "people" {
		t.Error("First wrong")
	}
	if Strip("@omit\nKeep me.") != "Keep me." {
		t.Error("Strip wrong")
	}
	if Strip("No tags here.") != "No tags here." {
		t.Error("Strip must not alter tag-free comments")
	}
}
