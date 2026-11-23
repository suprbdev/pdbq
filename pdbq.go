// Package pdbq wires the subsystems into a runnable application and is the
// embedding surface for third-party plugins: import pdbq as a library and
// run pdbq.New(cfg, pdbq.WithPlugins(myPlugin)).Serve(ctx).
package pdbq

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/suprbdev/pdbcore/cache"
	"github.com/suprbdev/pdbcore/httpx"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/metrics"
	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/advancedfilters"
	"github.com/suprbdev/pdbq/internal/plugins/aggregates"
	contribplugin "github.com/suprbdev/pdbq/internal/plugins/contrib"
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

	watchMu sync.Mutex
	stops   []func()

	watchReady <-chan struct{}

	metrics     *metrics.Metrics
	metricsErr  error
	metricsOnce sync.Once
	instruments *exec.Metrics

	// contractMetrics holds one Metrics per contract, labelled contract,
	// on metricsReg, when the App created its own metrics with contracts
	// configured; contractInstruments their executor counters.
	contractMetrics     map[string]*metrics.Metrics
	contractInstruments map[string]*exec.Metrics
	metricsReg          prometheus.Registerer

	// mint signs auth.mint_type results; mintErr is the error reading its
	// secret file, returned by Serve, Handler and Query.
	mint    exec.MintOptions
	mintErr error

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

// WithMetrics makes the App record into m instead of the instance it
// creates when metrics.enabled is set, for embedders that build m on their
// own registry (metrics.Options.Registerer and ConstLabels). The App
// instruments its handler, pool, executor and watcher with m whatever
// metrics.enabled says; metrics.path and metrics.addr still decide where
// Serve and Handler expose it. Each App needs its own m: the product
// counters cannot be registered twice.
func WithMetrics(m *metrics.Metrics) Option {
	return func(a *App) { a.metrics = m }
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
	a.mint, a.mintErr = mintOptions(cfg.Auth)
	if a.metrics == nil && cfg.Metrics.Enabled {
		if len(cfg.Contracts) > 0 {
			a.metricsErr = a.newContractMetrics()
		} else {
			a.metrics, a.metricsErr = metrics.New(metrics.Options{Namespace: "pdbq"})
		}
	}
	a.readOnly.Store(cfg.Server.ReadOnly)
	return a
}

// mintOptions resolves the JWT minting options of auth.mint_type, reading
// the secret file.
func mintOptions(a config.Auth) (exec.MintOptions, error) {
	if a.MintType == "" {
		return exec.MintOptions{}, nil
	}
	secret, err := a.JWT.ResolveSecret()
	if err != nil {
		return exec.MintOptions{}, fmt.Errorf("auth.jwt.secret_file: %w", err)
	}
	s, n, _ := strings.Cut(a.MintType, ".")
	return exec.MintOptions{
		Schema:   s,
		Type:     n,
		Secret:   secret,
		Issuer:   a.JWT.Issuer,
		Audience: a.JWT.Audience,
	}, nil
}

// newContractMetrics creates one Metrics per contract on a shared registry,
// told apart by a contract label; the App's own Metrics is the first
// contract's, whose Handler serves them all.
func (a *App) newContractMetrics() error {
	reg, err := metrics.NewRegistry()
	if err != nil {
		return err
	}
	a.metricsReg = reg
	a.contractMetrics = map[string]*metrics.Metrics{}
	for _, ct := range a.Config.Contracts {
		m, err := metrics.New(metrics.Options{
			Namespace:   "pdbq",
			Registerer:  reg,
			ConstLabels: prometheus.Labels{"contract": ct.Name},
		})
		if err != nil {
			return err
		}
		a.contractMetrics[ct.Name] = m
		if a.metrics == nil {
			a.metrics = m
		}
	}
	return nil
}

// metricsFor returns the Metrics recording contract name: its own with
// per-contract metrics, else the App's.
func (a *App) metricsFor(name string) *metrics.Metrics {
	if m, ok := a.contractMetrics[name]; ok {
		return m
	}
	return a.metrics
}

// instrumentsFor returns the executor counters of contract name, as
// metricsFor.
func (a *App) instrumentsFor(name string) *exec.Metrics {
	if m, ok := a.contractInstruments[name]; ok {
		return m
	}
	return a.instruments
}

