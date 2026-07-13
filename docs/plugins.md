# Plugin author guide

pdbq v1 plugins are **compile-time**: a plugin is any Go type implementing
one or more hook interfaces, registered in an ordered registry. Third
parties embed pdbq as a library:

```go
app := pdbq.New(cfg, pdbq.WithPlugins(myPlugin{}))
app.Serve(ctx)
```

(Out-of-process plugins — go-plugin/wasm — are a v2 track; these interfaces
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
  PostToManyFilter`) — and exposes eligible computed columns (single
  row-argument stable functions) as filter fields and orderBy enum values
  (`postCount: BigIntFilterOps`, `POST_COUNT_DESC`).
- Each added field carries a binding (`InputField.Relation`,
  `InputField.Computed`, `EnumValue.Computed`) that `Builder.Build()` copies
  into `schema.Built`. The core compiler consults those tables when it meets
  a filter key with no column mapping — relations become `EXISTS` subqueries,
  computed columns become function calls over the current row — and stays
  inert when they are empty, so disabling the plugin removes the feature
  end to end. No `CompileHook` needed.

The halves toggle independently:
`plugins.settings.advanced-filters.relations` and `...computed` (both default
true). Computed filters are not index-backed, so they sidestep the
`filters.indexed_only` policy by nature — leave `computed: false` if that
matters for your workload.

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

`internal/plugins/nestedmutations` exercises every hook surface:

- **SchemaHook** — walks foreign keys and adds nested input fields:
  `PostCreateInput.author: {create | connect}` (FK on the table) and
  `UserCreateInput.postsByAuthorId: {create: [...]}` (FKs pointing at it),
  relaxing now-optional FK columns.
- **CompileHook** — when a create mutation actually uses a nested field, it
  replaces the DML with a multi-CTE statement (parents inserted/looked-up
  first, then the main row, then children), and hands the CTE chain to
  `compile.MutationWithCTEs`, which reuses the core payload compiler. Rows
  inserted in CTEs are invisible to same-statement table scans, so it also
  passes row-source overrides mapping the child table to a UNION of its
  insert CTEs.
- **RequestHook** — detects nested inputs in the operation and sets
  `op.ForceTx`, so nested mutations are atomic even when
  `transactions.mutations: false`
  (`plugins.settings.nested-mutations.force_transactions: false` opts out).

Nesting depth is bounded (`plugins.settings.nested-mutations.max_depth`,
default 3).
