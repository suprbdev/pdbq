package exec

import (
	"context"
	"encoding/json"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbq/internal/compile"
)

// rootTablesSQL maps the (schema, relation) pairs $1, $2 to the OID and
// qualified name of each relation's partitioned root, or of the relation
// itself outside a partition tree.
const rootTablesSQL = `
SELECT DISTINCT r.oid, pg_catalog.quote_ident(n.nspname) || '.' || pg_catalog.quote_ident(r.relname)
FROM unnest($1::text[], $2::text[]) AS p(nsp, rel)
JOIN pg_catalog.pg_namespace pn ON pn.nspname = p.nsp
JOIN pg_catalog.pg_class c ON c.relnamespace = pn.oid AND c.relname = p.rel
JOIN pg_catalog.pg_class r ON r.oid = coalesce(pg_catalog.pg_partition_root(c.oid), c.oid)
JOIN pg_catalog.pg_namespace n ON n.oid = r.relnamespace`

// ReferencedTables returns the tables that the statements of fields, root
// fields of the Query type, read: the OID and quoted qualified name of each
// one's partitioned root (a table outside a partition tree is its own
// root). It asks the planner: every statement is compiled and EXPLAINed,
// not run, under op's role and claims, so views are expanded into the
// tables they read and the tables read by row-level security policies are
// included. Tables read only inside functions that the planner does not
// inline are not seen. A failure is returned as a GraphQL error, with
// database messages sanitized as for a field error.
func (e *Executor) ReferencedTables(ctx context.Context, op *Operation, fields []*ast.Field) (map[uint32]string, error) {
	var stmts []*compile.Statement
	for _, f := range fields {
		if isIntrospectionField(f.Name) {
			continue
		}
		stmt, err := e.compileField(ctx, op, f)
		if err != nil {
			return nil, e.gqlError(err, f)
		}
		stmts = append(stmts, stmt)
	}
	out := map[uint32]string{}
	if len(stmts) == 0 {
		return out, nil
	}
	q := *op
	q.Type = ast.Query
	opts, id := e.txOptions(&q)
	err := pgexec.RunTx(ctx, e.Pool, opts, id, func(tx pgexec.Querier) error {
		clear(out)
		var nsps, rels []string
		seen := map[[2]string]bool{}
		for _, s := range stmts {
			var plan []byte
			if err := tx.QueryRow(ctx, "EXPLAIN (VERBOSE, FORMAT JSON) "+s.SQL, s.Args...).Scan(&plan); err != nil {
				return err
			}
			var doc any
			if err := json.Unmarshal(plan, &doc); err != nil {
				return err
			}
			planRelations(doc, func(nsp, rel string) {
				if k := [2]string{nsp, rel}; !seen[k] {
					seen[k] = true
					nsps, rels = append(nsps, nsp), append(rels, rel)
				}
			})
		}
		if len(nsps) == 0 {
			return nil
		}
		rows, err := tx.Query(ctx, rootTablesSQL, nsps, rels)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var oid uint32
			var name string
			if err := rows.Scan(&oid, &name); err != nil {
				return err
			}
			out[oid] = name
		}
		return rows.Err()
	})
	if err != nil {
		return nil, e.gqlError(err, nil)
	}
	return out, nil
}

// planRelations calls fn with the schema and name of every relation that a
// JSON EXPLAIN (VERBOSE) plan scans, in any node, subplan or init plan.
func planRelations(v any, fn func(nsp, rel string)) {
	switch v := v.(type) {
	case map[string]any:
		if rel, ok := v["Relation Name"].(string); ok {
			if nsp, ok := v["Schema"].(string); ok {
				fn(nsp, rel)
			}
		}
		for _, c := range v {
			planRelations(c, fn)
		}
	case []any:
		for _, c := range v {
			planRelations(c, fn)
		}
	}
}