// Metrics returns the App's Prometheus instruments: the instance passed to
// WithMetrics, the one created for metrics.enabled, or nil when metrics are
// off. Its Handler serves the metrics, for embedders that mount them
// themselves.
func (a *App) Metrics() *metrics.Metrics { return a.metrics }

// initMetrics registers the pool collector and the executor's product
// counters, once, after Connect.
func (a *App) initMetrics() error {
	a.metricsOnce.Do(func() {
		if a.metricsErr != nil || a.metrics == nil {
			return
		}
		if a.contractMetrics != nil {
			if a.metricsErr = metrics.RegisterPoolOn(a.metricsReg, "pdbq", a.pool); a.metricsErr != nil {
				return
			}
			a.contractInstruments = map[string]*exec.Metrics{}
			for name, m := range a.contractMetrics {
				if a.contractInstruments[name], a.metricsErr = exec.NewMetrics(m); a.metricsErr != nil {
					return
				}
			}
			return
		}
		if a.pool != nil {
			if a.metricsErr = a.metrics.RegisterPool(a.pool); a.metricsErr != nil {
				return
			}
		}
		a.instruments, a.metricsErr = exec.NewMetrics(a.metrics)
	})
	return a.metricsErr
}

// SetReadOnly toggles read-only mode at runtime: when on, every mutation
// operation is rejected before it compiles or touches the database. Safe for
// concurrent use; the setting persists across watch-mode schema swaps.
func (a *App) SetReadOnly(v bool) { a.readOnly.Store(v) }

// ReadOnly reports whether read-only mode is currently on.
func (a *App) ReadOnly() bool { return a.readOnly.Load() }

