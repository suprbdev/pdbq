package introspect

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Querier is the subset of pgx used by introspection (satisfied by *pgx.Conn,
// pgxpool.Pool, and pgx.Tx).
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Introspect reads pg_catalog for the given schemas and builds a Catalog.
// pg_catalog is used instead of information_schema because the latter loses
// index, RLS, and array/domain detail.
func Introspect(ctx context.Context, db Querier, schemas []string) (*Catalog, error) {
	if len(schemas) == 0 {
		schemas = []string{"public"}
	}
	cat := &Catalog{FormatVersion: CatalogFormatVersion, Schemas: schemas}

	if err := db.QueryRow(ctx, `SHOW server_version`).Scan(&cat.ServerVersion); err != nil {
		return nil, fmt.Errorf("introspect: server version: %w", err)
	}
	if err := readTables(ctx, db, schemas, cat); err != nil {
		return nil, err
	}
	if err := readColumns(ctx, db, schemas, cat); err != nil {
		return nil, err
	}
	if err := readConstraints(ctx, db, schemas, cat); err != nil {
		return nil, err
	}
	if err := readIndexes(ctx, db, schemas, cat); err != nil {
		return nil, err
	}
	if err := readEnums(ctx, db, schemas, cat); err != nil {
		return nil, err
	}
	if err := readComposites(ctx, db, schemas, cat); err != nil {
		return nil, err
	}
	if err := readFunctions(ctx, db, schemas, cat); err != nil {
		return nil, err
	}
	cat.sortStable()
	return cat, nil
}

func readTables(ctx context.Context, db Querier, schemas []string, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT n.nspname, c.relname, c.relkind, c.relrowsecurity,
		       COALESCE(obj_description(c.oid, 'pg_class'), ''),
		       has_table_privilege(c.oid, 'SELECT'),
		       has_table_privilege(c.oid, 'INSERT'),
		       has_table_privilege(c.oid, 'UPDATE'),
		       has_table_privilege(c.oid, 'DELETE')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = ANY($1)
		  AND c.relkind IN ('r', 'p', 'v', 'm')
		ORDER BY n.nspname, c.relname`, schemas)
	if err != nil {
		return fmt.Errorf("introspect: tables: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		t := &Table{}
		var kind rune
		if err := rows.Scan(&t.Schema, &t.Name, &kind, &t.RLSEnabled, &t.Comment,
			&t.Privileges.Select, &t.Privileges.Insert, &t.Privileges.Update, &t.Privileges.Delete); err != nil {
			return err
		}
		switch kind {
		case 'r', 'p':
			t.Kind = RelTable
		case 'v':
			t.Kind = RelView
		case 'm':
			t.Kind = RelMaterializedView
		}
		if t.Privileges.Select {
			cat.Tables = append(cat.Tables, t)
		}
	}
	return rows.Err()
}

func readColumns(ctx context.Context, db Querier, schemas []string, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT n.nspname, c.relname, a.attname, a.attnum,
		       tn.nspname,
		       CASE WHEN t.typtype = 'd' THEN bt.typname ELSE t.typname END,
		       t.typcategory = 'A' OR (t.typtype = 'd' AND bt.typcategory = 'A'),
		       a.attnotnull,
		       a.atthasdef,
		       a.attidentity <> '' OR a.attgenerated <> '',
		       COALESCE(col_description(c.oid, a.attnum), '')
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_type t ON t.oid = a.atttypid
		JOIN pg_namespace tn ON tn.oid = t.typnamespace
		LEFT JOIN pg_type bt ON t.typtype = 'd' AND bt.oid = t.typbasetype
		WHERE n.nspname = ANY($1)
		  AND c.relkind IN ('r', 'p', 'v', 'm')
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY n.nspname, c.relname, a.attnum`, schemas)
	if err != nil {
		return fmt.Errorf("introspect: columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var schema, table string
		col := &Column{}
		if err := rows.Scan(&schema, &table, &col.Name, &col.Position,
			&col.TypeSchema, &col.PGType, &col.IsArray,
			&col.NotNull, &col.HasDefault, &col.Generated, &col.Comment); err != nil {
			return err
		}
		if t := cat.Table(schema, table); t != nil {
			t.Columns = append(t.Columns, col)
		}
	}
	return rows.Err()
}

func readConstraints(ctx context.Context, db Querier, schemas []string, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT n.nspname, c.relname, con.conname, con.contype,
		       ARRAY(SELECT a.attname FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
		             JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
		             ORDER BY k.ord),
		       COALESCE(fn.nspname, ''), COALESCE(fc.relname, ''),
		       COALESCE(ARRAY(SELECT a.attname FROM unnest(con.confkey) WITH ORDINALITY AS k(attnum, ord)
		             JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum
		             ORDER BY k.ord), '{}'),
		       COALESCE(obj_description(con.oid, 'pg_constraint'), '')
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_class fc ON fc.oid = con.confrelid
		LEFT JOIN pg_namespace fn ON fn.oid = fc.relnamespace
		WHERE n.nspname = ANY($1) AND con.contype IN ('p', 'u', 'f')
		ORDER BY n.nspname, c.relname, con.conname`, schemas)
	if err != nil {
		return fmt.Errorf("introspect: constraints: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var schema, table, name, comment string
		var ctype rune
		var cols, refCols []string
		var refSchema, refTable string
		if err := rows.Scan(&schema, &table, &name, &ctype, &cols, &refSchema, &refTable, &refCols, &comment); err != nil {
			return err
		}
		t := cat.Table(schema, table)
		if t == nil {
			continue
		}
		switch ctype {
		case 'p':
			t.PrimaryKey = &Constraint{Name: name, Columns: cols, Comment: comment}
		case 'u':
			t.Uniques = append(t.Uniques, &Constraint{Name: name, Columns: cols, Comment: comment})
		case 'f':
			t.ForeignKeys = append(t.ForeignKeys, &ForeignKey{
				Name: name, Columns: cols,
				RefSchema: refSchema, RefTable: refTable, RefColumns: refCols,
				Comment: comment,
			})
		}
	}
	return rows.Err()
}

func readIndexes(ctx context.Context, db Querier, schemas []string, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT n.nspname, c.relname, ic.relname, i.indisunique, am.amname,
		       ARRAY(SELECT a.attname FROM unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
		             JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		             WHERE k.attnum > 0 ORDER BY k.ord)
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_class ic ON ic.oid = i.indexrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_am am ON am.oid = ic.relam
		WHERE n.nspname = ANY($1) AND i.indisvalid
		ORDER BY n.nspname, c.relname, ic.relname`, schemas)
	if err != nil {
		return fmt.Errorf("introspect: indexes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var schema, table string
		ix := &Index{}
		if err := rows.Scan(&schema, &table, &ix.Name, &ix.Unique, &ix.Method, &ix.Columns); err != nil {
			return err
		}
		if t := cat.Table(schema, table); t != nil {
			t.Indexes = append(t.Indexes, ix)
		}
	}
	return rows.Err()
}

func readEnums(ctx context.Context, db Querier, schemas []string, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT n.nspname, t.typname,
		       ARRAY(SELECT e.enumlabel FROM pg_enum e WHERE e.enumtypid = t.oid ORDER BY e.enumsortorder),
		       COALESCE(obj_description(t.oid, 'pg_type'), '')
		FROM pg_type t
		JOIN pg_namespace n ON n.oid = t.typnamespace
		WHERE n.nspname = ANY($1) AND t.typtype = 'e'
		ORDER BY n.nspname, t.typname`, schemas)
	if err != nil {
		return fmt.Errorf("introspect: enums: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		e := &Enum{}
		if err := rows.Scan(&e.Schema, &e.Name, &e.Values, &e.Comment); err != nil {
			return err
		}
		cat.Enums = append(cat.Enums, e)
	}
	return rows.Err()
}

// readComposites reads user-defined composite types (CREATE TYPE ... AS).
// relkind = 'c' restricts to standalone composites, excluding the row types
// PostgreSQL creates implicitly for every table and view.
func readComposites(ctx context.Context, db Querier, schemas []string, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT n.nspname, t.typname, a.attname, a.attnum,
		       atn.nspname,
		       CASE WHEN at.typtype = 'd' THEN bt.typname ELSE at.typname END,
		       at.typcategory = 'A' OR (at.typtype = 'd' AND bt.typcategory = 'A'),
		       COALESCE(col_description(c.oid, a.attnum), '')
		FROM pg_type t
		JOIN pg_namespace n ON n.oid = t.typnamespace
		JOIN pg_class c ON c.oid = t.typrelid AND c.relkind = 'c'
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		JOIN pg_type at ON at.oid = a.atttypid
		JOIN pg_namespace atn ON atn.oid = at.typnamespace
		LEFT JOIN pg_type bt ON at.typtype = 'd' AND bt.oid = at.typbasetype
		WHERE n.nspname = ANY($1) AND t.typtype = 'c'
		ORDER BY n.nspname, t.typname, a.attnum`, schemas)
	if err != nil {
		return fmt.Errorf("introspect: composites: %w", err)
	}
	defer rows.Close()
	byKey := map[string]*Composite{}
	for rows.Next() {
		var schema, name string
		f := &Column{}
		if err := rows.Scan(&schema, &name, &f.Name, &f.Position,
			&f.TypeSchema, &f.PGType, &f.IsArray, &f.Comment); err != nil {
			return err
		}
		key := schema + "." + name
		comp := byKey[key]
		if comp == nil {
			comp = &Composite{Schema: schema, Name: name}
			byKey[key] = comp
			cat.Composites = append(cat.Composites, comp)
		}
		comp.Fields = append(comp.Fields, f)
	}
	return rows.Err()
}

