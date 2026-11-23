package smartcomments

import (
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/smarttags"
	"github.com/suprbdev/pdbq/internal/omit"
)

// omitActions are the @omit actions pdbq honours (package omit), lowercased
// as smarttags.Omits reads them.
var omitActions = map[string]bool{
	string(omit.Read): true, string(omit.Create): true, string(omit.Update): true, string(omit.Delete): true,
	string(omit.Filter): true, string(omit.Order): true, string(omit.Many): true, string(omit.All): true,
	string(omit.Aggregates): true,
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

// knownBehavior reports whether pdbq honours the @behavior token tok: an
// omit token of [omit.BehaviorActions] (any case), an aggregates token, or
// a schema-contract token (±contract:<name>, contract:<name>).
func knownBehavior(tok string) bool {
	if _, ok := omit.BehaviorActions[strings.ToLower(tok)]; ok {
		return true
	}
	if aggregatesColumnBehaviors[tok] || aggregatesTableBehaviors[strings.TrimLeft(tok, "+-")] {
		return true
	}
	rest := tok
	if rest != "" && (rest[0] == '+' || rest[0] == '-') {
		rest = rest[1:]
	}
	name, ok := strings.CutPrefix(strings.ToLower(rest), "contract:")
	return ok && name != ""
}

// validateTags notes a warning for every unknown @omit action, unknown
// @behavior token and deprecated @behavior token in the comments of the
// catalog's objects, once per object and token.
func (p *Plugin) validateTags(c *introspect.Catalog) {
	for _, t := range c.Tables {
		kind := "table"
		if t.Kind != introspect.RelTable {
			kind = "view"
		}
		key := tableKey(t.Schema, t.Name)
		p.validateComment(kind, key, t.Comment)
		for _, col := range t.Columns {
			p.validateComment("column", key+"."+col.Name, col.Comment)
		}
		for _, fk := range t.ForeignKeys {
			p.validateComment("constraint", key+"."+fk.Name, fk.Comment)
		}
		cons := append([]*introspect.Constraint{t.PrimaryKey}, t.Uniques...)
		for _, con := range cons {
			if con != nil {
				p.validateComment("constraint", key+"."+con.Name, con.Comment)
			}
		}
	}
	for _, ct := range c.Composites {
		key := tableKey(ct.Schema, ct.Name)
		for _, f := range ct.Fields {
			p.validateComment("column", key+"."+f.Name, f.Comment)
		}
	}
	for _, f := range c.Functions {
		p.validateComment("function", tableKey(f.Schema, f.Name), f.Comment)
	}
	for _, e := range c.Enums {
		p.validateComment("enum", tableKey(e.Schema, e.Name), e.Comment)
	}
	for _, d := range c.Domains {
		p.validateComment("domain", tableKey(d.Schema, d.Name), d.Comment)
	}
}

// validateComment checks the @omit and @behavior tags of one object's
// comment.
func (p *Plugin) validateComment(kind, name, comment string) {
	if !strings.Contains(comment, "@") {
		return
	}
	tags, _ := smarttags.Parse(comment)
	seen := map[string]bool{}
	once := func(tok string) bool {
		if seen[tok] {
			return false
		}
		seen[tok] = true
		return true
	}
	for _, v := range tags.All("omit") {
		for _, a := range smarttags.SplitList(v) {
			if !omitActions[strings.ToLower(a)] && once("omit "+a) {
				p.note("smart_comments: unknown @omit action ignored", kind, name, "action", a)
			}
		}
	}
	for _, v := range tags.All("behavior") {
		for _, tok := range smarttags.SplitList(v) {
			switch {
			case !knownBehavior(tok):
				if once("behavior " + tok) {
					p.note("smart_comments: unknown @behavior token ignored", kind, name, "token", tok)
				}
			case omit.DeprecatedBehaviors[strings.ToLower(tok)] != "":
				if once("behavior " + tok) {
					p.note("smart_comments: deprecated @behavior token", kind, name, "token", tok,
						"use", omit.DeprecatedBehaviors[strings.ToLower(tok)])
				}
			}
		}
	}
}
