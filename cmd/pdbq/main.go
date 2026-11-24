// Command pdbq serves an automatically mapped GraphQL API for a PostgreSQL
// database, and provides schema-cache, config, and one-off query tooling.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	flag "github.com/spf13/pflag"

	"github.com/suprbdev/pdbcore/cache"
	"github.com/suprbdev/pdbcore/confx"
	"github.com/suprbdev/pdbcore/confx/cliflags"
	pgcontrib "github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/plugin"
)

var version = "dev"

func main() {
	if err := rootCmd().Execute(); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// exitError carries a process exit code without a message (the output was
// already written): pdbq query exits 2 when the response has GraphQL
// errors. Commands return it so their deferred cleanup runs before main
// exits.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

// configFlags defines flags that mirror config keys; their names are the
// koanf paths so precedence (flags > env > file) needs no mapping table.
func configFlags(cmd *cobra.Command) {
	fs := cmd.Flags()
	fs.String("database.url", "", "PostgreSQL connection URL")
	fs.Duration("database.statement_timeout", 25*time.Second, "per-statement timeout (0 disables); keep it below server.request_timeout")
	fs.String("server.addr", "", "HTTP listen address")
	fs.Bool("server.graphiql", false, "serve GraphiQL playground")
	fs.Bool("server.introspection", true, "answer __schema/__type introspection queries, serve the SDL at /schema.graphql and add \"Did you mean\" suggestions to validation errors")
	fs.StringSlice("server.cors_origins", nil, "allowed CORS origins for /graphql ('*' for any)")
	fs.Bool("server.compression", false, "gzip responses when the client accepts it")
	fs.Bool("server.read_only", false, "reject every mutation operation (read-only mode)")
	fs.Int("server.document_cache.size", 1000, "parsed+validated GraphQL documents to cache (0 disables)")
	fs.Int("server.document_cache.bytes", 64<<20, "estimated memory the document cache may retain")
	fs.Int("server.document_cache.max_entry_bytes", 1<<20, "estimated size above which a document is not cached")
	fs.Int64("limits.max_body_bytes", 1<<20, "maximum accepted request body size in bytes")
	fs.Int("limits.max_depth", 15, "maximum GraphQL selection depth per operation")
	fs.Int("limits.max_cost", 100000, "maximum estimated cost per operation")
	fs.Int("limits.max_page_size", 100, "maximum rows per page (and the default page size)")
	fs.Int("limits.max_headline_document", pgcontrib.MaxHeadlineDocument, "characters of a document a full-text headline reads")
	fs.Int("limits.max_query_tokens", 15000, "maximum lexical tokens in a GraphQL document")
	fs.Int("limits.max_input_depth", 32, "maximum nesting of input values (literals and variables)")
	fs.Int("limits.max_selections", 10000, "maximum selections in a document with fragments expanded")
	fs.Int("limits.max_introspection_bytes", 16<<20, "maximum estimated JSON bytes of __schema / __type fields per operation")
	fs.Int("limits.max_field_comparisons", 100000, "maximum field pairs compared when validating field merging")
	fs.Bool("persisted_queries.apq", false, "enable Apollo automatic persisted queries")
	fs.String("persisted_queries.path", "", "JSON file of persisted queries (sha256 hex hash to document)")
	fs.Bool("persisted_queries.only", false, "reject requests that do not reference a persisted query")
	fs.Int("persisted_queries.max_bytes", 8<<20, "memory the APQ-registered documents may hold")
	fs.Int("persisted_queries.max_entry_bytes", 64<<10, "size above which a document is not registered for APQ")
	fs.Bool("incremental.enabled", false, "serve the defer and stream directives (multipart/mixed over HTTP, next messages over WebSocket)")
	fs.Duration("incremental.write_timeout", 10*time.Second, "longest one incremental payload write may block before the response is aborted")
	fs.Int("incremental.max_responses", 5, "incremental responses holding a transaction at once (below database.max_conns)")
	fs.Bool("subscriptions.enabled", false, "serve listen subscriptions (LISTEN/NOTIFY) over WebSocket on /graphql")
	fs.Int("subscriptions.max_connections", 1000, "maximum WebSocket connections at once")
	fs.Int("subscriptions.max_operations", 10000, "maximum operations running at once over all WebSocket connections")
	fs.Int("subscriptions.subscribe_rate", 100, "subscribe messages per second per WebSocket connection")
	fs.Bool("live.enabled", false, "serve live queries (requires subscriptions.enabled)")
	fs.String("live.channel", "pdbq_live", "LISTEN channel and trigger name of live query change notifications")
	fs.Bool("live.install_triggers", true, "install the live query change triggers at startup")
	fs.Duration("live.throttle", 500*time.Millisecond, "shortest time between two runs of one live query")
	fs.Int("live.concurrency", 4, "live query runs in flight at once across the server")
	fs.Int("live.max_per_connection", 10, "maximum live queries per WebSocket connection")
	fs.Int("live.max_operations", 1000, "maximum live queries over all WebSocket connections")
	fs.StringSlice("schema.schemas", nil, "PostgreSQL schemas to expose")
	fs.String("schema.cache_path", "", "boot from schema cache file")
	fs.Bool("schema.expose_partitions", false, "expose partitions of partitioned tables as tables")
	fs.Bool("schema.domains", true, "expose domains over built-in scalars as custom scalars")
	fs.String("auth.mode", "jwt", "credential source: jwt|headers|none")
	fs.Bool("roles.switch", true, "run each request as its resolved role (SET LOCAL ROLE)")
	fs.String("roles.anonymous", "", "role of requests without credentials (empty refuses them with 401)")
	fs.Bool("aggregates.enabled", false, "aggregates for every table (otherwise only tables tagged @aggregates)")
	fs.Bool("transactions.partial.enabled", false, "accept the extensions.partialMutations opt-in (one savepoint per mutation field)")
	fs.Bool("watch.enabled", false, "watch for DDL changes (dev)")
	fs.Bool("filters.indexed_only", true, "only indexed columns filterable")
	fs.String("errors.detail", "", "error verbosity: dev|prod|strict")
	fs.String("log.level", "", "log level")
	fs.Bool("metrics.enabled", false, "serve Prometheus metrics")
	fs.String("metrics.path", "", "URL path of the metrics endpoint (default /metrics)")
	fs.String("metrics.addr", "", "separate listen address for the metrics endpoint")
	registerEnumCompletion(cmd, "auth.mode", "jwt", "headers", "none")
	registerEnumCompletion(cmd, "errors.detail", "dev", "prod", "strict")
	registerEnumCompletion(cmd, "log.level", "debug", "info", "warn", "error")
}

func registerEnumCompletion(cmd *cobra.Command, name string, values ...string) {
	_ = cmd.RegisterFlagCompletionFunc(name, cobra.FixedCompletions(values, cobra.ShellCompDirectiveNoFileComp))
}

func loadConfig(cmd *cobra.Command) (config.Config, error) {
	path, _ := cmd.Flags().GetString("config")
	if path == "" {
		path = confx.Discover("pdbq.yaml", "pdbq.yml")
	}
	return config.Load(path, changedConfigFlags(cmd))
}

// changedConfigFlags returns the config flags the user set on cmd. Config
// flags are named after dotted koanf paths; command-local flags (--config,
// --output, --var, ...) have no dot and stay out of the config.
func changedConfigFlags(cmd *cobra.Command) map[string]any {
	var local []string
	cmd.Flags().VisitAll(func(f *flag.Flag) {
		if !strings.Contains(f.Name, ".") {
			local = append(local, f.Name)
		}
	})
	return cliflags.Changed(cmd.Flags(), local...)
}

// loadCLIConfig loads the config of a command that runs without an HTTP
// request: auth.mode none and roles.switch false, so it runs as the
// connection role and needs no JWT secret.
func loadCLIConfig(cmd *cobra.Command) (config.Config, error) {
	path, _ := cmd.Flags().GetString("config")
	if path == "" {
		path = confx.Discover("pdbq.yaml", "pdbq.yml")
	}
	cfg, err := config.LoadUnvalidated(path, changedConfigFlags(cmd))
	if err != nil {
		return cfg, err
	}
	cfg.ConnectionIdentity()
	return cfg, cfg.Validate()
}

func signalContext() context.Context {
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return ctx
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "pdbq",
		Short:         "Zero-boilerplate GraphQL API for PostgreSQL",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return config.ExplainFlagError(err) })
	root.PersistentFlags().String("config", "", "path to config file (default: ./pdbq.yaml if present)")
	root.AddCommand(serveCmd(), queryCmd(), schemaCmd(), configCmd(), pluginsCmd())
	return root
}

func serveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the GraphQL API server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			app := pdbq.New(cfg)
			defer app.Close()
			return app.Serve(signalContext())
		},
	}
	configFlags(cmd)
	return cmd
}

func queryCmd() *cobra.Command {
	var vars []string
	var varsFile, operation, role, claimsJSON string
	var printSQL bool
	cmd := &cobra.Command{
		Use:   "query [QUERY|-]",
		Short: "Execute a one-off GraphQL query (pipe-friendly; reads stdin with '-' or no arg)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query, err := readQuery(args)
			if err != nil {
				return err
			}
			variables, err := collectVars(vars, varsFile)
			if err != nil {
				return err
			}
			claims, err := parseClaims(claimsJSON)
			if err != nil {
				return err
			}
			if role != "" && !pgexec.ValidRole(role) {
				return fmt.Errorf("--role %q is not an assumable role name ([A-Za-z0-9_]+, not none, public or pg_*)", role)
			}
			cfg, err := loadCLIConfig(cmd)
			if err != nil {
				return err
			}
			app := pdbq.New(cfg)
			defer app.Close()
			if printSQL {
				stmts, err := app.CompileQuery(signalContext(), query, variables, operation)
				if err != nil {
					return err
				}
				for _, s := range stmts {
					argsJSON, err := json.Marshal(s.Args)
					if err != nil {
						return fmt.Errorf("marshal args for %s: %w", s.Field, err)
					}
					fmt.Printf("-- field: %s\n%s\n-- args: %s\n", s.Field, s.SQL, argsJSON)
					if s.Then != nil {
						thenJSON, err := json.Marshal(s.Then.Args)
						if err != nil {
							return fmt.Errorf("marshal args for %s: %w", s.Field, err)
						}
						fmt.Printf("-- then:\n%s\n-- args: %s\n", s.Then.SQL, thenJSON)
					}
					fmt.Println()
				}
				return nil
			}
			res, err := app.Execute(signalContext(), pdbq.ExecRequest{
				Query:         query,
				OperationName: operation,
				Variables:     variables,
				Role:          role,
				Claims:        claims,
			})
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(res); err != nil {
				return err
			}
			if len(res.Errors) > 0 {
				return &exitError{code: 2}
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&vars, "var", nil, "variable as key=value (repeatable; value parsed as JSON, falling back to string)")
	cmd.Flags().StringVar(&varsFile, "vars-file", "", "JSON file with variables ('-' for stdin)")
	cmd.Flags().StringVar(&operation, "operation", "", "operation name to execute")
	cmd.Flags().BoolVar(&printSQL, "print-sql", false, "print the compiled SQL and parameters instead of executing")
	cmd.Flags().StringVar(&role, "role", "", "run as this database role (SET LOCAL ROLE; default: the connection role)")
	cmd.Flags().StringVar(&claimsJSON, "claims", "", "claims as a JSON object, applied through claims.prefix and claims.json_setting like a request's")
	configFlags(cmd)
	return cmd
}

func readQuery(args []string) (string, error) {
	if len(args) == 1 && args[0] != "-" {
		return args[0], nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read query from stdin: %w", err)
	}
	if len(b) == 0 {
		return "", fmt.Errorf("no query given (pass as argument or on stdin)")
	}
	return string(b), nil
}

// parseClaims decodes the --claims value, a JSON object; "" is no claims.
func parseClaims(s string) (map[string]any, error) {
	if s == "" {
		return nil, nil
	}
	var claims map[string]any
	if err := json.Unmarshal([]byte(s), &claims); err != nil || claims == nil {
		return nil, fmt.Errorf("--claims: want a JSON object, e.g. '{\"user_id\": 1}'")
	}
	return claims, nil
}

func collectVars(pairs []string, varsFile string) (map[string]any, error) {
	out := map[string]any{}
	if varsFile != "" {
		var r io.Reader
		if varsFile == "-" {
			r = os.Stdin
		} else {
			f, err := os.Open(varsFile)
			if err != nil {
				return nil, err
			}
			defer func() { _ = f.Close() }()
			r = f
		}
		if err := json.NewDecoder(r).Decode(&out); err != nil {
			return nil, fmt.Errorf("vars-file: %w", err)
		}
	}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("--var %q: expected key=value", p)
		}
		var parsed any
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			parsed = v // not JSON -> plain string
		}
		out[k] = parsed
	}
	return out, nil
}

func schemaCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "schema", Short: "Schema cache and SDL tools"}

	var out string
	dump := &cobra.Command{
		Use:   "dump",
		Short: "Introspect the database and write a schema cache file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadCLIConfig(cmd)
			if err != nil {
				return err
			}
			cfg.Schema.CachePath = "" // always introspect live
			app := pdbq.New(cfg)
			defer app.Close()
			cat, err := app.LoadCatalog(signalContext())
			if err != nil {
				return err
			}
			if err := cache.Save(out, cat, cache.Writer{Product: "pdbq", Version: version}); err != nil {
				return err
			}
			hash, _ := cat.Hash()
			fmt.Printf("wrote %s (%d tables, hash %s)\n", out, len(cat.Tables), hash[:12])
			return nil
		},
	}
	dump.Flags().StringVarP(&out, "output", "o", "schema.cache", "output path")
	configFlags(dump)

	var cachePath string
	check := &cobra.Command{
		Use:   "check",
		Short: "Compare live database schema against a cache file (exit 1 on drift)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadCLIConfig(cmd)
			if err != nil {
				return err
			}
			cfg.Schema.CachePath = ""
			app := pdbq.New(cfg)
			defer app.Close()
			cat, err := app.LoadCatalog(signalContext())
			if err != nil {
				return err
			}
			drift, err := cache.Check(cachePath, cat)
			if err != nil {
				return err
			}
			if drift != "" {
				return fmt.Errorf("%s", drift)
			}
			fmt.Println("schema cache is up to date")
			return nil
		},
	}
	check.Flags().StringVar(&cachePath, "cache", "schema.cache", "cache file to compare against")
	configFlags(check)

	var asJSON bool
	var contractName string
	print := &cobra.Command{
		Use:   "print",
		Short: "Print the generated GraphQL schema (SDL, or introspection JSON with --json)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadCLIConfig(cmd)
			if err != nil {
				return err
			}
			app := pdbq.New(cfg)
			defer app.Close()
			ctx := signalContext()
			cat, err := app.LoadCatalog(ctx)
			if err != nil {
				return err
			}
			built, err := app.BuildContractSchema(ctx, cat, contractName)
			if err != nil {
				return err
			}
			if !asJSON {
				fmt.Print(built.SDL)
				return nil
			}
			// Introspection resolves in memory: nil pool, no compile function.
			ex := exec.New(nil, built, nil, nil, exec.Options{Logger: app.Log})
			res := ex.Execute(ctx, exec.Request{Query: exec.IntrospectionQuery})
			if len(res.Errors) > 0 {
				return fmt.Errorf("introspection: %s", res.Errors.Error())
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(res)
		},
	}
	print.Flags().BoolVar(&asJSON, "json", false, "output introspection-format JSON instead of SDL")
	print.Flags().StringVar(&contractName, "contract", "", "print the schema of this configured contract instead of the full schema")
	configFlags(print)

	cmd.AddCommand(dump, check, print)
	return cmd
}

func configCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Configuration tools"}

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter pdbq.yaml in the current directory",
		RunE: func(_ *cobra.Command, _ []string) error {
			const path = "pdbq.yaml"
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists", path)
			}
			if err := os.WriteFile(path, []byte(config.StarterYAML), 0o644); err != nil {
				return err
			}
			fmt.Println("wrote", path)
			return nil
		},
	}

	validate := &cobra.Command{
		Use:   "validate",
		Short: "Validate the effective configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := loadConfig(cmd); err != nil {
				return err
			}
			fmt.Println("configuration is valid")
			return nil
		},
	}
	configFlags(validate)

	example := &cobra.Command{
		Use:   "example",
		Short: "Print the fully-commented reference configuration (generated from the config structs)",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Print(config.ExampleYAML())
		},
	}

	cmd.AddCommand(initCmd, validate, example)
	return cmd
}

func pluginsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "plugins", Short: "Plugin tools"}
	list := &cobra.Command{
		Use:   "list",
		Short: "List registered plugins and the hooks they implement",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadCLIConfig(cmd)
			if err != nil {
				return err
			}
			app := pdbq.New(cfg)
			for _, p := range app.Plugins() {
				status := "enabled"
				if !cfg.PluginEnabled(p.Name()) {
					status = "disabled"
				}
				fmt.Printf("%-20s priority=%-4d %-8s hooks: %s\n",
					p.Name(), p.Priority(), status, strings.Join(hookNames(p), ", "))
			}
			return nil
		},
	}
	configFlags(list)
	cmd.AddCommand(list)
	return cmd
}

func hookNames(p plugin.Plugin) []string {
	var out []string
	if _, ok := p.(plugin.CatalogHook); ok {
		out = append(out, "catalog")
	}
	if _, ok := p.(plugin.InflectionHook); ok {
		out = append(out, "inflection")
	}
	if _, ok := p.(plugin.SchemaHook); ok {
		out = append(out, "schema")
	}
	if _, ok := p.(plugin.CompileHook); ok {
		out = append(out, "compile")
	}
	if _, ok := p.(plugin.RequestHook); ok {
		out = append(out, "request")
	}
	if len(out) == 0 {
		out = append(out, "none")
	}
	return out
}
