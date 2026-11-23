# Plugin author guide

pdbq v1 plugins are **compile-time**: a plugin is any Go type implementing
one or more hook interfaces, registered in an ordered registry. Third
parties embed pdbq as a library:

```go
app := pdbq.New(cfg, pdbq.WithPlugins(myPlugin{}))
app.Serve(ctx)
```

(Out-of-process plugins — go-plugin/wasm — are a possible future direction; these interfaces
are the contract either way.)

## Hook surfaces

Implement only what you need; every hook is optional:

```go
type Plugin interface {            // required base
    Name() string                  // config key under plugins.settings
    Priority() int                 // lower runs earlier / outermost
}

type CatalogHook interface {       // mutate/filter the introspected catalog
    TransformCatalog(ctx context.Context, c *introspect.Catalog) error
}
type InflectionHook interface {    // control every generated name
    Inflect(kind inflect.Kind, in inflect.Input, next inflect.Next) string
}
type SchemaHook interface {        // add/remove/wrap fields & types
    TransformSchema(ctx context.Context, s *schema.Builder) error
}
type CompileHook interface {       // wrap SQL generation per root field
    WrapCompile(next compile.Func) compile.Func
}
type RequestHook interface {       // request lifecycle (auth, logging, tx policy)
    BeforeOperation(ctx context.Context, op *exec.Operation) (context.Context, error)
    AfterOperation(ctx context.Context, op *exec.Operation, res *exec.Result)
}
```

Import paths: `Plugin`, `CatalogHook` and `InflectionHook` — together with
`introspect.Catalog`, `inflect.Kind`/`Input`/`Next` and the naming helpers
(`inflect.UpperCamel`, `Singularize`, …) — are defined in the shared engine
module `github.com/suprbdev/pdbcore` (`pdbcore/plugin`, `pdbcore/introspect`,
`pdbcore/inflect`); a catalog- or naming-only plugin therefore compiles
against pdbcore alone and works in pdbr too. pdbq's `Kind*` constants
(`KindAllRowsField`, …) and `inflect.Default` stay in pdbq
(`internal/inflect`), as do the GraphQL-typed `SchemaHook`, `CompileHook` and
`RequestHook` (`internal/plugin`; re-exported for embedders through the
`pdbq` package: `pdbq.Plugin`, `pdbq.CatalogHook`, `pdbq.InflectionHook`,
`pdbq.SchemaHook`, `pdbq.CompileHook`, `pdbq.RequestHook`).

Inside a request, the executor attaches the in-flight operation to the
context before hooks and compilation run, so a `CompileHook` (whose
`compile.Func` only receives a context) can read the verified claims, role,
and operation metadata: `exec.OperationFromContext(ctx)` /
`exec.ClaimsFromContext(ctx)`.

