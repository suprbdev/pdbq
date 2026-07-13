// Public embedding surface: aliases that let external modules construct a
// Config, implement plugin hooks, and mount pdbq inside their own HTTP mux.
// The implementation stays under internal/; aliases are the contract.
package pdbq

import (
	"context"
	"net/http"

	flag "github.com/spf13/pflag"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/introspect"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/server"
	"github.com/suprbdev/pdbq/internal/watch"
)

// Configuration. Section types carry a Config suffix to avoid clashing with
// unrelated root-package names.
type (
	Config         = config.Config
	DatabaseConfig = config.Database
	ServerConfig   = config.Server
	SchemaConfig   = config.Schema
	FiltersConfig  = config.Filters
	RLSConfig      = config.RLS
	AuthConfig     = config.Auth
	TXConfig       = config.TX
	WatchConfig    = config.Watch
	ErrorsConfig   = config.Errors
	PluginsConfig  = config.Plugins
	LogConfig      = config.Log
)

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config { return config.DefaultConfig() }

// LoadConfig builds the effective config: defaults < YAML < PDBQ_* env <
// flags (nil flags is fine).
func LoadConfig(path string, flags *flag.FlagSet) (Config, error) {
	return config.Load(path, flags)
}

// Plugin hook surface.
type (
	Plugin         = plugin.Plugin
	CatalogHook    = plugin.CatalogHook
	InflectionHook = plugin.InflectionHook
	SchemaHook     = plugin.SchemaHook
	CompileHook    = plugin.CompileHook
	RequestHook    = plugin.RequestHook
	PluginRegistry = plugin.Registry
)

// Types referenced by hook signatures and the executor.
type (
	Catalog        = introspect.Catalog
	InflectKind    = inflect.Kind
	InflectInput   = inflect.Input
	InflectNext    = inflect.Next
	SchemaBuilder  = schema.Builder
	Built          = schema.Built
	CompileFunc    = compile.Func
	CompileRequest = compile.Request
	Statement      = compile.Statement
	Executor       = exec.Executor
	Operation      = exec.Operation
	Result         = exec.Result
	ExecRequest    = exec.Request
)

// Handler runs the boot pipeline (introspect -> plugins -> schema ->
// executor) and returns the GraphQL http.Handler for embedding in a larger
// mux. Watch mode, when enabled, hot-swaps the schema behind the returned
// handler exactly as Serve does. The caller owns the listener; server.addr
// and request timeouts still apply to the handler's internals
// (http.TimeoutHandler), but read/idle timeouts are the embedder's job.
func (a *App) Handler(ctx context.Context) (http.Handler, error) {
	srv, err := a.buildServer(ctx)
	if err != nil {
		return nil, err
	}
	return srv.Handler(), nil
}

// buildServer is the shared boot pipeline behind Serve and Handler.
func (a *App) buildServer(ctx context.Context) (*server.Server, error) {
	if !a.Config.RLS.Enabled {
		a.Log.Warn("RLS IS DISABLED — all requests run with the privileged connection role; do not use in production")
	}
	cat, err := a.LoadCatalog(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.Connect(ctx); err != nil {
		return nil, err
	}
	built, err := a.BuildSchema(ctx, cat)
	if err != nil {
		return nil, err
	}
	ex := a.Executor(built)
	srv, err := server.New(a.Config.Server, server.NewAuthenticator(a.Config.RLS), ex, built.SDL, a.Log)
	if err != nil {
		return nil, err
	}
	if a.Config.Watch.Enabled {
		w := &watch.Watcher{
			Pool:         a.pool,
			Schemas:      a.Config.Schema.Schemas,
			Channel:      a.Config.Watch.Channel,
			PollInterval: a.Config.Watch.PollInterval,
			Log:          a.Log,
			OnChange: func(cat *introspect.Catalog) {
				// Atomic swap: in-flight requests keep the old executor.
				nb, err := a.BuildSchema(ctx, cat)
				if err != nil {
					a.Log.Error("watch: schema rebuild failed", "err", err)
					return
				}
				srv.Swap(a.Executor(nb), nb.SDL)
			},
		}
		go func() {
			if err := w.Run(ctx); err != nil {
				a.Log.Error("watch: stopped", "err", err)
			}
		}()
	}
	return srv, nil
}
