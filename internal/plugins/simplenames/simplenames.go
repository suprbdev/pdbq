// Package simplenames is a built-in plugin proving that naming is fully
// hook-driven: `users` instead of `allUsers`, `user(id:)` instead of
// `userById`, `updateUser` instead of `updateUserById`, and shortened
// relation names where unambiguous. Ambiguous cases fall back to the verbose
// default (the builder additionally warns on any residual collision).
package simplenames

import (
	"context"

	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/inflect"
)

type Plugin struct {
	// catalog is captured by the CatalogHook so inflection can detect
	// ambiguous relations (multiple FKs between the same pair of tables).
	catalog *introspect.Catalog
}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string  { return "simple-names" }
func (p *Plugin) Priority() int { return 100 }

// TransformCatalog only captures the catalog; it does not modify it.
func (p *Plugin) TransformCatalog(_ context.Context, c *introspect.Catalog) error {
	p.catalog = c
	return nil
}

func (p *Plugin) Inflect(kind inflect.Kind, in inflect.Input, next inflect.Next) string {
	switch kind {
	case inflect.KindAllRowsField:
		return coreinflect.LowerCamel(coreinflect.Pluralize(coreinflect.Singularize(in.Table)))
	case inflect.KindRowByPKField:
		return coreinflect.LowerCamel(coreinflect.Singularize(in.Table))
	case inflect.KindUpdateMutation:
		return "update" + coreinflect.UpperCamel(coreinflect.Singularize(in.Table))
	case inflect.KindDeleteMutation:
		return "delete" + coreinflect.UpperCamel(coreinflect.Singularize(in.Table))
	case inflect.KindUpsertMutation:
		return "upsert" + coreinflect.UpperCamel(coreinflect.Singularize(in.Table))
	case inflect.KindRelationBackward:
		// Shorten postsByAuthorId -> posts, but only when there is exactly
		// one FK from that table to any given target (unambiguous).
		if p.fkCountFrom(in.Schema, in.Table) <= 1 {
			return coreinflect.LowerCamel(coreinflect.Pluralize(coreinflect.Singularize(in.Table)))
		}
		return next(kind, in)
	default:
		return next(kind, in)
	}
}

// fkCountFrom counts foreign keys declared on schema.table; more than one
// means backward relation names could collide, so we stay verbose.
func (p *Plugin) fkCountFrom(schema, table string) int {
	if p.catalog == nil {
		return 0
	}
	t := p.catalog.Table(schema, table)
	if t == nil {
		return 0
	}
	return len(t.ForeignKeys)
}
