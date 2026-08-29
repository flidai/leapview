# Local PostgreSQL provisioning

This directory provisions the development/test PostgreSQL baseline described
by ADR-0016. It is intentionally separate from the production Compose bundle:
production supplies externally managed control runtime and migrator URLs (and
optionally a readonly URL) through its secret manager and does not run this
service.

The loopback-only service uses the pinned PostgreSQL 18 image and initializes
two databases in one local server:

| Database | Owner | Runtime role | Migration role |
| --- | --- | --- | --- |
| `leapview_control` | `leapview_control_owner` | `leapview_control_runtime` (plus `leapview_control_readonly`) | `leapview_control_migrator` |
| `leapview_ducklake` | `leapview_ducklake_owner` | `leapview_ducklake_runtime` | `leapview_ducklake_migrator` |

Owner roles cannot log in. Runtime roles can connect only to their own
database, and migration roles receive owner membership for schema changes.
The control readonly role has an independent login credential for its bounded
pool. The backup role is a NOLOGIN group role with control database access;
deployments attach a separately authenticated operator role to it rather than
sharing runtime credentials.
Migration processes must explicitly `SET ROLE` to their capability owner when
performing owner-level DDL.
The initialization script revokes default `PUBLIC` database/schema access.
Capability baseline migrations own control-plane schemas and their table
grants; provisioning creates only the DuckLake catalog schema contract and
leaves its internal tables to the DuckLake migration authority.

Start the service with:

```sh
task postgres:dev:up
```

The target writes a mode-0600 environment file at `.tmp/postgres-dev.env`.
It contains local loopback URLs for the two runtime roles and may be sourced by
focused tests or development tooling. `task postgres:dev:down` stops the
service while retaining its named volume. `task postgres:dev:check` exercises
both runtime roles against their own and the other database, and verifies that
runtime roles cannot create schemas or assume owner roles. Test targets use a
separate worktree-derived port range to avoid collisions between worktrees.

The local defaults are disposable credentials, suitable only for an isolated
developer machine. Override the `LEAPVIEW_POSTGRES_*_PASSWORD` variables for
tests that need different credentials. The helper never prints passwords or
connection URLs.
