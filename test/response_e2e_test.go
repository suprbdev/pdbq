package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
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

// responseKeys returns the keys of the JSON object raw in document order.
func responseKeys(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not an object: %s", raw)
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
	return keys
}

// TestResponseFieldOrder checks that nested objects keep the selection's
// field order (jsonb would sort keys by length, then bytewise), including
// objects wider than one json_build_object call accepts.
func TestResponseFieldOrder(t *testing.T) {
	hs := testServer(t, nil)
	res := post(t, hs.URL, `{allPosts(first: 1, orderBy: ID_ASC) {totalCount nodes {title id __typename userByAuthorId {fullName email id}} pageInfo {hasNextPage endCursor}}}`, nil)
	requireNoErrors(t, res)
	conn := res.Data["allPosts"]
	if got, want := responseKeys(t, conn), []string{"totalCount", "nodes", "pageInfo"}; !slices.Equal(got, want) {
		t.Errorf("connection keys %v, want %v: %s", got, want, conn)
	}
	var parsed struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	mustUnmarshal(t, conn, &parsed)
	if len(parsed.Nodes) != 1 {
		t.Fatalf("nodes: %s", conn)
	}
	if got, want := responseKeys(t, parsed.Nodes[0]), []string{"title", "id", "__typename", "userByAuthorId"}; !slices.Equal(got, want) {
		t.Errorf("node keys %v, want %v", got, want)
	}
	var node struct {
		Author json.RawMessage `json:"userByAuthorId"`
	}
	mustUnmarshal(t, parsed.Nodes[0], &node)
	if got, want := responseKeys(t, node.Author), []string{"fullName", "email", "id"}; !slices.Equal(got, want) {
		t.Errorf("author keys %v, want %v", got, want)
	}

	res = post(t, hs.URL, `{allPosts(first: 2, orderBy: ID_ASC) {c: totalCount a: nodes {id} b: nodes {title} e: edges {n: node {id} cursor} p: pageInfo {more: hasNextPage}}}`, nil)
	requireNoErrors(t, res)
	aliased := res.Data["allPosts"]
	if got, want := responseKeys(t, aliased), []string{"c", "a", "b", "e", "p"}; !slices.Equal(got, want) {
		t.Errorf("aliased connection keys %v, want %v: %s", got, want, aliased)
	}
	if got, want := normalize(t, aliased), `{"a":[{"id":1},{"id":2}],"b":[{"title":"Notes on the Analytical Engine"},{"title":"Unpublished draft"}],"c":4,"e":[{"cursor":"WyJQb3N0IiwgMV0=","n":{"id":1}},{"cursor":"WyJQb3N0IiwgMl0=","n":{"id":2}}],"p":{"more":true}}`; got != want {
		t.Errorf("aliased connection:\n got %s\nwant %s", got, want)
	}

	var sel strings.Builder
	var want []string
	for i := 90; i > 0; i-- {
		key := fmt.Sprintf("k%02d", i)
		field := "id"
		if i%2 == 0 {
			field = "email"
		}
		fmt.Fprintf(&sel, "%s: %s ", key, field)
		want = append(want, key)
	}
	res = post(t, hs.URL, `{userById(id: 1) {`+sel.String()+`}}`, nil)
	requireNoErrors(t, res)
	row := res.Data["userById"]
	if got := responseKeys(t, row); !slices.Equal(got, want) {
		t.Errorf("wide object keys %v, want %v", got, want)
	}
	var values map[string]any
	mustUnmarshal(t, row, &values)
	if values["k90"] != "ada@example.com" || values["k01"] != float64(1) || len(values) != 90 {
		t.Errorf("wide object values: %s", row)
	}
}
