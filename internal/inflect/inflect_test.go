package inflect

import "testing"

func TestCamel(t *testing.T) {
	cases := []struct{ in, upper, lower string }{
		{"user_accounts", "UserAccounts", "userAccounts"},
		{"id", "Id", "id"},
		{"created_at", "CreatedAt", "createdAt"},
		{"a_b_c", "ABC", "aBC"},
	}
	for _, c := range cases {
		if got := UpperCamel(c.in); got != c.upper {
			t.Errorf("UpperCamel(%q) = %q, want %q", c.in, got, c.upper)
		}
		if got := LowerCamel(c.in); got != c.lower {
			t.Errorf("LowerCamel(%q) = %q, want %q", c.in, got, c.lower)
		}
	}
}

func TestSingularizePluralize(t *testing.T) {
	cases := []struct{ plural, singular string }{
		{"users", "user"},
		{"categories", "category"},
		{"boxes", "box"},
		{"posts", "post"},
		{"people", "person"},
		{"organisation_people", "organisation_person"},
		{"children", "child"},
	}
	for _, c := range cases {
		if got := Singularize(c.plural); got != c.singular {
			t.Errorf("Singularize(%q) = %q, want %q", c.plural, got, c.singular)
		}
	}
	plurals := []struct{ singular, plural string }{
		{"category", "categories"},
		{"box", "boxes"},
		{"person", "people"},
		{"organisation_person", "organisation_people"},
		{"people", "people"}, // already plural stays put
	}
	for _, c := range plurals {
		if got := Pluralize(c.singular); got != c.plural {
			t.Errorf("Pluralize(%q) = %q, want %q", c.singular, got, c.plural)
		}
	}
	if got := Singularize("person"); got != "person" {
		t.Errorf("Singularize(person) = %q, want person", got)
	}
}

func TestDefaultNames(t *testing.T) {
	cases := []struct {
		kind Kind
		in   Input
		want string
	}{
		{KindTypeName, Input{Table: "user_accounts"}, "UserAccount"},
		{KindAllRowsField, Input{Table: "users"}, "allUsers"},
		{KindRowByPKField, Input{Table: "users", Columns: []string{"id"}}, "userById"},
		{KindRowByUniqueField, Input{Table: "users", Columns: []string{"email"}}, "userByEmail"},
		{KindCreateMutation, Input{Table: "users"}, "createUser"},
		{KindUpdateMutation, Input{Table: "users", Columns: []string{"id"}}, "updateUserById"},
		{KindDeleteMutation, Input{Table: "users", Columns: []string{"id"}}, "deleteUserById"},
		{KindFilterTypeName, Input{Table: "users"}, "UserFilter"},
		{KindOrderByTypeName, Input{Table: "users"}, "UsersOrderBy"},
		{KindRelationForward, Input{Table: "users", Column: "author_id"}, "author"},
		{KindRelationBackward, Input{Table: "posts", Columns: []string{"author_id"}}, "postsByAuthorId"},
		{KindEnumValueName, Input{Value: "very happy"}, "VERY_HAPPY"},
		{KindEnumValueName, Input{Value: "1st"}, "_1ST"},
	}
	for _, c := range cases {
		if got := Default(c.kind, c.in); got != c.want {
			t.Errorf("Default(%s, %+v) = %q, want %q", c.kind, c.in, got, c.want)
		}
	}
}
