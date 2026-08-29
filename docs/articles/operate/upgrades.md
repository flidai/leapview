# Upgrades and migrations

Treat an upgrade as a coordinated change to application code, browser assets, persistent schemas, runtime configuration, and supported project contracts. An image rollback is useful but does not automatically reverse a persistent-state migration.

## Move from v0.1.0

LeapView v0.2.0-rc.1 is **fresh-install-only** from v0.1.0. Do not start the
candidate on a v0.1.0 volume and do not use `leapviewctl upgrade` with the
released v0.1.0 image:

```text
ghcr.io/yacobolo/libredash@sha256:677caaf256cb3a0d61efd47b289debbd91984976a5a5c4b372196a5d79ce7153
```

That release uses `LIBREDASH_*`, `/var/lib/libredash`, `libredash.db`,
`libredash-backup.json`, and the earlier publish/deployment model. The
candidate detects `libredash.db` before creating or migrating `leapview.db`;
the Compose controller rejects the released digest before it stops the old
service or writes a checkpoint.

Preserve and export the old instance with the released binary, not a rebuilt
approximation. The package currently requires registry authentication and
contains only a `linux/amd64` runtime. With the v0.1.0 container stopped and
its state volume still attached to `libredash-v010`, create an isolated
exporter:

```sh
V010_IMAGE=ghcr.io/yacobolo/libredash@sha256:677caaf256cb3a0d61efd47b289debbd91984976a5a5c4b372196a5d79ce7153
docker pull "$V010_IMAGE"
docker stop libredash-v010
docker create \
  --name libredash-v010-export \
  --volumes-from libredash-v010 \
  --tmpfs /tmp:rw,noexec,nosuid,size=2g \
  --env LIBREDASH_HOME=/var/lib/libredash \
  "$V010_IMAGE" \
  admin backup --out /tmp/libredash-v0.1.0.tar.gz
docker start --attach libredash-v010-export
docker cp \
  libredash-v010-export:/tmp/libredash-v0.1.0.tar.gz \
  ./libredash-v0.1.0.tar.gz
sha256sum ./libredash-v0.1.0.tar.gz > ./libredash-v0.1.0.tar.gz.sha256
docker rm libredash-v010-export
```

Do not restore that archive into LeapView; provision a fresh LeapView instance
and volume, redeploy the authored project from version control, reload each
source or managed dataset from its authority, and reprovision users, groups,
service principals, and grants. Validate dashboards, governed queries,
refreshes, denials, backup, and restore before cutover. Retain the stopped
v0.1.0 container, its volume, configuration, immutable image, archive, and
checksum as the rollback boundary until the migration is accepted.

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
- backup format changes;
- known rollback limitations.

Build or pull an immutable artifact and verify its provenance. Do not upgrade production from a mutable tag.

## Rehearse against restored state

Create and validate a production backup, then restore it into an isolated environment. Run the target version with production-like configuration and apply any documented project migration.

The rehearsal should cover startup migration, authentication, active deployments, semantic queries, dashboard interactions, refresh execution, backup creation, and the intended rollback procedure. Measure migration and restart duration to set the maintenance window.

## Prepare production

1. Confirm recent backups of every authoritative storage boundary.
2. Record current image digest, configuration version, active projects, and revisions.
3. Validate the target configuration with `leapview config validate --production`.
4. Pause or drain conflicting deployments, refreshes, and maintenance jobs.
5. Confirm disk headroom for migrations, new images, and rollback artifacts.
6. Notify users of the expected availability impact.

Use `leapview admin maintenance` or the deployment's maintenance mechanism only as documented by the release. Dry-run retention maintenance is not itself a general traffic-draining switch.

## Apply the upgrade

For the supported Hetzner topology, use the generated operations command with the target image digest. It pulls and validates the image, starts it, waits for health, and restores the prior image if health fails.

For another topology, preserve the same invariants: one controlled writer for persistent migrations, immutable artifact selection, bounded health wait, and an explicit decision point before old artifacts are removed.

Do not run two application versions against shared writable state unless the release explicitly declares mixed-version compatibility.

### SQLite-backed DuckLake catalogs

The first release using the process-owned DuckDB runtime automatically migrates the former SQLite-backed DuckLake metadata catalog before opening analytical storage. With the default layout, `ducklake/catalog.sqlite` is copied atomically to `ducklake/catalog.duckdb` and the SQLite source remains in place as a rollback backup. If `LEAPVIEW_DUCKLAKE_CATALOG_PATH` still names a SQLite file, LeapView converts that path in place and preserves the source beside it with the suffix `.legacy.sqlite`.

Budget disk space for both catalog files during the upgrade. Keep the legacy file until snapshot validation, representative queries, refresh, backup, and restore have all passed. The migration never replaces an existing DuckDB target; if an earlier failed start already created `catalog.duckdb`, preserve both files and resolve that partial upgrade before retrying.

## Verify after startup

Check more than readiness:

- browser assets and route shell load without cache/version mismatch;
- local or external authentication completes and sessions persist correctly;
- expected project resources, access declarations, and active deployments are present;
- one semantic model can be described and queried;
- one representative dashboard and interaction works;
- a refresh can complete and activate;
- metrics, logs, audit events, and backups still function;
- configuration validation reports no deprecated or missing settings.

Keep the maintenance window open until these checks pass.

### Plan-delivery pool admission migration

Migrations 073–087 add the target-owned physical-pool contract, serving-state
identity guards, rollback evidence, append-only delivery events, and durable GC
actor attribution.
They do not infer admission from configuration. After migration, a production
target with no admitted pool remains administrable but reports a stable
`missing_physical_pool_admission` readiness diagnostic. Run the controlled
offline bootstrap in [Plan, build, and publish](plan-build-publish) with a
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
target CAS, or restore the last validated control-plane backup and repeat the
offline repair.

One storage namespace has one deletion authority. Separate instance databases
must not independently admit the same namespace. Use a shared control database
or an external ownership/fencing service, or provision a distinct namespace
and isolation boundary before migration.

## Roll back carefully

If the failure is limited to application behavior and persistent state remains backward compatible, return to the previous immutable artifact. If a migration changed persistent state incompatibly, follow the release's restore procedure instead of starting old code against new state.

Preserve failure logs, migration output, the target artifact, and post-failure state for diagnosis. A rollback restores service; it does not remove the need to understand the failed upgrade.

Project YAML remains on its own delivery cadence unless the new application version requires a resource migration. In that case, version application and project changes together in the promotion record.
