package pdbq_test

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
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
