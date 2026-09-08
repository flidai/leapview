# Storage and recovery

LeapView separates application state, analytical metadata, analytical files,
managed source objects, and ephemeral runtime data. Production authority lives
in PostgreSQL and a PostgreSQL-backed DuckLake catalog; Parquet and managed
source objects remain in their configured object stores.

## Storage ownership

The `leapview_control` PostgreSQL database owns users, grants, projects,
environments, deployments, jobs, event and audit records, lineage projections,
leases, and active serving pointers. The separately owned
`leapview_ducklake` database contains DuckLake metadata: analytical schemas,
snapshots, changesets, statistics, and physical-file manifests. Parquet files,
managed-data objects, and immutable serving artifacts hold the bytes described
by those authorities. Runtime directories and the process-memory L1 query cache
are disposable. No L2 or L3 cache is admitted; a future L2 tier requires
separate qualification evidence before deployment.

A recoverable analytical state therefore requires the control-database
recovery point and the matching DuckLake-database and object-store recovery
points.
Backing up only a local `leapview.db`, a catalog file, or copied Parquet objects
does not recover a production target.

## Native recovery boundary

Use PostgreSQL's native backup/PITR facilities for control-plane recovery. Use
the DuckLake/catalog and object-store provider's native snapshot, versioning,
replication, or backup facilities for analytical and managed-data objects.
Coordinate the selected points and retain the encryption keys and secret-store
procedures required to restore them.

The removed offline administrative backup, restore, and storage-cleanup
commands are not production recovery or cleanup procedures. LeapView currently
has no product-owned local file archive that substitutes for PostgreSQL
backup/PITR. Follow the [PostgreSQL operations
guide](/docs/guides/operate/postgresql-operations) and [Backup and restore
guide](/docs/guides/operate/backup-restore) for the complete operational
procedure; do not invent CLI flags or manually rewrite catalog and pointer
metadata.

## Verify a recovery

Keep writes stopped while native recovery runs. Start LeapView with the matching
image and configuration only after both PostgreSQL databases and the
corresponding object-store points are restored. Follow the backup and restore
guide's `leapview admin recovery prepare`, `validate`, and `publish` sequence
for one immutable recovery-set document and its provider-produced evidence.
That sequence checks the exact control, DuckLake, object, active-generation,
snapshot-seal, and compatibility frontier before traffic can resume. Then
verify authorization, managed-data revisions, representative semantic queries,
and dashboards. Preserve recovery evidence and the failed state until
verification is complete.

Development and evaluation fixtures may use embedded SQLite and a local DuckLake
catalog, but those adapters are not a production fallback. Test fixture backup
and restore belongs to the fixture harness, not this production runbook.

## FAI-520 historical managed-object retrieval qualification

### Qualification goal

FAI-520's bounded PostgreSQL-era slice qualifies exact historical retrieval of
managed content bytes. It does not restore an application or publish a recovery
set. Existing FAI-521 admission validators and startup behavior are unchanged.

This validates historical managed-object retrieval. It does not prove successful physical disaster recovery.

### Provider assumptions

The selected provider is disposable MinIO, pinned to
`minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e`.
The experiment requires bucket versioning, immutable non-null version IDs,
explicit-version GET, retained historical versions behind a delete marker, and
authenticated access. It qualifies that image, not all S3-compatible services
or AWS production configurations. Expired lifecycle retention may make versions
unavailable; retrieval must then fail, never substitute latest.

### Test environment

`TestFAI520HistoricalManagedObjectRetrieval` is opt-in under
`fai520qualification`. Docker is required; inability to start the provider is a
failure, not a skipped qualification. Each run creates a fresh container,
random credentials, a unique versioned bucket, and two isolated prefixes.
Credentials are configured directly; no ambient credential chain is consulted.
MinIO's `/data` uses a bounded 1-GiB tmpfs (allocated on demand). This isolates
the tiny fixture from shared-host disk pressure; it does not test crash
persistence. Failed HTTP requests log only method/status, never URLs,
credentials, or response bodies.
Container cleanup removes the disposable data, including versions and delete
markers. No production endpoint or credentials are accepted.

The production managed-data S3 `Put` path creates two content-addressed blobs.
Raw provider writes then replace one blob's key with different bytes and corrupt
bytes; these writes simulate damage, not supported managed-data updates. A
delete marker makes latest unavailable. Another prefix retains two sentinel
versions. These are blob revisions, not PostgreSQL managed-revision records.

### Commands and observation records

Run from the repository root with the repository Go toolchain and Docker access:

```bash
go test -tags=fai520qualification ./internal/manageddata/storage/s3 -run '^TestFAI520' -count=2 -v
# Use -json instead of -v to retain machine-readable test output.
go test -tags=fai520qualification ./internal/manageddata/... ./internal/platform/objectstore/... ./internal/app/objectstore/... ./internal/recoveryset/... -count=2
go test ./internal/app -run PostgreSQLRecoveryAdmission -count=1
go test ./internal/app/adminpostgres/... ./internal/platform/architecture/... -count=1
task generated:check
task docs:check
git diff --check
```

Baseline test logs capture endpoint, region, bucket, exact key, version ID,
size, SHA-256 of fetched bytes, and observation time. Retrieval logs capture
selection, expected content size/digest, observed digest, start/end timestamps,
and allowlisted results. SDK error strings and credentials are not recorded.
The expected size is labeled as such; it is not a measured recovery duration or
RPO/RTO. These are test observations, not a new recovery-admission envelope.

### Recorded validation

