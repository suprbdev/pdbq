// Package inflect defines the naming pipeline for every generated GraphQL
// identifier. All names produced by the schema builder flow through a chain
// of InflectionHook implementations, so plugins can override any name without
// touching core code. The chain types and string helpers live in
// pdbcore/inflect (shared with pdbr); this package owns pdbq's name kinds and
// the PostGraphile-flavoured Default.
package inflect

import (
	"strings"

	"github.com/suprbdev/pdbcore/inflect"
)

// Kind identifies which name is being generated.
type Kind = inflect.Kind

// Input carries everything a hook may need to build a name.
type Input = inflect.Input

// Next continues the chain; the last Next is the default inflector.
type Next = inflect.Next

const (
	KindTypeName           Kind = "type_name"             // table -> object type (User)
	KindAllRowsField       Kind = "all_rows_field"        // table -> collection connection query (allUsers)
	KindRowByPKField       Kind = "row_by_pk_field"       // table -> single lookup (userById)
	KindRowByUniqueField   Kind = "row_by_unique_field"   // unique lookup (userByEmail)
	KindFieldName          Kind = "field_name"            // column -> field (firstName)
	KindEnumTypeName       Kind = "enum_type_name"        // pg enum -> GraphQL enum type
	KindEnumValueName      Kind = "enum_value_name"       // pg enum value -> GraphQL enum value
	KindFilterTypeName     Kind = "filter_type_name"      // UserFilter
	KindConditionTypeName  Kind = "condition_type_name"   // UserCondition
	KindOrderByTypeName    Kind = "order_by_type_name"    // UsersOrderBy
	KindDistinctOnTypeName Kind = "distinct_on_type_name" // UsersDistinctOn
	KindCreateMutation     Kind = "create_mutation"       // createUser
	KindUpdateMutation     Kind = "update_mutation"       // updateUserById (primary key)
	KindDeleteMutation     Kind = "delete_mutation"       // deleteUserById (primary key)
	// The by-unique variants are distinct kinds so naming plugins can shorten
	// the primary-key mutation without colliding with unique-constraint ones.
	KindUpdateByUniqueMutation Kind = "update_by_unique_mutation" // updateUserByEmail
	KindDeleteByUniqueMutation Kind = "delete_by_unique_mutation" // deleteUserByEmail
	KindUpsertMutation         Kind = "upsert_mutation"           // upsertUserById (primary key)
	KindUpsertByUniqueMutation Kind = "upsert_by_unique_mutation" // upsertUserByEmail
	KindCreateManyMutation     Kind = "create_many_mutation"      // createUsers
	KindUpdateManyMutation     Kind = "update_many_mutation"      // updateUsers (filtered)
	KindDeleteManyMutation     Kind = "delete_many_mutation"      // deleteUsers (filtered)
	KindCreateInput            Kind = "create_input"              // UserCreateInput
	KindUpdateInput            Kind = "update_input"              // UserUpdateInput
	KindPayloadTypeName        Kind = "payload_type_name"         // CreateUserPayload
	KindRelationForward        Kind = "relation_forward"          // FK column -> parent object field (author)
	KindRelationBackward       Kind = "relation_backward"         // reverse FK -> child list field (postsByAuthorId)
	KindFunctionField          Kind = "function_field"            // pg function -> query/mutation field
	KindFunctionInput          Kind = "function_input"            // volatile function -> input object type (LoginInput)
	KindFunctionPayload        Kind = "function_payload"          // volatile function -> payload object type (LoginPayload)
	KindComputedField          Kind = "computed_field"            // row-type function -> field on the row's type (postCount)
	KindFunctionRecord         Kind = "function_record"           // OUT/TABLE-parameter function -> its row object type (SearchStatsRecord)
)

// Default is the built-in PostGraphile-flavoured verbose naming.
func Default(kind Kind, in Input) string {
	switch kind {
	case KindTypeName:
		return inflect.UpperCamel(inflect.Singularize(in.Table))
	case KindAllRowsField:
		return "all" + inflect.UpperCamel(inflect.Pluralize(inflect.Singularize(in.Table)))
	case KindRowByPKField:
		return inflect.LowerCamel(inflect.Singularize(in.Table)) + "By" + inflect.ByColumns(in.Columns)
	case KindRowByUniqueField:
		return inflect.LowerCamel(inflect.Singularize(in.Table)) + "By" + inflect.ByColumns(in.Columns)
	case KindFieldName:
		return inflect.LowerCamel(in.Column)
	case KindEnumTypeName:
		return inflect.UpperCamel(in.Enum)
	case KindEnumValueName:
		return inflect.EnumValue(in.Value)
	case KindFilterTypeName:
		return inflect.UpperCamel(inflect.Singularize(in.Table)) + "Filter"
	case KindConditionTypeName:
		return inflect.UpperCamel(inflect.Singularize(in.Table)) + "Condition"
	case KindOrderByTypeName:
		return inflect.UpperCamel(inflect.Pluralize(inflect.Singularize(in.Table))) + "OrderBy"
	case KindDistinctOnTypeName:
		return inflect.UpperCamel(inflect.Pluralize(inflect.Singularize(in.Table))) + "DistinctOn"
	case KindCreateMutation:
		return "create" + inflect.UpperCamel(inflect.Singularize(in.Table))
	case KindUpdateMutation, KindUpdateByUniqueMutation:
		return "update" + inflect.UpperCamel(inflect.Singularize(in.Table)) + "By" + inflect.ByColumns(in.Columns)
	case KindDeleteMutation, KindDeleteByUniqueMutation:
		return "delete" + inflect.UpperCamel(inflect.Singularize(in.Table)) + "By" + inflect.ByColumns(in.Columns)
	case KindUpsertMutation, KindUpsertByUniqueMutation:
		return "upsert" + inflect.UpperCamel(inflect.Singularize(in.Table)) + "By" + inflect.ByColumns(in.Columns)
	case KindCreateManyMutation:
		return "create" + inflect.UpperCamel(inflect.Pluralize(inflect.Singularize(in.Table)))
	case KindUpdateManyMutation:
		return "update" + inflect.UpperCamel(inflect.Pluralize(inflect.Singularize(in.Table)))
	case KindDeleteManyMutation:
		return "delete" + inflect.UpperCamel(inflect.Pluralize(inflect.Singularize(in.Table)))
	case KindCreateInput:
		return inflect.UpperCamel(inflect.Singularize(in.Table)) + "CreateInput"
	case KindUpdateInput:
		return inflect.UpperCamel(inflect.Singularize(in.Table)) + "UpdateInput"
	case KindPayloadTypeName:
		return inflect.UpperCamel(in.Column) + inflect.UpperCamel(inflect.Singularize(in.Table)) + "Payload" // Column carries the verb here
	case KindRelationForward:
		return inflect.LowerCamel(strings.TrimSuffix(strings.TrimSuffix(in.Column, "_id"), "Id"))
	case KindRelationBackward:
		return inflect.LowerCamel(inflect.Pluralize(inflect.Singularize(in.Table))) + "By" + inflect.ByColumns(in.Columns)
	case KindFunctionField:
		return inflect.LowerCamel(in.Function)
	case KindFunctionInput:
		return inflect.UpperCamel(in.Function) + "Input"
	case KindFunctionPayload:
		return inflect.UpperCamel(in.Function) + "Payload"
	case KindComputedField:
		return inflect.LowerCamel(strings.TrimPrefix(in.Function, in.Table+"_"))
	case KindFunctionRecord:
		return inflect.UpperCamel(in.Function) + "Record"
	}
	return inflect.LowerCamel(in.Table)
}
