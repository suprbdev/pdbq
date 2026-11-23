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
// and the LISTEN connection.
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
	built, err := a.BuildSchema(ctx, cat)
	if err != nil {
		return nil, err
	}
	ex := a.Executor(built)
	srv, err := server.New(a.Config.Server, server.NewAuthenticator(a.Config.RLS, a.Log), ex, built.SDL, a.Log)
	if err != nil {
		return nil, err
	}
	srv.SetReadiness(a.pool.Ping)
	if a.Config.Server.Subscriptions {
		hub := subscribe.NewHub(subscribe.PoolDialer(a.pool), a.Log)
		srv.SetSubscriptions(hub)
		a.watchMu.Lock()
		a.stops = append(a.stops, func() {
			srv.CloseWebSockets()
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
		wctx, cancel := context.WithCancel(ctx)
		w := &watch.Watcher{
			Pool:         a.pool,
			Schemas:      a.Config.Schema.Schemas,
			Channel:      a.Config.Watch.Channel,
			TriggerName:  "pdbq_watch",
			InitialHash:  catHash,
			PollInterval: a.Config.Watch.PollInterval,
			Log:          a.Log,
			OnChange: func(cat *introspect.Catalog) {
				// Atomic swap: in-flight requests keep the old executor.
				nb, err := a.BuildSchema(wctx, cat)
				a.metrics.SchemaRebuilt(err)
				if err != nil {
					a.Log.Error("watch: schema rebuild failed", "err", err)
					return
				}
				srv.Swap(a.Executor(nb), nb.SDL)
			},
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
	return srv, nil
}