Local validation on 2026-09-08 used Linux/amd64, Go 1.26.8, Docker, and the
pinned provider above, based on main `3b201d71646ac4ec89feebfcdbb8901b4d958010`.
The provider matrix passed ten consecutive runs after waiting for provider
readiness (liveness alone can precede S3 initialization). The combined
managed-data/objectstore/recovery-set suites passed twice. Startup/admin
recovery and architecture tests, generated-state checks, and docs checks passed.

One recorded retrieval at `2026-09-08T05:45:56.35791612Z` returned historical
version `1f15342c-cc54-48e7-b191-b9b88966b55f`, 19 bytes, SHA-256
`26d870502c4a8863b3cf3bcbfe99723b6996597ee9443d09c456a37b18538c0c`,
matching the original managed blob. Wrong-version and corrupt-byte reads
returned different digests and were rejected; credential repair yielded that
same original digest twice. This ephemeral example is not a reusable recovery
point; its disposable container was removed.

### Positive evidence

- Exact-version GET returns historical A after replacement and current deletion.
- Returned version identity must match the request. Bounded response hashing
  must match the original managed content SHA-256 and size; ETag and metadata
  are not content proof.
- Two retries after credential repair reproduce the same successful digest.
- The unrelated managed blob remains readable through the production `Open`
  path; both sentinel versions remain byte-identical.
- Complete provider version/delete-marker inventories before and after the
  retrieval matrix must match, proving reads and retries have no write effects.

### Negative evidence

| Case | Required outcome |
|---|---|
| Missing historical version | Provider `NoSuchVersion`; no latest fallback |
| Empty or null version selection | `missing_version` before retrieval |
| Wrong retained version | `integrity_mismatch` against original content |
| Same-size corrupted bytes | `integrity_mismatch` |
| Invalid secret | Real provider `SignatureDoesNotMatch`; no success |
| Substituted endpoint, region, bucket, or prefix | `namespace_mismatch` before fetching |

Namespace matching belongs only to this test's selected observation boundary.
It does not add a production recovery validator. Negative observations are not
successful evidence and cannot be used to claim recovered content.

### Limitations and next dependency

This proves historical object availability, integrity, and isolation for the
selected provider. It does not prove coordinated disaster recovery, PostgreSQL
restore, PITR, key recovery, historical replica reconstruction, replacement
endpoint recovery, multipart recovery, governed queries, or operational RTO/RPO.
Random per-run identities in test output must not be reused as production
recovery evidence. Production IAM/KMS policy and provider lifecycle behavior
require separate qualification.

The next bounded slice should bind an existing recovery frontier's complete
managed-object closure to provider-native retained versions and qualify missing
members without mutating admission contracts. Physical restore orchestration
must wait for that closure and a reviewed PostgreSQL/provider consistency plan.

## FAI-520 managed-revision closure qualification

### Design and dependency discovery

`TestFAI520HistoricalManagedRevisionClosure` extends the opt-in
`fai520qualification` suite. A disposable PostgreSQL database is initialized
with the existing managed-data schema; fixture writes and reads use the normal
runtime role and repository. Production S3 `Put` writes three content-addressed
blobs before `CreateUploadSession` and `CompleteUpload` persist a revision.
The fixture's `metadata.json` is an ordinary explicitly declared file, not an
invented product metadata object. The canonical revision manifest remains in
PostgreSQL.

Given only the operational revision ID, discovery uses `RevisionByID` and
`ListRevisionFiles`. The test compares the persisted canonical manifest digest
with the listed dependencies using the existing `Manifest.RevisionID` contract.
It traverses the complete manifest in logical-path order, not provider listing
order or the number of observations supplied by a caller. Every member must
pass exact-version retrieval, response-version checking, size, and fetched-byte
SHA-256 validation before the closure result can be `verified`.

### Provider version mapping boundary

Neither `RevisionFile` nor `BlobStore` records a provider version ID or exposes
a historical restore API. The fixture therefore captures versions from real
provider HEAD responses immediately after production writes, before damage.
It supplies that separate observation inventory to the test-only traversal.
There is no production restore API, new recovery envelope, admission validator,
or provider scan that guesses historical versions from ETags or latest objects.

This proves closure availability only when the revision metadata and exact
provider-version observations have survived. A durable, reviewed binding of
that inventory to the selected recovery frontier remains a prerequisite for
physical recovery. The test does not claim the product discovers lost provider
version mappings automatically.

### Positive and negative evidence

The fixture replaces every dependency with different bytes and adds current
delete markers, retaining the selected historical versions. It also writes
unrelated objects. Test output records the revision ID, manifest digest,
dependency paths/hashes/sizes, requested version IDs, traversal order, observed
hashes, timestamps, terminal result, and verified-member count. Failures use
bounded reason codes, not credential-bearing provider errors.

| Scenario | Required result |
|---|---|
| Complete three-member historical closure | All three digests match |
| Missing first dependency observation | `missing_dependency`, zero verified members |
| Missing last dependency observation | `missing_dependency`, two members; never complete |
| Missing historical version of second member | `NoSuchVersion`, no latest fallback |
| Wrong retained version of second member | `integrity_mismatch` |
| Extra unrelated observation | Ignored; same complete closure |
| Invalid credentials on second member | `SignatureDoesNotMatch`, one member; never complete |
| Two retries after credential repair | Same ordered complete digest list |

Unrelated sentinel bytes and the complete provider version/delete-marker
inventory must remain unchanged. Retrieval has no writes or activation side
effects. Partial verified-member lists are diagnostics, not successful closure
evidence. PostgreSQL and provider startup failures fail this opt-in test rather
than skipping it.

### Commands and remaining qualification

The initial closure matrix passed five repeated runs and the broader suites.
A subsequent evidence run failed during seeding with HTTP 507 while the shared
host filesystem was nearly full. The fixture now isolates MinIO data in tmpfs;
storage exhaustion is not bypassed or treated as successful retrieval.

