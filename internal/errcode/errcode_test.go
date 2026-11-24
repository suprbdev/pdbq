package errcode

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

func TestPG(t *testing.T) {
	if got := PG("23505"); got != "pg:23505" || !got.IsPG() {
		t.Errorf("PG = %q", got)
	}
	if Internal.IsPG() || Code("pg:").IsPG() {
		t.Error("IsPG accepted a non-database code")
	}
}

func TestErrorfAndOf(t *testing.T) {
	err := Errorf(ReadOnly, "no %s", "writes")
	if err.Message != "no writes" || Of(err) != ReadOnly {
		t.Fatalf("got %q %q", err.Message, Of(err))
	}
	if Of(fmt.Errorf("wrapped: %w", err)) != ReadOnly {
		t.Error("Of does not unwrap")
	}
	if Of(errors.New("plain")) != "" || Of(gqlerror.Errorf("plain")) != "" {
		t.Error("uncoded error has a code")
	}
}

func TestWithKeepsExistingCodeAndCopies(t *testing.T) {
	shared := map[string]any{"file": "x"}
	a := &gqlerror.Error{Message: "a", Extensions: shared}
	With(a, Timeout)
	if Of(a) != Timeout || len(shared) != 1 {
		t.Fatalf("code %q, shared map %v", Of(a), shared)
	}
	With(a, Internal)
	if Of(a) != Timeout {
		t.Errorf("With replaced a code: %q", Of(a))
	}
	if With(nil, Internal) != nil {
		t.Error("With(nil) != nil")
	}
}

func TestWithAll(t *testing.T) {
	list := gqlerror.List{gqlerror.Errorf("a"), Errorf(NotFound, "b")}
	WithAll(list, GraphQLValidationFailed)
	if Of(list[0]) != GraphQLValidationFailed || Of(list[1]) != NotFound {
		t.Errorf("codes %q %q", Of(list[0]), Of(list[1]))
	}
	single := gqlerror.Errorf("c")
	WithAll(single, GraphQLParseFailed)
	if Of(single) != GraphQLParseFailed {
		t.Errorf("code %q", Of(single))
	}
}

// TestDocumented keeps docs/errors.md and the vocabulary in step: every
// code has a row in the code table, and every row is a code.
func TestDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../docs/errors.md")
	if err != nil {
		t.Fatal(err)
	}
	section := regexp.MustCompile("(?s)## Code vocabulary\n(.*?)\n## ").FindSubmatch(doc)
	if section == nil {
		t.Fatal("docs/errors.md has no Code vocabulary section")
	}
	rows := map[string]bool{}
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z_:<>A-Z]+)` \\|").FindAllSubmatch(section[1], -1) {
		rows[string(m[1])] = true
	}
	known := map[string]bool{"pg:<SQLSTATE>": true, "code": true}
	for _, c := range All {
		known[string(c)] = true
		if !rows[string(c)] {
			t.Errorf("code %s has no row in docs/errors.md", c)
		}
	}
	for r := range rows {
		if !known[r] {
			t.Errorf("docs/errors.md documents unknown code %s", r)
		}
	}
	if !rows["pg:<SQLSTATE>"] {
		t.Error("docs/errors.md has no pg:<SQLSTATE> row")
	}
}