A `compile.Statement` may carry a `Then` statement, run after it in the same
transaction with its result as `$1` (a custom mutation whose payload reads
tables; see [Mutations](mutations.md#what-the-payload-sees)). A
`CompileHook` that rewrites a statement returned by `next` keeps `Then`;
the field's value is what the last statement returns.

One plugin instance builds every schema the App serves: one per
[contract](contracts.md), and again on every watch-mode rebuild, while the
executors of earlier schemas keep serving. A plugin whose `CompileHook` or
`RequestHook` needs what its `SchemaHook` learned stores it in
`SchemaBuilder.Ext[<plugin name>]` and reads it back from the request's
schema (`compile.Request.Built.Ext`, `exec.Operation.Built.Ext`), never from
its own fields; `nested_mutations` does this.

Hooks compose middleware-style: call `next(...)` to continue the chain (the
innermost link is pdbq's default behaviour). Ordering is deterministic —
priority, then registration order.

## Built-in plugins and their settings

Plugin names are snake_case: `smart_comments` (priority 50),
`simple_names` (100), `advanced_filters` (150), `postgis` (160), `contrib`
(170), `aggregates` (180) and `nested_mutations` (200). Users disable a
built-in by name in `plugins.disabled`; any other name is a config error with
a "did you mean" hint (`smart-comments` → `smart_comments`). A plugin added
with `pdbq.WithPlugins` is enabled by registering it.

Each configurable built-in has a typed settings section under
`plugins.settings.<name>`, declared by its package as a `Settings` struct
(with `koanf` and `doc` tags, a `DefaultSettings()` and, where values are
constrained, a `Validate()` run by config validation) and passed to its
`New`. Unknown keys are config errors, and environment variables decode like
any other key:

```console
$ PDBQ_PLUGINS_SETTINGS_ADVANCED__FILTERS_RELATIONS=false pdbq serve
$ PDBQ_PLUGINS_SETTINGS_NESTED__MUTATIONS_MAX__DEPTH=2 pdbq serve
```

(`__` keeps the underscore inside `advanced_filters` and `max_depth`.) The
`aggregates` plugin is the exception: it is configured by the top-level
`aggregates` section (`PDBQ_AGGREGATES_ENABLED=true`), whose `enabled`
gate has the same name in pdbr; see [Aggregates](aggregates.md#settings).

| Key | Default | Effect |
|---|---|---|
| `advanced_filters.relations` | `true` | relation filters (forward, reverse `some`/`none`/`every`, `<rel>Exists`) |
| `advanced_filters.computed` | `true` | filtering and ordering by computed columns |
| `advanced_filters.relation_order` | `true` | relational orderBy values (`REL__COLUMN_ASC`) |
| `advanced_filters.relations_opt_in` | `false` | only `@filterable` foreign keys get relation surface |
| `advanced_filters.computed_opt_in` | `false` | only `@filterable` computed columns get filter/order surface |
| `postgis.filters` | `true` | `GeometryFilterOps`, `RasterFilterOps` |
| `postgis.derived` | `true` | derived and raster accessors |
| `postgis.ordering` | `true` | distance ordering, `<field>Distance`, `near:` |
| `postgis.projection` | `true` | `buffer:`, `simplify:`, `transform:`, `precision:` |
| `postgis.typed_geometry` | `false` | typed geometry objects instead of `GeoJSON` |
| `contrib.filters` | `true` | contrib filter operators and rank fields ([Contrib](contrib.md#settings)) |
| `contrib.ordering` | `true` | rank, similarity and distance ordering |
| `nested_mutations.max_depth` | `3` | nesting levels per root field (`>= 1`) |
| `nested_mutations.max_operations` | `50` | nested operations per root field (`>= 1`) |

`smart_comments` and `simple_names` have no settings.

## Worked example 1: simple_names

`internal/plugins/simplenames` is a pure naming plugin: ~60 lines, zero core
changes. It overrides a handful of `inflect.Kind`s (`users` instead of
`allUsers`, `user` instead of `userById`, `posts` instead of
`postsByAuthorId`) and delegates everything else to `next`. It also
implements `CatalogHook` — not to change the catalog, but to capture it so
it can detect ambiguous relations (a table with several FKs) and fall back
to the verbose name instead of colliding. Residual collisions are caught by
the schema builder, which keeps the first field and logs a warning.

## Worked example 2: advanced_filters

`internal/plugins/advancedfilters` shows the "schema surface + metadata"
pattern: the plugin implements only `SchemaHook`, yet changes compiled SQL.

- It adds relation fields to every `<Type>Filter` input — a forward FK takes
  the parent's filter directly (`PostFilter.author: UserFilter`), a reverse
  FK takes a `{some|none|every}` wrapper (`UserFilter.postsByAuthorId:
  PostToManyFilter`), and each direction gets a Boolean existence variant
  (`PostFilter.authorExists: Boolean`, `UserFilter.postsByAuthorIdExist:
  Boolean`) compiling to a bare `(NOT) EXISTS` — adds one-level relational
  orderBy values (`AUTHOR__EMAIL_ASC` on posts,
  `POSTS_BY_AUTHOR_ID__TITLE_DESC` on users; reverse relations aggregate with
  `MIN`/`MAX`) — and exposes eligible
  computed columns (single
  row-argument stable functions) as filter fields and orderBy enum values
  (`postCount: BigIntFilterOps`, `POST_COUNT_DESC`).
- Each added field carries a binding (`InputField.Relation`,
  `InputField.Computed`, `EnumValue.Computed`, `EnumValue.Relation`) that
  `Builder.Build()` copies
  into `schema.Built`. The core compiler consults those tables when it meets
  a filter key with no column mapping — relations become `EXISTS` subqueries,
  computed columns become function calls over the current row — and stays
  inert when they are empty, so disabling the plugin removes the feature
  end to end. No `CompileHook` needed.
- Forward relation names come from `Builder.ForwardRelationName`, which
  inflects `KindRelationForward`, or `KindRelationForwardMany` for a
  temporal (`PERIOD`) foreign key; such a key may reference several rows
  (`ForeignKey.ReferencesOne()` is false), so it gets the reverse-relation
  shapes (see [Temporal keys](temporal.md)).

The halves toggle independently:
`plugins.settings.advanced_filters.relations` and `...computed` (both default
true), plus `...relation_order` (default true, also off when `relations` is
off) for the relational orderBy values. Computed filters are not
index-backed, so they sidestep the `filters.indexed_only` policy by nature —
leave `computed: false` if that matters for your workload. Relational order
columns *do* respect `filters.indexed_only`, exactly like the related table's
own orderBy enum.

The built-in `postgis` plugin (priority 160) follows exactly this shape for
PostGIS columns: it adds `GeometryFilterOps` fields, derived accessors,
projection arguments and distance-ordering enum values plus their bindings
(and the raster accessors, pixel value, statistics and `RasterFilterOps`),
and the core compiler reads the bindings without a `CompileHook`. See
[PostGIS](postgis.md). The `contrib` plugin (priority 170) does the same for
full-text, pg_trgm, unaccent, hstore, ltree, range, inet, pgvector, cube
and earthdistance columns (vocabulary operators, `<field>Rank` and
`<field>Headline` fields, rank, similarity, vector, cube and earth distance
ordering); see
[Contrib types](contrib.md). Fields whose values are expensive to compute
declare it on their binding: `FieldMeta.RowCost` is charged each time the
field is evaluated (headlines, raster pixel values and summary statistics) and `FieldMeta.ArgCost` per argument given a
non-null value (the spatial `buffer:`), both in the cost estimate. The `aggregates` plugin (priority 180, opt-in)
adds `@graphile/pg-aggregates`-style connection, grouped, relational-order and
relational-filter aggregates the same way: `FieldMeta.Aggregate`,
`InputField.Aggregate`, `EnumValue.GroupBy` and `OrderRelation.Aggregate`
bindings carry SQL templates the core compiler renders inside the statement,
and `FieldMeta.ScanCost` feeds the cost estimate; see
[Aggregates](aggregates.md).

## Worked example 3: smart_comments

`internal/plugins/smartcomments` combines three hooks to turn PostgreSQL
`COMMENT` tags (`@omit`, `@name`, `@primaryKey`, …) into schema customization
(full tag reference: `docs/smart-comments.md`):

- **CatalogHook** — parses and indexes every comment's tags, then edits the
  catalog: fully-omitted tables/functions/constraints are removed, mutation
  omits clear privilege flags, `@primaryKey`/`@unique`/`@foreignKey` add
  logical constraints, `@notNull` fixes view nullability, `@filterable` adds
  a synthetic index (the builder's indexed-only allow set). It then drops
  what an omitted object would leave reachable: keys over a column hidden
  from read, and functions over an omitted table's rows.
- **InflectionHook** — `@name` replaces the pg identifier at the *head* of
  the naming chain and delegates to `next`, so downstream naming plugins
  (`simple_names`) and the default inflector derive everything from the new
  name; `@fieldName`/`@foreignFieldName` short-circuit with exact names.
  This is why the plugin runs at priority 50 (before `simple_names` at 100).
- **SchemaHook** — removes what has no catalog representation (per-action
  table/column omits, orderBy and distinctOn values), marks fields `@deprecated`, and strips
  tag lines from GraphQL descriptions. It runs before `advanced_filters`
  (150), so a dropped `<Type>Filter` is never referenced by relation filters.

`advanced_filters` reads the same tags straight from the catalog (`@omit
filter` on a FK, opt-in via `relations_opt_in`/`computed_opt_in` settings), so
per-object filter tuning works even with `smart_comments` disabled. Every
plugin decides through `internal/omit`, the one place that maps tags to the
surfaces they hide.

## Worked example 4: nested_mutations

`internal/plugins/nestedmutations` exercises the schema and compile hook
surfaces. Its input shapes follow
[postgraphile-plugin-nested-mutations](https://github.com/mlipscombe/postgraphile-plugin-nested-mutations).

### Input shape

Every create input (`PostCreateInput`) and patch (`PostUpdateInput`) gets one
field per foreign key, in both directions, named like the relation field of
the row type (`author`, `postsByAuthorId`; `@fieldName`/`@foreignFieldName`
rename both). Its type lists the operations on the related rows:

| Operation | Forward (`post.author`, the FK is on this row) | Backward (`user.postsByAuthorId`, the FK points here) |
|---|---|---|
| `connectBy<Key>`, `connectByNodeId` | look the row up and point this row's FK at it | point the row's FK at this row |
| `create` | insert the row and point this row's FK at it | insert rows referencing this row |
| `updateBy<Key>`, `updateByNodeId` (patches only) | patch the row this row references | patch rows that reference this row |
| `deleteBy<Key>`, `deleteByNodeId` (patches only) | delete the row this row references | delete rows that reference this row |
| `disconnectBy<Key>`, `disconnectByNodeId` (patches only) | clear this row's FK if it references the row | clear the FK of rows that reference this row |

`<Key>` is each primary key and unique constraint of the related table
(`connectById`, `connectByEmail`, `connectByRoomIdAndValidAt`); the
`ByNodeId` variants exist when its type implements `Node`. A forward
operation takes one input, a backward one a list (a single object is
coerced to a one-element list):

```graphql
mutation {
  updateUserById(id: 1, patch: {
    fullName: "Ada"
    postsByAuthorId: {
      connectById: [{id: 7}]
      create: [{title: "New"}]
      updateById: [{id: 3, postPatch: {title: "Renamed"}}]
      deleteByNodeId: [{nodeId: "WyJQb3N0IiwgNF0="}]
    }
  }) { user { postsByAuthorId { totalCount nodes { title } } } }
  createPost(input: {title: "Hi", author: {connectByEmail: {email: "ada@example.com"}}}) {
    post { author { fullName } }
  }
}
```

- Create inputs take `<Type><Relation>NestedInput` (`PostAuthorNestedInput`,
  `UserPostsByAuthorIdNestedInput`), which only offers `connect…` and
  `create`: a row being created references nothing yet and nothing
  references it. Patches take `<Type><Relation>NestedPatchInput` with every
  operation.
- Key inputs are `<Type><Verb>By<Key>Input` (`UserConnectByEmailInput {
  email }`, `UserDeleteByIdInput { id }`) and `<Type><Verb>ByNodeIdInput {
  nodeId }`; update inputs add the patch as `<type>Patch`
  (`UserUpdateByIdInput { id, userPatch: UserUpdateInput! }`), which may
  carry nested fields itself.
- An operation is only offered when the role may run it and the input may
  write the columns it sets: `create` needs the related create input,
  `update` its patch, `delete` the `DELETE` privilege; operations that set
  an FK (`connect…`, `create`, `disconnect…`) need its columns in the input
  that would otherwise set them (an FK column hidden by `@omit create` or
  `@omit update` gets no such operation on that input) and the referenced
  columns readable (an FK referencing a column tagged `@omit read` gets none,
  since they would copy the hidden value into the readable FK column), and
  `disconnect…` needs all of them nullable (a `NOT NULL` FK has no
  disconnect fields). `@omit many` on the FK or the referencing table
  removes the one-to-many nested field.
- A forward field takes at most one of `create`, `connect…` and
  `disconnect…` (they set this row's FK), plus any `update…` and
  `delete…`, which only reach the row this row references before the
  mutation. Forward delete leaves the FK alone, so the database's `ON
  DELETE` action applies; pair it with `disconnect…` to clear a nullable FK
  in the same mutation. Backward fields take any combination; `update…`,
  `delete…` and `disconnect…` only reach rows that reference this row.
- A patch may not change a column that a backward nested field of the same
  row matches its children on (the referenced key); that is a client error.
- `connect…`, `update…` and `delete…` must match a row, and so must the key
  of an update mutation using nested fields: a key that matches nothing
  (missing, or filtered out by RLS) fails the whole mutation with
  `nested_mutations: <path> matched 0 rows` (SQLSTATE `22P02`, e.g.
  `postsByAuthorId.updateById[0]` or `updateUserById`). `disconnect…` of a
  row that is not linked does nothing.
- At most `plugins.settings.nested_mutations.max_operations` operations
  (default 50, each list item counting once) run per root field, nested at
  most `max_depth` levels (default 3).
- The bulk mutations (`createUsers`, `updateUsers`) and upserts take no
  nested fields; using one is an error.
- A row may be modified only once per statement (a PostgreSQL rule for
  data-modifying CTEs): two operations updating the same row leave only one
  change, and an operation then reports `matched 0 rows`.
- **Referential actions are not reflected in the payload.** `ON DELETE
  CASCADE`/`SET NULL`/`SET DEFAULT` and `ON UPDATE CASCADE` run as
  triggers after the statement's last query, so rows they change or delete
  still show in the payload as they were before them (a forward
  `deleteBy…` whose FK cascades to this row returns the row although it is
  gone). The database state is correct; query it again to see the result.

Differences from the PostGraphile plugin: the relation fields keep pdbq's
relation names instead of `<table>To<Columns>`/`<tables>Using<Columns>`,
the root mutations keep pdbq's argument shapes (`createPost(input: {…})`,
`updatePostById(id:, patch:)`), forward `update…`/`delete…` act on the
referenced row instead of relinking, create inputs offer no
update/delete/disconnect, and there is no `deleteOthers`.

### Hooks

- **SchemaHook** — walks foreign keys and adds the nested fields above to
  create inputs and patches, relaxing FK columns a nested operation can
  supply to optional in create inputs (the database still enforces `NOT
  NULL`).
- **CompileHook** — when a create or update mutation actually uses a nested
  field, it replaces the DML with one multi-CTE statement: forward
  operations (lookups, inserts, `UPDATE … RETURNING`, `DELETE … RETURNING`)
  first, then the main row's `INSERT` or `UPDATE … FROM <parent CTEs>`,
  then the backward operations joined to the main row's CTE. A patch that
  sets no column locks the row with `SELECT … FOR UPDATE` instead, which
  still requires the `UPDATE` privilege and passes the table's `UPDATE`
  policies. Each operation that must match a row adds a guard, `(SELECT
  CASE WHEN count(*) > 0 THEN true ELSE concat('nested_mutations: …',
  count(*), ' rows')::boolean END FROM <cte>)`. `compile.MutationStatement`
  evaluates the guards in a `MATERIALIZED` CTE scanned as the outer side of
  a `LEFT JOIN LATERAL` with the payload, so they run even when the payload
  is empty, and a failing guard raises inside the statement: it is atomic
  even without a transaction. Values go through
  `compile.ParamSet.Value`/`KeyValue` (enum labels, contrib and PostGIS
  constructors) and key comparisons through `compile.EqualOperator`
  (citext).

  The payload is compiled with `compile.MutationPayload`. Rows written by
  CTEs are invisible to same-statement table scans, so it passes row-source
  overrides mapping every table the chain wrote to a visibility CTE:
  `__vis_n AS NOT MATERIALIZED (SELECT <cols> FROM <table> WHERE <not
  replaced or deleted> UNION ALL SELECT <cols> FROM <insert and update
  CTEs> …)`. Replaced and deleted rows are excluded by a `NOT NULL` key the
  operation leaves alone, matched against its returned rows (or by the
  operation's key lookup when it patches every such key). Every payload
  relation, connection, `totalCount`, relation filter and relational
  `orderBy` over that table reads the CTE, so the payload sees the table as
  it stands after the statement. The table half stays subject to the
  role's RLS policies, and the returned rows already passed them (an
  `INSERT`/`UPDATE … RETURNING` whose new row fails a `SELECT` policy is an
  error, rolling back the whole statement). Every operation runs as the
  request's role, so `UPDATE`/`DELETE` policies narrow what it can match.

  `RETURNING` and `SELECT` need the `SELECT` privilege per column, so
  nothing selects `*`: once the payload is compiled, its
  `MutationPayload.Aliases` and `compile.ReferencedColumns` give the
  columns it reads through each override and `__mut`. Visibility CTEs
  project only those (and are left out for tables the payload does not
  read), and each CTE's `RETURNING` list is resolved to the columns the
  payload, the visibility CTEs and the other CTEs (FK copies, child
  matching, exclusion keys) read from it.
