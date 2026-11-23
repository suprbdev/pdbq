package live

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbcore/pgexec"
)

// truncateSuffix names the TRUNCATE trigger of a partitioned table.
const truncateSuffix = "_truncate"

// MaxChannelBytes is the longest channel accepted by ValidChannel: the
// TRUNCATE trigger's name, the channel plus "_truncate", must fit in
// PostgreSQL's 63-byte identifiers.
const MaxChannelBytes = 63 - len(truncateSuffix)

var channelPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidChannel reports whether channel can name the change notification
// channel, its triggers and its trigger function: letters, digits and
// underscores, not starting with a digit, at most MaxChannelBytes bytes.
func ValidChannel(channel string) bool {
	return len(channel) <= MaxChannelBytes && channelPattern.MatchString(channel)
}

// InstallOptions configures Install.
type InstallOptions struct {
	// Channel is the notification channel; it also names the triggers
	// (Channel, and Channel_truncate on partitioned tables) and the
	// trigger function (Channel_notify).
	Channel string
	// Schemas are the exposed schemas. Their tables are tracked, as are
	// the tables their views read, in any schema.
	Schemas []string
	// Create installs the function and the missing triggers; false only
	// reports which tables have them.
	Create bool
	Log    *slog.Logger
}

// tablesSQL lists the tables to track: the ordinary and partitioned tables
// of the schemas ($1) and the tables their views read, recursively through
// views, but not partitions, which the triggers of their partitioned root
// cover. has_trigger and has_truncate tell whether the triggers named $2
// and $2_truncate exist.
const tablesSQL = `
WITH RECURSIVE rels(oid) AS (
	SELECT c.oid FROM pg_catalog.pg_class c
	JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname = ANY ($1::text[]) AND c.relkind IN ('r', 'p', 'v')
	UNION
	SELECT d.refobjid FROM rels
	JOIN pg_catalog.pg_class v ON v.oid = rels.oid AND v.relkind = 'v'
	JOIN pg_catalog.pg_rewrite rw ON rw.ev_class = v.oid
	JOIN pg_catalog.pg_depend d ON d.classid = 'pg_catalog.pg_rewrite'::regclass
		AND d.objid = rw.oid AND d.refclassid = 'pg_catalog.pg_class'::regclass AND d.refobjid <> v.oid
)
SELECT c.oid, pg_catalog.quote_ident(n.nspname) || '.' || pg_catalog.quote_ident(c.relname), c.relkind = 'p',
	EXISTS (SELECT 1 FROM pg_catalog.pg_trigger t WHERE t.tgrelid = c.oid AND t.tgname = $2),
	EXISTS (SELECT 1 FROM pg_catalog.pg_trigger t WHERE t.tgrelid = c.oid AND t.tgname = $2 || '` + truncateSuffix + `')
FROM rels
JOIN pg_catalog.pg_class c ON c.oid = rels.oid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition AND NOT c.relpersistence = 't'
	AND n.nspname NOT IN ('pg_catalog', 'information_schema')
ORDER BY 2`

type table struct {
	oid         uint32
	name        string
	partitioned bool
	hasTrigger  bool
	hasTruncate bool
}

// Install installs the change triggers of live queries and returns the
// tracked tables that have them, by OID. Every ordinary table gets an
// AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE statement trigger; a
// partitioned table gets an AFTER INSERT OR UPDATE OR DELETE row trigger,
// which PostgreSQL clones onto every partition, present and future, plus
// an AFTER TRUNCATE statement trigger. Each trigger calls Channel_notify,
// which notifies Channel with the table's (or partitioned root's) OID, so
// a statement changing many rows sends one notification per transaction.
// Existing triggers are kept. A trigger that cannot be created (the table
// is not owned by the connecting role, or a lock is not granted within 5
// seconds) is logged and left out of the result; the error reports only a
// failure to list the tables or to create the function.
func Install(ctx context.Context, pool *pgxpool.Pool, o InstallOptions) (map[uint32]bool, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if !ValidChannel(o.Channel) {
		return nil, fmt.Errorf("live: invalid channel %q", o.Channel)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	tables, err := listTables(ctx, conn, o)
	if err != nil {
		return nil, err
	}
	tracked := map[uint32]bool{}
	var missing []table
	for _, t := range tables {
		if t.hasTrigger && (!t.partitioned || t.hasTruncate) {
			tracked[t.oid] = true
		} else {
			missing = append(missing, t)
		}
	}
	if len(missing) == 0 || !o.Create {
		if len(missing) > 0 {
			o.Log.Warn("live: tables without the change trigger; their changes do not refresh live queries", "tables", names(missing))
		}
		return tracked, nil
	}
	fn := pgexec.QuoteIdent(o.Channel + "_notify")
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
	PERFORM pg_notify(%s, TG_ARGV[0]);
	RETURN NULL;
END $$`, fn, quoteLiteral(o.Channel))); err != nil {
		return tracked, fmt.Errorf("live: create trigger function: %w", err)
	}
	if _, err := conn.Exec(ctx, "SET lock_timeout = '5s'"); err != nil {
		return tracked, err
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "RESET lock_timeout") }()
	var failed []string
	for _, t := range missing {
		arg := quoteLiteral(strconv.FormatUint(uint64(t.oid), 10))
		var stmts []string
		if t.partitioned {
			if !t.hasTrigger {
				stmts = append(stmts, fmt.Sprintf("CREATE TRIGGER %s AFTER INSERT OR UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s(%s)",
					pgexec.QuoteIdent(o.Channel), t.name, fn, arg))
			}
			if !t.hasTruncate {
				stmts = append(stmts, fmt.Sprintf("CREATE TRIGGER %s AFTER TRUNCATE ON %s FOR EACH STATEMENT EXECUTE FUNCTION %s(%s)",
					pgexec.QuoteIdent(o.Channel+truncateSuffix), t.name, fn, arg))
			}
		} else {
			stmts = append(stmts, fmt.Sprintf("CREATE TRIGGER %s AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON %s FOR EACH STATEMENT EXECUTE FUNCTION %s(%s)",
				pgexec.QuoteIdent(o.Channel), t.name, fn, arg))
		}
		ok := true
		for _, sql := range stmts {
			if _, err := conn.Exec(ctx, sql); err != nil && !isDuplicate(err) {
				if ctx.Err() != nil {
					return tracked, ctx.Err()
				}
				o.Log.Debug("live: create change trigger", "table", t.name, "err", err)
				ok = false
				break
			}
		}
		if ok {
			tracked[t.oid] = true
		} else {
			failed = append(failed, t.name)
		}
	}
	if len(failed) > 0 {
		o.Log.Warn("live: cannot install the change trigger (the table must be owned by the connecting role); changes to these tables do not refresh live queries", "tables", strings.Join(failed, ", "))
	}
	return tracked, nil
}

func listTables(ctx context.Context, conn *pgxpool.Conn, o InstallOptions) ([]table, error) {
	rows, err := conn.Query(ctx, tablesSQL, o.Schemas, o.Channel)
	if err != nil {
		return nil, fmt.Errorf("live: list tables: %w", err)
	}
	defer rows.Close()
	var out []table
	for rows.Next() {
		var t table
		if err := rows.Scan(&t.oid, &t.name, &t.partitioned, &t.hasTrigger, &t.hasTruncate); err != nil {
			return nil, fmt.Errorf("live: list tables: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("live: list tables: %w", err)
	}
	return out, nil
}

func names(ts []table) string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.name
	}
	return strings.Join(out, ", ")
}

// isDuplicate reports a trigger created concurrently by another instance.
func isDuplicate(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42710"
}

func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
