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

	"github.com/suprbdev/pdbcore/plugin"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// Plugin is the base interface: identity plus default priority. Lower
// priority runs earlier (outermost in middleware chains); ties break by
// registration order.
type Plugin = plugin.Plugin

// CatalogHook mutates or filters the introspected catalog before schema
// building (e.g. hide tables, rename via comments).
type CatalogHook = plugin.CatalogHook

// InflectionHook controls every generated GraphQL name. Call next to get the
// downstream (ultimately default) name; return your own to override.
type InflectionHook = plugin.InflectionHook

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

// Registry holds plugins in deterministic order. The generic half (ordering,
// CatalogHook, InflectionHook) is pdbcore's; this wrapper adds the
// GraphQL-typed hook chains.
type Registry struct {
	*plugin.Registry
}

func NewRegistry(plugins ...Plugin) *Registry {
	return &Registry{Registry: plugin.NewRegistry(plugins...)}
}

// Inflector composes all InflectionHooks into a single naming function with
// inflect.Default as the innermost link.
func (r *Registry) Inflector(disabled map[string]bool) inflect.Next {
	return r.Registry.Inflector(disabled, inflect.Default)
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
