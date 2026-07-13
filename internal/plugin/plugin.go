// Package plugin defines the hook surfaces and the ordered registry.
//
// v1 plugins are compile-time: a plugin is any Go type implementing one or
// more of the hook interfaces below, registered via Register (built-ins) or
// pdbq.WithPlugins (library embedding). The interfaces are deliberately
// small and orthogonal — they are also the contract for a future
// out-of-process (go-plugin / wasm) transport.
package plugin

import (
	"context"
	"sort"
	"sync"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/introspect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// Plugin is the base interface: identity plus default priority. Lower
// priority runs earlier (outermost in middleware chains); ties break by
// registration order.
type Plugin interface {
	Name() string
	Priority() int
}

// CatalogHook mutates or filters the introspected catalog before schema
// building (e.g. hide tables, rename via comments).
type CatalogHook interface {
	TransformCatalog(ctx context.Context, c *introspect.Catalog) error
}

// InflectionHook controls every generated GraphQL name. Call next to get the
// downstream (ultimately default) name; return your own to override.
type InflectionHook interface {
	Inflect(kind inflect.Kind, in inflect.Input, next inflect.Next) string
}

// SchemaHook adds/removes/wraps types and fields after default generation.
type SchemaHook interface {
	TransformSchema(ctx context.Context, s *schema.Builder) error
}

// CompileHook wraps SQL generation per field, middleware-style.
type CompileHook interface {
	WrapCompile(next compile.Func) compile.Func
}

// RequestHook observes and controls the request lifecycle (auth, logging,
// transaction policy).
type RequestHook interface {
	BeforeOperation(ctx context.Context, op *exec.Operation) (context.Context, error)
	AfterOperation(ctx context.Context, op *exec.Operation, res *exec.Result)
}

// Registry holds plugins in deterministic order.
type Registry struct {
	mu      sync.Mutex
	plugins []Plugin
}

func NewRegistry(plugins ...Plugin) *Registry {
	r := &Registry{}
	for _, p := range plugins {
		r.Add(p)
	}
	return r
}

// Add registers a plugin, keeping the list sorted by (priority, insertion).
func (r *Registry) Add(p Plugin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plugins = append(r.plugins, p)
	sort.SliceStable(r.plugins, func(i, j int) bool {
		return r.plugins[i].Priority() < r.plugins[j].Priority()
	})
}

// All returns plugins in execution order.
func (r *Registry) All() []Plugin {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Plugin, len(r.plugins))
	copy(out, r.plugins)
	return out
}

// Enabled returns plugins in execution order, excluding names disabled in cfg.
func (r *Registry) Enabled(disabled map[string]bool) []Plugin {
	var out []Plugin
	for _, p := range r.All() {
		if !disabled[p.Name()] {
			out = append(out, p)
		}
	}
	return out
}

// TransformCatalog runs all CatalogHooks in order.
func (r *Registry) TransformCatalog(ctx context.Context, disabled map[string]bool, c *introspect.Catalog) error {
	for _, p := range r.Enabled(disabled) {
		if h, ok := p.(CatalogHook); ok {
			if err := h.TransformCatalog(ctx, c); err != nil {
				return err
			}
		}
	}
	return nil
}

// Inflector composes all InflectionHooks into a single naming function with
// inflect.Default as the innermost link.
func (r *Registry) Inflector(disabled map[string]bool) inflect.Next {
	chain := inflect.Next(inflect.Default)
	plugins := r.Enabled(disabled)
	// Build inside-out so lower priority ends up outermost.
	for i := len(plugins) - 1; i >= 0; i-- {
		h, ok := plugins[i].(InflectionHook)
		if !ok {
			continue
		}
		next := chain
		chain = func(kind inflect.Kind, in inflect.Input) string {
			return h.Inflect(kind, in, next)
		}
	}
	return chain
}

// TransformSchema runs all SchemaHooks in order.
func (r *Registry) TransformSchema(ctx context.Context, disabled map[string]bool, b *schema.Builder) error {
	for _, p := range r.Enabled(disabled) {
		if h, ok := p.(SchemaHook); ok {
			if err := h.TransformSchema(ctx, b); err != nil {
				return err
			}
		}
	}
	return nil
}

// CompileChain wraps base with all CompileHooks (outermost = lowest priority).
func (r *Registry) CompileChain(disabled map[string]bool, base compile.Func) compile.Func {
	plugins := r.Enabled(disabled)
	chain := base
	for i := len(plugins) - 1; i >= 0; i-- {
		if h, ok := plugins[i].(CompileHook); ok {
			chain = h.WrapCompile(chain)
		}
	}
	return chain
}

// RequestHooks returns the RequestHook implementations in order.
func (r *Registry) RequestHooks(disabled map[string]bool) []RequestHook {
	var out []RequestHook
	for _, p := range r.Enabled(disabled) {
		if h, ok := p.(RequestHook); ok {
			out = append(out, h)
		}
	}
	return out
}

// defaultRegistry collects built-ins registered from init().
var defaultRegistry = NewRegistry()

// Register adds a plugin to the process-wide default registry (built-ins).
func Register(p Plugin) { defaultRegistry.Add(p) }

// Default returns the process-wide registry.
func Default() *Registry { return defaultRegistry }
