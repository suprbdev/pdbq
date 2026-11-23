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
		{KindRelationForwardMany, Input{Table: "room_bookings", Column: "room_id", Columns: []string{"room_id", "valid_at"}}, "roomBookingsByRoomIdAndValidAt"},
		{KindEnumValueName, Input{Value: "very happy"}, "VERY_HAPPY"},
		{KindEnumValueName, Input{Value: "1st"}, "_1ST"},
		{KindDomainTypeName, Input{Schema: "app", Domain: "email_address"}, "EmailAddress"},
		{KindFunctionRecord, Input{Function: "order_status_counts"}, "OrderStatusCountsRecord"},
		{KindTypeName, Input{Table: "purchases"}, "Purchase"},
		{KindTypeName, Input{Table: "order_statuses"}, "OrderStatus"},
		{KindTypeName, Input{Table: "spatial_ref_sys"}, "SpatialRefSys"},
		{KindFunctionResultField, Input{Function: "publish_post", Value: "Post"}, "post"},
		{KindFunctionResultField, Input{Function: "publish_posts", Value: "Post", IsList: true}, "posts"},
		{KindFunctionResultField, Input{Function: "add_numbers", Value: "Int"}, "integer"},
		{KindFunctionResultField, Input{Function: "lengths", Value: "Int", IsList: true}, "integers"},
		{KindFunctionResultField, Input{Function: "f", Value: "Float"}, "float"},
		{KindFunctionResultField, Input{Function: "f", Value: "Boolean"}, "boolean"},
		{KindFunctionResultField, Input{Function: "f", Value: "String"}, "string"},
		{KindFunctionResultField, Input{Function: "f", Value: "BigInt"}, "bigInt"},
		{KindFunctionResultField, Input{Function: "f", Value: "GeoJSON"}, "geoJson"},
		{KindFunctionResultField, Input{Function: "f", Value: "JSON"}, "json"},
		{KindFunctionResultField, Input{Function: "f", Value: "UUID", IsList: true}, "uuids"},
		{KindFunctionResultField, Input{Function: "stats"}, "result"},
		{KindFunctionResultField, Input{Function: "stats", IsList: true}, "results"},
		{KindFunctionResultField, Input{Function: "f", Column: "new_total", Value: "Int"}, "newTotal"},
	}
	for _, c := range cases {
		if got := Default(c.kind, c.in); got != c.want {
			t.Errorf("Default(%s, %+v) = %q, want %q", c.kind, c.in, got, c.want)
		}
	}
}

func TestCamelWords(t *testing.T) {
	for in, want := range map[string]string{
		"Post":              "post",
		"GeoJSON":           "geoJson",
		"JSONData":          "jsonData",
		"KeyValueHash":      "keyValueHash",
		"Int4Range":         "int4Range",
		"PublishPostRecord": "publishPostRecord",
		"snake_case_name":   "snakeCaseName",
		"":                  "",
	} {
		if got := CamelWords(in); got != want {
			t.Errorf("CamelWords(%q) = %q, want %q", in, got, want)
		}
	}
}
