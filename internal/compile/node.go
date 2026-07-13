package compile

import (
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// nodeQuery compiles Query.node(nodeId: ID!): the ID is decoded at compile
// time (variables are available), dispatched to its table by type name, and
// the selection resolves against the concrete type — inline fragments with
// non-matching type conditions drop out.
func (s *stmt) nodeQuery(f *ast.Field) (string, error) {
	v, ok := s.argValue(f, "nodeId")
	if !ok {
		return "", fmt.Errorf("compile: node: missing nodeId argument")
	}
	sv, _ := v.(string)
	typeName, keys, err := decodeNodeID(sv)
	if err != nil {
		return "", err
	}
	t := s.c.Built.TableForType[typeName]
	if t == nil || t.PrimaryKey == nil {
		// Unknown or non-Node type: per Relay, resolve to null.
		return "SELECT NULL::jsonb AS data WHERE false", nil
	}
	alias := s.alias("t")
	jsonExpr := "'{}'::jsonb"
	var laterals []string
	// Only non-matching fragments can leave the concrete selection empty.
	if len(s.expandFor(f.SelectionSet, typeName)) > 0 {
		jsonExpr, laterals, err = s.rowJSON(t, f.SelectionSet, alias, 1)
		if err != nil {
			return "", err
		}
	}
	conds, err := s.keyCondsFromValues(t, t.PrimaryKey.Columns, keys, alias)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT %s AS data\nFROM %s AS %s%s\nWHERE %s\nLIMIT 1",
		jsonExpr, s.sourceRef(t), alias, joinLaterals(laterals), strings.Join(conds, " AND ")), nil
}