```bash
go test -tags=fai520qualification ./internal/manageddata/storage/s3 -run '^TestFAI520' -count=5
go test -tags=fai520qualification ./internal/manageddata/... ./internal/platform/objectstore/... ./internal/app/objectstore/... ./internal/recoveryset/... -count=1
go test ./internal/platform/architecture/... -count=1
task generated:check
task docs:check
git diff --check
```

Use `-json` to retain the per-member and terminal test observations. A metadata
database is seeded, not restored. No PostgreSQL recovery, PITR, key recovery,
coordinated restore, activation, production-provider IAM/KMS behavior, or RPO/RTO
is qualified here. Closure means the declared managed revision's files, not
DuckLake/catalog or serving-artifact dependencies.

This validates historical managed-object retrieval. It does not prove successful physical disaster recovery.

## Provider-version observation binding qualification

### Model and evidence flow

There are two distinct evidence boundaries today. They must not be confused:

1. A managed revision owns its canonical dependency manifest (logical paths,
   SHA-256 values, and sizes). Persisted revision files identify storage keys.
   The S3 provider owns historical version IDs. The qualification captures
   those IDs from provider responses after real managed-data writes, and
   verifies the selected version's returned identity, size, and fetched bytes.
2. A recovery set owns a digest-bound frontier and immutable validation results.
   Its v1 contract admits exactly one DuckLake root and one serving-artifact
   root. Each root binds a URI, version ID, digest, and provider recovery
   frontier. It does **not** admit a managed-revision observation inventory.

Thus the requested flow currently stops at a contract boundary:

```text
managed revision → manifest → object key → captured provider version → verified bytes
                                         │
                                         └─ no typed managed-observation binding in recovery-set v1

existing DuckLake/artifact roots → frontier digest → attempt-bound validation evidence
```

The capture is not a transactionally atomic provider inventory. Managed-data
`BlobStore` writes do not return a durable version mapping, and a later HEAD
can race an external overwrite. Exact historical-byte verification detects a
wrong selection, but cannot reconstruct an observation that was never saved.
ETags, current/latest objects, or a revision digest are not substitutes for a
provider-issued version ID.

### Qualification boundaries and rejection cases

The opt-in suite exercises captured-observation replay and the existing
recovery-set evidence boundary separately. A serialized test observation
artifact is not a new product recovery contract or an authoritative ledger.
No managed data is relabeled as a DuckLake or serving-artifact root, and no
inventory is hidden inside the opaque `provider_recovery_frontier` string.

`TestFAI520ManagedProviderBindingObservationReplay` writes three real managed
objects to disposable versioned MinIO, persists and rereads a test observation
artifact, rebuilds selections from that artifact, and repeats closure validation
after overwriting a current object. The historical selection still produces the
same ordered content digests. The full three-test provider suite passed five
runs per test.

`TestProviderObservationDurabilityAndExactReplay` exercises the existing
PostgreSQL recovery repository with **synthetic DuckLake/artifact root
identities**, not the managed objects from the provider fixture. A separate
read-only process consumes the persisted frontier and validation result and
compares canonical bytes and digests. The set remains prepared. Its companion
tests exercise conflicts and strict rejection of unsupported managed evidence.
These tests intentionally do not claim a cross-system managed-inventory binding.

| Case | Current boundary |
|---|---|
| Complete retained three-object closure | Captured versions can reproduce the manifest's exact hashes and sizes |
| Missing provider-version observation | Existing closure qualification rejects an incomplete selection |
| Version points to wrong bytes | Historical byte/size verification rejects it |
| Provider endpoint/bucket/key differs | Qualification rejects the namespace mismatch before trusting bytes |
| Typed validation-result digest differs from its evidence | Existing recovery-set validation rejects it |
| Missing, conflicting, or stale root/frontier identity | Existing typed frontier validation rejects the mismatch |
| Managed inventory digest, duplicate observations, or capture time outside a frontier | No typed managed-observation contract exists; these are **not qualified product rejections** |

Root/frontier rejection is narrower than managed-observation rejection. A valid
root envelope is not proof that every managed dependency was observed or that
the observations belong to a coherent PostgreSQL/provider recovery point.

### Durability and replay

Existing recovery-set repositories persist exact frontier identities and
attempt-bound canonical evidence. Identical replay is idempotent; conflicting
replacement of the same durable identity is rejected. Another validation
attempt can consume the same frontier, but its evidence digest changes because
the attempt ID is part of the envelope. Only replay of the **same** attempt is
expected to have an identical result digest.

These protections do not automatically persist the separate captured managed
provider-version inventory. The MinIO fixture only writes and rereads its
observation file in the same process: it does not prove restart durability,
atomic publication, or immutability of that file. The separate-process test
applies only to existing PostgreSQL root evidence, not those managed observations.
Provider retention and access must independently keep every referenced
historical version available.

| State | Current storage | Durability/immutability qualified |
|---|---|---|
| Managed revision and declared dependencies | PostgreSQL managed-data repository | Existing persisted identity; no provider version map |
| Captured managed provider observations | Test memory and a disposable `provider-observations.json` file | File round-trip only; mutable and removed at test cleanup |
| Recovery frontier and typed root identities | PostgreSQL recovery-set repository | Fresh-process read and same-ID conflict rejection |
| Attempt-bound root validation result | PostgreSQL `recovery.validation_result` | Identical replay; conflicting updates rejected |

