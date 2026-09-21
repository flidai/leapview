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
2. Record current image digest, configuration version, the active server-bound Project, and revisions.
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

### PostgreSQL-era transition preflight

FAI-518 defines the transition boundary for a PostgreSQL-era predecessor and
candidate. The pure preflight consumes exact immutable OCI artifact identities,
the PostgreSQL control-schema projection, Goose ownership, River's upstream
operational-schema and LeapView-owned job-history projections, the exact
five-field DuckLake compatibility tuple, the release policy, and a mandatory
recovery frontier reference. The frontier anchors the transition phases even
when the decision permits binary rollback. A predecessor from the older
SQLite-era architecture is outside this boundary and is unsupported. Admission
and recovery references are owner-produced projections; this library does not
query them live.

The evaluator emits exactly one decision:

- `binary-rollback-compatible` means every persistent domain is explicitly
  backward compatible and the matching release policy permits returning to the
  predecessor artifact through the deployment platform.
- `provider-recovery-required` means at least one persistent domain is
  explicitly incompatible, and a matching provider-recovery policy and valid
  recovery frontier reference are present. Restore mutually consistent
  PostgreSQL and DuckLake/object-provider state before starting the predecessor.
- `unsupported` means the evidence is incomplete, unknown, ambiguous, has
  mismatched ownership or policy metadata, or otherwise cannot prove either
  path safely.

This FAI-518 preflight is the eligibility boundary for the later FAI-519
transition execution work. The read-only Go entrypoint accepts exact owner
references rather than a caller-assembled evaluator input. It resolves the
predecessor and candidate from the immutable OCI-admission authority, the
target identity and compatibility projections from their migration owners,
the matching release policy from its policy owner, and an exact recovery-set
identity from PostgreSQL. The PostgreSQL recovery-frontier adapter accepts only
a published set whose exact passed validation attempt, result digest, evidence
envelope, frontier digest, and target binding all agree. Missing, ambiguous,
mutable, stale, or mismatched owner results fail before evaluation. The
application-level production composition supplies the concrete OCI-admission,
release-policy, target, migration-capability, and RecoverySet authorities; it
does not accept caller-provided projections.

The resolver then calls the existing pure evaluator and returns its canonical
evidence bytes and domain-separated digest; it does not define another evidence
format. Owner adapters must not substitute project release rows, serving
artifact digests, mutable image tags, or the latest recovery set for the
required OCI admission, release policy, and exact frontier authorities. Run the
bounded PostgreSQL-backed resolver qualification with:

```sh
task qualify:ubdr:release-transition-preflight
```

The entrypoint remains read-only: it does not run Goose or River migrations,
contact a recovery provider, acquire an execution fence, switch an image, or
mutate release or recovery state. FAI-519 must revalidate the same immutable
evidence at its execution boundary. Preflight evidence does not execute a
release transition.

### Forward-only transition qualification

The build-tagged qualification lane is the bounded end-to-end check for the
forward-only execution boundary that follows preflight. Run it from a checkout
with the qualification PostgreSQL service available:

```sh
task qualify:ubdr:release-transition
```

The lane resolves the exact predecessor, candidate, target, policy, migration
capabilities, and published recovery frontier through the authoritative
production preflight composition. It then applies the real PostgreSQL Goose
and River migration runners against an already-current disposable schema,
stages and activates the immutable host-install generation through the
production primitives, launches a fresh qualification process, and checks its
candidate-image/target-identity handshake. The operation is also exercised
through its concurrency and injected-failure matrix. A separate PostgreSQL
connection and release repository read the durable state back, proving that
the operation identity and phase evidence are not held only in process memory.

The report directory is supplied by the task and is bounded to the run's
evidence. A success report is published with an atomic rename only after every
phase and readback check passes; failed or interrupted runs must not publish a
success report. Preserve the resulting report with the release evidence and
the exact immutable image references.

For the real predecessor/candidate artifact lane, run
`task qualify:ubdr:release-transition-artifact` with the exact image and source
revision inputs required by `scripts/qualify_fai518_real_artifact.sh`. Both
images must pass live artifact admission. The lane loads the complete Goose
migration set from each admitted artifact's attested source revision. The
selected predecessor must contain exactly revisions 001–019; the candidate
must contain exactly 001–020. Checkout migrations are never supplied to this
candidate migration provider. An extra candidate migration, including 021,
fails before migration execution rather than being filtered away.

The existing signed Goose capability binds the candidate admission and target
to the migration-set digest. Migration execution runs under the existing
migration fence. Post-validation requires observed database revision 020;
it does not use the checkout's current revision. The durable migration-phase
result and `transition-report.json` retain both admission digests, target
identity, candidate image/source, capability and migration-set digests, and
the observed revision. Regressions cover a checkout containing 021, an extra
candidate migration, inconsistent bindings, and rejection of a database or
success report at revision 021. This evidence does not qualify host upgrades
or rollback.

