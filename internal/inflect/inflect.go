// Package inflect defines the naming pipeline for every generated GraphQL
// identifier. All names produced by the schema builder flow through a chain
// of InflectionHook implementations, so plugins can override any name without
// touching core code.
package inflect

import (
	"strings"
	"unicode"
)

// Kind identifies which name is being generated.
type Kind string

const (
	KindTypeName         Kind = "type_name"           // table -> object type (User)
	KindAllRowsField     Kind = "all_rows_field"      // table -> collection connection query (allUsers)
	KindRowByPKField     Kind = "row_by_pk_field"     // table -> single lookup (userById)
	KindRowByUniqueField Kind = "row_by_unique_field" // unique lookup (userByEmail)
	KindFieldName        Kind = "field_name"          // column -> field (firstName)
	KindEnumTypeName     Kind = "enum_type_name"      // pg enum -> GraphQL enum type
	KindEnumValueName    Kind = "enum_value_name"     // pg enum value -> GraphQL enum value
	KindFilterTypeName     Kind = "filter_type_name"      // UserFilter
	KindOrderByTypeName    Kind = "order_by_type_name"    // UsersOrderBy
	KindDistinctOnTypeName Kind = "distinct_on_type_name" // UsersDistinctOn
	KindCreateMutation   Kind = "create_mutation"     // createUser
	KindUpdateMutation   Kind = "update_mutation"     // updateUserById (primary key)
	KindDeleteMutation   Kind = "delete_mutation"     // deleteUserById (primary key)
	// The by-unique variants are distinct kinds so naming plugins can shorten
	// the primary-key mutation without colliding with unique-constraint ones.
	KindUpdateByUniqueMutation Kind = "update_by_unique_mutation" // updateUserByEmail
	KindDeleteByUniqueMutation Kind = "delete_by_unique_mutation" // deleteUserByEmail
	KindUpsertMutation         Kind = "upsert_mutation"           // upsertUserById (primary key)
	KindUpsertByUniqueMutation Kind = "upsert_by_unique_mutation" // upsertUserByEmail
	KindCreateManyMutation     Kind = "create_many_mutation"      // createUsers
	KindUpdateManyMutation     Kind = "update_many_mutation"      // updateUsers (filtered)
	KindDeleteManyMutation     Kind = "delete_many_mutation"      // deleteUsers (filtered)
	KindCreateInput      Kind = "create_input"        // UserCreateInput
	KindUpdateInput      Kind = "update_input"        // UserUpdateInput
	KindPayloadTypeName  Kind = "payload_type_name"   // CreateUserPayload
	KindRelationForward  Kind = "relation_forward"    // FK column -> parent object field (author)
	KindRelationBackward Kind = "relation_backward"   // reverse FK -> child list field (postsByAuthorId)
	KindFunctionField    Kind = "function_field"      // pg function -> query/mutation field
	KindFunctionInput    Kind = "function_input"      // volatile function -> input object type (LoginInput)
	KindFunctionPayload  Kind = "function_payload"    // volatile function -> payload object type (LoginPayload)
	KindComputedField    Kind = "computed_field"      // row-type function -> field on the row's type (postCount)
)

// Input carries everything a hook may need to build a name.
type Input struct {
	Schema     string   // pg schema name
	Table      string   // pg table/view name
	Column     string   // pg column name (when applicable)
	Columns    []string // multi-column keys
	Constraint string   // pg constraint name backing the name (relation kinds)
	Function   string   // pg function name (when applicable)
	Enum       string   // pg enum type name
	Value      string   // enum value
	IsList     bool
}

// Next continues the chain; the last Next is the default inflector.
type Next func(kind Kind, in Input) string

