package exec

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// legacyEncode is the encoding/json rendering Result used before it was
// written by hand: a map, so keys sorted, through json.Encoder.
func legacyEncode(t *testing.T, r *Result) string {
	t.Helper()
	out := map[string]any{}
	if len(r.Data) > 0 || len(r.Errors) == 0 {
		out["data"] = r.Data
	}
	if len(r.Errors) > 0 {
		out["errors"] = r.Errors
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustJSON(t *testing.T, r *Result) string {
	t.Helper()
	b, err := r.AppendJSON(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Fatalf("invalid JSON: %s", b)
	}
	return string(b)
}

func TestResultRootFieldOrder(t *testing.T) {
	var r Result
	r.Set("zed", json.RawMessage(`{"id": 1}`))
	r.Set("alpha", json.RawMessage(`{"id": 2}`))
	r.Set("mid", json.RawMessage(`null`))
	want := `{"data":{"zed":{"id": 1},"alpha":{"id": 2},"mid":null}}`
	if got := mustJSON(t, &r); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestResultRawPassthrough(t *testing.T) {
	raw := json.RawMessage("{\"html\": \"<b>&amp;</b>\", \"ws\" : [1,\n 2], \"u\": \"\u2028é\"}")
	var r Result
	r.Set("x", raw)
	want := `{"data":{"x":` + string(raw) + `}}`
	if got := mustJSON(t, &r); got != want {
		t.Fatalf("raw bytes were rewritten:\n got %s\nwant %s", got, want)
	}
}

func TestResultKeysWithDirectMapEdits(t *testing.T) {
	var r Result
	r.Set("b", json.RawMessage(`1`))
	r.Set("a", json.RawMessage(`2`))
	r.Set("gone", json.RawMessage(`3`))
	r.Set("b", json.RawMessage(`4`))
	delete(r.Data, "a")
	r.Set("a", json.RawMessage(`2`))
	delete(r.Data, "gone")
	r.Data["zz"] = json.RawMessage(`5`)
	r.Data["hook"] = nil
	if got, want := r.Keys(), []string{"b", "a", "hook", "zz"}; !slices.Equal(got, want) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	if got, want := mustJSON(t, &r), `{"data":{"b":4,"a":2,"hook":null,"zz":5}}`; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestResultEnvelopeMatchesLegacyEncoding(t *testing.T) {
	fieldErr := &gqlerror.Error{
		Message:    `bad <value> & "quote"`,
		Path:       ast.Path{ast.PathName("user")},
		Locations:  []gqlerror.Location{{Line: 1, Column: 2}},
		Extensions: map[string]any{"code": "23505", "detail": "<x>"},
	}
	cases := map[string]*Result{
		"errors only":      {Errors: gqlerror.List{gqlerror.Errorf("operation %q not found", "X")}},
		"empty data":       {Data: map[string]json.RawMessage{}},
		"nil data":         {},
		"empty data+error": {Data: map[string]json.RawMessage{}, Errors: gqlerror.List{fieldErr}},
		"data and errors":  {Data: map[string]json.RawMessage{"user": json.RawMessage(`null`)}, Errors: gqlerror.List{fieldErr}},
		"escaped key":      {Data: map[string]json.RawMessage{"a<b": json.RawMessage(`1`)}},
	}
	for name, r := range cases {
		if got, want := mustJSON(t, r), legacyEncode(t, r); got != want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, want)
		}
	}
}

func TestResultExtensions(t *testing.T) {
	r := &Result{Errors: gqlerror.List{gqlerror.Errorf("boom")}, Extensions: map[string]any{"cost": 3}}
	if got, want := mustJSON(t, r), `{"errors":[{"message":"boom"}],"extensions":{"cost":3}}`; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	r = &Result{Extensions: map[string]any{"cost": 3}}
	r.Set("a", json.RawMessage(`1`))
	if got, want := mustJSON(t, r), `{"data":{"a":1},"extensions":{"cost":3}}`; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestExecuteKeepsSelectionOrder(t *testing.T) {
	e := cacheExecutor(t, 10)
	q := `query { zed: __typename ... on Query { mid: __type(name: "User") { name } } alpha: __typename }`
	for range 2 {
		res := e.Execute(context.Background(), Request{Query: q})
		if len(res.Errors) > 0 {
			t.Fatal(res.Errors)
		}
		b, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"data":{"zed":"Query","mid":{"name":"User"},"alpha":"Query"}}`
		if string(b) != want {
			t.Fatalf("got  %s\nwant %s", b, want)
		}
	}
}
