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
| `leapview_control` | `leapview_control_owner` | `leapview_control_runtime` (plus `leapview_control_readonly`) | `leapview_control_migrator` (control migrations) and `leapview_control_upgrade_coordinator` (guarded DuckLake authority) |
| `leapview_ducklake` | `leapview_ducklake_owner` | `leapview_ducklake_runtime` | `leapview_ducklake_migrator` |

Owner roles cannot log in. Runtime roles can connect only to their own
database, and the catalog migration role receives owner membership only in
the separate DuckLake database. The control upgrade coordinator has no owner
membership and can invoke only the guarded DuckLake-control authority
functions; it cannot connect to the DuckLake database. The control migrator
retains owner membership for ordinary control-plane schema migrations.
The separately authenticated `leapview_control_maintenance` login is bounded
to maintenance grants and is opened only for explicit maintenance operations;
it is never reused as a runtime or migration credential.
The control readonly role has an independent login credential for its bounded
pool. The backup role is a NOLOGIN group role with control database access;
deployments attach a separately authenticated operator role to it rather than
sharing runtime credentials.
Migration processes must explicitly `SET ROLE` to their capability owner when
performing owner-level DDL. Ordinary DuckLake runtime attachments always use
`AUTOMATIC_MIGRATION=false`; catalog bootstrap and upgrades require the
separate `leapview_ducklake_migrator` credential.
That migrator alone has narrowly scoped `CREATE` on the DuckLake database so
an explicit bootstrap can precreate the hash-qualified per-pool metadata
schema; the runtime role has no database or schema `CREATE` capability.
The initialization script revokes default `PUBLIC` database/schema access.
Capability baseline migrations own control-plane schemas and their table
grants; provisioning creates only the exact DuckLake catalog schema contract
and leaves its internal tables to the DuckLake migration authority.

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
developer machine. Override the specific password settings documented in the
Compose file for tests that need different credentials. The helper never
prints passwords or connection URLs.
