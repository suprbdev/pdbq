package main

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/suprbdev/pdbcore/cache"
	"github.com/suprbdev/pdbcore/testutil"
)

func subcommand(t *testing.T, parent *cobra.Command, name string) *cobra.Command {
	t.Helper()
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	t.Fatalf("no %s command", name)
	return nil
}

func TestCommandFlagsStayOutOfConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	dump := subcommand(t, schemaCmd(), "dump")
	if err := dump.ParseFlags([]string{"-o", "x.cache", "--database.url", "postgres://flag/db"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadCLIConfig(dump)
	if err != nil {
		t.Fatalf("schema dump flags: %v", err)
	}
	if cfg.Database.URL != "postgres://flag/db" {
		t.Errorf("database.url = %q", cfg.Database.URL)
	}

	query := queryCmd()
	if err := query.ParseFlags([]string{"--var", "a=1", "--operation", "Q", "--print-sql", "--roles.switch=true"}); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCLIConfig(query); err != nil {
		t.Fatalf("query flags: %v", err)
	}

	serve := serveCmd()
	if err := serve.ParseFlags([]string{"--auth.mode", "none", "--roles.anonymous", "web"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(serve)
	if err != nil {
		t.Fatalf("serve flags: %v", err)
	}
	if cfg.Auth.Mode != "none" || cfg.Roles.Anonymous != "web" || !cfg.Roles.Switch {
		t.Errorf("identity = %+v %+v", cfg.Auth, cfg.Roles)
	}
}

// runCLI runs the pdbq command line with args and returns its stdout and
// error.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := rootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// fixtureCache writes the fixture catalog as a schema cache, so pdbq query
// runs without a database for fields resolved in memory.
func fixtureCache(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.cache")
	if err := cache.Save(path, testutil.FixtureCatalog(), cache.Writer{Product: "pdbq"}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestQueryRunsOnDefaultConfig(t *testing.T) {
	cachePath := fixtureCache(t)
	t.Chdir(t.TempDir())
	out, err := runCLI(t, "query", "{ __typename }", "--schema.cache_path", cachePath)
	if err != nil {
		t.Fatalf("query on the default config: %v", err)
	}
	if !strings.Contains(out, `"__typename": "Query"`) {
		t.Errorf("output %s", out)
	}
}

func TestQueryGraphQLErrorsExitTwo(t *testing.T) {
	cachePath := fixtureCache(t)
	t.Chdir(t.TempDir())
	out, err := runCLI(t, "query", "{ nope }", "--schema.cache_path", cachePath)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Fatalf("err = %v, want exit 2", err)
	}
	if !strings.Contains(out, `"errors"`) {
		t.Errorf("output %s", out)
	}
}

func TestQueryIdentityFlagsRejected(t *testing.T) {
	cachePath := fixtureCache(t)
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"--role", "none"},
		{"--role", "pg_read_all_data"},
		{"--role", "bad-role"},
		{"--claims", "[1]"},
		{"--claims", "{"},
		{"--claims", "null"},
	} {
		_, err := runCLI(t, append([]string{"query", "{ __typename }", "--schema.cache_path", cachePath}, args...)...)
		var ee *exitError
		if err == nil || errors.As(err, &ee) {
			t.Errorf("%v: err = %v, want a usage error", args, err)
		}
	}
}

func TestParseClaims(t *testing.T) {
	got, err := parseClaims(`{"user_id": 1, "org": "a"}`)
	if err != nil || got["user_id"] != float64(1) || got["org"] != "a" {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := parseClaims(""); got != nil || err != nil {
		t.Errorf("empty: %v, %v", got, err)
	}
}

func TestOldFlagsNameTheirReplacement(t *testing.T) {
	for args, want := range map[string]string{
		"serve --rls.enabled=false":                           "unknown flag: --rls.enabled: moved to --roles.switch",
		"serve --server.max_cost=5":                           "unknown flag: --server.max_cost: moved to --limits.max_cost",
		"serve --plugins.settings.advanced-filters.enabled=1": "moved to --plugins.settings.advanced_filters.enabled",
		"plugins list --server.subscriptions":                 "moved to --subscriptions.enabled",
		"serve --transactions.mutations":                      "removed: mutations always run in a transaction",
	} {
		cmd := rootCmd()
		cmd.SetArgs(strings.Fields(args))
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", args, err, want)
		}
	}
}