The end-to-end requested binding remains blocked. A managed observation can
survive only if its separate artifact survives, and the current product contract
cannot decide whether that artifact is authoritative for a selected frontier.
In particular, managed duplicate/conflict rejection and capture-frontier
staleness are not supplied by a generic JSON file round-trip.

### Contract gap and smallest future extension

A reviewed future version of the existing recovery evidence contract should
bind an immutable managed-observation manifest by digest. That manifest needs
the managed revision/manifest identity and complete per-member logical path,
provider identity (including endpoint/region and bucket/key), historical version
ID, SHA-256, and size. Its capture boundary must be tied explicitly to the
selected recovery frontier, not inferred from local timestamps or latest
provider state. Conflicting duplicates must be rejected before canonicalization;
the same durable identity must never be overwritten by a conflicting capture.

The frontier must commit to that manifest digest, and validation must require
complete membership and exact provider bytes before accepting the evidence.
Retention, provenance, and credential-free provider identity semantics need
review with that extension. This slice adds none of these production fields,
does not change migrations, and does not create an alternate admission path.

The next phase should specify and approve this minimal binding contract, then
qualify durable capture, restart/replay, conflicts, and frontier staleness against
it. PostgreSQL restore, PITR, coordinated recovery, activation, governed-query
recovery, and measured RPO/RTO remain later UBDR work.

This validates recovery evidence binding. It does not prove successful physical disaster recovery.

## Proposed managed observation manifest contract

The following proposal and implementation-planning sections are retained as
design history, not the current implementation specification. The implemented
evidence-only contract, resolved capture/retention decisions, and remaining
limitations are documented in [Recovery evidence manifests](/docs/guides/data/recovery-evidence-manifest).
Future SQL admission, restore, and activation phases below remain unimplemented.

**Referenced-manifest design approved for implementation planning; not implemented
or approved for production.**
This section proposes the smallest versioned extension for FAI-520. It does not
authorize migrations, restore orchestration, PostgreSQL recovery, PITR, key
recovery, or activation changes.

### Problem and current ownership

The current owner is `internal/recoveryset`: `RecoverySet` and
`ValidationEvidenceEnvelope` are both v1. `validateCanonicalObjectRoots` requires
exactly the DuckLake and serving-artifact roots matching the serving seal.
`ParseValidationEvidenceEnvelope` rejects unknown fields. PostgreSQL's
`recovery.guard_validation_result_insert` reconstructs that exact v1 envelope;
adding Go fields alone would either fail insertion or undermine the SQL guard.

Managed-data owns `RevisionByID`, `ListRevisionFiles`, and the canonical
`Manifest.RevisionID` content digest. An operational revision ID is distinct
from its content digest. `RevisionFile` carries a storage key but no provider
version ID. `BlobStore` offers content-addressed Put/Stat/Open, not historical
retrieval. Existing `RecoverySet.Digest` excludes mutable publication metadata;
`IdentityEqual` additionally protects exact creation identity. Attempt IDs are
part of validation-result digests, so different attempts must not share a result
digest merely because they validate the same frontier.

### Alternatives

| Option | Benefits | Costs / decision |
|---|---|---|
| A: additional recovery roots | Reuses root persistence | Root tuples lack revision membership, per-object sizes, and capture semantics; overloads seal-root meaning and changes the exactly-two invariant. Reject. |
| B: immutable manifest referenced by frontier | One content identity covers complete membership; reusable across attempts; bounded envelope | Requires owned durable manifest storage and versioned reference validation. Recommend. |
| C: embed all observations in each evidence envelope | One submitted document | Repeats potentially large inventory per attempt, pressures strict envelope limits, and leaves the prepared frontier ambiguous unless it also commits to the inventory. Reject in favor of a reference. |

Do not encode inventory JSON inside `provider_recovery_frontier`, use an ETag as
a digest, or label managed data as DuckLake/artifact content. B is an extension
of the existing recovery owner, not a second recovery model or validator.

### Proposed contract and scope

Propose RecoverySet **v2**, ValidationEvidenceEnvelope **v2**, and a separately
versioned ManagedObservationManifest **v1**. Names below describe proposed
fields, not existing CLI inputs or public JSON fields.

Each v2 set requires one `managed_observation_manifest_digest`. The envelope
carries that same digest and the final frontier digest; it does not repeat the
inventory. The manifest contains all managed revisions required by the selected
target/generation, not just whichever revision an operator chooses to submit.
The bounded first implementation supports one selected target, not a fleet.

| Manifest component | Required contents |
|---|---|
| Format | Manifest version; strict known fields; canonical encoding |
| Capture | UTC capture start/completion timestamps; immutable capture-evidence reference/digest; producing authority identity without credentials |
| Frontier anchor | Recovery-set UUID and source-frontier anchor digest, covering exact selected database recovery identities, delivery, seal, catalog, roots, and compatibility |
| Revision membership | Project/collection identity, operational revision ID, canonical revision manifest digest, and canonical declared dependency files |
| Managed closure identity | Domain-separated digest of the complete sorted revision membership and dependency list, including logical path, expected SHA-256 and size; separate from the serving seal's existing ClosureDigest |
| Provider observations | Exactly one record per required revision/path: provider implementation, non-secret account/tenant identity where applicable, canonical endpoint, region, bucket/container, namespace prefix, exact object key, opaque non-null version ID, content SHA-256 and nonnegative size |

The same content may appear at multiple logical paths or revisions. Keep those
membership edges; physical reads may be deduplicated only when provider/key/
version/digest/size are identical. Reject conflicting versions for the same
provider object identity within this single frontier. No maps that silently
overwrite duplicate entries during parsing.

