package main

import (
	"testing"

	"github.com/spf13/cobra"
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
