# PostgreSQL authority reconciliation

This document records the reusable PostgreSQL authority foundations extracted
from draft PR #386 and reconciled by PR #460. FAI-636 and FAI-637 now extend
that baseline with access-owned, forward-only typed semantic-attribute state.
They do not claim a PostgreSQL-only serving cutover or completion of the
ADR-0017 semantic-consumer integration.

## Reconciliation classification

| Classification | PR #386 material or later extension | Reconciled outcome |
| --- | --- | --- |
| Keep | PostgreSQL 18 pool/runtime policy | `internal/platform/postgres` owns bounded pool configuration, TLS and role checks, cancellation, timeouts, explicit read/write intent, and leased query rows. |
| Keep | Capability-owned migrations | The platform runner owns only advisory locking, checksum/revision evidence, and ordered execution. `platform.bootstrap`, `access`, `physical_pool`, and the minimal DuckLake catalog-bootstrap ledger each own their DDL. |
| Keep | pgx/sqlc repository pattern | Static PostgreSQL statements are generated from capability-local query/schema sources. Dynamic identifiers are limited to validated schema/role DDL and documented beside the call site. |
| Keep | Caller-owned transactions | Cross-capability initialization and physical-pool bootstrap receive one caller-owned `pgx.Tx`; repositories do not commit or roll it back. |
| Keep | Immutable transactional audit | Access mutations append their audit event in the same transaction. Database triggers reject update/delete of audit history and catalog/bootstrap identities. |
| Keep | Least-privilege roles | Non-login owner roles are distinct from login migrator, runtime, maintenance, readonly, and backup roles. The baseline fails if the required control roles were not provisioned first and reasserts ACLs on replay. |
| Keep | PostgreSQL 18 harness | The pinned PostgreSQL 18 container harness exercises pool behavior, migration replay, immutable evidence, and privilege boundaries. |
| Keep | Native initialization and pool bootstrap | Production admin initialization uses PostgreSQL platform/access authorities. Physical-pool bootstrap verifies exact compatibility evidence, creates the namespace ownership marker, initializes the separate PostgreSQL DuckLake catalog, and registers the exact catalog identity in the caller-owned control transaction. |
| Keep | FAI-636 typed registry, revision 2 | `internal/access/postgres` owns `semantic_attribute_registry` and `semantic_attribute_definition`: profile-qualified identity, type/shape, stewardship metadata, lifecycle, versioning, deterministic registry digest, and audited mutations. |
| Keep | FAI-637 durable control, revision 3 | `internal/access/postgres` owns `semantic_attribute_control_state`, direct principal/group assignments, trusted claim mappings, control digest/revision, tombstone lifecycle, expected-version concurrency, canonical-value validation, and audited mutations. |
| Drop | Other capability migrations and repositories | Jobs, deployment, cache, lineage, refresh, source, project, serving-state, and retention ports from PR #386 are not part of this baseline. |
| Drop | Branch-wide generated/deployment state | Stale generated files, merge-resolution artifacts, historical deployment rewrites, and the draft branch's aggregate application graph are excluded. |
| Defer | ADR-0017 semantic-consumer integration | SemanticModel policy compilation, candidate/generation references, planner evaluation, catalog filtering, dashboards, Explore, agents, exports, APIs/MCP, embedding, cache/event invalidation, and source-provider adapters remain outside FAI-637. |

The existing generic `principal.attributes` and
`access_group.attributes` JSON columns retain their pre-registry access and
SCIM metadata role. They are not the typed ADR-0017 registry, do not use
semantic-value canonical identities, and provide no semantic authorization
evidence. No project YAML or legacy `DataPolicy` value can populate the typed
registry/control tables; a future typed semantic policy must enter through the
SemanticModel contract and the access control plane.

## Ownership boundaries

`internal/platform/postgres` owns connection mechanics only. It does not know
product schemas or open a second transaction for a repository operation.