This qualification is a local disposable-PostgreSQL and host-install
simulation: it does not prove provider-specific image rollout, cloud recovery,
or production systemd/container orchestration. The disposable database is
already at the current schema, so the lane does not qualify a real
predecessor-to-candidate schema delta. It is forward-only and does not qualify
binary rollback or restore a recovery provider; those remain separate
operational procedures.
The restart gate launches the qualification test process with the exact
admitted image and target identities from the staged generation; it verifies
the orchestration and identity handshake, not execution of that OCI image by a
production container or systemd runtime.

Forward transition qualification does not prove rollback or physical disaster recovery.

### Interruption and stopped-target qualification

Run the PostgreSQL-backed interruption matrix with:

```sh
task qualify:ubdr:migration-interruption
```

The lane terminates disposable transition-runner processes at the migration,
staging, activation, restart, and post-validation boundaries. It lets the
original transition fence expire, records the uncertain attempt as
indeterminate, and resumes the same FAI-1001 recovery-ledger occurrence under
a new fence. Migration state, immutable generations, the active-generation
pointer, and runtime identity are read back from their existing owners so a
retry does not duplicate completed mutations. The stopped-target cases use an
exclusive runtime lock and a real
replacement process, and reject mutation while the predecessor still owns the
target. Terminal preflight failure, indeterminate post-effect failure, stale
transition and ledger fences, evidence-publication retry, and abandoned-run
cleanup are included in the matrix.

Machine-readable evidence is written to
`.tmp/qualification/ubdr/migration-interruption/`, including
`scenario-matrix.json`, per-scenario reports and checkpoints, and
`ledger-export.json`. This qualification does not implement provider restore,
replacement-host rebuild, rollback, or RPO/RTO evaluation.

The release-owned PostgreSQL policy authority stores one immutable policy for
each exact predecessor/candidate artifact-digest pair. Policy publication is a
controlled maintenance operation; the application runtime has read-only
resolution access and cannot replace an admitted pair. Resolution recomputes
the existing `release-policy/v1` digest and verifies the rollback direction
against both artifact identities. It never derives policy from mutable release
rows, runtime configuration, or a caller-supplied policy projection.

This provides authoritative policy resolution. It does not execute a release transition.

The OCI artifact admission authority converts a reviewed CI admission result
into one canonical, append-only release record for an exact
`repository@sha256:digest` identity. The record binds immutable release and
repository identity to the admitted decision, provenance and SBOM references,
security-policy result, admission contract version, and admission timestamp.
Maintenance can publish an exact record or append a revocation; application
runtime access is read-only and resolves only the exact digest-pinned image.
Every read reparses the canonical bytes, recomputes their domain-separated
digest, and checks the denormalized database identity. Mutable tags,
unsupported policy versions, missing records, digest substitutions, and
revoked admissions fail closed. Publication also validates the repository and
workflow against the approved producer profile, requires an immutable source
commit, accepts only the SPDX Buildx SBOM profile and pinned Trivy policy, and
derives the stored verification results from that evidence instead of trusting
caller booleans. The authority stores neither registry credentials nor signing
keys.

This provides authoritative artifact admission resolution. It does not execute a release transition.

### Immutable migration compatibility owner evidence

`migration-compatibility/v2` is the immutable handoff contract between the
Goose, River/jobs, DuckLake, and PhysicalPool owners and the release-transition
preflight. Each owner produces a separate canonical evidence envelope after it
has independently resolved the same exact transition binding:

- the predecessor OCI admission digest;
- the candidate OCI admission digest; and
- the digest of the deployment-target identity.

The aggregate contract derives its binding from the Goose envelope and
requires byte-for-byte agreement from the other three owners. It does not
accept a separate caller binding. Each envelope also includes the fixed owner
identity, supported owner-contract version, explicit compatibility verdict,
the owner's version state, and a domain-separated SHA-256 digest. DuckLake and
PhysicalPool must report identical predecessor and candidate physical-pool
tuples. Missing, stale, ambiguous, or conflicting owner evidence fails closed.

Canonical JSON follows the declared struct field order and contains no omitted
or optional fields. Owner digests use
`leapview/migration-compatibility/v2/owner/<owner-identity>\n`; the complete
document digest uses `leapview/migration-compatibility/v2\n`. The checked-in
canonical-byte and digest vector freezes this representation. Version 2 is a
new contract and never reinterprets `migration-compatibility/v1` evidence.

Parsing verifies canonical encoding, internal binding consistency, and digest
integrity. Those checks do not establish provenance by themselves. Production
composition must obtain each envelope directly from its concrete subsystem
owner, which must resolve the artifact admissions and deployment target from
authoritative state before creating the envelope. Caller-created projections
must not cross that boundary.

This provides authoritative migration compatibility evidence binding. It does not execute a release transition.

