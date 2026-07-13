package exec

import (
	"testing"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

func TestIsSerializationFailure(t *testing.T) {
	cases := []struct {
		name string
		errs gqlerror.List
		want bool
	}{
		{"empty", nil, false},
		{"serialization failure", gqlerror.List{
			{Message: "database error", Extensions: map[string]any{"code": "40001"}},
		}, true},
		{"deadlock", gqlerror.List{
			{Message: "deadlock detected", Extensions: map[string]any{"code": "40P01"}},
		}, true},
		{"constraint violation", gqlerror.List{
			{Message: "duplicate key", Extensions: map[string]any{"code": "23505"}},
		}, false},
		{"no extensions", gqlerror.List{{Message: "compile: bad"}}, false},
		{"mixed", gqlerror.List{
			{Message: "x", Extensions: map[string]any{"code": "22P02"}},
			{Message: "y", Extensions: map[string]any{"code": "40001"}},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSerializationFailure(tc.errs); got != tc.want {
				t.Fatalf("isSerializationFailure = %v, want %v", got, tc.want)
			}
		})
	}
}
