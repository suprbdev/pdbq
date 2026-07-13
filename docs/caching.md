# Schema cache & CI/CD

Introspection needs broad `pg_catalog` access and adds cold-start latency.
The schema cache removes both:

```console
$ pdbq schema dump -o schema.cache        # at build/deploy time
$ pdbq serve --schema.cache_path schema.cache   # at runtime
```

The cache is gzipped JSON of the introspected catalog with a format version
and a content hash; `serve` refuses mismatched versions and corrupted files.
Booting from cache never touches `pg_catalog`, so the runtime role only
needs privileges on the actual data.

## CI drift gate

```console
$ pdbq schema check --cache schema.cache   # exit 1 when live DB drifted
```

Typical pipeline:

1. Migrations run.
2. `pdbq schema check` — fails the build if the committed cache no longer
   matches the migrated database.
3. On failure, regenerate (`pdbq schema dump`), review the diff of
   `pdbq schema print` output, commit both.

## Interactions

- `watch.enabled` and `schema.cache_path` are mutually exclusive (validated
  at startup): watch mode exists to chase a changing schema, the cache
  exists to freeze one.
- The cache stores the *catalog*, not the GraphQL schema — plugins and
  naming config still apply at boot, so changing plugin config does not
  require a new dump.