func builtinPlugins(cfg config.Config) []plugin.Plugin {
	s := cfg.Plugins.Settings
	return []plugin.Plugin{
		smartcomments.New(),
		simplenames.New(s.SimpleNames),
		advancedfilters.New(s.AdvancedFilters),
		postgisplugin.New(s.Postgis),
		contribplugin.New(s.Contrib),
		aggregates.New(cfg.Aggregates, s.AdvancedFilters.RelationsOptIn),
		nestedmutations.New(s.NestedMutations),
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

// WatchReady returns a channel closed once the schema watcher started by the
// last Handler or Serve call is listening for DDL; changes committed after it
// closes are never missed. It is already closed when watch mode is off or no
// server has been built.
func (a *App) WatchReady() <-chan struct{} {
	a.watchMu.Lock()
	defer a.watchMu.Unlock()
	if a.watchReady == nil {
		c := make(chan struct{})
		close(c)
		return c
	}
	return a.watchReady
}

// Close stops any running schema watcher and waits for it to release its
// connection, closes subscription WebSockets and the LISTEN connection, then
// closes the pool. It is idempotent, needs no prior Serve or
// Handler call, and does not require the context given to them to be
// cancelled first.
func (a *App) Close() {
	a.watchMu.Lock()
	stops := a.stops
	a.stops = nil
	a.watchMu.Unlock()
	for _, stop := range stops {
		stop()
	}
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
		FilterIndexedOnly:   a.Config.Filters.IndexedOnly,
		AllowColumns:        a.Config.Filters.AllowColumns,
		Functions:           a.Config.Schema.Functions,
		ExposePartitions:    a.Config.Schema.ExposePartitions,
		DomainScalars:       a.Config.Schema.Domains,
		JWTType:             a.Config.Auth.MintType,
		Subscriptions:       a.Config.Subscriptions.Enabled,
		Live:                a.Config.Live.Enabled,
		IncrementalDelivery: a.Config.Incremental.Enabled,
		MaxHeadlineDocument: a.Config.Limits.MaxHeadlineDocument,
		Logger:              a.Log,
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

// BuildContractSchema builds the schema of the configured contract name
// from a filtered copy of cat, leaving cat unmodified so every contract
// can be built from one catalog; "" builds the full schema exactly as
// BuildSchema does (modifying cat).
func (a *App) BuildContractSchema(ctx context.Context, cat *introspect.Catalog, name string) (*schema.Built, error) {
	if name == "" {
		return a.BuildSchema(ctx, cat)
	}
	ct, err := a.Config.Contract(name)
	if err != nil {
		return nil, err
	}
	view, sum := ct.Spec().View(cat)
	a.Log.Debug("contract view", "contract", name, "hidden_tables", sum.Tables, "hidden_columns", sum.Columns,
		"hidden_functions", sum.Functions, "hidden_foreign_keys", sum.ForeignKeys)
	built, err := a.BuildSchema(ctx, view)
	if err != nil {
		return nil, fmt.Errorf("contract %s: %w", name, err)
	}
	return built, nil
}

// Executor assembles the request executor for a built schema.
func (a *App) Executor(built *schema.Built) *exec.Executor {
	return a.executor(built, a.instruments)
}

func (a *App) executor(built *schema.Built, instruments *exec.Metrics) *exec.Executor {
	disabled := a.Config.DisabledPlugins()
	compiler := compile.New(built)
	compileFn := a.Registry.CompileChain(disabled, compiler.Compile)
	var hooks []exec.RequestHook
	for _, h := range a.Registry.RequestHooks(disabled) {
		hooks = append(hooks, h)
	}
	iso := pgexec.ParseIsolation(a.Config.TX.Isolation)
	return exec.New(a.pool, built, compileFn, hooks, exec.Options{
		MaxDepth:                   a.Config.Limits.MaxDepth,
		MaxCost:                    a.Config.Limits.MaxCost,
		MaxPageSize:                a.Config.Limits.MaxPageSize,
		TxPerRequest:               a.Config.TX.PerRequest,
		PartialMutations:           a.Config.TX.Partial.Enabled,
		PartialMaxItems:            a.Config.TX.Partial.MaxItems,
		TxRetries:                  a.Config.TX.MaxRetries,
		Isolation:                  iso,
		ClaimsPrefix:               a.Config.Claims.Prefix,
		ClaimsJSONSetting:          a.Config.Claims.JSONSetting,
		DevErrors:                  a.Config.Errors.Detail == "dev",
		StrictErrors:               a.Config.Errors.Detail == "strict",
		DisableIntrospection:       !a.Config.Server.Introspection,
		Mint:                       a.mint,
		DocumentCacheSize:          a.Config.Server.DocumentCache.Size,
		DocumentCacheBytes:         a.Config.Server.DocumentCache.Bytes,
		DocumentCacheMaxEntryBytes: a.Config.Server.DocumentCache.MaxEntryBytes,
		MaxQueryTokens:             a.Config.Limits.MaxQueryTokens,
		MaxInputDepth:              a.Config.Limits.MaxInputDepth,
		MaxSelections:              a.Config.Limits.MaxSelections,
		MaxIntrospectionBytes:      a.Config.Limits.MaxIntrospectionBytes,
		MaxFieldComparisons:        a.Config.Limits.MaxFieldComparisons,
		Metrics:                    instruments,
		Logger:                     a.Log,
		ReadOnly:                   &a.readOnly,
		Incremental: exec.IncrementalOptions{
			Enabled:         a.Config.Incremental.Enabled,
			MaxDeferred:     a.Config.Incremental.MaxDeferred,
			StreamBatchSize: a.Config.Incremental.StreamBatchSize,
		},
	})
}

// Serve runs the HTTP server (and watch mode when enabled) until ctx ends.
// With metrics on and metrics.addr set, it also serves metrics.path on that
// second listener; either listener failing stops both.
func (a *App) Serve(ctx context.Context) error {
	srv, err := a.buildServer(ctx)
	if err != nil {
		return err
	}
	if a.metrics == nil || a.Config.Metrics.Addr == "" {
		return srv.ListenAndServe(ctx)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	mux := http.NewServeMux()
	mux.Handle("GET "+a.Config.Metrics.Path, a.metrics.Handler())
	errc := make(chan error, 2)
	go func() { errc <- srv.ListenAndServe(ctx) }()
	go func() {
		errc <- httpx.ListenAndServe(ctx, mux, httpx.ServeOptions{
			Addr:     a.Config.Metrics.Addr,
			Log:      a.Log,
			LogAttrs: []any{"metrics", a.Config.Metrics.Path},
		})
	}()
	err = <-errc
	cancel()
	if err2 := <-errc; err == nil {
		err = err2
	}
	return err
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
	if a.mintErr != nil {
		return nil, a.mintErr
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
	return ex.Execute(ctx, exec.Request{Query: query, OperationName: operation, Variables: vars}), nil
}
