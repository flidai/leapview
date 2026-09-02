# Upgrades and migrations

Treat an upgrade as a coordinated change to application code, browser assets,
persistent schemas, runtime configuration, and supported project contracts. A
provider-managed image rollback is useful but does not automatically reverse a
persistent-state migration.

## Move from v0.1.0

LeapView v0.2.0-rc.1 is **fresh-install-only** from v0.1.0. Do not start the
candidate on a v0.1.0 volume or treat the released v0.1.0 image as an in-place
upgrade target:

```text
ghcr.io/yacobolo/libredash@sha256:677caaf256cb3a0d61efd47b289debbd91984976a5a5c4b372196a5d79ce7153
```

That release uses `LIBREDASH_*`, `/var/lib/libredash`, `libredash.db`,
`libredash-backup.json`, and the earlier publish/deployment model. The current
image detects `libredash.db` before creating or migrating `leapview.db`; the
Compose and host packages provide no in-place migration or historical-state
import path.

Preserve and export the old instance with the released binary, not a rebuilt
approximation. The package currently requires registry authentication and
contains only a `linux/amd64` runtime. With the v0.1.0 container stopped and
its state volume still attached, follow that release's documented export
procedure and retain its checksum. LeapView does not provide a compatible
import or local-file recovery path for this historical state.

Do not restore that export into LeapView; provision a fresh LeapView instance
and volume, redeploy the authored project from version control, reload each
source or managed dataset from its authority, and reprovision users, groups,
service principals, and grants. Validate dashboards, governed queries,
refreshes, denials, and the provider-native recovery evidence before cutover.
Retain the stopped v0.1.0 container, its volume, configuration, immutable image,
export, and checksum as the rollback boundary until the migration is accepted.

## Inspect the automated v0.1 preservation gate

Every release runs the v0.1 preservation qualification before publication.
The gate consumes the real assembled-image admission record, the
candidate-bound transition policy, its exact SHA-256, and the reviewed
historical v0.1 identity. Because the historical image is available only for
`linux/amd64`, its execution journey runs in the release workflow's `amd64`
pre-publication job. A failure in that job prevents the dependent publication
job from releasing the image or archives.

The release runner must have Docker Engine with Buildx, permission to create
and remove isolated containers, networks, volumes, and run directories, and an
owner-readable Docker credential configuration with pull access to
`ghcr.io/yacobolo/libredash`. The credential requires package read access only.
Qualification fails closed when credentials are missing, the exact artifact is
unavailable, or the registry returns different immutable OCI bytes. There is no
tag, alternate namespace, local image, or source-build fallback.

Inspect the GitHub Actions **Pre-publication qualification (amd64)** job and its
`prepublication-<release-tag>-amd64` artifact. It contains two related JSON
documents:

- `v0.1-reviewed-identity.json` proves that the exact authenticated historical
  artifact, platform manifest, config, and source provenance matched the
  reviewed policy identity. It is bound to the candidate policy SHA-256 but is
  not execution evidence.
- `v0.1-preservation-qualification.json` extends the same evidence contract
  with the authentic v0.1 application journey, stopped-state inventory and
  before/after checksums, clean restart proof, isolated candidate identity,
  fresh-install and legacy-state denial decisions, mutation-free checksums, and
  cleanup result.

The final document is published atomically only after owner validation. Its
historical identity, artifact graph, provenance, policy version, and policy
digest must match the reviewed document, while its candidate identity must
match the admission record. If the final document is absent, qualification did
not succeed even when the reviewed identity file was uploaded.

A successful gate means deterministic state created through supported v0.1
interfaces survived clean shutdown and restart, while the admitted candidate
started in separate clean state and rejected unsupported legacy-state reuse
without mutation. It does not make v0.1 state compatible with LeapView and does
not replace the export-and-fresh-install procedure above.

For a failure, preserve the bounded workflow diagnostic and both evidence files
that exist. Reauthenticate for a credential error; escalate an unavailable or
digest-mismatched historical artifact without substituting it; re-extract the
candidate archive for a policy checksum error; regenerate predecessor evidence
with the same shipped controller and policy if it is rejected; and rebuild and
readmit the candidate when its admission identity or qualification journey
fails. Never edit evidence, policy, or admission JSON to make a release pass.

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
artifacts; the complete procedure belongs in the separate native runbook and
ADR.

The rehearsal should cover startup migration, authentication, active
deployments, semantic queries, dashboard interactions, refresh execution, and
the provider's image rollout or rollback procedure. Measure migration and
restart duration to set the maintenance window.

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
a bounded health wait, and an explicit decision point before old artifacts are
removed. LeapView's Compose and host controllers do not perform image upgrades
or paired state rollback.

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

### Plan-delivery pool admission migration

Migrations 073–087 add the target-owned physical-pool contract, serving-state
identity guards, rollback evidence, append-only delivery events, and durable GC
actor attribution.
They do not infer admission from configuration. After migration, a production
target with no admitted pool remains administrable but reports a stable
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
