package compile

import "github.com/vektah/gqlparser/v2/ast"

// SkipByDirectives reports whether a selection is excluded by its @skip /
// @include directives per the GraphQL spec: excluded when any @skip has
// if: true or any @include has if: false (with both present, the selection
// survives only when @skip is false and @include is true). The `if` argument
// is Boolean! and validated upstream; an unresolvable value keeps the
// selection.
func SkipByDirectives(dl ast.DirectiveList, vars map[string]any) bool {
	for _, d := range dl {
		var keepWhen bool
		switch d.Name {
		case "skip":
			keepWhen = false
		case "include":
			keepWhen = true
		default:
			continue
		}
		arg := d.Arguments.ForName("if")
		if arg == nil {
			continue
		}
		v, err := arg.Value.Value(vars)
		if err != nil {
			continue
		}
		if b, ok := v.(bool); ok && b != keepWhen {
			return true
		}
	}
	return false
}