Required revision membership must be derived by the managed-data owner from
the selected immutable serving-generation bindings and its declared retention
scope in the selected control recovery point. Never use current environment
pointers or the submitted inventory itself as the completeness authority.
Resolving and verifying this exact binding-set selection is a prerequisite,
not a capability claimed by the current qualification fixtures. Extra raw
provider objects are ignored; extra entries in the authoritative manifest are
rejected. An empty manifest is valid only when that independent selection proves
there are no required managed dependencies; missing is not equivalent to empty.

### Non-circular immutable identity

Use one explicitly versioned canonical projection owned by recoveryset:

```text
selected immutable frontier fields + set UUID
                  |
                  v
            source anchor A
                  |
                  v
capture evidence + revisions + observations --> manifest digest M
                                                   |
immutable frontier fields + M ----------------------+
                  |
                  v
        final v2 frontier digest F
                  |
                  v
        attempt-bound envelope (F, M)
```

A is a domain-separated hash of the v2 immutable frontier projection excluding
only the managed-manifest reference and the final digest, with lifecycle/fence/
audit/creation metadata excluded as in the current frontier identity. Freeze
the exact projection and canonical bytes in golden fixtures. Do not obtain A by
calling a legacy decoder or silently downgrading a v2 set to v1.

The manifest stores A and the set UUID, **not F**. Its own digest M is external
to the hashed manifest bytes. F commits to the manifest reference as well as the
immutable frontier projection. The validator recomputes A, M, and F. This binds
the manifest bidirectionally without asking it to contain the hash of itself.
Any referenced capture receipt likewise binds A, not M or F; it must not create
a second indirect hash cycle back to the containing manifest.
A is an internal commitment, not a second publishable recovery frontier. The
v1 digest algorithm and canonical bytes remain unchanged.

Canonicalization must reject duplicate JSON keys, unknown fields, invalid
digests and integer overflow before hashing; sort revisions by full identity
and observations by revision/path. Preserve opaque object keys and version IDs
exactly: never collapse path segments or decode them into aliases. Define one
credential-free endpoint normalization rule; reject userinfo, queries, fragments,
and ambiguous encodings. Namespace matching must respect a prefix boundary,
not a raw string prefix. Initial qualification limits proposed for review:
8 MiB canonical manifest and 10,000 logical members, with bounded parse/read
allocation. Oversize fails explicitly; no truncation, opaque pagination, or
chunking schema is introduced in this first extension.

### Validation invariants

1. **Completeness:** independently selected revision membership and canonical
   dependency identities match exactly. Missing/duplicate/extra observations,
   missing revision metadata, and scope changes fail closed.
2. **Integrity:** exact-version provider GET returns the requested version;
   hash all returned bytes with a size bound and compare SHA-256 and size to the
   managed owner manifest, not only the submitted observation. Reject truncated,
   oversized, or changed content. ETag and HEAD metadata are not byte proof.
3. **Identity:** A and set UUID match the selected frontier, M matches immutable
   bytes, and F matches the set. Provider/namespace/key must match the trusted
   storage configuration for those selected bindings. No latest-version,
   endpoint-substitution, or credential fallback.
4. **Capture:** timestamps are canonical, ordered, and bounded by a trusted
   verification clock, but timestamps alone do not establish a consistent
   recovery point. The capture evidence must identify the exact source control
   snapshot/recovery identity, binding scope, and retained provider observations.
   A capture for another anchor or recovery identity is stale even if recent.
   A later verification of an unchanged historical version is not a new capture.
   No arbitrary maximum-age cutoff should invalidate a retained older recovery
   point. Provider-specific proof of that capture boundary must be reviewed
   before a physical-recovery claim; an opaque timestamp/token is insufficient.
5. **Immutability:** reject replacement from first durable insertion, not merely
   after publication. Same set UUID with different M conflicts. Same M with
   different bytes is an integrity failure. Correction requires a new set and
   manifest identity, never editing a prepared/published record.
6. **Authority:** a content hash proves commitment, not honest capture or byte
   verification. The existing authorized recovery validation path must consume
   owner-validated probe results; copying a manifest digest into an envelope
   cannot manufacture a passed attempt. SQL guards protect structure/identity,
   not remote provider truth. Capture authority and offline evidence trust must
   be specified explicitly before implementing provider verification.

### Persistence, restart, and publication

Recommend canonical manifest bytes in a bounded, append-only child store under
the existing PostgreSQL recovery repository, keyed by M and bound uniquely to a
set UUID. This avoids introducing a new evidence bucket/availability dependency.
Store exact canonical bytes, not only JSONB (which loses original byte encoding),
and verify digest/header consistency. It is evidence storage, not a provider
restore API. An off-host export is still needed for total database loss; evidence
stored in the database being recovered is not itself a disaster-recovery backup.

Capture/validate inputs before creating the prepared set. Insert v2 set and
manifest atomically in one short transaction with deferred referential checks
or equivalent transactional completeness guards; no remote I/O inside it.
Never expose a usable prepared set whose manifest is absent. Same-identity
inserts compare immutable bytes; never use overwrite-on-conflict. Failure or
crash before commit leaves no usable set; after commit a new process reloads by
exact set UUID/M and verifies canonical identities without recapturing latest.

An attempt performs completeness and byte checks, persists the small v2 result,
and completes through the existing lifecycle. Publication still selects one
exact passed attempt under its existing fence; no new publication state or
activation pathway. It must require the stored manifest reference to match the
passed envelope. A repeat of the same attempt returns identical evidence/digest;
a different attempt has a different result digest but the same F and M. Local
readiness consumes the selected validated identity; it must not start provider
GETs. Retention must preserve manifest bytes and required historical objects
for the set's retention/hold lifetime; collection must not delete referenced
evidence. Retention policy changes do not rewrite immutable capture bytes.