// Default is the built-in PostGraphile-flavoured verbose naming.
func Default(kind Kind, in Input) string {
	switch kind {
	case KindTypeName:
		return UpperCamel(Singularize(in.Table))
	case KindAllRowsField:
		return "all" + UpperCamel(Pluralize(Singularize(in.Table)))
	case KindRowByPKField:
		return LowerCamel(Singularize(in.Table)) + "By" + byColumns(in.Columns)
	case KindRowByUniqueField:
		return LowerCamel(Singularize(in.Table)) + "By" + byColumns(in.Columns)
	case KindFieldName:
		return LowerCamel(in.Column)
	case KindEnumTypeName:
		return UpperCamel(in.Enum)
	case KindEnumValueName:
		return EnumValue(in.Value)
	case KindFilterTypeName:
		return UpperCamel(Singularize(in.Table)) + "Filter"
	case KindOrderByTypeName:
		return UpperCamel(Pluralize(Singularize(in.Table))) + "OrderBy"
	case KindDistinctOnTypeName:
		return UpperCamel(Pluralize(Singularize(in.Table))) + "DistinctOn"
	case KindCreateMutation:
		return "create" + UpperCamel(Singularize(in.Table))
	case KindUpdateMutation, KindUpdateByUniqueMutation:
		return "update" + UpperCamel(Singularize(in.Table)) + "By" + byColumns(in.Columns)
	case KindDeleteMutation, KindDeleteByUniqueMutation:
		return "delete" + UpperCamel(Singularize(in.Table)) + "By" + byColumns(in.Columns)
	case KindUpsertMutation, KindUpsertByUniqueMutation:
		return "upsert" + UpperCamel(Singularize(in.Table)) + "By" + byColumns(in.Columns)
	case KindCreateManyMutation:
		return "create" + UpperCamel(Pluralize(Singularize(in.Table)))
	case KindUpdateManyMutation:
		return "update" + UpperCamel(Pluralize(Singularize(in.Table)))
	case KindDeleteManyMutation:
		return "delete" + UpperCamel(Pluralize(Singularize(in.Table)))
	case KindCreateInput:
		return UpperCamel(Singularize(in.Table)) + "CreateInput"
	case KindUpdateInput:
		return UpperCamel(Singularize(in.Table)) + "UpdateInput"
	case KindPayloadTypeName:
		return UpperCamel(in.Column) + UpperCamel(Singularize(in.Table)) + "Payload" // Column carries the verb here
	case KindRelationForward:
		return LowerCamel(strings.TrimSuffix(strings.TrimSuffix(in.Column, "_id"), "Id"))
	case KindRelationBackward:
		return LowerCamel(Pluralize(Singularize(in.Table))) + "By" + byColumns(in.Columns)
	case KindFunctionField:
		return LowerCamel(in.Function)
	case KindFunctionInput:
		return UpperCamel(in.Function) + "Input"
	case KindFunctionPayload:
		return UpperCamel(in.Function) + "Payload"
	case KindComputedField:
		return LowerCamel(strings.TrimPrefix(in.Function, in.Table+"_"))
	}
	return LowerCamel(in.Table)
}

func byColumns(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = UpperCamel(c)
	}
	return strings.Join(parts, "And")
}

// UpperCamel converts snake_case to UpperCamelCase.
func UpperCamel(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		switch {
		case r == '_' || r == '-' || r == ' ':
			up = true
		case up:
			b.WriteRune(unicode.ToUpper(r))
			up = false
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// LowerCamel converts snake_case to lowerCamelCase.
func LowerCamel(s string) string {
	c := UpperCamel(s)
	if c == "" {
		return c
	}
	return strings.ToLower(c[:1]) + c[1:]
}

// EnumValue converts a Postgres enum label to a GraphQL enum value name.
func EnumValue(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case unicode.IsLetter(r):
			if unicode.IsUpper(r) && i > 0 && b.Len() > 0 {
				prev := rune(s[i-1])
				if unicode.IsLower(prev) {
					b.WriteRune('_')
				}
			}
			b.WriteRune(unicode.ToUpper(r))
		case unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "_"
	}
	if unicode.IsDigit(rune(out[0])) {
		out = "_" + out
	}
	return out
}

// Irregular noun forms, matched on the trailing word of snake_case names so
// organisation_person -> organisation_people. Plugins can override the rest.
var irregularPlurals = map[string]string{
	"person": "people",
	"child":  "children",
}

var irregularSingulars = func() map[string]string {
	out := make(map[string]string, len(irregularPlurals))
	for s, p := range irregularPlurals {
		out[p] = s
	}
	return out
}()

// irregular replaces the trailing word of s using the given form map, or
// returns "" when no irregular applies.
func irregular(s string, forms map[string]string) string {
	for from, to := range forms {
		if s == from {
			return to
		}
		if strings.HasSuffix(s, "_"+from) {
			return s[:len(s)-len(from)] + to
		}
	}
	return ""
}

// Singularize applies simple English singularization rules — enough for
// common table names; plugins can override anything it gets wrong.
func Singularize(s string) string {
	if out := irregular(s, irregularSingulars); out != "" {
		return out
	}
	if irregular(s, irregularPlurals) != "" {
		return s // already singular (person stays person, not perso)
	}
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 3:
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "ses") || strings.HasSuffix(s, "xes") || strings.HasSuffix(s, "zes") || strings.HasSuffix(s, "ches") || strings.HasSuffix(s, "shes"):
		return s[:len(s)-2]
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss"):
		return s[:len(s)-1]
	}
	return s
}

// Pluralize applies simple English pluralization rules.
func Pluralize(s string) string {
	if out := irregular(s, irregularPlurals); out != "" {
		return out
	}
	if irregular(s, irregularSingulars) != "" {
		return s // already plural (people stays people)
	}
	switch {
	case strings.HasSuffix(s, "y") && len(s) > 1 && !isVowel(s[len(s)-2]):
		return s[:len(s)-1] + "ies"
	case strings.HasSuffix(s, "s") || strings.HasSuffix(s, "x") || strings.HasSuffix(s, "z") || strings.HasSuffix(s, "ch") || strings.HasSuffix(s, "sh"):
		return s + "es"
	default:
		return s + "s"
	}
}

func isVowel(b byte) bool {
	switch b {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}