The per-artifact migration capability authority closes the ownership gap
between OCI admission and subsystem compatibility evaluation. A controlled
subsystem owner signs one canonical `migration-capability-owner-evidence/v1`
envelope containing the exact `migration-capability/v1` bytes for each OCI
admission digest and deployment-target identity. Publication verifies the
detached Ed25519 proof against trusted owner-key configuration before any
database write; there is no production path that accepts a bare capability
projection. Goose records its owned schema and migration-graph
capability; River/jobs records both River schema and product job-history
capability; DuckLake records the artifact-owned catalog schema, runtime tuple,
and migration graph; PhysicalPool records its target-bound compatibility
tuple. Candidate catalog schema is therefore never copied from mutable current
catalog state.

Capability publication is append-only. An exact signed retry returns the
existing record, while a different capability or owner envelope for the same
artifact, target, and subsystem fails closed. The PostgreSQL foreign key
requires a durable admitted OCI artifact, maintenance has INSERT-only
publication access, and runtime has SELECT-only resolution access. Publication
and admission revocation serialize on the same artifact row: publication may
commit before revocation, or revocation wins and publication is rejected, but
evidence cannot be appended after a committed revocation. Every read reparses
both canonical documents, recomputes their domain-separated digests, verifies
the owner proof and denormalized bindings, and rechecks that the referenced
artifact admission remains valid and unrevoked. Trusted registries retain old
public keys for immutable historical verification; private keys are never
persisted by this authority.
The authority does not compose predecessor/candidate compatibility or execute
migrations; future `migration-compatibility/v2` owner adapters must resolve two
exact artifact capabilities and independently compare them.

This provides authoritative per-artifact migration capability resolution. It does not execute a release transition.

### Concrete migration compatibility owners

The PostgreSQL-era owner adapters produce the four
`migration-compatibility/v2` envelopes from authenticated, subsystem-owned
`migration-capability/v1` records. Their production input is a set of lookup
selectors only: exact predecessor and candidate OCI references and a
deployment-target ID.
There is no input field for an admission digest, target digest, compatibility
verdict, version projection, or owner envelope.

Each adapter independently resolves both immutable OCI admissions from the
release authority and the exact target revision from the deployment authority.
It then resolves the predecessor and candidate capability for its fixed
subsystem, exact admission digests, and target digest through the concrete
PostgreSQL capability authority:

- Goose compares the two artifact-owned schema versions and runnable sets;
- River/jobs compares the two artifact-owned River and job-history versions
  and runnable sets;
- DuckLake uses each artifact's catalog schema and runtime tuple, so candidate
  schema is never copied from mutable current catalog state; and
- PhysicalPool verifies that each artifact explicitly admits the other exact
  tuple digest for rollback-safe compatibility.

The artifact admissions, target revision, and both capability digests are
resolved again before the adapter returns. A changed or revoked authority
record, missing capability, target mismatch, digest substitution, or
unsupported owner state prevents the adapter from emitting an envelope.
Compatibility differences remain explicit and fail the aggregate transition
closed; the adapters do not infer or execute a migration.
This provides authoritative migration compatibility resolution. It does not execute a release transition.

### Authoritative production preflight composition

The production preflight entrypoint accepts only exact predecessor and
candidate OCI references, a deployment-target ID, and a published RecoverySet
frontier reference. Its constructor requires the concrete PostgreSQL release,
deployment-target, migration-capability, and RecoverySet repositories. It has
no interface or request field for caller-created artifact, release-policy,
migration-compatibility, subsystem-capability, target, or frontier
projections.

For each request, the release authority resolves both unrevoked OCI admissions
and their exact pair policy. The deployment authority resolves the exact
target revision. The migration authority then obtains all four independently
bound `migration-compatibility/v2` owner envelopes from authenticated
per-artifact capabilities and validates their canonical aggregate. Finally,
the RecoverySet authority resolves the exact published, passed, target-bound
frontier in one read-only snapshot. Only after those owner checks does the
existing evaluator produce its unchanged canonical transition-preflight
evidence.

The v2 admission bindings are verified before the existing preflight artifact
identity digests are derived. The historical caller-constructible
`migration-compatibility/v1` projection is not accepted, wrapped, or
reinterpreted by this path. PhysicalPool incompatibility remains fail-closed
when mapped into the existing persistent-domain decision model.

Preflight evidence does not execute a release transition.

The DuckLake compatibility value is the owner-produced verdict over the exact
predecessor and candidate tuples recorded in the evidence. The preflight does
not infer cross-version safety from tuple equality. A binary policy also binds
the explicit candidate-to-predecessor rollback direction; authorizing only the
forward pair is insufficient.

This validates transition eligibility. It does not prove successful upgrade, rollback, or disaster recovery.

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

The source root remains on its own delivery cadence unless the new application
version requires a resource migration. In that case, version application and
source-root changes together in the promotion record. The durable Project
identity remains bound to the target instance.