Each capability exposes an embedded `SchemaSQL` and a narrow pgx repository.
The application composition in `internal/app/postgresbaseline` orders those
components and records one checksummed baseline revision. Adding another
capability requires adding its component explicitly; a platform migration
must not absorb capability tables. FAI-636 and FAI-637 remain access-owned
forward migrations, and revision 1 is never edited.

The semantic-attribute registry and control tables are product authority, not
platform connection metadata. Access repositories perform domain validation,
lock the appropriate singleton, canonicalize values, advance the corresponding
identity, and append audit evidence. Top-level methods create one transaction;
the explicit `...Tx` methods accept a caller-owned transaction. Audit is written
before commit, so a failed audit append rolls back the state change.

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
  granted by the baseline, including non-secret semantic-attribute metadata
  and control projections.

Production provisioning must grant database `CONNECT` to
`leapview_control_migrator` and database `CREATE` to the non-login
`leapview_control_owner`. The application connects with the migrator
credential, then `Apply` assumes the owner role for all baseline DDL; the
migrator must not be granted database `CREATE` directly.

Database roles are separate from the application platform-admin role. A
semantic-attribute HTTP request first authenticates a principal, then checks
the durable instance-wide platform role. A request API token may attenuate that
role (nil capabilities inherit, explicit empty denies, explicit non-empty must
include `PROJECT_ADMIN`); authoring credentials and principal-mismatched
credentials are denied. Possessing a database runtime credential does not
make a principal a platform administrator.

Production initialization and physical-pool bootstrap never open SQLite.
Development and the isolated evaluation target continue to use the explicit
offline adapter. The broader PostgreSQL-only serving application graph remains
a separate architecture milestone.

## Lifecycle, identity, and compatibility

The baseline is a clean-slate revision. Its checksum frames the platform
foundation, ordered capability name and SQL bytes, and role policy. A matching
revision is replay-safe; a different migration ID or checksum fails closed.
Because the revision is immutable, changing any component after release
requires a new forward revision rather than editing revision 1.

FAI-636 revision 2 and FAI-637 revision 3 follow the same rule. Their migration
IDs and SQL bytes are access-owned and composed in order after revision 1.
Revision 2's registry singleton has `(profile, registry_revision,
registry_digest)` identity. Revision 3's control singleton independently has
`(profile, control_revision, control_digest)` identity over ordered active and
tombstoned assignments and mappings. Replay leaves each identity unchanged;
an effective mutation advances its revision exactly once with a new digest.

Definition identity and logical type are immutable. Metadata and lifecycle
changes increment definition version; deletion is rejected. An assignment or
claim mapping is an immutable incarnation with versioned updates/tombstoning;
expected-version mismatches fail closed, tombstones remain, and restoration
creates a new identity. Database triggers enforce identity/type stability,
database-owned timestamps, monotonic versions, and control-state advancement.

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
go test -tags=duckdb_arrow ./internal/analytics/ducklake/... ./internal/analytics/physicalpool/... ./internal/access/postgres ./internal/access/trustedclaims ./internal/platform/bootstrap/postgres ./internal/app/adminpostgres ./internal/app/postgresbaseline
go test ./internal/platform/architecture
LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1 go test ./internal/platform/postgres/... ./internal/platform/bootstrap/postgres ./internal/access/postgres ./internal/analytics/physicalpool/postgres ./internal/access/http/mcpoauth -count=1 -v
task ci
```

The required PostgreSQL lane must report an unavailable Docker provider or
pinned image as a failure. Local optional runs may skip only when
`LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED` is unset.

PR #460 merged the reconciliation. FAI-636 and FAI-637 build on it with
access-owned forward migrations and the merged semantic-value canonicalizer;
they do not rewrite this baseline. The typed registry/control identities and
audit boundaries are in place, while semantic compiler/consumer integration,
real SAML/OIDC/embed/service-token adapters, and cache/event invalidation
remain deferred. VAL-11 consequently remains explicitly **Partial**.
