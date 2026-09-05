# Upgrades and migrations

Treat an upgrade as a coordinated change to application code, browser assets,
persistent schemas, runtime configuration, and supported project contracts. A
provider-managed image rollback is useful but does not automatically reverse a
persistent-state migration.

## Assess the release

Before scheduling an upgrade, review release notes for:

- minimum Go, browser, database, or infrastructure requirements;
- control-plane or DuckLake migrations;
- environment variables added, removed, or made mandatory;
- resource schema changes and project migration steps;
- API or CLI compatibility changes;
- known rollback limitations.

Build or pull an immutable artifact and verify its provenance. Do not upgrade production from a mutable tag.

## Rehearse against restored state

Use the provider's native PostgreSQL/PITR and DuckLake/object-store recovery
procedures to restore a mutually consistent point into an isolated environment.
Run the target version with production-like configuration and apply any
documented project migration. LeapView does not create or restore the recovery
artifacts; follow the [PostgreSQL operations
guide](/docs/guides/operate/postgresql-operations) and [Backup and restore
guide](/docs/guides/operate/backup-restore) for the complete procedure.

The rehearsal should cover the explicit Goose migration, read-only startup
verification, authentication, active deployments, semantic queries, dashboard
interactions, refresh execution, and the provider's image rollout or rollback
procedure. Measure migration and restart duration to set the maintenance
window; serving startup must never apply pending migrations implicitly.

## Prepare production

1. Confirm recent provider-native recovery points for every authoritative storage boundary.
2. Record current image digest, configuration version, active projects, and revisions.
3. Validate the target configuration with `leapview config validate --production`.
4. Pause or drain conflicting deployments, refreshes, and maintenance jobs.
5. Confirm disk headroom for migrations and the deployment platform's image artifacts.
6. Notify users of the expected availability impact.

Use `leapview admin maintenance` or the deployment's maintenance mechanism only as documented by the release. Dry-run retention maintenance is not itself a general traffic-draining switch.

## Apply the upgrade

For any supported topology, use the provider or container platform's
immutable-image rollout with one controlled writer for persistent migrations,
using the explicit River upstream schema migration and Goose v3.27.1 for the
product baseline and forward migrations. Follow it with read-only Goose/River
schema verification, a bounded health wait, and an explicit decision point
before old artifacts are removed. LeapView's Compose and host controllers do
not perform image upgrades or paired state rollback.

Do not run two application versions against shared writable state unless the release explicitly declares mixed-version compatibility.

### Catalog compatibility boundary

Production upgrades operate only on an admitted PostgreSQL-backed DuckLake
catalog. LeapView does not import or convert SQLite-backed DuckLake catalogs;
this clean-install architecture has no legacy catalog migration path. A
configured SQLite catalog is rejected, and an adjacent `catalog.sqlite` file is
ignored rather than treated as authority. Restore or upgrade only from a
qualified PostgreSQL/DuckLake recovery set.

When the target release changes the admitted DuckDB, DuckLake, catalog-format,
or catalog-schema tuple, keep serving stopped and inject the operation-only
control upgrade coordinator and DuckLake catalog migrator credentials. First
preview the exact target contract; add `--apply` only after the preview matches
the reviewed artifacts and the drain and backup assertions are true:

```sh
leapview admin delivery pool upgrade \
  --pool /run/leapview/target-pool.json \
  --evidence /run/leapview/target-conformance.json \
  --migration-id 0198f2c0-7c7a-7f00-8a11-000000000001 \
  --catalog-schema-version 1 \
  --recovery-decision rollback \
  --drain-verified \
  --backup-verified

# Repeat the identical command with --apply to execute it.
```

The preview validates the supplied identities and prints redacted expected
evidence; it does not connect to or inspect PostgreSQL. Apply appends the target
pool admission, acquires the global and pool catalog-migration fences, performs
the explicit DuckLake automatic migration through the owner-only session,
checks the catalog's resulting schema version, requalifies every retained
snapshot, and only then advances the mutable catalog-runtime compatibility row.
Ordinary startup and serving connections cannot invoke this path. Preserve the
migration ID and output with the release evidence, then remove both operation-
only credentials before starting the service.

## Verify after startup

Check more than readiness:

- browser assets and route shell load without cache/version mismatch;
- local or external authentication completes and sessions persist correctly;
- expected project resources, access declarations, and active deployments are present;
- one semantic model can be described and queried;
- one representative dashboard and interaction works;
- a refresh can complete and activate;
- metrics, logs, and audit events still function;
- configuration validation reports no deprecated or missing settings.

Keep the maintenance window open until these checks pass.

### Plan-delivery pool admission

The clean-slate PostgreSQL target has one Goose baseline; legacy numbered
SQLite migration references are not a production upgrade path. Physical-pool
admission remains a separate, target-owned bootstrap and readiness contract.
It does not infer admission from configuration. A production target with no
admitted pool remains administrable but reports a stable
`missing_physical_pool_admission` readiness diagnostic. Run the controlled
native bootstrap in [Plan, build, and publish](plan-build-publish) with a
fresh local or MinIO conformance artifact.

Rows retained from an older schema with an empty serving-state identity are
quarantined for inspection. They cannot be selected as a verified seal,
ready candidate, prepared/active generation, or serving root; repair them by
rebuilding and sealing a candidate with the current target revision. Do not
update the identity columns manually.

Restart the process once after the migration and before reopening traffic. The
startup check must report the same target-owned pool admission and serving
pointer on both starts. A missing target revision, missing serving identity,
mixed legacy path, or indeterminate publication is a fail-closed diagnostic;
do not infer activation from an object-store acknowledgement or retry publish
with a new request. Reconcile the original publication against the durable
target CAS. If recovery is required, use the PostgreSQL and storage-provider
recovery procedure approved for the deployment; LeapView has no offline
catalog-repair or local-archive fallback.

One storage namespace has one deletion authority. Separate instance databases
must not independently admit the same namespace. Use a shared control database
or an external ownership/fencing service, or provision a distinct namespace
and isolation boundary before migration.

## Roll back carefully

If the failure is limited to application behavior and persistent state remains
backward compatible, return to the previous immutable artifact through the
deployment platform. If a migration changed persistent state incompatibly,
follow the provider-native recovery procedure instead of starting old code
against new state.

Preserve failure logs, migration output, the target artifact, and post-failure
state for diagnosis. A platform rollback restores service; it does not remove
the need to understand the failed upgrade. A target-level `leapview rollback`
only selects a retained serving generation and cannot roll back an application
image or persistent schema.

Project YAML remains on its own delivery cadence unless the new application version requires a resource migration. In that case, version application and project changes together in the promotion record.
