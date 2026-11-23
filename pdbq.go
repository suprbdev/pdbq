// Package pdbq wires the subsystems into a runnable application and is the
// embedding surface for third-party plugins: import pdbq as a library and
// run pdbq.New(cfg, pdbq.WithPlugins(myPlugin)).Serve(ctx).
package pdbq

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbcore/cache"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/advancedfilters"
	"github.com/suprbdev/pdbq/internal/plugins/nestedmutations"
	postgisplugin "github.com/suprbdev/pdbq/internal/plugins/postgis"
	"github.com/suprbdev/pdbq/internal/plugins/simplenames"
	"github.com/suprbdev/pdbq/internal/plugins/smartcomments"
	"github.com/suprbdev/pdbq/internal/schema"
)

// App is a configured pdbq instance.
type App struct {
	Config   config.Config
	Registry *plugin.Registry
	Log      *slog.Logger

	pool  *pgxpool.Pool
	built *schema.Built

	// readOnly lives on the App (not in exec.Options as a value) so a single
	// flag is shared by every executor the App builds — flipping it at
	// runtime survives watch-mode schema swaps.
	readOnly atomic.Bool
}

// Option customizes an App.
type Option func(*App)

// WithPlugins registers additional plugins (the library embedding pattern).
func WithPlugins(plugins ...plugin.Plugin) Option {
	return func(a *App) {
		for _, p := range plugins {
			a.Registry.Add(p)
		}
	}
}

// WithLogger overrides the default logger.
func WithLogger(l *slog.Logger) Option {
	return func(a *App) { a.Log = l }
}

// New creates an App with the built-in plugins registered.
func New(cfg config.Config, opts ...Option) *App {
	a := &App{
		Config:   cfg,
		Registry: plugin.NewRegistry(builtinPlugins(cfg)...),
		Log:      newLogger(cfg.Log),
	}
	for _, o := range opts {
		o(a)
	}
	a.readOnly.Store(cfg.Server.ReadOnly)
	return a
}

// SetReadOnly toggles read-only mode at runtime: when on, every mutation
// operation is rejected before it compiles or touches the database. Safe for
// concurrent use; the setting persists across watch-mode schema swaps.
func (a *App) SetReadOnly(v bool) { a.readOnly.Store(v) }

// ReadOnly reports whether read-only mode is currently on.
func (a *App) ReadOnly() bool { return a.readOnly.Load() }

func builtinPlugins(cfg config.Config) []plugin.Plugin {
	return []plugin.Plugin{
		smartcomments.New(),
		simplenames.New(),
		advancedfilters.New(cfg.Plugins.Settings["advanced-filters"]),
		postgisplugin.New(cfg.Plugins.Settings["postgis"]),
		nestedmutations.New(cfg.Plugins.Settings["nested-mutations"]),
	}
}

// BuiltinPluginNames lists built-ins for `pdbq plugins list`.
func (a *App) Plugins() []plugin.Plugin {
	return a.Registry.All()
}

func newLogger(cfg config.Log) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.Format == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

// Connect opens the pool.
func (a *App) Connect(ctx context.Context) error {
	if a.pool != nil {
		return nil
	}
	pool, err := pgexec.Connect(ctx, pgexec.PoolConfig{
		URL:              a.Config.Database.URL,
		MaxConns:         a.Config.Database.MaxConns,
		ConnectTimeout:   a.Config.Database.ConnectTimeout,
		StatementTimeout: a.Config.Database.StatementTimeout,
	})
	if err != nil {
		return err
	}
	a.pool = pool
	return nil
}

// Pool exposes the connection pool (nil before Connect).
func (a *App) Pool() *pgxpool.Pool { return a.pool }

// Close releases resources.
func (a *App) Close() {
	if a.pool != nil {
		a.pool.Close()
	}
}

// LoadCatalog introspects the database, or loads the schema cache when
// configured.
func (a *App) LoadCatalog(ctx context.Context) (*introspect.Catalog, error) {
	if p := a.Config.Schema.CachePath; p != "" {
		a.Log.Info("loading schema cache", "path", p)
		return cache.Load(p)
	}
	if err := a.Connect(ctx); err != nil {
		return nil, err
	}
	start := time.Now()
	cat, err := introspect.Introspect(ctx, a.pool, a.Config.Schema.Schemas)
	if err != nil {
		return nil, err
	}
	a.Log.Info("introspected", "tables", len(cat.Tables), "enums", len(cat.Enums),
		"functions", len(cat.Functions), "took", time.Since(start).Round(time.Millisecond))
	return cat, nil
}