### Compatibility and migration impact

- Preserve v1 decoding, hashing, exact retries, and historical FAI-521 fixtures.
  A missing managed manifest means **unqualified**, not an empty managed closure.
  Do not backfill old sets with fabricated observations or mutate their hashes.
- New v2 requests always require an explicit manifest, including an independently
  proven empty one. A workflow requiring managed recovery must reject v1 rather
  than retrying with weaker semantics. Existing v1 records remain readable and
  usable only under their original root-admission guarantee, never upgraded
  implicitly to managed-recovery evidence.
- Readers must explicitly dispatch supported set/envelope versions. Unknown
  versions and mismatched set/envelope versions fail closed. Roll out compatible
  readers and SQL guards before enabling v2 writers. An old binary cannot be
  assumed to operate on a selected v2 set; binary downgrade needs an independent
  compatibility decision, not evidence deletion or a v1 rewrite.
- Future changes would add manifest storage/reference constraints, extend the
  current `schema_version = 1` check to supported versions, update immutable
  identity guards, and version SQL envelope reconstruction/digest checks.
  Keep the two existing root semantics. Update recovery-owned DDL, the canonical
  Goose migration path, generated queries, parsers, and conformance tests
  together; changing only `CREATE TABLE IF NOT EXISTS` does not upgrade an
  existing installation. No migration is created by this design task.
- Maintain existing least-privilege recovery maintenance access. No application
  role gains update/delete authority over evidence. SQL inserts must not bypass
  version, completeness, digest, or immutable-reference checks. Raw provider
  error text, signed URLs, credentials, and key material are excluded; hashes
  and capture receipts do not replace key availability qualification.

### Implementation phases and qualification plan

1. **Approve the contract:** freeze A/M/F canonical projections, exact managed
   binding/retention scope, bounded format, capture authority/proof, and the v1
   compatibility matrix. These decisions gate coding; no version number alone
   proves capture consistency.
2. **Owner contract tests:** complete/empty manifests, order-independent
   canonical digest, scope completeness, duplicate JSON/object rejection,
   digest/size tampering, wrong provider/namespace, stale anchor, future/reversed
   timestamps, limits/overflow, and preservation of all v1 golden digests.
3. **Durable repository:** fresh install and upgrade fixtures, atomic creation,
   crash before/after commit, separate-process read/revalidation, identical retry,
   concurrent conflicting inserts, direct-SQL bypass rejection, immutable bytes,
   retention holds, and no replacement of selected successful evidence.
4. **Real provider qualification:** capture production writes in disposable
   versioned MinIO, persist M with an exact frontier, terminate the capturing
   process, then independently discover/retrieve all dependencies. Exercise
   missing observation/version, same-length wrong bytes, conflicting versions,
   substituted provider, partial results, credential repair, and changed source
   recovery identity. Check no partial attempt becomes publishable. Do not
   claim a provider can mutate an immutable version in place: simulate changed
   returned bytes in a separate fault-injection test and require rejection.
5. **Existing admission integration:** exact-attempt publication and local
   readiness binding remain intact; two attempts use the same immutable manifest
   without sharing attempt digests. Re-run FAI-521 completeness tests and mixed
   v1/v2 compatibility matrices before any production writer enablement.

Only after those phases should UBDR address coordinated PostgreSQL/catalog/
object recovery, migration interruption, release rollback compatibility,
off-host rebuild, and RPO/RTO measurement. This proposal does not resume FAI-518
or implement PostgreSQL restore/PITR.

## Managed observation implementation specification

This specification refines the approved referenced-manifest architecture.
Only documentation is changed. The detailed defaults below require the listed
sign-offs before contract/golden fixtures are frozen; approval of the architecture
does not imply that provider capture authority or physical consistency is solved.

### Storage decision

| Storage option | Assessment |
|---|---|
| PostgreSQL canonical bytes plus digest | Recommended first implementation: atomically committed with the frontier, existing access/backup boundaries, deterministic restart reads |
| Immutable provider object | Adds bootstrap credentials, retention, retrieval and failure dependencies just to read admission evidence; defer |
| Separate content-addressed store | Content addressing supplies identity, not durability or authority; another store is unnecessary |
| PostgreSQL index plus external bytes | Useful for large future inventories, but creates a distributed commit and orphan-reclamation problem; defer |

Use content addressing **inside PostgreSQL**, not a new storage service. Limit
canonical bytes to 8 MiB and logical file memberships to 10,000 per set for the
initial contract. Exceeding either rejects creation before persistence; no
silent truncation or fallback to v1. These are proposed qualification limits,
not measured production capacity. One set owns one manifest; because the set ID
is in the manifest, sharing is across attempts, not different recovery sets.

### Exact canonical shape and hashing plan

The proposed fixed field order is:

- Manifest: `manifest_version`, `set_id`, `source_frontier_anchor_digest`,
  `capture`, `managed_closure_digest`, `revisions`.
- Capture: `authority_id`, `capture_id`, `started_at`, `completed_at`,
  `receipt_digest`. The immutable receipt binds the source control recovery
  identity, selected scope and provider capture boundary to A; it cannot refer
  back to M or F. Receipt verification is a gated dependency, not an arbitrary
  self-declared success token.
- Revision: `project_id`, `collection_id`, `revision_id`,
  `revision_manifest_digest`, `files`.
- File: `path`, `sha256`, `size`, `provider`.
- Provider: `implementation`, `account_identity`, `endpoint`, `region`,
  `bucket`, `prefix`, `key`, `version_id`.

