package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	build := func(v string, ok bool) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: v}}, ok
		}
	}
	tests := []struct {
		name    string
		stamped string
		read    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{"stamped wins", "v1.2.3", build("v9.9.9", true), "v1.2.3"},
		{"go install version", "dev", build("v1.2.3", true), "v1.2.3"},
		{"devel build", "dev", build("(devel)", true), "dev"},
		{"empty version", "dev", build("", true), "dev"},
		{"no build info", "dev", build("", false), "dev"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVersion(tc.stamped, tc.read); got != tc.want {
				t.Fatalf("resolveVersion(%q) = %q, want %q", tc.stamped, got, tc.want)
			}
		})
	}
}
