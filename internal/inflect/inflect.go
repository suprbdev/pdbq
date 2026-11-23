// Package inflect defines the naming pipeline for every generated GraphQL
// identifier. All names produced by the schema builder flow through a chain
// of InflectionHook implementations, so plugins can override any name without
// touching core code. The chain types and string helpers live in
// pdbcore/inflect (shared with pdbr); this package owns pdbq's name kinds and
// the PostGraphile-flavoured Default.
package inflect

import (
	"strings"
	"unicode"

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
	KindPayloadDeletedNodeID   Kind = "payload_deleted_node_id"   // DeleteUserPayload -> deletedUserNodeId
	KindRelationForward        Kind = "relation_forward"          // FK -> parent object field (userByAuthorId)
	KindRelationBackward       Kind = "relation_backward"         // reverse FK -> child list field (postsByAuthorId)
	KindRelationForwardMany    Kind = "relation_forward_many"     // PERIOD FK -> parent connection field (roomBookingsByRoomIdAndValidAt)
	KindFunctionField          Kind = "function_field"            // pg function -> query/mutation field
	KindFunctionInput          Kind = "function_input"            // volatile function -> input object type (LoginInput)
	KindFunctionPayload        Kind = "function_payload"          // volatile function -> payload object type (LoginPayload)
	KindComputedField          Kind = "computed_field"            // row-type function -> field on the row's type (postCount)
	KindFunctionRecord         Kind = "function_record"           // OUT/TABLE-parameter function -> its row object type (SearchStatsRecord)
	KindDomainTypeName         Kind = "domain_type_name"          // pg domain -> custom scalar (Email)
	// KindFunctionResultField names the result field of a volatile function's
	// payload (post, integer, posts). Column carries the function's single
	// output parameter, if it has exactly one; Value the result's named
	// GraphQL type, empty for a record of several output parameters; IsList
	// whether the result is a list (SETOF or an array).
	KindFunctionResultField Kind = "function_result_field"
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
	case KindDomainTypeName:
		return inflect.UpperCamel(in.Domain)
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
	case KindPayloadDeletedNodeID:
		return "deleted" + inflect.UpperCamel(inflect.Singularize(in.Table)) + "NodeId"
	case KindRelationForward:
		return inflect.LowerCamel(inflect.Singularize(in.Table)) + "By" + inflect.ByColumns(in.Columns)
	case KindRelationBackward, KindRelationForwardMany:
		return inflect.LowerCamel(inflect.Pluralize(inflect.Singularize(in.Table))) + "By" + inflect.ByColumns(in.Columns)
	case KindFunctionField:
		return inflect.LowerCamel(in.Function)
	case KindFunctionInput:
		return inflect.UpperCamel(in.Function) + "Input"
	case KindFunctionPayload:
		return inflect.UpperCamel(in.Function) + "Payload"
	case KindFunctionResultField:
		name := functionResultBase(in)
		if in.IsList {
			return inflect.Pluralize(name)
		}
		return name
	case KindComputedField:
		return inflect.LowerCamel(strings.TrimPrefix(in.Function, in.Table+"_"))
	case KindFunctionRecord:
		return inflect.UpperCamel(in.Function) + "Record"
	}
	return inflect.LowerCamel(in.Table)
}

// functionResultBase is PostGraphile's singular result field name: the
// single output parameter's name, a word for the built-in scalars Int,
// Float, Boolean and String, result for a record, and otherwise the
// lowerCamel type name.
func functionResultBase(in Input) string {
	if in.Column != "" {
		return inflect.LowerCamel(in.Column)
	}
	switch in.Value {
	case "":
		return "result"
	case "Int":
		return "integer"
	case "Float":
		return "float"
	case "Boolean":
		return "boolean"
	case "String":
		return "string"
	}
	return CamelWords(in.Value)
}

// CamelWords lowerCamel-cases an identifier by its words, splitting at case
// changes, so acronyms fold like other words: GeoJSON -> geoJson, UUID ->
// uuid, BigInt -> bigInt, KeyValueHash -> keyValueHash.
func CamelWords(s string) string {
	var words []string
	runes := []rune(s)
	start := 0
	for i := 1; i <= len(runes); i++ {
		if i < len(runes) && !wordBreak(runes, i) {
			continue
		}
		if w := strings.Trim(string(runes[start:i]), "_- "); w != "" {
			words = append(words, w)
		}
		start = i
	}
	var b strings.Builder
	for i, w := range words {
		w = strings.ToLower(w)
		if i > 0 {
			w = strings.ToUpper(w[:1]) + w[1:]
		}
		b.WriteString(w)
	}
	return b.String()
}

// wordBreak reports whether a new word starts at runes[i]: after a
// separator, at a lower-to-upper or letter-digit change, and before the last
// capital of an acronym followed by a lowercase letter (JSONData -> JSON
// Data).
func wordBreak(runes []rune, i int) bool {
	prev, cur := runes[i-1], runes[i]
	switch {
	case isSep(prev) || isSep(cur):
		return true
	case unicode.IsLower(prev) && unicode.IsUpper(cur):
		return true
	case unicode.IsDigit(prev) != unicode.IsDigit(cur):
		return true
	case unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(runes) && unicode.IsLower(runes[i+1]):
		return true
	}
	return false
}

func isSep(r rune) bool { return r == '_' || r == '-' || r == ' ' }
