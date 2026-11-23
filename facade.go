// Public embedding surface: aliases that let external modules construct a
// Config, implement plugin hooks, and mount pdbq inside their own HTTP mux.
// The implementation stays under internal/; aliases are the contract.
package pdbq

import (
	"context"
	"fmt"
	"net/http"

	flag "github.com/spf13/pflag"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/watch"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/live"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/server"
	"github.com/suprbdev/pdbq/internal/subscribe"
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
	MetricsConfig  = config.Metrics
	ContractConfig = config.Contract
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
// handler exactly as Serve does. With metrics on, the handler is
// instrumented and serves metrics.path, unless metrics.addr is set: then the
// embedder mounts App.Metrics().Handler() itself. The caller owns the
// listener; server.addr and request timeouts still apply to the handler's
// internals (http.TimeoutHandler), but read/idle timeouts are the
// embedder's job. With server.subscriptions on, the handler also serves
// WebSocket subscriptions on /graphql; App.Close closes those connections
// and the LISTEN connection. With contracts configured, the handler serves
// each contract's schema under its path instead (see docs/contracts.md).
func (a *App) Handler(ctx context.Context) (http.Handler, error) {
	srv, err := a.buildServer(ctx)
	if err != nil {
		return nil, err
	}
	return srv.Handler(), nil
}

// httpServer is what Serve and Handler run: a Server, or a server.Group
// with one Server per contract.
type httpServer interface {
	Handler() http.Handler
	ListenAndServe(ctx context.Context) error
}

// buildServer is the shared boot pipeline behind Serve and Handler.
func (a *App) buildServer(ctx context.Context) (httpServer, error) {
	if !a.Config.RLS.Enabled {
		a.Log.Warn("RLS IS DISABLED — all requests run with the privileged connection role; do not use in production")
	}
	if a.Config.RLS.Enabled && a.Config.RLS.Auth.Mode == "headers" && len(a.Config.RLS.Auth.TrustedProxies) == 0 {
		a.Log.Warn("headers auth mode without rls.auth.trusted_proxies — ANY client reaching this port can forge claims and roles; set trusted_proxies or ensure only the gateway can connect")
	}
	if a.Config.RLS.Enabled && len(a.Config.RLS.AllowedRoles) == 0 &&
		(a.Config.RLS.Auth.Mode == "jwt" || a.Config.RLS.Auth.Mode == "headers") {
		a.Log.Warn("rls.allowed_roles is empty — any role named in a verified claim will be assumed, including privileged database roles; list the roles clients may request")
	}
	cat, err := a.LoadCatalog(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.Connect(ctx); err != nil {
		return nil, err
	}
	if err := a.initMetrics(); err != nil {
		return nil, err
	}
	var catHash string
	if a.Config.Watch.Enabled {
		if catHash, err = cat.Hash(); err != nil {
			return nil, fmt.Errorf("watch: catalog hash: %w", err)
		}
	}
	if len(a.Config.Contracts) > 0 {
		return a.buildContractServer(ctx, cat, catHash)
	}
	built, err := a.BuildSchema(ctx, cat)
	if err != nil {
		return nil, err
	}
	ex := a.Executor(built)
	srv, err := server.New(a.serverConfig(), server.NewAuthenticator(a.Config.RLS, a.Log), ex, built.SDL, a.Log)
	if err != nil {
		return nil, err
	}
	srv.SetReadiness(a.pool.Ping)
	var tracker *live.Tracker
	if a.Config.Server.Subscriptions {
		hub := subscribe.NewHub(subscribe.PoolDialer(a.pool), a.Log)
		srv.SetSubscriptions(hub)
		if tracker, err = a.liveTracker(ctx, hub); err != nil {
			hub.Close()
			return nil, err
		}
		if tracker != nil {
			srv.SetLive(tracker)
		}
		a.watchMu.Lock()
		a.stops = append(a.stops, func() {
			srv.CloseWebSockets()
			if tracker != nil {
				tracker.Close()
			}
			hub.Close()
		})
		a.watchMu.Unlock()
	}
	if a.Config.Metrics.Addr == "" {
		srv.SetMetrics(a.metrics, a.Config.Metrics.Path)
	} else {
		srv.SetMetrics(a.metrics, "")
	}
	if a.Config.Watch.Enabled {
		a.startWatch(ctx, catHash, func(wctx context.Context, cat *introspect.Catalog) {
			// Atomic swap: in-flight requests keep the old executor.
			nb, err := a.BuildSchema(wctx, cat)
			a.metrics.SchemaRebuilt(err)
			if err != nil {
				a.Log.Error("watch: schema rebuild failed", "err", err)
				return
			}
			srv.Swap(a.Executor(nb), nb.SDL)
			a.installLive(wctx, tracker)
		})
	}
	return srv, nil
}

// serverConfig is the server section with the defaults that depend on other
// sections resolved.
func (a *App) serverConfig() config.Server {
	cfg := a.Config.Server
	cfg.IncrementalMaxResponses = a.Config.IncrementalResponses()
	return cfg
}

// buildContractServer builds one schema, executor and Server per contract
// from cat and mounts them on a server.Group; watch mode rebuilds every
// contract.
func (a *App) buildContractServer(ctx context.Context, cat *introspect.Catalog, catHash string) (*server.Group, error) {
	authn := server.NewAuthenticator(a.Config.RLS, a.Log)
	group := server.NewGroup(a.serverConfig(), a.Log)
	group.SetReadiness(a.pool.Ping)
	if a.Config.Metrics.Addr == "" {
		group.SetMetrics(a.metrics, a.Config.Metrics.Path)
	}
	servers := make([]*server.Server, len(a.Config.Contracts))
	for i, ct := range a.Config.Contracts {
		built, err := a.BuildContractSchema(ctx, cat, ct.Name)
		if err != nil {
			return nil, err
		}
		srv, err := server.New(a.serverConfig(), authn, a.executor(built, a.instrumentsFor(ct.Name)), built.SDL, a.Log)
		if err != nil {
			return nil, err
		}
		srv.SetMetrics(a.metricsFor(ct.Name), "")
		group.Add(ct.URLPath(""), srv)
		servers[i] = srv
		a.Log.Info("contract ready", "contract", ct.Name, "path", ct.URLPath(""))
	}
	var tracker *live.Tracker
	if a.Config.Server.Subscriptions {
		hub := subscribe.NewHub(subscribe.PoolDialer(a.pool), a.Log)
		var err error
		if tracker, err = a.liveTracker(ctx, hub); err != nil {
			hub.Close()
			return nil, err
		}
		for _, srv := range servers {
			srv.SetSubscriptions(hub)
			if tracker != nil {
				srv.SetLive(tracker)
			}
		}
		a.watchMu.Lock()
		a.stops = append(a.stops, func() {
			group.CloseWebSockets()
			if tracker != nil {
				tracker.Close()
			}
			hub.Close()
		})
		a.watchMu.Unlock()
	}
	if a.Config.Watch.Enabled {
		a.startWatch(ctx, catHash, func(wctx context.Context, cat *introspect.Catalog) {
			for i, ct := range a.Config.Contracts {
				nb, err := a.BuildContractSchema(wctx, cat, ct.Name)
				a.metricsFor(ct.Name).SchemaRebuilt(err)
				if err != nil {
					a.Log.Error("watch: schema rebuild failed", "contract", ct.Name, "err", err)
					continue
				}
				servers[i].Swap(a.executor(nb, a.instrumentsFor(ct.Name)), nb.SDL)
			}
			a.installLive(wctx, tracker)
		})
	}
	return group, nil
}

// liveTracker returns the live query tracker listening through hub, with
// the change triggers installed, or nil when server.live is off.
func (a *App) liveTracker(ctx context.Context, hub *subscribe.Hub) (*live.Tracker, error) {
	if !a.Config.Server.Live {
		return nil, nil
	}
	m, err := live.NewMetrics(a.metrics)
	if err != nil {
		return nil, err
	}
	t := live.NewTracker(hub, live.Options{
		Channel:     a.Config.Server.LiveChannel,
		Throttle:    a.Config.Server.LiveThrottle,
		Concurrency: a.Config.Server.LiveConcurrency,
		Metrics:     m,
		Log:         a.Log,
	})
	a.installLive(ctx, t)
	return t, nil
}

// installLive installs the change triggers of live queries (or, with
// server.live_install_triggers off, finds the tables that have them) and
// hands the tracked tables to t. A nil t does nothing.
func (a *App) installLive(ctx context.Context, t *live.Tracker) {
	if t == nil {
		return
	}
	tracked, err := live.Install(ctx, a.pool, live.InstallOptions{
		Channel: a.Config.Server.LiveChannel,
		Schemas: a.Config.Schema.Schemas,
		Create:  a.Config.Server.LiveInstallTriggers,
		Log:     a.Log,
	})
	if err != nil {
		a.Log.Warn("live: cannot install the change triggers; live queries refresh only for tables that already have them", "err", err)
	}
	t.SetTracked(tracked)
}

// startWatch runs the schema watcher until ctx ends or the App closes,
// calling rebuild with every changed catalog.
func (a *App) startWatch(ctx context.Context, catHash string, rebuild func(context.Context, *introspect.Catalog)) {
	wctx, cancel := context.WithCancel(ctx)
	w := &watch.Watcher{
		Pool:         a.pool,
		Schemas:      a.Config.Schema.Schemas,
		Channel:      a.Config.Watch.Channel,
		TriggerName:  "pdbq_watch",
		InitialHash:  catHash,
		PollInterval: a.Config.Watch.PollInterval,
		Log:          a.Log,
		OnChange:     func(cat *introspect.Catalog) { rebuild(wctx, cat) },
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		go func() {
			select {
			case <-w.Ready():
				a.Log.Info("watch: ready")
			case <-wctx.Done():
			}
		}()
		if err := w.Run(wctx); err != nil {
			a.Log.Error("watch: stopped", "err", err)
		}
	}()
	a.watchMu.Lock()
	a.watchReady = w.Ready()
	a.stops = append(a.stops, func() {
		cancel()
		<-done
	})
	a.watchMu.Unlock()
}