func readFunctions(ctx context.Context, db Querier, schemas []string, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT n.nspname, p.proname, p.provolatile, p.proretset, rt.typname, rtn.nspname,
		       COALESCE(p.proargnames, '{}'),
		       ARRAY(SELECT t.typname FROM unnest(p.proargtypes) WITH ORDINALITY AS a(oid, ord)
		             JOIN pg_type t ON t.oid = a.oid ORDER BY a.ord),
		       ARRAY(SELECT tn.nspname FROM unnest(p.proargtypes) WITH ORDINALITY AS a(oid, ord)
		             JOIN pg_type t ON t.oid = a.oid
		             JOIN pg_namespace tn ON tn.oid = t.typnamespace ORDER BY a.ord),
		       COALESCE(obj_description(p.oid, 'pg_proc'), '')
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		JOIN pg_type rt ON rt.oid = p.prorettype
		JOIN pg_namespace rtn ON rtn.oid = rt.typnamespace
		WHERE n.nspname = ANY($1)
		  AND p.prokind = 'f'
		  AND p.pronargs = COALESCE(array_length(p.proargtypes, 1), 0)
		  AND rt.typname NOT IN ('trigger', 'event_trigger', 'internal')
		  AND has_function_privilege(p.oid, 'EXECUTE')
		  -- extension members (pgcrypto, uuid-ossp, ...) are implementation
		  -- detail, not API surface
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d
		                  WHERE d.objid = p.oid AND d.classid = 'pg_proc'::regclass
		                    AND d.deptype = 'e')
		ORDER BY n.nspname, p.proname`, schemas)
	if err != nil {
		return fmt.Errorf("introspect: functions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		f := &Function{}
		var vol rune
		var argNames, argTypes, argSchemas []string
		if err := rows.Scan(&f.Schema, &f.Name, &vol, &f.ReturnsSet, &f.ReturnType, &f.ReturnTypeSchema, &argNames, &argTypes, &argSchemas, &f.Comment); err != nil {
			return err
		}
		switch vol {
		case 'i':
			f.Volatility = VolatilityImmutable
		case 's':
			f.Volatility = VolatilityStable
		default:
			f.Volatility = VolatilityVolatile
		}
		if len(argNames) < len(argTypes) {
			continue // unnamed args not mappable to GraphQL arguments
		}
		for i, at := range argTypes {
			f.Args = append(f.Args, FuncArg{Name: argNames[i], PGType: at, TypeSchema: argSchemas[i]})
		}
		cat.Functions = append(cat.Functions, f)
	}
	return rows.Err()
}
