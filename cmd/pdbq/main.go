// Command pdbq serves an automatically mapped GraphQL API for a PostgreSQL
// database, and provides schema-cache, config, and one-off query tooling.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	flag "github.com/spf13/pflag"

	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/cache"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/plugin"
)

var version = "dev"

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// configFlags defines flags that mirror config keys; their names are the
// koanf paths so precedence (flags > env > file) needs no mapping table.
func configFlags(cmd *cobra.Command) {
	fs := cmd.Flags()
	fs.String("database.url", "", "PostgreSQL connection URL")
	fs.String("server.addr", "", "HTTP listen address")
	fs.Bool("server.graphiql", false, "serve GraphiQL playground")
	fs.Bool("server.expose_schema", false, "serve generated SDL at /schema.graphql")
	fs.StringSlice("server.cors_origins", nil, "allowed CORS origins for /graphql ('*' for any)")
	fs.Bool("server.compression", false, "gzip responses when the client accepts it")
	fs.StringSlice("schema.schemas", nil, "PostgreSQL schemas to expose")
	fs.String("schema.cache_path", "", "boot from schema cache file")
	fs.Bool("rls.enabled", true, "enable RLS role switching")
	fs.Bool("watch.enabled", false, "watch for DDL changes (dev)")
	fs.Bool("filters.indexed_only", true, "only indexed columns filterable")
	fs.String("errors.detail", "", "error verbosity: dev|prod")
	fs.String("log.level", "", "log level")
	registerEnumCompletion(cmd, "errors.detail", "dev", "prod")
	registerEnumCompletion(cmd, "log.level", "debug", "info", "warn", "error")
}

func registerEnumCompletion(cmd *cobra.Command, name string, values ...string) {
	_ = cmd.RegisterFlagCompletionFunc(name, cobra.FixedCompletions(values, cobra.ShellCompDirectiveNoFileComp))
}

func loadConfig(cmd *cobra.Command) (config.Config, error) {
	path, _ := cmd.Flags().GetString("config")
	if path == "" {
		for _, cand := range []string{"pdbq.yaml", "pdbq.yml"} {
			if _, err := os.Stat(cand); err == nil {
				path = cand
				break
			}
		}
	}
	changed := flag.NewFlagSet("changed", flag.ContinueOnError)
	cmd.Flags().Visit(func(f *flag.Flag) {
		if f.Name != "config" {
			changed.AddFlag(f)
		}
	})
	return config.Load(path, changed)
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
	var varsFile, operation string
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
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			// One-off CLI queries run without HTTP auth: no role switch.
			cfg.RLS.Auth.Mode = "none"
			if err := cfg.Validate(); err != nil {
				return err
			}
			app := pdbq.New(cfg)
			defer app.Close()
			res, err := app.Query(signalContext(), query, variables, operation)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(res); err != nil {
				return err
			}
			if len(res.Errors) > 0 {
				os.Exit(2) // GraphQL errors -> distinct exit code for scripts
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&vars, "var", nil, "variable as key=value (repeatable; value parsed as JSON, falling back to string)")
	cmd.Flags().StringVar(&varsFile, "vars-file", "", "JSON file with variables ('-' for stdin)")
	cmd.Flags().StringVar(&operation, "operation", "", "operation name to execute")
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
			defer f.Close()
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
			cfg, err := loadConfig(cmd)
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
			if err := cache.Save(out, cat); err != nil {
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
			cfg, err := loadConfig(cmd)
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
	print := &cobra.Command{
		Use:   "print",
		Short: "Print the generated GraphQL schema (SDL, or introspection JSON with --json)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd)
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
			built, err := app.BuildSchema(ctx, cat)
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
			starter := `# pdbq configuration. Run "pdbq config example" for the full annotated reference.
database:
  url: "postgres://postgres:postgres@localhost:5432/postgres"
server:
  addr: ":8080"
schema:
  schemas: ["public"]
rls:
  enabled: false # enable + configure auth before production use
`
			if err := os.WriteFile(path, []byte(starter), 0o644); err != nil {
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
			cfg, err := loadConfig(cmd)
			if err != nil {
				// plugins list should work without a valid DB config
				cfg = config.DefaultConfig()
				cfg.RLS.Enabled = false
			}
			app := pdbq.New(cfg)
			disabled := cfg.DisabledPlugins()
			for _, p := range app.Plugins() {
				status := "enabled"
				if disabled[p.Name()] {
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
