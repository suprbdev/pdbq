# Security policy

## Reporting a vulnerability

Report vulnerabilities privately through GitHub security advisories:
[Report a vulnerability](https://github.com/suprbdev/pdbq/security/advisories/new)
(the repository's Security tab, "Report a vulnerability"). Do not open a
public issue or pull request for a suspected vulnerability.

Include the pdbq version (`pdbq --version`), the PostgreSQL version, the
relevant configuration (redact secrets) and a reproduction: the GraphQL
document, the database schema and the roles involved. A proof of concept
against the fixture schema in `db/init/01-schema.sql` is ideal.

pdbq is maintained by a small team. Response targets, measured from the
report:

| Step | Target |
|---|---|
| Acknowledge the report | 3 working days |
| Triage and confirm or decline, with severity | 7 days |
| Fix or mitigation for a confirmed high or critical issue | 30 days |
| Coordinated disclosure | with the fixed release, credited if you wish |

These are targets, not guarantees. The reporter is kept informed if one
slips.

## Supported versions

| Version | Supported |
|---|---|
| Latest minor release of 1.x (once 1.0 is released) | Security and critical bug fixes |
| Older 1.x minors | Upgrade to the latest minor |
| 0.x | Latest release only: fixes ship in the next 0.x release |

Fixes are released as a patch of the latest minor. pdbq depends on
[pdbcore](https://github.com/suprbdev/pdbcore), the shared engine; an issue
in pdbcore is fixed there and released with a pdbq update that pins the fixed
version. Supported PostgreSQL versions are listed in
[docs/compatibility.md](docs/compatibility.md#supported-versions).

## Scope

In scope:

- Authentication bypass: accepting a forged, expired or wrongly signed JWT,
  trusting claim headers from an untrusted peer, running a request under a
  role it is not allowed to assume.
- Row-level security bypass: one request's identity (role or claims) leaking
  into another request or connection, or a policy not applying to a query pdbq
  compiles.
- Exposure of data or schema hidden by `@omit` and the other smart comments,
  by the privilege-driven generation rules, or by `server.introspection:
  false`.
- SQL injection: user-controlled input reaching a statement as anything but a
  bind parameter or a validated identifier.
- Denial of service that gets around the documented limits (`limits.*`,
  `server.request_timeout`, `database.statement_timeout`, subscription and
  live-query caps) or exhausts memory with a bounded request.
- Cross-site request forgery and CORS handling on `/graphql`.

Out of scope:

- Findings that need superuser, `BYPASSRLS` or table-owner access to the
  database, or write access to the configuration file.
- Running with `auth.mode: none` or `roles.switch: false` and finding that
  requests run as the connection role: that is what those settings do.
- Load that stays inside the configured limits, and limits you have raised.
- Watch mode (`watch.enabled`), which is a development convenience.
- Vulnerabilities in PostgreSQL, PostGIS, extensions or Go itself. Report
  them upstream.

## Verifying releases

Every release publishes `checksums.txt` next to the archives, a CycloneDX
SBOM (`pdbq_<version>_sbom.cdx.json`) and a keyless
[cosign](https://github.com/sigstore/cosign) signature bundle
(`checksums.txt.sigstore.json`). The archives and the container image also
carry GitHub build provenance attestations.

```console
$ sha256sum --check --ignore-missing checksums.txt
$ cosign verify-blob checksums.txt \
    --bundle checksums.txt.sigstore.json \
    --certificate-identity-regexp '^https://github.com/suprbdev/pdbq/\.github/workflows/release\.yaml@refs/tags/v' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com
$ gh attestation verify pdbq_<version>_linux_amd64.tar.gz --repo suprbdev/pdbq
$ cosign verify ghcr.io/suprbdev/pdbq:<version> \
    --certificate-identity-regexp '^https://github.com/suprbdev/pdbq/\.github/workflows/release\.yaml@refs/tags/v' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Hardening a deployment

[docs/deployment.md](docs/deployment.md) has the production checklist and
[docs/rls.md](docs/rls.md) explains roles, claims and policies. The short
version: run with `auth.mode: jwt` or a trusted gateway, never as a
superuser or a `BYPASSRLS` role, switch roles per request, keep
`server.introspection: false` where the schema shape is sensitive, and
enable `FORCE ROW LEVEL SECURITY` on tables the connection role owns.