// BuildSchema runs the full pipeline: catalog hooks -> inflection -> default
// generation -> schema hooks -> SDL validation.
func (a *App) BuildSchema(ctx context.Context, cat *introspect.Catalog) (*schema.Built, error) {
	disabled := a.Config.DisabledPlugins()
	if err := a.Registry.TransformCatalog(ctx, disabled, cat); err != nil {
		return nil, fmt.Errorf("catalog hooks: %w", err)
	}
	b := schema.New(cat, a.Registry.Inflector(disabled), schema.Options{
		FilterIndexedOnly: a.Config.Filters.IndexedOnly,
		AllowColumns:      a.Config.Filters.AllowColumns,
		Functions:         a.Config.Schema.Functions,
		JWTType:           a.Config.RLS.Auth.JWTType,
		Logger:            a.Log,
	})
	if err := a.Registry.TransformSchema(ctx, disabled, b); err != nil {
		return nil, fmt.Errorf("schema hooks: %w", err)
	}
	built, err := b.Build()
	if err != nil {
		return nil, err
	}
	a.built = built
	return built, nil
}

// Executor assembles the request executor for a built schema.
func (a *App) Executor(built *schema.Built) *exec.Executor {
	disabled := a.Config.DisabledPlugins()
	compiler := compile.New(built)
	compileFn := a.Registry.CompileChain(disabled, compiler.Compile)
	var hooks []exec.RequestHook
	for _, h := range a.Registry.RequestHooks(disabled) {
		hooks = append(hooks, h)
	}
	iso := pgexec.ParseIsolation(a.Config.TX.Isolation)
	var mint exec.MintOptions
	if jt := a.Config.RLS.Auth.JWTType; jt != "" {
		s, n, _ := strings.Cut(jt, ".")
		mint = exec.MintOptions{
			Schema:   s,
			Type:     n,
			Secret:   a.Config.RLS.Auth.JWTSecret,
			Issuer:   a.Config.RLS.Auth.JWTIssuer,
			Audience: a.Config.RLS.Auth.JWTAudience,
		}
	}
	return exec.New(a.pool, built, compileFn, hooks, exec.Options{
		MaxDepth:             a.Config.Server.MaxDepth,
		MaxCost:              a.Config.Server.MaxCost,
		MaxPageSize:          a.Config.Server.MaxPageSize,
		TxMutations:          a.Config.TX.Mutations,
		TxPerRequest:         a.Config.TX.PerRequest,
		TxRetries:            a.Config.TX.MaxRetries,
		Isolation:            iso,
		RLS:                  a.Config.RLS.Enabled,
		ClaimsPrefix:         a.Config.RLS.ClaimsPrefix,
		DevErrors:            a.Config.Errors.Detail == "dev",
		StrictErrors:         a.Config.Errors.Detail == "strict",
		DisableIntrospection: a.Config.Server.DisableIntrospection,
		Mint:                 mint,
		Logger:               a.Log,
		ReadOnly:             &a.readOnly,
	})
}

// Serve runs the HTTP server (and watch mode when enabled) until ctx ends.
func (a *App) Serve(ctx context.Context) error {
	srv, err := a.buildServer(ctx)
	if err != nil {
		return err
	}
	return srv.ListenAndServe(ctx)
}

// CompileQuery compiles a one-off GraphQL request into its per-root-field SQL
// statements without executing anything (the `pdbq query --print-sql` path).
// With a schema cache configured it needs no database connection at all.
func (a *App) CompileQuery(ctx context.Context, query string, vars map[string]any, operation string) ([]exec.CompiledField, error) {
	cat, err := a.LoadCatalog(ctx)
	if err != nil {
		return nil, err
	}
	built, err := a.BuildSchema(ctx, cat)
	if err != nil {
		return nil, err
	}
	ex := a.Executor(built)
	return ex.CompileRequest(ctx, exec.Request{Query: query, OperationName: operation, Variables: vars})
}

// Query executes a one-off GraphQL request (the `pdbq query` path).
func (a *App) Query(ctx context.Context, query string, vars map[string]any, operation string) (*exec.Result, error) {
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
	return ex.Execute(ctx, exec.Request{Query: query, OperationName: operation, Variables: vars}), nil
}
