package inflect

import "testing"

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
		{KindConditionTypeName, Input{Table: "users"}, "UserCondition"},
		{KindOrderByTypeName, Input{Table: "users"}, "UsersOrderBy"},
		{KindRelationForward, Input{Table: "users", Column: "author_id"}, "author"},
		{KindRelationBackward, Input{Table: "posts", Columns: []string{"author_id"}}, "postsByAuthorId"},
		{KindEnumValueName, Input{Value: "very happy"}, "VERY_HAPPY"},
		{KindEnumValueName, Input{Value: "1st"}, "_1ST"},
		{KindFunctionRecord, Input{Function: "order_status_counts"}, "OrderStatusCountsRecord"},
		{KindTypeName, Input{Table: "purchases"}, "Purchase"},
		{KindTypeName, Input{Table: "order_statuses"}, "OrderStatus"},
		{KindTypeName, Input{Table: "spatial_ref_sys"}, "SpatialRefSys"},
	}
	for _, c := range cases {
		if got := Default(c.kind, c.in); got != c.want {
			t.Errorf("Default(%s, %+v) = %q, want %q", c.kind, c.in, got, c.want)
		}
	}
}
