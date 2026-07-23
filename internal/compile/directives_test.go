package compile_test

import (
	"testing"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbq/internal/compile"
)

func dir(name string, val *ast.Value) *ast.Directive {
	return &ast.Directive{
		Name:      name,
		Arguments: ast.ArgumentList{{Name: "if", Value: val}},
	}
}

func boolVal(b bool) *ast.Value {
	raw := "false"
	if b {
		raw = "true"
	}
	return &ast.Value{Kind: ast.BooleanValue, Raw: raw}
}

func varVal(name string) *ast.Value {
	return &ast.Value{Kind: ast.Variable, Raw: name}
}

func TestSkipByDirectives(t *testing.T) {
	vars := map[string]any{"yes": true, "no": false}
	cases := []struct {
		name string
		dl   ast.DirectiveList
		want bool
	}{
		{"none", nil, false},
		{"skip true", ast.DirectiveList{dir("skip", boolVal(true))}, true},
		{"skip false", ast.DirectiveList{dir("skip", boolVal(false))}, false},
		{"include true", ast.DirectiveList{dir("include", boolVal(true))}, false},
		{"include false", ast.DirectiveList{dir("include", boolVal(false))}, true},
		{"skip var true", ast.DirectiveList{dir("skip", varVal("yes"))}, true},
		{"include var false", ast.DirectiveList{dir("include", varVal("no"))}, true},
		{"both keep", ast.DirectiveList{dir("skip", boolVal(false)), dir("include", boolVal(true))}, false},
		{"both skip wins", ast.DirectiveList{dir("skip", boolVal(true)), dir("include", boolVal(true))}, true},
		{"both include wins", ast.DirectiveList{dir("skip", boolVal(false)), dir("include", boolVal(false))}, true},
		{"other directive", ast.DirectiveList{dir("deprecated", boolVal(true))}, false},
		{"missing var keeps", ast.DirectiveList{dir("skip", varVal("absent"))}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compile.SkipByDirectives(tc.dl, vars); got != tc.want {
				t.Errorf("SkipByDirectives = %v, want %v", got, tc.want)
			}
		})
	}
}