Each file is a membership edge with its expected content and exact observation;
there is no second independently editable copy of the file inventory. Rebuild
the managed-data `Manifest` projection from path/SHA-256/size and use its owner
digest function, comparing with the independently loaded selected revision.
Shared content keeps all membership edges. The managed-closure projection is
the sorted revision array with only the provider fields removed, including
revision identity and owner manifest digest.

Define C as bounded canonical UTF-8 JSON, fixed field order above, no whitespace
or trailing newline, decimal integers without exponent, no floats, and explicit
arrays (`[]`, never null). UTC timestamps have exactly six fractional digits
and `Z`; reject sub-microsecond inputs rather than silently rounding identity.
Use the current owner encoder's string escaping convention (Go JSON marshal
escaping, including HTML characters) and golden-test the SQL implementation
against it. Reject invalid UTF-8, duplicate keys at every nesting level, unknown
fields, case aliases, repeated revision identities and repeated paths **before**
normalization. No Unicode normalization of object keys or version IDs.

Sort revisions lexicographically by UTF-8 bytes of
`(project_id, collection_id, revision_id)` and files by logical path bytes.
SQL must use equivalent byte ordering, not database locale ordering. Endpoint
rules lower-case scheme/host and remove only a default port; reject userinfo,
query, fragment and endpoint path prefixes in the first contract. Preserve
opaque keys/version IDs; compare namespace prefixes on a delimiter boundary.
An account identity may be an explicit empty string only for an approved
provider profile where endpoint/namespace uniquely defines it; never infer
missing identity from credentials. HTTPS is required outside explicitly
disposable local qualification. Freeze provider-profile rules with fixtures.

Use lowercase hex SHA-256 with `sha256:` for contract digests; managed file
digests retain the managed-data owner's existing unprefixed representation.
Proposed domain strings below include a final newline byte:

1. A = SHA256(`leapview/recovery-source-anchor/v2\n` || C(anchor)). Anchor keys
   are exactly `schema_version` (2), `set_id`, `cluster_points`, `delivery`,
   `serving`, `catalog`, `object_roots`, `compatibility`, preserving their existing
   typed subfields and canonical array rules. Exclude lifecycle, fence, audit,
   creation metadata and managed reference entirely; no recursive call to v1.
2. Closure = SHA256(`leapview/managed-closure/v1\n` || C(closure projection)).
3. M = SHA256(`leapview/managed-observations/v1\n` || C(manifest)). The manifest
   contains A and Closure, not M or F.
4. F = SHA256(`leapview/recovery-frontier/v2\n` || C(anchor fields plus
   `managed_observation_manifest_digest` as the final field)).

The v2 envelope has existing envelope fields plus that manifest reference;
its attempt-bound digest uses a separately frozen v2 envelope domain. Preserve
the v1 envelope algorithm exactly. A receipt digest commits to immutable receipt
bytes whose authority/proof format must be approved; hashing does not establish
honesty. An empty `revisions` array has defined C/Closure/M but is accepted only
after the managed-data owner proves the selected generation requires no files.

### Future SQL model (description, not DDL)

| Relation | Keys / indexes | Constraints and purpose |
|---|---|---|
| `recovery.managed_observation_manifest` (new) | PK `manifest_digest`; UNIQUE `set_id`; UNIQUE `(set_id, manifest_digest)` for a matching composite reference | `manifest_version = 1`, set UUID, A, Closure, canonical bytes, byte length, revision/member counts, repository `recorded_at`; strict size/digest/header/count checks; insert-only |
| `recovery.recovery_set` (extend) | Existing set PK; composite deferred FK `(set_id, managed_observation_manifest_digest)` to manifest | v1 reference must be NULL; v2 reference required; supported version check; immutable F includes M |
| Manifest ownership FK | Manifest `set_id` references set PK, deferred to transaction commit | Enforces one coherent aggregate; prevents a manifest belonging to a different set |
| `recovery.validation_result` (existing) | Existing attempt PK | Version-aware strict envelope guard compares exact set/F/M; no large inventory duplicated per attempt |

Do **not** add a separate manifest-entry table initially. The bounded canonical
bytes are the authority; parsed entries are ephemeral validator input, not a
second mutable representation. Header/count columns are checked projections of
those bytes. If entry indexing is later justified, its rows must be atomically
derived and immutable, not an alternate source of truth. No provider-key,
timestamp, or JSON GIN index is needed for the exact-ID read path. Do not index
capture completion as an automatic deletion deadline.

The database insertion guard must validate supported shape, canonical encoding,
digest, header projections, duplicate rejection and the A/set association for
direct SQL as well as repository inserts. Parsing raw JSON as JSONB first would
lose duplicate keys; preserve raw bytes and validate before that conversion.
Implement a schema-specific, versioned guard with Go/SQL golden conformance,
not a generic JSON ingestion channel. SQL cannot verify remote object bytes.
Reuse the existing owner validator and authorized evidence-submission boundary
for that check; its trust/receipt policy is an explicit prerequisite.

### Lifecycle and ownership

1. **Select/capture:** the authorized maintenance qualification runner selects
   an exact source frontier and independently resolved managed binding scope.
   Managed-data owns revision discovery; the provider adapter owns version
   observations and exact GET; recoveryset owns format/identity validation.
   Inputs include the source control recovery identity, immutable generation
   bindings, expected manifests, trusted provider configuration, read-only
   credentials resolved outside evidence, and capture authority/receipt.
   HEAD after Put can race external mutation: capture the returned provider
   version where available, otherwise verify exact-version bytes and receipt
   provenance. Never declare capture authoritative from HEAD alone.
2. **Construct:** establish A, verify full membership and versions, produce the
   receipt and canonical manifest, compute M/F. Capture is authoritative only
   after approved receipt/point validation and complete byte verification.
   No public prepared set exists while inputs are incomplete. Retry a saved
   capture rather than changing capture timestamps; a recapture creates a new
   set identity.
