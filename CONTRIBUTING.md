# Contributing to pdbq

Thanks for helping. pdbq is a zero-boilerplate GraphQL API for PostgreSQL;
the transport-neutral engine it shares with pdbr lives in
[pdbcore](https://github.com/suprbdev/pdbcore). Security issues go through
[SECURITY.md](SECURITY.md), not public issues.

## Development setup

You need Go (the version in `go.mod`), Docker with the Compose plugin for the
database-backed tests, and optionally `golangci-lint` and
[`act`](https://github.com/nektos/act).

```console
$ git clone https://github.com/suprbdev/pdbq && cd pdbq
$ make test        # unit and golden tests, hermetic, go test -race ./...
$ make lint        # golangci-lint
$ make build       # static binary in bin/pdbq
$ make help        # every target
```

`make dev` starts a Postgres and pdbq in watch mode (`docker compose up --build`); the stack publishes no host ports, so add a gitignored `compose.override.yaml` with port mappings (see `docs/quickstart.md`).

## Tests

- `make test` needs no database.
- `make test-e2e` starts `compose.test.yaml` (project `pdbq-test`, host port
  5433, `postgis/postgis:18-3.7`), installs pgvector into the container on a
  best-effort basis (its tests skip without it), runs `./test/` and tears the
  database down. The suite reads
  `PDBQ_TEST_DATABASE_URL`, so you can also point it at your own database loaded from
  `db/init/01-schema.sql`.
- **Compiler goldens.** Each `internal/compile/testdata/*.graphql` document has
  a sibling `*.sql` with the compiled statement. After touching the compiler,
  regenerate with `go test ./internal/compile/ -update` and review the diff:
  a changed golden is a behaviour change. The format is described in
  `CLAUDE.md`'s "Golden tests" section.
- **Generated files.** `make example-config` regenerates `examples/pdbq.example.yaml` from the
  config struct tags (never edit it by hand), and every config key needs a row
  in the knobs table of `docs/configuration.md`.
- `make bench` and `make fuzz` (`FuzzFilter`) cover benchmarks and fuzzing.

## The fixture lives in pdbcore

The demo schema and its Go twin are shared by pdbcore, pdbq and pdbr.
`db/init/01-schema.sql` is generated from pdbcore (`make fixture`, never edit it
by hand). A fixture change is a pdbcore pull request first, then a pdbcore
release, then `go get github.com/suprbdev/pdbcore@vX.Y.Z` and `make fixture` here
together with the golden updates that follow. To co-develop, add a
`replace github.com/suprbdev/pdbcore => ../pdbcore` directive locally and never
commit it. pdbcore's CI builds and tests this repository's `main` against every
pdbcore pull request, so a break in pdbq shows up there.

## Running CI locally

Run the workflow with `act` before pushing, with `--pull=false` to avoid Docker
Hub rate limits:

```console
$ act pull_request -W .github/workflows/ci.yaml -j lint-test --pull=false
$ act pull_request -W .github/workflows/ci.yaml -j e2e --matrix pg:19 --pull=false
```

Matrix entries must run one at a time (they share host port 5433). The e2e job can hang in `apt-get` under `act`: stop it after about ten minutes and rely on hosted CI. Pull requests run PostgreSQL 14 and 19; pushes to `main` run 14
to 19. If another `act` run owns the containers, wait and retry rather than
removing them.

## Load, DoS and fuzz tests

Anything that starts a server for load, DoS or fuzz probing must run under a
hard memory cap, because an uncapped probe once exhausted a developer machine:

```console
$ systemd-run --user --scope -p MemoryMax=2G -p MemorySwapMax=0 <command>
```

Keep request sizes bounded. Load benchmarks against PostgREST and PostGraphile
live in [pg-vs-pdbq](https://github.com/suprbdev/pg-vs-pdbq).

## Commits and pull requests

Use [Conventional Commits](https://www.conventionalcommits.org/) for commit
messages and pull request titles: `type(scope): subject` with types such as
`feat`, `fix`, `perf`, `refactor`, `test`, `docs`, `ci` and `chore`, and `!` or
a `BREAKING CHANGE:` footer for breaking changes. Merges are squashed and the
squash subject becomes the changelog entry, so make it read as one. Do not put
issue references in commit messages. In Markdown, put code-like `@` words
(smart tags such as `@omit`) in backticks so GitHub does not mention an account.

Tick the box in [docs/features.md](docs/features.md) in the pull request that
ships a feature, and add a line under "Unreleased" in
[CHANGELOG.md](CHANGELOG.md) for anything a user would notice. Changes to the
documented surface (configuration keys, CLI, HTTP, error codes) follow
[docs/compatibility.md](docs/compatibility.md) and, until 1.0, are listed in
[docs/upgrading.md](docs/upgrading.md).

## Releasing

Maintainers run `make release` on an up-to-date `main`: it checks the tree, asks
for `vX.Y.Z`, runs the tests and lint, tags and pushes. The `Release` workflow
builds the binaries and the container image, generates a CycloneDX SBOM, signs
the checksums and the image with cosign and attests build provenance. Move the
"Unreleased" notes in `CHANGELOG.md` under the new version before tagging.
pdbq is released after pdbcore and before pdbr.

