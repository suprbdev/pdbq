package server

import (
	"testing"

	"github.com/suprbdev/pdbq/internal/config"
)

func TestResponseKeepsRootFieldOrder(t *testing.T) {
	h := apqServer(t, config.Server{MaxBodyBytes: 1 << 20}).Handler()
	rec := postJSON(t, h, `{"query":"{zed: __typename mid: __type(name: \"User\") {name} alpha: __typename}"}`)
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type %q", got)
	}
	want := `{"data":{"zed":"Query","mid":{"name":"User"},"alpha":"Query"}}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// TestResponseErrorFormat pins error bodies to the bytes the encoding/json
// writer produced, for a cache miss and the cached repeat.
func TestResponseErrorFormat(t *testing.T) {
	h := apqServer(t, config.Server{MaxBodyBytes: 1 << 20}).Handler()
	cases := map[string]string{
		`{"query":"{nope}"}`:                           `{"errors":[{"message":"Cannot query field \"nope\" on type \"Query\". Did you mean \"node\"?","locations":[{"line":1,"column":2}]}]}`,
		`{"query":"{userById(id: 1) {"}`:               `{"errors":[{"message":"Expected Name, found \u003cEOF\u003e","locations":[{"line":1,"column":19}]}]}`,
		`{"query":"{__typename}","operationName":"X"}`: `{"errors":[{"message":"operation \"X\" not found"}]}`,
	}
	for body, want := range cases {
		for range 2 {
			if got := postJSON(t, h, body).Body.String(); got != want+"\n" {
				t.Fatalf("%s:\n got %s\nwant %s", body, got, want)
			}
		}
	}
}
