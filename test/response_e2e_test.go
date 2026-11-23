package e2e

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
)

// TestResponseOrderAndPassthrough checks that root fields come back in
// selection order and that PostgreSQL's JSON reaches the client verbatim.
func TestResponseOrderAndPassthrough(t *testing.T) {
	hs := testServer(t, nil)
	q := `{zed: userById(id: 2) {id} __typename alpha: userById(id: 1) {id email} mid: allUsers(first: 1) {totalCount}}`
	body, _ := json.Marshal(map[string]any{"query": q})
	raw, err := httpPost(hs.URL+"/graphql", body)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("bad response: %v: %s", err, raw)
	}
	dec := json.NewDecoder(bytes.NewReader(env.Data))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"zed", "__typename", "alpha", "mid"}; !slices.Equal(keys, want) {
		t.Fatalf("root field order %v, want %v: %s", keys, want, raw)
	}

	var data map[string]json.RawMessage
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	alpha := data["alpha"]
	if got, want := normalize(t, alpha), `{"email":"ada@example.com","id":1}`; got != want {
		t.Fatalf("alpha = %s, want %s", got, want)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, alpha); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(compact.Bytes(), alpha) {
		t.Fatalf("alpha %s was re-encoded instead of passed through from PostgreSQL", alpha)
	}
}
