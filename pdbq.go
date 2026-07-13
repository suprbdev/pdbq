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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbq/internal/cache"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/introspect"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/advancedfilters"
	"github.com/suprbdev/pdbq/internal/plugins/nestedmutations"
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
	exec  *exec.Executor
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
	return a
}

func builtinPlugins(cfg config.Config) []plugin.Plugin {
	return []plugin.Plugin{
		smartcomments.New(),
		simplenames.New(),
		advancedfilters.New(cfg.Plugins.Settings["advanced-filters"]),
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
	pcfg, err := pgxpool.ParseConfig(a.Config.Database.URL)
	if err != nil {
		return fmt.Errorf("database url: %w", err)
	}
	if a.Config.Database.MaxConns > 0 {
		pcfg.MaxConns = a.Config.Database.MaxConns
	}
	pcfg.ConnConfig.ConnectTimeout = a.Config.Database.ConnectTimeout
	if t := a.Config.Database.StatementTimeout; t > 0 {
		if pcfg.ConnConfig.RuntimeParams == nil {
			pcfg.ConnConfig.RuntimeParams = map[string]string{}
		}
		pcfg.ConnConfig.RuntimeParams["statement_timeout"] = fmt.Sprintf("%d", t.Milliseconds())
	}
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
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
	iso := pgx.ReadCommitted
	switch a.Config.TX.Isolation {
	case "repeatable_read":
		iso = pgx.RepeatableRead
	case "serializable":
		iso = pgx.Serializable
	}
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
		MaxDepth:     a.Config.Server.MaxDepth,
		MaxCost:      a.Config.Server.MaxCost,
		MaxPageSize:  a.Config.Server.MaxPageSize,
		TxMutations:  a.Config.TX.Mutations,
		TxPerRequest: a.Config.TX.PerRequest,
		TxRetries:    a.Config.TX.MaxRetries,
		Isolation:    iso,
		RLS:          a.Config.RLS.Enabled,
		ClaimsPrefix: a.Config.RLS.ClaimsPrefix,
		DevErrors:    a.Config.Errors.Detail == "dev",
		Mint:         mint,
		Logger:       a.Log,
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