3. **Persist:** one short transaction inserts the prepared set and immutable
   manifest, verifies the deferred pair and commits. No network calls in this
   transaction. Crash before commit rolls back both; crash after commit allows
   exact reload. `ON CONFLICT` may compare existing bytes/identity, never update
   them. Repository insertion time is not part of M. Concurrent creators with
   different bytes for the same set deterministically conflict.
4. **Validate/replay:** each new attempt loads exact set/M, validates canonical
   bytes and receipt, independently checks membership and retrieves selected
   provider versions. Complete success alone can produce passed evidence.
   Exact retry of a terminal attempt returns its immutable recorded outcome;
   it is not a new availability check. Rechecking live availability requires a
   new attempt, same F/M and different attempt-bound result digest. Interrupted
   attempts never turn a partial file list into success.
5. **Publish:** retain existing exact-passed-attempt and fence checks. M must
   already be immutable before Prepare becomes visible, not frozen only on
   Publish. Selection cannot swap manifests. Startup continues reading selected
   local evidence rather than contacting object providers. No new activation
   path is designed here.
6. **Retain:** initially retain manifest bytes for as long as the owning recovery
   set exists, including prepared, invalid and superseded sets. Existing recovery
   evidence mutation guards forbid deletion; add no expiry-based collector or
   DELETE privilege in this extension. Referenced provider versions and capture
   receipts need independently enforceable retention/holds; a PostgreSQL FK
   cannot keep an S3 version alive. Future cleanup requires a separately reviewed
   retirement procedure proving no selected set, live attempt, hold or retained
   audit reference needs the evidence. Set-specific copies avoid shared-digest
   deletion ambiguity. Off-host export/import and key availability are separate
   requirements, not guaranteed by this database storage choice.

### Compatibility and migration prerequisites

Keep old rows, serialized v1 documents, FAI-521 fixtures and hashes untouched.
V1 remains valid under its existing root-only guarantee; it never gains managed
coverage. All v2 paths require M, including independently proven empty scope.
Never catch a v2 missing-evidence error and retry as v1. A reader explicitly
supports known version pairs; unknown/mixed versions reject before publication.

Future migration order is: reviewed owner types/golden vectors; additive table
and deferred references; version-dispatched shape/hash/immutability guards;
repository/generated query updates and compatible readers; migration/conformance
proof; only then authorization to enable v2 creation. Recovery maintenance gets
bounded insert/read authority; runtime remains read-only, and no role receives
manifest update/delete. Keep existing v1 SQL validation branches unchanged.
Fresh-install schema and the normal Goose upgrade must produce identical
constraints/grants; `CREATE TABLE IF NOT EXISTS` alone is insufficient.

Test the migration on existing v1 prepared/published records and verify exact
hash preservation. Schema downgrade must fail safely once v2 data exists;
do not drop manifest evidence to make an old binary start. Compatible reader
rollout and selected-frontier behavior need a release compatibility decision
before enabling writers. This task creates neither DDL nor migration files.

### Qualification phases and gates

| Phase | Tests / evidence | Exit condition |
|---|---|---|
| 1: contract/golden | Fixed C/A/Closure/M/F/envelope vectors; shuffled arrays; duplicate keys/paths; shared files; empty scope; limits; wrong provider and stale anchor; v1 regression vectors | Go/SQL byte and hash rules agreed, no ambiguous identity |
| 2: persistence/restart | Real PostgreSQL; atomic create crash before/after commit; separate process reload; missing bytes/reference; exact terminal retry; canonical-byte corruption | Reload reproduces F/M and outcome without recapture |
| 3: concurrency/conflicts | Concurrent identical/different captures, same-ID replacement, direct SQL bypass, failed/partial attempts, retention privileges | Exactly one immutable set/manifest identity; no partial success |
| 4: provider replay | Real versioned MinIO writes, saved receipt/manifest, capture process exit, new attempt reads exact versions; missing/wrong version, bytes/size mismatch, credential repair, provider substitution | Complete closure passes; every mismatch fails with safe diagnostics |
| 5: admission compatibility | Existing Prepare/Validate/Publish path; passed attempt/F/M binding; v1/v2 matrix; migration upgrades; readiness consumes local selected evidence | Existing FAI-521 guarantees preserved; v2 cannot omit managed evidence |

These are future acceptance tests, not claims that the unimplemented contract
passes. Existing historical/closure qualification remains reusable; do not build
another discovery algorithm or admission validator.

### Open decisions requiring approval

- **Capture trust:** select the permitted authority and receipt verification
  mechanism, how it proves the selected control/provider boundary, and how
  receipt bytes survive restart. The proposed digest/reference alone cannot
  answer this; it blocks production acceptance and Phase 4's authority claims.
- **Scope:** approve the exact generation binding-set and retention-root source
  used to enumerate all required revisions, including retained non-serving
  revisions. Without this, completeness cannot be authoritative.
- **Canonical/profile limits:** approve the concrete field order, domains,
  timestamp precision, provider endpoint/account rules, 8-MiB/10,000-member
  limits, and proposed rejection of endpoint path prefixes before freezing goldens.
- **Retention/rollout:** approve initial no-deletion evidence retention and its
  capacity impact, provider version/receipt hold obligations, and v1-only reader
  behavior once v2 frontiers exist. No automatic TTL or binary downgrade promise.

Implementation planning is complete with these explicit gates. Next approve
them and begin Phase 1 only; PostgreSQL restore, PITR, coordinated recovery,
off-host rebuild and RPO/RTO remain later work.
