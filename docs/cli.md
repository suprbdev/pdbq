# CLI reference

```
pdbq serve                                run the API server
pdbq query [QUERY|-] [flags]              one-off query, JSON to stdout
pdbq schema dump|check|print              schema cache + SDL tools
pdbq config init|validate|example         configuration tools
pdbq plugins list                         registered plugins + hooks
```

All commands accept `--config` plus the config-path flags
(`--database.url`, ...).

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

## pdbq schema

```console
$ pdbq schema dump -o schema.cache   # introspect + write versioned cache
$ pdbq schema check --cache schema.cache   # exit 1 on drift (CI gate)
$ pdbq schema print                  # generated SDL to stdout
$ pdbq schema print --json           # introspection-format JSON (codegen tooling)
```

`print --json` runs the standard introspection query in-process and emits the
spec-shaped `{"data": {"__schema": ...}}` response, ready for
`graphql-codegen`, `graphql-inspector`, and similar tools. Combined with
`--schema.cache_path` it needs no database connection.

See [caching.md](caching.md) for the CI/CD patterns.

## Shell completions

`pdbq completion bash|zsh|fish|powershell` prints a completion script for the
given shell. Flag values with a fixed vocabulary (`--errors.detail`,
`--log.level`) complete too.

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
simple-names         priority=100  enabled  hooks: catalog, inflection
nested-mutations     priority=200  enabled  hooks: schema, compile, request
```
