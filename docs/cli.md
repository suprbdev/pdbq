# CLI reference

```
pdbq serve                                run the API server
pdbq query [QUERY|-] [flags]              one-off query, JSON to stdout
pdbq schema dump|check|print              schema cache + SDL tools
pdbq config init|validate|example         configuration tools
pdbq plugins list                         registered plugins + hooks
```

All commands accept `--config` plus the config-path flags
(`--database.url`, `--auth.mode`, `--roles.switch`, `--roles.anonymous`,
`--database.statement_timeout`, ...).

## Identity of commands without an HTTP request

`pdbq query`, `pdbq schema dump|check|print` and `pdbq plugins list` run
without an HTTP request, so they have no credentials to verify: they load the
config, set `auth.mode: none` and `roles.switch: false` whatever the file
says, and then validate it. They therefore run as the **connection role**,
with no claims set, and work on a default config that has no JWT secret.
Point `--database.url` at a role whose privileges fit what you run; row-level
security policies for request roles do not apply. `pdbq serve` and `pdbq
config validate` use the configuration as written.

## pdbq query

Pipe-friendly: the query comes from the argument, or stdin with `-`/no
argument. Variables via repeatable `--var k=v` (values parsed as JSON,
falling back to plain strings) or `--vars-file file.json` (`-` = stdin).

```console
$ echo '{allUsers(first: 5) {nodes {email}}}' | pdbq query -
$ pdbq query 'query($m: Mood!){allUsers(filter:{mood:{equalTo:$m}}){nodes {email}}}' --var m='"HAPPY"'
$ jq '.data.allUsers.nodes[].email' < <(pdbq query '{allUsers {nodes {email}}}')
```

Exit codes: `0` success, `1` transport/config error, `2` the response
contained GraphQL errors — so scripts can distinguish "down" from "bad
query".

### --print-sql

`--print-sql` prints the compiled SQL statement (one per root field) and its
parameter list instead of executing the query. All other flags work the same —
variables are still coerced and land in the parameter list, so what you see is
exactly what would run:

```console
$ pdbq query '{allUsers(first: 2) {nodes {email}}}' --print-sql
-- field: allUsers
SELECT json_build_object(...) AS data FROM ...
-- args: [2]
```

A custom mutation whose payload reads tables compiles to two statements (see
[What the payload sees](mutations.md#what-the-payload-sees)): the second
follows the first under a `-- then:` line, and reads the first one's result
as `$1`, so its parameter list starts at `$2`.

Nothing is executed — mutations are safe to inspect this way. Combined with
`--schema.cache_path` it needs no database connection at all. Introspection
root fields (`__schema`, `__type`) are resolved in memory and produce no SQL,
so they are omitted from the output.

## pdbq schema

```console
$ pdbq schema dump -o schema.cache   # introspect + write versioned cache
$ pdbq schema check --cache schema.cache   # exit 1 on drift (CI gate)
$ pdbq schema print                  # generated SDL to stdout
$ pdbq schema print --json           # introspection-format JSON (codegen tooling)
$ pdbq schema print --contract public  # the schema of one configured contract
```

`print --json` runs the standard introspection query in-process and emits the
spec-shaped `{"data": {"__schema": ...}}` response, ready for
`graphql-codegen`, `graphql-inspector`, and similar tools. Combined with
`--schema.cache_path` it needs no database connection.

`--contract <name>` prints the schema of a contract from the `contracts`
config list instead of the full schema (see [contracts.md](contracts.md)).

See [caching.md](caching.md) for the CI/CD patterns.

## Shell completions

`pdbq completion bash|zsh|fish|powershell` prints a completion script for the
given shell. Flag values with a fixed vocabulary (`--auth.mode`,
`--errors.detail`, `--log.level`) complete too.

```console
# bash (requires bash-completion)
$ pdbq completion bash > /etc/bash_completion.d/pdbq
# zsh
$ pdbq completion zsh > "${fpath[1]}/_pdbq"
# fish
$ pdbq completion fish > ~/.config/fish/completions/pdbq.fish
```

Run `pdbq completion <shell> --help` for per-shell install notes.

## pdbq plugins list

```console
$ pdbq plugins list
smart_comments       priority=50   enabled  hooks: catalog, inflection, schema
simple_names         priority=100  enabled  hooks: catalog, inflection
advanced_filters     priority=150  enabled  hooks: schema
postgis              priority=160  enabled  hooks: schema
contrib              priority=170  enabled  hooks: schema
aggregates           priority=180  enabled  hooks: schema
nested_mutations     priority=200  enabled  hooks: schema, compile, request
```
