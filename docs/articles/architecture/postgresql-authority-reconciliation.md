# PostgreSQL authority reconciliation

This change reconciles the reusable PostgreSQL authority foundations from
draft PR #386 onto the current architecture. It is deliberately a clean-slate
baseline, not a merge of that branch and not an implementation of the
ADR-0017 typed attribute registry.

## Reconciliation classification

| Classification | PR #386 material | Reconciled outcome |
| --- | --- | --- |
| Keep | PostgreSQL 18 pool/runtime policy | `internal/platform/postgres` owns bounded pool configuration, TLS and role checks, cancellation, timeouts, explicit read/write intent, and leased query rows. |
| Keep | Capability-owned migrations | The platform runner owns only advisory locking, checksum/revision evidence, and ordered execution. `platform.bootstrap`, `access`, `physical_pool`, and the minimal DuckLake catalog-bootstrap ledger each own their DDL. |
| Keep | pgx/sqlc repository pattern | Static PostgreSQL statements are generated from capability-local query/schema sources. Dynamic identifiers are limited to validated schema/role DDL and documented beside the call site. |
| Keep | Caller-owned transactions | Cross-capability initialization and physical-pool bootstrap receive one caller-owned `pgx.Tx`; repositories do not commit or roll it back. |
| Keep | Immutable transactional audit | Access mutations append their audit event in the same transaction. Database triggers reject update/delete of audit history and catalog/bootstrap identities. |
| Keep | Least-privilege roles | Non-login owner roles are distinct from login migrator, runtime, maintenance, readonly, and backup roles. The baseline fails if the required control roles were not provisioned first and reasserts ACLs on replay. |
| Keep | PostgreSQL 18 harness | The pinned PostgreSQL 18 container harness exercises pool behavior, migration replay, immutable evidence, and privilege boundaries. |
| Keep | Native initialization and pool bootstrap | Production admin initialization uses PostgreSQL platform/access authorities. Physical-pool bootstrap verifies exact compatibility evidence, creates the namespace ownership marker, initializes the separate PostgreSQL DuckLake catalog, and registers the exact catalog identity in the caller-owned control transaction. |
| Drop | Other capability migrations and repositories | Jobs, deployment, cache, lineage, refresh, source, project, serving-state, and retention ports from PR #386 are not part of this baseline. |
| Drop | Branch-wide generated/deployment state | Stale generated files, merge-resolution artifacts, historical deployment rewrites, and the draft branch's aggregate application graph are excluded. |
| Defer | ADR-0017 registry and integrations | Typed attribute definitions/assignments, semantic attribute storage, VAL-11 expansion, claim ingestion, validation, runtime evaluation, digest/cache, audit projection, and consumer-facing semantic access behavior remain outside this change. |

The existing generic `principal.attributes` and `access_group.attributes`
columns retain their pre-registry access metadata role. They are not the
typed ADR-0017 registry, do not use semantic-value canonical identities, and
provide no VAL-11 qualification evidence.

## Ownership boundaries

`internal/platform/postgres` owns connection mechanics only. It does not know
product schemas or open a second transaction for a repository operation.

Each capability exposes an embedded `SchemaSQL` and a narrow pgx repository.
The application composition in `internal/app/postgresbaseline` orders those
components and records one checksummed baseline revision. Adding another
capability requires adding its component explicitly; a platform migration
must not absorb capability tables.

The production admin adapter separates credentials by operation:

- the control migrator applies the baseline inside `SET LOCAL ROLE
  leapview_control_owner`;
- the control runtime performs ordinary access operations without DDL or
  physical-pool admission rights;
- the DuckLake catalog migrator connects only to `leapview_ducklake`, creates
  the deterministic per-pool metadata schema, and performs the initialize-only
  attach;
- DuckLake runtime and maintenance roles receive DML/sequence rights on that
  exact metadata schema but no schema creation or owner membership; and
- readonly and backup control roles receive only the explicit projections
  granted by the baseline.

Production provisioning must grant database `CONNECT` to
`leapview_control_migrator` and database `CREATE` to the non-login
`leapview_control_owner`. The application connects with the migrator
credential, then `Apply` assumes the owner role for all baseline DDL; the
migrator must not be granted database `CREATE` directly.

Production initialization and physical-pool bootstrap never open SQLite.
Development and the isolated evaluation target continue to use the explicit
offline adapter.

This is the focused extraction of the FAI-609 prerequisite: native production
administrator initialization, one-time credential handoff, and physical-pool
bootstrap run against the PostgreSQL authorities above. The broader
PostgreSQL-only serving application graph remains a separate architecture
milestone; this reconciliation does not claim that cutover.

## Lifecycle and compatibility

The baseline is a clean-slate revision. Its checksum frames the platform
foundation, ordered capability name and SQL bytes, and role policy. A matching
revision is replay-safe; a different migration ID or checksum fails closed.
Because the revision is immutable, changing any component after release
requires a new forward revision rather than editing revision 1.

SQLite development databases that already applied migration 073 receive
`encryption_domain` through forward migration 095. Existing rows are
backfilled from `isolation_boundary`; that equal-value spelling retains the
legacy content-addressed pool identity, while a distinct encryption domain is
identity-significant. New rows must always provide the domain explicitly.

Physical-pool identity, namespace ownership, conformance evidence, and
DuckLake catalog identity are immutable. Bootstrap replay succeeds only when
the pool ID, compatibility digest, evidence digest, catalog database,
deterministic catalog UUID/schema, and catalog schema version are identical.
The catalog initializer always sets `AUTOMATIC_MIGRATION=false`; catalog
upgrade authority is deferred to a separately reviewed milestone.

The control and DuckLake databases are separate transaction domains. The
control transaction can atomically register platform, access, physical-pool,
and catalog identity evidence, while external namespace-marker and DuckLake
catalog creation are replayable side effects. No code claims a distributed
transaction across those authorities.

## Validation contract

Run the following before review:

```sh
task db:generate
task generated:check
go test -tags=duckdb_arrow ./internal/analytics/ducklake/... ./internal/analytics/physicalpool/... ./internal/access/postgres ./internal/platform/bootstrap/postgres ./internal/app/adminpostgres ./internal/app/postgresbaseline
go test ./internal/platform/architecture
LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1 go test ./internal/platform/postgres/... ./internal/platform/bootstrap/postgres ./internal/access/postgres ./internal/analytics/physicalpool/postgres ./internal/access/http/mcpoauth -count=1 -v
task ci
```

The required PostgreSQL lane must report an unavailable Docker provider or
pinned image as a failure. Local optional runs may skip only when
`LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED` is unset.

PR #460 merged the reconciliation. FAI-636 builds on it with an access-owned
forward migration and the merged semantic-value canonicalizer; it does not
rewrite this baseline. Attribute assignments and the remaining consumer paths
stay outside the registry milestone, so the partial VAL-11 qualification
boundary is unchanged.
