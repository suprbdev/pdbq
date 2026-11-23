package pdbq_test

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/contribfixture"
)

func TestBuiltinPluginNames(t *testing.T) {
	cfg := pdbq.DefaultConfig()
	var names []string
	for _, p := range pdbq.New(cfg).Plugins() {
		names = append(names, p.Name())
	}
	if !slices.Equal(names, config.BuiltinPluginNames) {
		t.Fatalf("registered plugins %v, config.BuiltinPluginNames %v", names, config.BuiltinPluginNames)
	}
}

func relationFilterSDL(t *testing.T, cfg pdbq.Config) bool {
	t.Helper()
	app := pdbq.New(cfg, pdbq.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	built, err := app.BuildSchema(context.Background(), testutil.FixtureCatalog())
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(built.SDL, "PostToManyFilter")
}

func TestPluginSettingFromEnvReachesPlugin(t *testing.T) {
	t.Setenv("PDBQ_AUTH_MODE", "none")
	t.Setenv("PDBQ_ROLES_ANONYMOUS", "anonymous")
	cfg, err := pdbq.LoadConfig("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !relationFilterSDL(t, cfg) {
		t.Fatal("relation filters missing with the default settings")
	}
	t.Setenv("PDBQ_PLUGINS_SETTINGS_ADVANCED__FILTERS_RELATIONS", "false")
	if cfg, err = pdbq.LoadConfig("", nil); err != nil {
		t.Fatal(err)
	}
	if relationFilterSDL(t, cfg) {
		t.Fatal("PDBQ_PLUGINS_SETTINGS_ADVANCED__FILTERS_RELATIONS=false did not reach the plugin")
	}
}

// TestNamingModes builds the shared fixtures with every built-in plugin in
// both naming modes: neither may hit a relation-name conflict.
func TestNamingModes(t *testing.T) {
	for _, simple := range []bool{false, true} {
		for name, cat := range map[string]func() *introspect.Catalog{
			"fixture": testutil.FixtureCatalog, "contrib": contribfixture.Catalog,
		} {
			cfg := pdbq.DefaultConfig()
			cfg.Schema.Functions = true
			cfg.Aggregates.Enabled = true
			cfg.Plugins.Settings.SimpleNames.Enabled = simple
			app := pdbq.New(cfg, pdbq.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			built, err := app.BuildSchema(context.Background(), cat())
			if err != nil {
				t.Fatalf("%s, simple_names %t: %v", name, simple, err)
			}
			forward, plural := "  userByAuthorId: User", "  allUsers("
			if simple {
				forward, plural = "  author: User", "  users("
			}
			for _, want := range []string{forward, plural} {
				if !strings.Contains(built.SDL, want) {
					t.Errorf("%s, simple_names %t: SDL missing %q", name, simple, want)
				}
			}
		}
	}
}
