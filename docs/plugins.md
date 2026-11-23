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

Hooks compose middleware-style: call `next(...)` to continue the chain (the
innermost link is pdbq's default behaviour). Ordering is deterministic —
priority, then registration order. Users can disable any plugin by name
(`plugins.disabled`) and configure it under `plugins.settings.<name>`.

## Worked example 1: simple-names

`internal/plugins/simplenames` is a pure naming plugin: ~60 lines, zero core
changes. It overrides a handful of `inflect.Kind`s (`users` instead of
`allUsers`, `user` instead of `userById`, `posts` instead of
`postsByAuthorId`) and delegates everything else to `next`. It also
implements `CatalogHook` — not to change the catalog, but to capture it so
it can detect ambiguous relations (a table with several FKs) and fall back
to the verbose name instead of colliding. Residual collisions are caught by
the schema builder, which keeps the first field and logs a warning.

## Worked example 2: advanced-filters

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
`plugins.settings.advanced-filters.relations` and `...computed` (both default
true), plus `...relation_order` (default true, also off when `relations` is
off) for the relational orderBy values. Computed filters are not
index-backed, so they sidestep the `filters.indexed_only` policy by nature —
leave `computed: false` if that matters for your workload. Relational order
columns *do* respect `filters.indexed_only`, exactly like the related table's
own orderBy enum.

The built-in `postgis` plugin (priority 160) follows exactly this shape for
PostGIS columns: it adds `GeometryFilterOps` fields, derived accessors,
projection arguments and distance-ordering enum values plus their bindings,
and the core compiler reads the bindings without a `CompileHook`. See
[PostGIS](postgis.md). The `contrib` plugin (priority 170) does the same for
full-text, pg_trgm, hstore, ltree, range and inet columns (vocabulary
operators, `<field>Rank` fields, rank and similarity ordering); see
[Contrib types](contrib.md). The `aggregates` plugin (priority 180, opt-in)
adds `@graphile/pg-aggregates`-style connection, grouped, relational-order and
relational-filter aggregates the same way: `FieldMeta.Aggregate`,
`InputField.Aggregate`, `EnumValue.GroupBy` and `OrderRelation.Aggregate`
bindings carry SQL templates the core compiler renders inside the statement,
and `FieldMeta.ScanCost` feeds the cost estimate; see
[Aggregates](aggregates.md).

## Worked example 3: smart-comments

`internal/plugins/smartcomments` combines three hooks to turn PostgreSQL
`COMMENT` tags (`@omit`, `@name`, `@primaryKey`, …) into schema customization
(full tag reference: `docs/smart-comments.md`):

- **CatalogHook** — parses and indexes every comment's tags, then edits the
  catalog: fully-omitted tables/functions/constraints are removed, mutation
  omits clear privilege flags, `@primaryKey`/`@unique`/`@foreignKey` add
  logical constraints, `@notNull` fixes view nullability, `@filterable` adds
  a synthetic index (the builder's indexed-only allow set).
- **InflectionHook** — `@name` replaces the pg identifier at the *head* of
  the naming chain and delegates to `next`, so downstream naming plugins
  (`simple-names`) and the default inflector derive everything from the new
  name; `@fieldName`/`@foreignFieldName` short-circuit with exact names.
  This is why the plugin runs at priority 50 (before `simple-names` at 100).
- **SchemaHook** — removes what has no catalog representation (per-action
  table/column omits, orderBy values), marks fields `@deprecated`, and strips
  tag lines from GraphQL descriptions. It runs before `advanced-filters`
  (150), so a dropped `<Type>Filter` is never referenced by relation filters.

`advanced-filters` reads the same tags straight from the catalog (`@omit
filter` on a FK, opt-in via `relations_opt_in`/`computed_opt_in` settings), so
per-object filter tuning works even with `smart-comments` disabled.

## Worked example 4: nested-mutations

`internal/plugins/nestedmutations` exercises every hook surface. Its input
shapes follow
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
| `updateBy<Key>`, `updateByNodeId` | patch the row and point this row's FK at it | patch rows that reference this row |
| `deleteBy<Key>`, `deleteByNodeId` | delete the row | delete rows that reference this row |
| `disconnectBy<Key>`, `disconnectByNodeId` | clear this row's FK if it references the row (update mutations only) | clear the FK of rows that reference this row |

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

- Key inputs are `<Type><Verb>By<Key>Input` (`UserConnectByEmailInput {
  email }`, `UserDeleteByIdInput { id }`) and `<Type><Verb>ByNodeIdInput {
  nodeId }`; update inputs add the patch as `<type>Patch`
  (`UserUpdateByIdInput { id, userPatch: UserUpdateInput! }`), which may
  carry nested fields itself. The nested inputs are
  `<Type><Relation>NestedInput` (`PostAuthorNestedInput`,
  `UserPostsByAuthorIdNestedInput`).
- An operation is only offered when the role may run it: `create` needs the
  related create input, `update` its patch, `delete` the `DELETE` privilege,
  backward `connect` the `UPDATE` privilege on the referencing table, and
  `disconnect` an FK whose columns are all nullable (a `NOT NULL` FK has no
  disconnect fields).
- A forward field takes at most one of `create`, `connect…`, `update…` and
  `disconnect…` (they all set this row's FK), plus any `delete…`. Forward
  delete leaves the FK alone, so the database's `ON DELETE` action applies;
  pair it with `disconnect…` to clear a nullable FK in the same mutation.
  Backward fields take any combination.
- Backward `update…`, `delete…` and `disconnect…` only reach rows that
  reference this row (on a create mutation there are none yet).
- `connect…`, `update…` and `delete…` must match a row: a key that matches
  nothing (missing, or filtered out by RLS) fails the whole mutation with
  `nested-mutations: <path> matched 0 rows` (SQLSTATE `22P02`, e.g.
  `postsByAuthorId.updateById[0]`). `disconnect…` of a row that is not
  linked does nothing. An update mutation whose own key matches nothing
  fails as usual (`no row matched`), rolling back its nested operations.
- The bulk mutations (`createUsers`, `updateUsers`) and upserts take no
  nested fields; using one is an error.
- A row may be modified only once per statement (a PostgreSQL rule for
  data-modifying CTEs): two operations updating the same row leave only one
  change, and an operation then reports `matched 0 rows`.

Differences from the PostGraphile plugin: the relation fields keep pdbq's
relation names instead of `<table>To<Columns>`/`<tables>Using<Columns>`,
the root mutations keep pdbq's argument shapes (`createPost(input: {…})`,
`updatePostById(id:, patch:)`), and there is no `deleteOthers`.

### Hooks

- **SchemaHook** — walks foreign keys and adds the nested fields above to
  create inputs and patches, relaxing FK columns a nested operation can
  supply to optional in create inputs (the database still enforces `NOT
  NULL`).
- **CompileHook** — when a create or update mutation actually uses a nested
  field, it replaces the DML with one multi-CTE statement: forward
  operations (lookups, inserts, `UPDATE … RETURNING`, `DELETE … RETURNING`)
  first, then the main row's `INSERT` or `UPDATE … FROM <parent CTEs>` (a
  patch setting no column selects the row instead), then the backward
  operations joined to the main row's CTE. Each operation that must match a
  row adds a guard, `(SELECT CASE WHEN count(*) > 0 THEN true ELSE
  concat('nested-mutations: …', count(*), ' rows')::boolean END FROM
  <cte>)`, which `compile.MutationWithCTEs` puts in the `WHERE` of the
  statement's final `SELECT`: a failing guard raises inside the statement,
  so it is atomic even without a transaction. Values go through
  `compile.ParamSet.Value`/`KeyValue` (enum labels, contrib and PostGIS
  constructors) and key comparisons through `compile.EqualOperator`
  (citext). The chain is handed to `compile.MutationWithCTEs`, which reuses
  the core payload compiler. Rows written by CTEs are invisible to
  same-statement table scans, so it also passes row-source overrides
  mapping every table the chain wrote to a visibility CTE: `__vis_n AS NOT
  MATERIALIZED (SELECT * FROM <table> WHERE <not replaced or deleted> UNION
  ALL SELECT * FROM <insert and update CTEs> …)`. Replaced and deleted rows
  are excluded by a `NOT NULL` key the operation leaves alone, matched
  against its `RETURNING` rows (or by the operation's key lookup when it
  patches every such key). Every payload relation, connection,
  `totalCount`, relation filter and relational `orderBy` over that table
  reads the CTE, so the payload sees the table as it stands after the
  statement. The table half stays subject to the role's RLS policies, and
  the `RETURNING` rows already passed them (an `INSERT`/`UPDATE …
  RETURNING` whose new row fails a `SELECT` policy is an error, rolling
  back the whole statement). Every operation runs as the request's role, so
  `UPDATE`/`DELETE` policies narrow what it can match.
- **RequestHook** — detects nested fields in a mutation's `input` or
  `patch` and sets `op.ForceTx`, so nested mutations (and the other fields
  of the operation) are atomic even when `transactions.mutations: false`
  (`plugins.settings.nested-mutations.force_transactions: false` opts out).

Nesting depth is bounded (`plugins.settings.nested-mutations.max_depth`,
default 3).
