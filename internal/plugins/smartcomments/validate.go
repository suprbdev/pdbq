package smartcomments

import (
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/smartcomments/catalog"
	"github.com/suprbdev/pdbcore/smarttags"
	"github.com/suprbdev/pdbq/internal/omit"
)

// vocabulary is what pdbq honours: the @omit actions of package omit, and
// on top of pdbcore's omit and contract tokens, the @behavior tokens of the
// aggregates plugin.
var vocabulary = smarttags.Vocabulary{
	OmitActions: []string{
		string(omit.Read), string(omit.Create), string(omit.Update), string(omit.Delete),
		string(omit.Filter), string(omit.Order), string(omit.Many), string(omit.All),
		string(omit.Aggregates),
	},
	Behavior: aggregatesBehavior,
}

// aggregatesBehaviors are the @behavior tokens of the aggregates plugin,
// matched case-sensitively as that plugin does: table tokens with an
// optional + or - sign, column tokens negative only.
var (
	aggregatesTableBehaviors = map[string]bool{
		"aggregates": true, "groupedAggregates": true, "resource:groupedAggregates": true,
		"aggregates:filterBy": true, "resource:aggregates:filterBy": true,
		"aggregates:orderBy": true, "manyRelation:aggregates:orderBy": true,
	}
	aggregatesColumnBehaviors = map[string]bool{
		"-aggregate": true, "-aggregates": true, "-attribute:aggregate": true,
		"-groupBy": true, "-attribute:groupBy": true,
	}
)

func aggregatesBehavior(tok string) bool {
	return aggregatesColumnBehaviors[tok] || aggregatesTableBehaviors[strings.TrimLeft(tok, "+-")]
}

// validateTags notes a warning for every unknown @omit action, unknown
// @behavior token and deprecated @behavior token in the comments of the
// catalog's objects, once per object and token (see catalog.Check).
func (p *Plugin) validateTags(c *introspect.Catalog) {
	for _, f := range catalog.Check(c, vocabulary) {
		p.note("smart_comments: "+f.Message(), f.Args()...)
	}
}
