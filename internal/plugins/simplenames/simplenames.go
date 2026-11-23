// Package simplenames is the opt-in built-in plugin for PgSimplifyInflector-
// style short names: `users` instead of `allUsers`, `user(id:)` instead of
// `userById`, `updateUser` instead of `updateUserById`, and shortened
// relation names derived from the schema's shape alone. It is registered
// but inert unless [Settings.Enabled] is set, and it is the minimal worked
// example of a naming plugin.
//
// Relation names:
//
//   - forward: a single-column foreign key whose column ends in _id or Id
//     is named after the column without the suffix (author_id -> author);
//     any other key keeps the verbose name (userByAuthor,
//     userByOrgIdAndUserId).
//   - backward: the plural child table name (posts) when the child table
//     has exactly one foreign key to the parent table, otherwise the
//     verbose name (postsByAuthorId).
//
// Names never depend on other fields: a name that is already taken on its
// type is a schema build error, fixed with a smart comment.
package simplenames

import (
	"context"
	"strings"

	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/inflect"
)

// Settings is the plugins.settings.simple_names section.
type Settings struct {
	Enabled bool `koanf:"enabled" doc:"Use the short names (users, user(id:), updateUser, author, posts) instead of PostGraphile's verbose names (allUsers, userById, updateUserById, userByAuthorId, postsByAuthorId)."`
}

// DefaultSettings returns the documented defaults: the plugin is off.
func DefaultSettings() Settings { return Settings{} }

// Plugin is the simple_names naming plugin.
type Plugin struct {
	enabled bool
	catalog *introspect.Catalog
}

// New builds the plugin from its settings.
func New(s Settings) *Plugin { return &Plugin{enabled: s.Enabled} }

// Name is the plugin's name in plugins.disabled and plugins.settings.
const Name = "simple_names"

func (p *Plugin) Name() string  { return Name }
func (p *Plugin) Priority() int { return 100 }

// TransformCatalog only captures the catalog; it does not modify it.
func (p *Plugin) TransformCatalog(_ context.Context, c *introspect.Catalog) error {
	p.catalog = c
	return nil
}

// Inflect shortens the names listed in the package documentation when the
// plugin is enabled and passes every other name on unchanged.
func (p *Plugin) Inflect(kind inflect.Kind, in inflect.Input, next inflect.Next) string {
	if !p.enabled {
		return next(kind, in)
	}
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
	case inflect.KindRelationForward:
		if base, ok := idColumnBase(in.Columns); ok {
			return coreinflect.LowerCamel(base)
		}
	case inflect.KindRelationBackward:
		if p.onlyKeyToParent(in) {
			return coreinflect.LowerCamel(coreinflect.Pluralize(coreinflect.Singularize(in.Table)))
		}
	}
	return next(kind, in)
}

// idColumnBase returns the column of a single-column key without its _id or
// Id suffix, and false for any other key.
func idColumnBase(cols []string) (string, bool) {
	if len(cols) != 1 {
		return "", false
	}
	for _, suffix := range []string{"_id", "Id"} {
		if base, ok := strings.CutSuffix(cols[0], suffix); ok && strings.Trim(base, "_") != "" {
			return base, true
		}
	}
	return "", false
}

// onlyKeyToParent reports whether the foreign key behind a backward
// relation (in.Constraint, declared on the child table in.Table of
// in.Schema) is the child table's only foreign key to its parent table.
func (p *Plugin) onlyKeyToParent(in inflect.Input) bool {
	child, fk := p.foreignKey(in)
	if fk == nil {
		return false
	}
	n := 0
	for _, other := range child.ForeignKeys {
		if other.RefSchema == fk.RefSchema && other.RefTable == fk.RefTable {
			n++
		}
	}
	return n == 1
}

// foreignKey finds the constraint in.Constraint of schema in.Schema and its
// table. in.Table may already carry a smart-comment rename, so it only
// disambiguates constraints of the same name on several tables.
func (p *Plugin) foreignKey(in inflect.Input) (*introspect.Table, *introspect.ForeignKey) {
	if p.catalog == nil {
		return nil, nil
	}
	var (
		table *introspect.Table
		found *introspect.ForeignKey
		n     int
	)
	for _, t := range p.catalog.Tables {
		if t.Schema != in.Schema {
			continue
		}
		for _, fk := range t.ForeignKeys {
			if fk.Name != in.Constraint {
				continue
			}
			if t.Name == in.Table {
				return t, fk
			}
			table, found = t, fk
			n++
		}
	}
	if n != 1 {
		return nil, nil
	}
	return table, found
}
