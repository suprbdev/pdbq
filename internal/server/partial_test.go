package server

import (
	"encoding/json"
	"testing"
)

func TestPartialMutationsExtension(t *testing.T) {
	for ext, want := range map[string]bool{
		``:                                  false,
		`{}`:                                false,
		`{"partialMutations":true}`:         true,
		`{"partialMutations":false}`:        false,
		`{"partialMutations":"true"}`:       false,
		`{"partialMutations":1}`:            false,
		`{"persistedQuery":{"version":1}}`:  false,
		`not json`:                          false,
		`{"partialMutations":true,"x":[1]}`: true,
	} {
		r := gqlHTTPRequest{Extensions: json.RawMessage(ext)}
		if got := r.partialMutations(); got != want {
			t.Errorf("extensions %q: partialMutations() = %v, want %v", ext, got, want)
		}
	}
}
