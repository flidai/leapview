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

## Exact-version S3 evidence adapter qualification

This section defines the qualification scope for the exact-version S3 evidence
adapter. The adapter is responsible for validating the configured provider
namespace, reading an already selected historical object, and returning only
bytes that match the declared transport size and raw SHA-256 identity. The
RecoverySet repository separately revalidates canonical and domain identity.
The adapter is a read-only evidence
transport boundary, not an uploader, capture owner, or recovery lifecycle
controller.

### Trusted construction and exact verification

Construction is the trusted profile/client boundary. A caller provisions the
S3 client and an explicit profile containing the endpoint, region, bucket,
namespace, and bounded read policy. The adapter validates that profile once and
always uses that client; an observation's endpoint, bucket, key, or credentials
cannot select a different client or route. Invalid or missing profile/client
inputs fail construction. The portable evidence reference contains no client,
credentials, or signed URL.

For every source or evidence object, the adapter must issue `HeadObject` and
`GetObject` with the supplied nonempty, non-`null` `VersionID`. It requires the
provider to echo that exact version, requires `ContentLength` to equal the
declared size, bounds the streamed response, and hashes the fetched bytes with
SHA-256. A version, size, or hash mismatch fails closed; latest-object lookup,
ETag, metadata, and a successful HEAD are not content proof. Version IDs are
observations supplied by the capture boundary, not values inferred by this
adapter.

### Intended qualification cases

The qualification uses a disposable versioned MinIO bucket and the frozen
RecoverySet v3 evidence bundle. It uploads all seven canonical payloads, captures
their provider VersionIDs, overwrites each current object, and creates a delete
marker before the repository reads anything. `CreateSet3` succeeds only by
using the selected historical versions. After the PostgreSQL connection is
closed and reopened, `ReadSet3` retrieves and verifies the same exact off-host
bytes again; identical create/read retries preserve the canonical set bytes.

The rejection matrix covers missing, `latest`, wrong, deleted, and corrupt
versions; denied credentials; and bucket, profile, and endpoint substitution.
Direct adapter calls reject profile substitutions before provider I/O.
Provider failures are
reduced to bounded categories and cannot expose provider messages, credentials,
signed URLs, or response bodies. Every failed repository submission leaves no
successor association.

### Explicit limitations

This adapter qualification uses MinIO and does not establish AWS or other
S3-compatible provider behavior. Its trusted endpoint identity is deliberately
separate from the disposable MinIO transport endpoint, so it does not qualify
TLS, redirect, or endpoint-discovery behavior. It makes no retention or lifecycle
guarantee. Write-time capture is qualified below, but durable association and
signed Manifest v2 creation remain separate prerequisites. It does not add
recovery admission, publication, or startup/readiness behavior. It does not
perform PostgreSQL restore or PITR, activation, or provider disaster recovery,
and it makes no physical DR, RPO, or RTO claim.

This validates historical managed-object retrieval. It does not prove successful physical disaster recovery.

## Write-time S3 VersionID capture

The managed-data S3 store now has an opt-in trusted observation profile. When
that profile is configured, a successful ordinary `PutObject` or multipart
`CompleteMultipartUpload` must return a nonempty exact `VersionID`. The store
retains that value directly from the write response, verifies the same exact
version by key, size, and SHA-256 bytes, and returns a provider-version
observation beside the immutable blob result. It never discovers or replaces
the captured identity with a later `HeadObject` of the latest object.

The observation binds the trusted profile identity, implementation, account,
endpoint, region, bucket and namespace to the exact key/version, content digest,
size, and UTC capture time. Construction rejects a profile whose bucket or
namespace differs from the store. Missing, `null`, or `latest` response versions
fail closed when capture is enabled; failed provider writes and failed exact-byte
verification return no observation. Provider failures remain sanitized by the
existing storage boundary.

`ProviderVersionObservationSet` is a bounded in-process handoff helper, not a
database or recovery manifest. It makes identical observation retries
idempotent, rejects replacement of the first observation for one profile/object
identity, rejects one profile identity resolving to conflicting trusted
configuration, and rejects conflicting version, digest, size, or capture metadata.
Distinct content-addressed object keys retain distinct observations. Qualification now takes managed object
VersionIDs from the production write result rather than inferring them with a
post-write latest-object HEAD. Ordinary and multipart response handling share
the same exact-version and integrity checks.

An exact retry may reuse an observation already returned by this writer and is
reverified at that exact version. When capture is enabled, finding an existing
object without such a prior write observation fails closed; the store does not
turn a latest-object HEAD into replacement evidence. Multipart reconciliation
likewise cannot convert an ambiguous preexisting completion into an observation.

This slice does not configure capture authority in production, create or sign
ManagedObservationManifest v2, enforce provider
retention, or connect evidence to admission, publication, startup, restore, PITR,
RPO, or RTO. Those remain explicit later gates.

This validates recovery evidence binding. It does not prove successful physical disaster recovery.

## Captured provider-version observation persistence

The managed-data PostgreSQL owner now provides the durable handoff between an
exact S3 write response and a future recovery capture. Trusted profile
configuration is inserted once by profile identity; each profile/object key then
accepts exactly one provider `VersionID`, SHA-256 digest, size, and canonical UTC
capture timestamp. The runtime capability may only select and insert these rows.
Update and delete paths are denied and owner-side triggers reject mutation;
maintenance, read-only, and backup capabilities can inspect but cannot insert or
rewrite observations.

The transaction starts only after S3 has completed and the storage adapter has
verified the returned exact version and bytes. It atomically inserts or compares
the profile and observation. Identical retries return the immutable stored row;
any profile, version, digest, size, key, or capture-time conflict fails without a
partial profile/observation pair. A reconnect reloads all fields and reruns the
storage contract validator, rather than trusting a cached verification flag.

There is deliberately no distributed transaction with S3. A provider write may
exist when PostgreSQL persistence fails, but that object is not a durable recovery
observation until this transaction commits. Retry must carry the exact observation
already returned by the write; latest-object discovery remains forbidden. Capture
generation and signing fences belong to the later authority/Manifest v2 phase and
are not fabricated at this write-fact boundary.

Durable observation storage does not itself create signed Manifest v2 evidence.

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
## Recovery evidence contract reconciliation

### Decision and baseline

This 2026-09-08 design decision reconciles main at `a97e07e35` (PR #538)
with the independently reviewed, unmerged FAI-520 signed-manifest prototype.
It supersedes earlier proposals on this page wherever they assign the signed
contract the already-used RecoverySet version 2 or observation manifest version 1.
It does not change code, historical evidence, or current admission capabilities.

Choose **Option A: preserve the merged contracts and version the successor**.
RecoverySet v1 and main's qualification-only RecoverySet v2 retain their exact
wire formats and digest algorithms. The single future integration direction is
a signed, source-anchored **RecoverySet v3**, referencing a **managed observation
manifest v2**. These successor numbers are a design reservation, not supported
reader/writer versions today. Do not deploy the local prototype's version-2
dispatcher or version-1 manifest as replacements for main's owners.

The successor combines trusted capture and independently retained evidence from
main with the prototype's explicit authority/profile, closure, receipt and
domain-separated commitment requirements. It is not permission to implement
admission, publication, startup or restore. The unmerged prototype remains an
isolated compatibility artifact until its successor wire vectors are reviewed.

### Conflicting models and information boundaries

| Concern | Merged main | Reviewed local prototype | Reconciled requirement |
|---|---|---|---|
| Recovery set | Existing roots plus nested `managed_evidence`: manifest, boundary and descriptor digests, and boundary document | Separate exact-field set, source-anchor digest, manifest version/digest and F2 commitment | Preserve both historical interpretations in their own readers; use a new v3 writer contract |
| Manifest | `Boundary`, `Inventory`, `Objects`; inventory binds revision IDs, paths, managed file hashes, storage keys and sizes | Capture/anchor/closure/revision membership, provider profiles and historical observations | Successor must retain revision/path membership and exact provider identities; neither representation is a lossless alias |
| Capture frontier | PostgreSQL database/system identity, timeline, LSN, restore-point name and inventory digest | Source anchor binds set identity, database/catalog/snapshot frontier, closure and profiles | Trusted anchor must include independently verified capture-boundary identity, not only a caller-supplied hash |
| Shared objects | Membership is revision ID/path; conflicts checked for the same endpoint/region/bucket/key/version | Per-revision membership; one authoritative version/content per provider object in a capture | Preserve all memberships; reject different selected versions for the same scoped object in the successor, even when main's older evidence can represent them |
| Trust | Opaque PostgreSQL capture adapter, exact provider replay, independent evidence store and retention checks | Detached signed receipt with pinned authority/profile verification | Require both capture/closure verification and signed authority; Object Lock is not a signature and a signature is not proof of provider bytes |
| Lifecycle | Prepared, off-host qualification evidence; SQL create remains v1-only | Planned PostgreSQL atomic association and immutable evidence retention | No activation guarantee follows from either format; persistence and admission remain separate gates |

Code anchors are `internal/recoveryset/frontier_v2.go`,
`internal/recoveryset/frontier_identity.go`, `internal/recoveryset/observation/`,
`internal/recoveryset/observationstore/`, and
`internal/recoveryset/postgres/observation_capture.go`. The unmerged comparison
uses `internal/manageddata/observation/` and
`internal/recoveryset/compatibility/` in the reviewed prototype worktree; those
packages are not dependencies present on this main baseline.

Do not infer catalog/snapshot consistency from the control database's WAL marker.
Do not translate a storage key into a provider profile without independently
approved endpoint, account, region, bucket and namespace rules. Do not collapse
revision/path membership when deduplicating byte retrieval. Missing scope, key,
profile or retention evidence blocks new authority rather than being backfilled.

### Cryptography and version dispatch

Main's frontier digest hashes its existing immutable normalized record projection
with lifecycle fields cleared. Its manifest hashes canonical manifest JSON with
plain SHA-256. The prototype instead uses domain-separated manifest hashing and
the `leapview/recovery-frontier/v2` five-field F2 projection. These are different
identities, not alternate encodings of the same hash. Preserve every existing
byte sequence, golden vector and digest under its original contract.

Future storage/lookups must qualify a digest by **contract family and version**;
a bare numeric version or digest does not identify an evidence format. Known
main v1/v2 inputs retain their existing owner behavior. Legacy prototype bytes
may only be inspected through an explicitly named prototype import boundary;
never guess their contract by trying multiple parsers until one accepts them.
Unknown formats, ambiguous input and unsupported versions fail closed. Old
readers must reject successors, not reinterpret them as v1/v2.

For the successor, preserve the non-circular construction:

```text
trusted database/catalog/snapshot frontier + closure + profiles
  -> source anchor A
  -> manifest M (includes A and receipt-core digest, not core bytes or signature)
  -> detached receipt signing M
  -> final frontier commitment (includes A and M)
```

Use successor-specific domains and explicit versions, not the prototype's F2
domain with silently changed payloads. Exact v3/manifest-v2 field order, domains,
receipt and validation-envelope/result version pairing and golden vectors must
be frozen together before SQL
implementation. Renumbering creates NEW bytes and NEW identities; retain old
prototype fixtures unchanged as provenance, not as successor goldens. Do not
automatically re-sign old captures or give them stronger guarantees.

An old evidence object can be wrapped as an opaque, typed reference without
changing its bytes/hash. Such a wrapper proves only that those bytes were
referenced. It cannot manufacture a trusted source anchor, signing authority or
complete capture scope. If explicitly retained as provenance for a new capture,
both old and new typed digests are recorded and independently checked; they are
never equated. Dual hashes are therefore provenance links, not required aliases
for every successor manifest. Reject a generic migration bridge or automatic
compatibility conversion: incomplete old evidence requires fresh authorized
capture and byte verification, not a synthetic manifest/receipt.

A reference-only frontier hash does not independently validate materialized
serving, delivery or object-root fields. Future consumers must rederive the
source anchor from those fields and independently trusted scope, then verify
manifest and receipt bindings through their owners. Structural validation or a
matching commitment alone must never authorize publication or readiness.

### Ownership and storage direction

| Asset | Authoritative owner | Persistence/retention responsibility |
|---|---|---|
| Closure inventory and revision membership | Managed-data owner, captured through the trusted database boundary adapter | Recovery retains the exact inventory/anchor evidence; storage cannot redefine membership |
| Provider observations and object mappings | Storage adapter verifies exact versions, size and actual bytes under approved profiles | Provider/operator protects historical source versions; recovery records and rechecks protection evidence |
| Canonical manifest, anchor and profile documents | Recovery contract owner composes and verifies existing domain-owner outputs | Recovery-owned PostgreSQL immutable records plus independently retained exact off-host copies |
| Receipt/signature | Approved capture authority signs; recovery verifies against independently configured keys/profiles | Preserve receipt bytes, verification metadata and public-key history; private keys/secrets remain outside evidence |
| Frontier association | Recovery owner | PostgreSQL atomically commits a typed manifest reference and frontier; no dangling reference or mutable replacement |
| Retention | Recovery owns reference/hold lifecycle; operator owns provider retention and key availability | Protect evidence and provider versions for the longest active recovery/hold obligation; no automatic deletion in the first slice |

PostgreSQL and the off-host store serve complementary purposes. PostgreSQL owns
local transactional consistency, not the only surviving copy of recovery
evidence. Off-host storage preserves exact evidence independently of the control
database; it does not select admission/publication/readiness state. Do not promise
a distributed transaction across PostgreSQL and S3. Future writes first verify
and durably retain off-host evidence, then atomically insert/associate its verified
local representation. A failed database transaction may leave an unreferenced
protected off-host object, never a committed dangling set. Retries must verify
identical typed bytes/receipt and reject conflicting associations. Cleanup of
unreferenced objects is a later, retention-aware operation, not compensating
deletion of possibly shared evidence.

### Compatibility, migration implications and implementation gates

1. Preserve v1 SQL rows, serialization, hashes, validation attempts and FAI-521
   evidence without backfills or new implied guarantees. Main's v2 remains
   qualification-only; storing evidence must not remove its activation guard.
2. Freeze the successor schema/domain/version matrix and cross-family rejection
   vectors first. Test main-v1, main-v2, isolated prototype and successor identities
   separately, including shared-object version divergence and empty verified scope.
3. Introduce reader capability before any successor writer. Keep production
   writers disabled until the supported fleet and migration boundary are qualified.
4. Design additive storage for typed immutable bytes, receipt/anchor/profile
   evidence and atomic references. Do not repurpose a v1/v2 column/hash or mutate
   an existing set's version. Rollback may disable new writers; it must not delete
   successor evidence or let an older reader serve an unsupported selected set.
5. Qualify exact retry, concurrent conflicts, database rollback, off-host retention,
   restart verification, lost-control-database evidence availability and key/profile
   failures before proposing admission integration. Retention truth must be checked
   against the provider, not inferred solely from a saved deadline.

The architecture collision is resolved by this direction, but persistence coding
is **not yet unconditionally ready**: successor byte-level contracts/goldens,
trusted capture deployment, authority rotation/revocation, retention horizons,
provider profiles, permissions and rollout/downgrade gates still need executable
specification or qualification. Revocation must distinguish historical inspection
from permission to establish new recovery authority. No endpoint substitution,
unsigned legacy import or unavailable off-host object may silently fall back to
latest data or weaker evidence.

The next bounded task is successor contract/golden reconciliation, reusing owner
validators and capture/replay mechanisms without duplicate validation logic. This
decision introduces no SQL, repositories, migrations or production behavior.

This validates recovery evidence binding. It does not prove successful physical disaster recovery.

## RecoverySet v3 / Manifest v2 frozen successor contract

This contract-only successor is isolated under `internal/recoveryset/successor`.
It does not replace the RecoverySet v1/v2 or observation-v1 readers, migrate old
evidence, or establish a production admission path. Canonical serialization and
signature verification are evidence checks, not physical restore operations.

### Manifest wire and membership

The RecoverySet v3 field order is `id`, `schema_version`, `cluster_points`,
`delivery`, `serving`, `catalog`, `object_roots`, `compatibility`,
`source_frontier_anchor_digest`, `managed_observation_manifest_version`,
`managed_observation_manifest_digest`, `frontier_digest`, `fence_epoch`,
`audit_identity`, `status`, `published_validation_attempt_id`, `created_by`,
`created_at`. Only `published_validation_attempt_id` is conditionally omitted;
the legacy status owner controls when it is required. These are contract fields,
not permission to produce a publication. The version pair is set 3 / manifest 2.
Database, delivery, seal, catalog and compatibility child fields reuse the exact
existing owner structs at this baseline; static golden bytes pin their field
order. Future changes to those owner fields require compatibility review before
altering successor serialization.

The frontier commitment hashes the ordered projection `schema_version`, `set_id`,
`source_frontier_anchor_digest`, `managed_observation_manifest_version`,
`managed_observation_manifest_digest`. It deliberately excludes lifecycle fields.
Full evidence verification must additionally compare the actual set roots with
the trusted anchor; a reference-only commitment cannot detect root substitution
without that comparison.

ManagedObservationManifest v2 has the following field order. All fields are
required; an absent value, JSON `null`, unknown field or aliased field name is
not an alternative encoding. There are no signature bytes or self/final-frontier
digests in the manifest.

| Document | Fields in canonical order | Meaning |
|---|---|---|
| Manifest | `manifest_version`, `set_id`, `source_frontier_anchor_digest`, `capture`, `managed_closure_digest`, `revisions` | Version 2; canonical recovery UUID; independent anchor A; capture core reference; independently verifiable membership commitment |
| Capture | `authority_id`, `capture_id`, `started_at`, `completed_at`, `receipt_digest` | Capturing authority/attempt, bounded capture interval, **receipt-core digest**, not detached receipt digest |
| Revision | `project_id`, `collection_id`, `revision_id`, `revision_manifest_digest`, `files` | Tenant-scoped membership; managed-data owner validates the file manifest identity |
| File | `path`, `sha256`, `size`, `provider` | Logical path, raw lowercase 64-hex content SHA-256, nonnegative integer bytes, exact historical observation |
| Provider | `implementation`, `account_identity`, `endpoint`, `region`, `bucket`, `prefix`, `key`, `version_id` | Explicit account/namespace, object location and immutable selected provider version; no credentials |

Revision order is the bytewise `(project_id, collection_id, revision_id)` tuple;
files sort by logical path. Duplicate revisions or paths reject rather than
being collapsed. Revision/file arrays are always explicit, including `[]`.
The closure commitment excludes provider observations and commits project,
collection, revision and managed-manifest identities plus path/hash/size; the
observation projection additionally commits provider identities. Neither alone
establishes trusted capture provenance.

All memberships of a shared scoped physical object must select identical
version, content hash and size. Different revisions may reference that same
tuple; retrieval may deduplicate the physical read only after checking agreement.
It must not remove logical memberships, ignore conflicts or select latest.
Provider prefix boundaries must match complete path segments, not string-prefix
lookalikes. Provider credentials and signing private keys are never wire fields.

### Ownership and compatibility guarantees

The source-anchor field order is `anchor_version`, `set_id`, `cluster_points`,
`delivery`, `serving`, `catalog`, `object_roots`, `compatibility`,
`managed_closure_digest`, `provider_profile_digest`. It excludes capture receipt,
manifest and final frontier digests. Existing recovery owners validate the
database identities, delivery/seal/catalog consistency, compatibility tuple and
two required roots. Cluster points sort by database role; roots by kind, URI and
version ID. Each root explicitly encodes `kind`, `uri`, `version_id`, `digest`,
`provider_recovery_frontier`, including a permitted empty local frontier.

Provider-profile documents encode `profile_version`, `profiles`. Profiles encode
`implementation`, `account_identity`, `endpoint`, `region`, `bucket`,
`version_semantics`, `namespaces`. Namespace entries encode `project_id`,
`collection_id`, `prefix`. Account identity is independently configured, never
inferred from endpoint or credentials. Profiles authorize specific logical
project/collection namespaces, not arbitrary objects sharing a bucket.

Existing recovery roots and managed inventory remain owned by their existing
contracts. Reusing an explicitly versioned nested owner document is not an
upgrade of that document's guarantees: the successor must additionally bind it
to the independently selected capture scope and verify the successor signature.
The new wire identity cannot be inferred from shape, nor selected by trying old
parsers until one succeeds. Historical local prototype bytes remain prototype
evidence and cannot acquire successor guarantees through renumbering.

An accepted receipt does not prove that an operator restored PostgreSQL or
retrieved the provider bytes. The trusted capture implementation must separately
establish the database/catalog boundary, authoritative complete membership and
exact historical byte observations. A verified empty scope requires independent
confirmation, not just an empty submitted array. Future persistence must retain
all canonical dependencies and receipts; a stored digest alone is insufficient.

### Receipt boundary

The successor version matrix is RecoverySet **3**, manifest **2**, source anchor
**2**, provider-profile set **2**, receipt core **2**, detached receipt **2** and
authority registry **2**. Supporting document identifiers are not reused from the
historical prototype. Version dispatch is explicit; other version combinations
are unsupported even if the JSON otherwise resembles a known document.

Canonical documents use UTF-8 compact JSON in declared field order, no trailing
newline, decimal integers and UTC timestamps with exactly six fractional digits
(`YYYY-MM-DDTHH:MM:SS.ffffffZ`). Typed builders may sort collections into canonical
order without mutating inputs; parsers require those exact canonical bytes.
String escaping follows Go `encoding/json`: quotes/backslashes are escaped,
`<`, `>` and `&` use lowercase Unicode escapes, and U+2028/U+2029 are escaped;
other accepted Unicode remains UTF-8. Integer consumers must preserve exact
signed 64-bit values rather than round through binary floating point. Existing
managed-data file/path/size limits apply in addition to successor document limits.
Alternate field order, whitespace, number spelling, duplicate keys, nulls and
case aliases are rejected on the persisted-wire boundary. SHA-256 identities
use `sha256:` plus 64 lowercase hexadecimal digits. Hash inputs are the exact
ASCII domain including its final newline followed by canonical document bytes:

| Commitment | Domain |
|---|---|
| Manifest | `leapview/managed-observations/v2\n` |
| Provider-free closure | `leapview/managed-closure/v2\n` |
| Provider-bearing revision projection | `leapview/managed-observation-projection/v2\n` |
| Source anchor | `leapview/recovery-source-anchor/v2\n` |
| Provider-profile set | `leapview/managed-provider-profiles/v2\n` |
| Receipt core | `leapview/managed-capture-core/v2\n` |
| Detached receipt | `leapview/managed-capture-receipt/v2\n` |
| Signature payload (not a replacement digest) | `leapview/managed-capture-signature/v2\n` |
| Frontier projection | `leapview/recovery-frontier/v3\n` |
| Authority registry | `leapview/authority-registry/v2\n` |

The receipt core field order is `core_version`, `authority_id`, `key_id`,
`capture_id`, `set_id`, `started_at`, `completed_at`,
`source_frontier_anchor_digest`, `managed_closure_digest`,
`provider_profile_digest`, `observation_projection_digest`, `membership_count`,
`object_count`, `result`. The sole successful result is `verified`; zero counts
are meaningful only with independently verified empty membership. The manifest
commits this core's digest before the final manifest digest is calculated.

The detached receipt fields are `receipt_version`, `core`, `manifest_digest`,
`signature`. Ed25519 signs the signature-domain bytes followed by canonical JSON
of `receipt_version`, `core`, `manifest_digest`, excluding the signature. Signature
and public-key bytes use padded standard Base64, with exact decoded lengths and
round-trip encoding checks. A registry supplied independently of the receipt
selects authority/key identity, algorithm, public key, validity interval,
revocation state and allowed provider-profile digests. Neither embedded claims
nor a valid signature may extend that allowlist.

Verification must join **all** commitments: set ID, trusted source anchor,
managed closure, profile digest, receipt core, manifest digest, observation
projection and counts. Validating each document in isolation is insufficient.
Expiry/revocation rejection must preserve a useful failure cause; historical
inspection never creates a new permission to recover.

Authority registry fields are `registry_version`, `keys`; keys encode
`authority_id`, `key_id`, `algorithm`, `public_key`, `not_before`, `not_after`,
`revoked`, `provider_profile_digests`. Key identity tuples sort by authority/key;
profile allowlists must already be sorted and unique. Revoked or unknown keys
reject. The complete capture interval must fall within `[not_before, not_after)`.
`ValidateEvidence` also requires an independently supplied verification clock and
rejects captures ending in its future. Ordinary key expiry after an authorized
capture does not rewrite historical cryptographic evidence; deciding whether
that history may establish new recovery authority belongs to future admission,
which this package does not implement.

Limits are 1 MiB for canonical set bytes, 8 MiB per supporting document and JSON
depth 16; at most 10,000 revisions/file memberships, profiles and total namespace
entries. Text fields are bounded to 4,096 bytes unless narrower rules apply;
endpoint length is 2,048 bytes and bucket length 3–63. This successor profile is
conservative: S3, explicit nonempty account identity, canonical HTTPS endpoint,
and `opaque-exact-version` semantics. Text must be NFC UTF-8 without controls;
non-NFC opaque keys/version IDs are **rejected, never rewritten**, and are outside
this contract's qualified scope. Version IDs remain case-sensitive and are never
inferred from ETags or latest. Equivalent endpoint spellings must arrive in the
one accepted canonical form; unsupported provider profiles require an explicit
future contract review.

Static fixtures under `internal/recoveryset/successor/testdata/frozen` pin the
minimal, shared-object closure and verified-empty documents, receipt signatures
and domain digests. Fixture files have one transport newline which is excluded
from the canonical bytes. Rejection vectors are under `testdata/rejected`.
Legacy recovery and observation vectors remain in
`internal/recoveryset/testdata/successor-legacy`; none are converted to successors.

Contract qualification commands (no provider operations):

```bash
go test ./internal/recoveryset/successor ./internal/recoveryset ./internal/recoveryset/observation -count=2
go test -race ./internal/recoveryset/successor ./internal/recoveryset ./internal/recoveryset/observation -count=2
go vet ./internal/recoveryset/successor ./internal/recoveryset ./internal/recoveryset/observation
go test ./internal/platform/architecture -count=1
task docs:generate
task docs:check
task generated:check
git diff --check
```

### Remaining integration gates

No successor SQL table, migration, repository, admission envelope, publication
attempt or startup callback is introduced by this slice. Existing FAI-521
validation-envelope/result bytes and selected-attempt behavior remain unchanged.
Before a future admission envelope/result is enabled, freeze its distinct version
pairing and rejection vectors; neither the old envelope nor a successful contract
test may be treated as successor admission evidence.

The next persistence review must cover immutable typed lookup, atomic references,
concurrent conflicts, restart verification and independent off-host retention.
Trusted capture deployment, key rotation/revocation, provider protection, schema
rollout and mixed-reader behavior remain separate operational gates. There is no
provider restore, PITR, key recovery, activation or RPO/RTO claim.

This validates recovery evidence binding. It does not prove successful physical disaster recovery.

## RecoverySet v3 persistence architecture

This is the design for the next bounded storage slice, not implemented storage.
It refines the earlier reconciliation's PostgreSQL/off-host split: PostgreSQL
owns immutable **references and associations**, while independently retained
off-host storage owns the canonical payloads. No change to the frozen successor
wire, hash domains, existing SQL, readers or recovery lifecycle is made here.
The baseline is `a97e07e35` plus the isolated successor contract freeze above.

### Ownership and retrieval boundary

```text
Trusted capture + independently selected roots/closure/profiles/authority
    -> canonical manifest, anchor, profiles, receipt and prepared set bytes
    -> protected off-host exact versions, read back and verified
    -> PostgreSQL transaction: typed references + complete set association
    -> restart: exact-version retrieval + owner verification
    -> future admission (not implemented or authorized by persistence)
```

| Owner | Durable responsibility | Must not imply |
|---|---|---|
| Recovery-owned PostgreSQL | Globally unique local recovery-set identity across supported versions; immutable typed evidence references; root/anchor/manifest/frontier associations; append-only verification observations and retention dependencies | That a row or cached success proves current object availability or readiness |
| Off-host evidence store | Exact canonical manifest, detached receipt, source-anchor, profile and prepared-set bytes; bounded supporting capture/verification payloads; independently protected exact object versions | That a storage key selects a recovery set, supplies trusted keys, or authorizes activation |
| Managed-data/capture owners | Authoritative revision membership, complete closure and coordinated source frontier | That submitted hashes alone establish capture truth |
| Provider/operator | Exact historical source-version availability and retention; evidence-store protection in a separate failure domain | That retained evidence bytes are retained managed-data bytes |
| Authority/key owner | Independently configured trust registry, rotation/revocation and public-key history | That an embedded public key or saved verification result remains trusted |

Reuse the ownership patterns in `internal/recoveryset/postgres/repository.go`
(complete creation, caller-owned transactions, exact conflicts),
`internal/recoveryset/observationstore/store.go` (pinned clients, exact versions,
byte checks and retention) and its descriptor/frontier helpers. These existing
implementations accept their own older formats; they are **not** successor
storage APIs and must not be repurposed by changing a version number.

Lookup identity is `(contract family, wire version, domain digest)`, never a bare
digest. Each immutable locator additionally pins the configured evidence-store
profile/account/endpoint identity, bucket, key, exact version, byte length and
raw transport SHA-256. Raw byte SHA-256 and the frozen domain digest are separate
checks with separate purposes; neither replaces the other. Storage routes use
an independently configured client, never a caller-supplied endpoint or URL.
No credentials, signed access URLs or private keys belong in these records.

Retrieval bounds the stream before allocation, hashes actual bytes, requires the
frozen canonical parser and recomputes the family-specific commitment. It then
joins set roots, anchor, closure, manifest, receipt and trusted profile/key
bindings through the existing successor verifier. No latest-version fallback,
parser probing, reserialization repair or unsigned replacement is permitted.
Canonical evidence can be inspected after restart; establishing trust also
requires independently loaded authority, scope and clock. A saved authority
snapshot is provenance, not a replacement trust source.

### Logical persistence entities (no SQL specification)

| Entity | Identity and immutable contents | Constraints and lifecycle |
|---|---|---|
| Recovery-set identity and v3 record | Set UUID, version 3, existing roots, anchor-v2 reference, manifest-v2 reference, F3 commitment, creator/audit/creation metadata and exact initial prepared-set payload reference | One identity across v1/v2/v3; no in-place version transition. First complete creation wins. Initial state is prepared with no published attempt |
| Evidence reference | Family/version/domain digest, canonical length and raw SHA-256, accepted exact-version locator, creation metadata | Unique typed digest; same identity with different bytes or locator conflicts. Immutable insert only; a conflict must never become an update |
| Manifest association | Set identity, manifest version/digest and matching anchor/closure/profile references | Exactly one manifest per v3 set, including independently verified empty scope; all referenced rows must exist in the committing transaction |
| Receipt reference | Detached receipt version/digest and locator, core digest, manifest digest, authority/key identity, provider-profile digest and signature metadata derived from canonical bytes | One accepted receipt selection for the set; mismatched or replacement receipt conflicts. Manifest `capture.receipt_digest` means **core** digest, not detached-receipt digest |
| Verification observation | Attempt identity, exact evidence tuple, verifier implementation identity, trusted registry/profile identity, clock, outcome and safe cause | Append-only; immutable historical result, never a mutable trusted boolean. Rechecks append observations; failures cannot rewrite a successful observation or association |
| Retention dependency | Set/hold identity, referenced evidence and provider-version identities, required protection horizon and observed protection | Hold acquisition/release is a separately fenced lifecycle, not mutation of canonical evidence. No GC in the first implementation |

PostgreSQL may store derived scalar fields for indexed lookup, but readback must
compare them with canonical payloads; JSONB reserialization is not canonical
byte storage. Avoid a second inventory copy as independent truth. The manifest
and protected source-capture dependencies retain complete membership. The first
slice requires one accepted locator per typed object; replica addition/relocation
is a later verified, append-only locator operation, never reference replacement.

Use distinct storage names for M (manifest digest), C (receipt-core digest) and
R (detached-receipt digest); verify `capture.receipt_digest == C` and the detached
receipt's manifest reference equals M. A generic receipt-digest column would hide
this distinction. Because the manifest binds set identity, its association cannot
be reused for a different set; content retrieval may still deduplicate shared
source objects without deduplicating logical membership.

F3 commits the frozen five-field frontier projection, **not the full record**.
It cannot be used alone for exact-retry comparisons: compare all immutable set
fields and exact initial payload bytes as well as every selected evidence
reference. An exact prepared-set payload is retained off-host for reconstruction;
future lifecycle changes cannot overwrite it. Reconstruction from that payload
does not recreate publication authority lost with PostgreSQL.

Only state, fence and selected validation-attempt identity are lifecycle-managed
in a future integration, using owner-authorized conditional transitions with
history. This storage slice exposes no such transition or publication operation.
Creator, roots, version, anchor, manifest, receipt selection and F3 never change.
New conflicting evidence requires a new authorized recovery identity/capture,
not edits to an existing set or invented retry metadata.

### Create, retry and transaction protocol

1. Resolve independent source scope, configured evidence location, authority and
   verification time. Validate all canonical documents and their cross-bindings
   through the frozen owners. Require the source versions' protection/availability
   evidence separately; a signature is not a physical replay check.
2. Establish a protected off-host upload intent/hold, then conditionally create
   each typed content-addressed payload. Read back the exact versions, verify
   bytes and provider-side retention through the required horizon. A preexisting
   object is reusable only after identical-byte/version/protection verification.
   An unsupported conditional-write or retention provider blocks this protocol.
3. Enter one short PostgreSQL transaction **after** network operations. Pin the
   local trust-policy generation and retention intent, and insert/compare evidence
   references, receipt binding, verification observation, complete set roots and
   associations. Unique keys, referential constraints and immutable-write guards
   enforce the model; no partially populated v3 set may commit. A changed local
   authority/profile generation requires fresh verification rather than accepting
   the stale pre-transaction decision.
   These invariants apply to direct SQL too, not only repository pre-read checks.
4. Commit before reporting a persisted set. A storage result means only durable
   association, not admission/publication/readiness. Provider immutability and the
   protected intent cover the interval between external verification and commit;
   the database transaction itself cannot prevent provider loss or revocation.

The implementation must define a bounded operation deadline, a minimum retention
margin exceeding that deadline, and an explicit required recovery/hold horizon.
Reject if the remaining protection is too short at commit. Do not hold SQL locks
while retrieving objects. Later consumers recheck external availability,
protection and current authorization; verification is not a perpetual guarantee.

| Situation | Required result |
|---|---|
| Identical retry after success or an unknown commit result | Resolve the same set identity; compare complete immutable tuple and bytes, then return the existing association without new selections or duplicate attempt results. Required external/trust rechecks may fail availability without undoing historical persistence |
| Conflicting retry | Stable conflict; no replacing bytes, receipt, locator, roots, metadata or manifest reference, even if F3 happens to match |
| Failure before SQL commit | Roll back every local insert/association. Protected off-host objects may remain; do not compensate by deleting potentially shared evidence |
| Process restart during upload or commit | Recover the same operation/set identity; inspect committed state and exact protected payloads, then resume verification or return the existing result. Never infer success from object presence alone |
| Two identical writers | One immutable insertion, both may succeed after exact comparison. Concurrent readers see either no committed set or its complete graph |
| Two conflicting writers | First successful PostgreSQL commit wins; the other receives a conflict. Winner scheduling is not predetermined; the conflict rule and surviving evidence are deterministic |
| Deadlock/serialization failure | Bounded retry of the whole transaction in stable typed-key order; preserve operation identity and never relax comparisons |

There is no distributed SQL/S3 transaction. Local foreign keys prevent dangling
**local references**, not disappearance of remote bytes. The off-host intent must
be discoverable independently of PostgreSQL for eventual orphan reconciliation;
losing local rows must never be interpreted as permission to delete their payloads.
The intent/locator operational representation still needs a reviewed successor
storage contract before implementation; this document does not invent a new
signed evidence envelope or modify the frozen one.
That review must pin operation/set identity, typed evidence identities, expected
locators, retention horizon and policy generation, append-only intent states,
idempotent retries and independent orphan reconciliation. It must not reuse the
older evidence store's set-ID object as PostgreSQL association authority.

### Retention and failure semantics

Recovery owns dependencies across prepared/published/retained sets and explicit
holds. Provider/operator protection must cover the maximum outstanding obligation
for manifest, receipt, anchor, profiles, capture dependencies and **source object
versions**. Snapshot retention is another independent dependency: its presence
does not protect S3 bytes. Authority/profile history and independently recoverable
configuration must also survive for inspection; private-key recovery is separate.
The required horizon is derived from active set, attempt, hold and audit policy,
not an arbitrary caller-supplied deadline. Record that policy/hold generation and
the provider verification time; checks prove protection only at that instant.

Expiry never changes immutable evidence. Expired/unknown protection, key revocation
or missing source bytes blocks new trusted use, with a fresh diagnostic, rather
than rewriting previous verification results. Renewal must happen before expiry
and cannot shorten another set's hold. Future deletion requires a fenced scan of
all references/holds plus off-host intent reconciliation and provider permission;
invalid/superseded status alone is not deletion authority. No automated cleanup,
retention bypass or GC is part of the first persistence slice.

These are required distinguishable failure classes, not new runtime error codes:

| Failure | Result and retry condition |
|---|---|
| Missing evidence object / backend unavailable | No new association or trusted read; preserve provider cause. Retry exact locator after repair, never latest |
| Transport hash, domain digest or canonical-byte mismatch | Integrity rejection; no cache repair, object substitution or overwrite |
| Invalid signature / unknown or revoked authority / disallowed profile | Trust rejection with safe owner cause; saved success cannot bypass it |
| Stale anchor, scope or trust-policy generation | Binding rejection; reselect trusted inputs and verify. Do not change the existing evidence to fit |
| Missing historical source version / insufficient retention | Explicit availability/protection rejection, even when evidence payloads remain intact |
| Unsupported family/version | Fail closed without trying another historical parser |
| Conflicting evidence or association | Immutable conflict; exact historical winner remains unchanged |

Diagnostics identify the failed family/version, safe digest and stage; credentials,
signed URLs and unrestricted provider response bodies must not be logged.

### Reader-first rollout and implementation prerequisites

1. **Schema/read capability:** design additive recovery-owned storage and access
   policy, preserving v1 rows, owner serialization, hashes and FAI-521 attempts.
   Main's qualification v2 keeps its guards. Introduce explicit successor read
   capability before enabling writes; old binaries must reject unsupported sets.
2. **Evidence storage availability:** qualify configured exact-version clients,
   protected upload intents, least-privilege credentials, canonical retrieval,
   off-host reconstruction and retention in the chosen provider/failure domain.
3. **Validation support:** connect persistence reads to existing successor owners
   and independently selected scope/clock/trust. Qualify corrupt SQL metadata,
   missing objects, replay, revocation and transaction rollback without admission.
4. **Writer enablement:** qualify fresh/upgrade databases, v1 coexistence, immutable
   retries, competing writers and crashes around every upload/commit boundary.
   Enable only with compatible readers and reviewed operational limits.
5. **Admission adoption (separate approval):** freeze distinct successor admission
   envelope/result versions and qualify publication/startup selection before any
   successor record can affect readiness. Persistence alone cannot open this gate.

No backfill, automatic conversion, re-signing or in-place version mutation is
permitted. Downgrade disables successor writers and preserves all v3 evidence;
destructive down migration must refuse while evidence exists. An older application
may run only when its required selected state is supported, never by silently
falling back to a different set. Off-host payloads are not a rollback mechanism
for the database schema or a substitute for publication history.

The architecture is specified, but coding still requires review of the additive
schema/permissions and operational locator/intent contract, provider retention
capabilities and horizons, trust-policy generation fencing, trusted capture/key
deployment and resource limits. Preserve the existing 1 MiB set / 8 MiB document
bounds and bounded parallel retrieval. No provider restore or RPO/RTO claim follows.
The prior full CI run was blocked by MinIO HTTP 507 `XMinioStorageFull`; use a
runner with sufficient free storage for future persistence qualification. This
documentation-only step runs documentation/generated/diff checks, not recovery CI.

This validates recovery evidence binding. It does not prove successful physical disaster recovery.

## RecoverySet v3 capture and transport contract

This design freezes the operational boundary left open by the persistence
architecture. It adds no implementation or signed evidence format. The frozen
RecoverySet 3, Manifest 2 and receipt/profile/anchor version-2 bytes and domains
remain unchanged. Transport metadata is not a recovery admission envelope.

### Lifecycle and authority boundaries

| Stage | Owner and trusted inputs | Required output and failure boundary |
|---|---|---|
| Capture request | Recovery coordinator authenticates an authorized recovery operator/service; independently resolves set scope, source frontier, complete managed membership and approved policy generation | Immutable request/capture identity and scope; missing authorization, scope or fresh policy rejects before capture/upload |
| Evidence generation | Domain capture owners establish database/catalog/snapshot and managed closure facts; configured storage worker verifies exact source versions and bytes | Recovery contract owner constructs anchor and manifest; approved capture authority signs the detached receipt only after complete verification. Partial capture cannot claim `verified` |
| Payload upload | Coordinator authorizes bounded upload intents; pinned storage worker has create/read access only in the approved evidence namespace | Protected conditional-create payloads and exact provider-version results; upload success alone does not qualify a reference |
| Verification | Recovery verifier independently resolves scope, clock, current authority/profile generation and pinned storage clients | Exact readback, size/raw hash/canonical/domain checks, complete receipt bindings and source/evidence retention checks; any missing dependency rejects the complete set |
| PostgreSQL association | Recovery persistence owner uses current generation and worker fence in one short transaction | Complete immutable association and verification observation commit together; stale/conflicting/partial inputs cannot commit |
| Recovery availability | Recovery evidence consumer reloads the associated graph and rechecks current trust, exact versions and protection | A time-bounded availability observation, not publication/readiness or proof of physical recovery. Loss/revocation appends failure, never rewrites historical bytes |

The capture request fixes the set UUID, capture UUID, independently selected
source scope, requester identity and operation deadline. Its immutable identity
cannot be reused for a different frontier. No capture worker may infer scope from
submitted manifests, substitute a provider account or assert empty scope solely
because an array is empty. Evidence generation completes locally before payload
intents are issued, so every upload has known canonical bytes, digest and size.
Existing per-document bounds apply; no unbounded streaming capture exemption.

### Upload intent: authorization, not bearer authority

Define the operational family `leapview.recovery-upload-intent`, version **1**.
It is distinct from every historical manifest/receipt family. One immutable
intent covers one canonical payload, and all intents for a set are bound to the
same capture request. The ordered fields are:

| Field | Required value and rule |
|---|---|
| `kind`, `version` | Exact family above and integer 1; no alternative parser/version fallback |
| `intent_id`, `request_id`, `set_id`, `capture_id` | Canonical UUIDs; allocated by coordinator, not storage worker |
| `authorized_by` | Authenticated coordinator service identity; a claim to compare with authoritative authorization, not proof by itself |
| `trust_generation`, `worker_fence` | Generation tuple below; positive coordinator-issued integer worker fence |
| `payload_family`, `payload_version`, `payload_digest` | Explicit frozen evidence family/version and its domain commitment; only an allowlisted supported combination |
| `payload_sha256`, `payload_size` | Raw lowercase 64-hex SHA-256 of exact canonical bytes; positive integer size within family bounds. F3 is not this full-payload hash |
| `target` | Immutable destination target defined below; no unknown version placeholder |
| `issued_at`, `deadline`, `retain_until` | UTC microsecond timestamps; issued < deadline < retain-until, with policy-required retention margin/horizon |

The coordinator authorizes only already-verified payloads whose set/capture and
M/C/R/A/F3 bindings agree. Authority is an authenticated control-plane decision
against the current generation, not a new signature added to the manifest.
Persist a protected off-host copy of the intent before writing its payload for
orphan accounting, but possession of that copy grants no write or association
permission. Live authorization/fence must be checked at each boundary. After
loss of authoritative control state, historical intent copies are audit inputs;
they cannot self-authorize a resumed writer. Reauthorization requires the trusted
recovery procedure, outside this slice.

Intent fields never change. Exact retry reuses the same identity and bytes; a
changed digest, size, destination, scope, generation or deadline requires a new
authorized intent, never an update. Reauthorization may reuse verified immutable
payloads only after full checks under the new generation. It cannot silently
re-sign an old capture or extend a receipt's authority. A worker takeover gets
a higher fence and new intent; old workers cannot finalize with the new fence.

Operational outcomes are append-only observations keyed by intent plus attempt:
`authorized`, `uploaded`, `verified`, `associated`, or a terminal `failed`,
`expired`, `revoked`. Retries are separate observations, not overwritten results.
Only PostgreSQL commit establishes `associated`; an off-host completion copy is
a repairable projection and cannot select a recovery state. No payload/upload
event bypasses full-set verification. Deadline expiry forbids new writes,
verification acceptance and association; it does not delete retained evidence.

### Destination target and exact locator

Target fields, in order: `backend`, `storage_profile_id`,
`storage_profile_revision`, `account_identity`, `endpoint`, `region`, `bucket`,
`namespace`, `key`. Backend is `s3`; profile ID is a configured canonical UUID;
revision is a positive immutable configuration revision. The profile registry
independently pins this entire tuple, allowed evidence families and required
conditional-create/retention capabilities. It is the **evidence destination**
profile, not the source-provider profile signed in the capture receipt.

The first transport profile supports canonical HTTPS DNS endpoints only: lowercase
host, no userinfo, path, query, fragment or explicit default port; approved
non-default ports remain explicit. Account/region/bucket/namespace must match
configuration exactly. Namespace is a relative sequence of nonempty segments,
without leading/trailing slash, dot segments, backslash or percent escapes.
Target keys are derived, not supplied arbitrarily:
`<namespace>/<payload-family-token>/v<version>/sha256/<payload-sha256>`.
The family-token mapping is a fixed allowlist for the frozen successor document
families, not a caller string inserted into a path. No URL decoding, path cleanup
or redirect may change the target; unsupported spelling rejects.

| `payload_family` | `payload_version` | Path token | Commitment owner |
|---|---|---|---|
| `leapview.recovery-set` | 3 | `recovery-set` | Frozen F3 projection; raw SHA-256 additionally binds full prepared-set bytes |
| `leapview.managed-observation-manifest` | 2 | `managed-observation-manifest` | Manifest domain |
| `leapview.recovery-source-anchor` | 2 | `recovery-source-anchor` | Source-anchor domain |
| `leapview.managed-provider-profiles` | 2 | `managed-provider-profiles` | Provider-profile domain |
| `leapview.managed-capture-core` | 2 | `managed-capture-core` | Receipt-core domain |
| `leapview.managed-capture-receipt` | 2 | `managed-capture-receipt` | Detached-receipt domain |
| `leapview.authority-registry` | 2 | `authority-registry` | Authority-registry domain; archived provenance only |

The intent itself is stored in a separate coordinator-owned protected namespace,
not as a recursively self-authorizing payload in this allowlist. Provider retention
of this operational record is required, but its location is not authority.
The independently deployed destination profile pins an `intent_namespace` and
the deterministic key `<intent_namespace>/recovery-upload-intent/v1/<incarnation-id>/<intent-id>`.
The coordinator conditionally writes the exact canonical intent bytes there and
verifies their raw SHA-256, size, returned provider version and retention through
at least `retain_until` before authorizing payload writes. No intent authorizes
its own creation: that write is the authenticated coordinator operation under the
current policy. Its resulting exact-version reference follows the locator rules
above except that it identifies the operational intent family and raw hash, not
a frozen evidence-domain commitment; it is not accepted as a payload locator.

Orphan reconciliation uses this separately configured profile/namespace and
provider version enumeration, not only SQL indexes. Discovery/listing is untrusted:
enumerate explicit candidate versions, retrieve each by exact version, check
canonical fields, key/intent/incarnation bindings, raw bytes and protection, and
quarantine conflicting versions for one intent identity. A raw hash of discovered
bytes proves no authorization. Without surviving authoritative authorization,
preserve the objects as untrusted audit material; neither resume upload nor infer
permission to associate/delete. The independent configuration and enumeration
permissions must be recoverable without the control database. No mutable latest
index or self-referential intent hash is required.

An accepted locator is family `leapview.recovery-evidence-locator`, version **1**,
with ordered fields `kind`, `version`, `target`, `version_id`, `payload_family`,
`payload_version`, `payload_digest`, `payload_sha256`, `payload_size`.
`version_id` is required, nonempty, case-sensitive opaque canonical UTF-8 text;
empty, `null` (case-insensitive), sentinel `latest` and missing versions reject.
Do not parse it as an ETag or rewrite it. The version is unknown before creation:
the intent has a target, never a fabricated pre-upload locator. Only exact-version
readback can turn a write result into an accepted locator.

Both operational documents use compact UTF-8 JSON, exact field order, no unknown
or omitted fields, canonical integers and the frozen UTC-microsecond/escaping/NFC
rules. `trust_generation` encodes `incarnation_id`, `revision`, `policy_digest`
in that order. Canonical content comparison, not tolerant JSON parsing, defines
an identical retry. There is no new signature or replacement recovery hash for
these transport documents. Their immutability comes from protected storage and
authoritative control-plane binding, never from their self-declared fields.

Limits: 16 KiB per intent/locator, 4,096 bytes per opaque text field, 2,048-byte
endpoint, 1,024-byte derived S3 key, standard 3–63-byte bucket; tighter configured
bounds win. Long namespace/family combinations reject rather than truncate.
No embedded secrets, presigned URLs or arbitrary redirects are accepted.

Payload writes use conditional create and provider-native immutable retention;
no overwrite/delete/retention-shortening permission is granted to the uploader.
An existing key is resolved through the pinned client, its exact version bound,
then all bytes/protection checks run. Different bytes or unavailable protection
are conflicts, not permission to upload a newer replacement. Once accepted, a
locator is never refreshed to latest, even for identical content. Duplicate
writers may converge only on the same verified exact locator; a different
version is not an identical retry. Backend capability qualification must prove
conditional-create behavior under concurrent writers.

### Capture authority, delegation and generation fence

Domain owners supply frontier/closure facts; the recovery coordinator constructs
the source anchor and manifest using those facts. The registered capture signer
alone creates the detached receipt, using its pinned authority/key identity and
source-profile allowlist. The coordinator alone authorizes upload intents under
the separate destination profile. Storage workers cannot sign captures, choose
keys/profiles, expand namespaces or grant recovery authority.

Workers authenticate using deployment-managed service identities and a bounded
assignment `(request, intent, worker identity, fence, generation, deadline)`.
Delegation is limited to executing that assignment; no transitive delegation or
possession-based bearer upload authority. The signer rechecks authorization before
signing. Signing credentials stay at the approved signer/key service, not in
worker payloads, manifests or locators. Infrastructure administrators control
deployment credentials; runtime workers cannot edit the authority registry.

The authoritative trust-generation tuple is `(incarnation UUID, monotonic positive
revision, immutable policy digest)`. Its policy binds approved capture authorities,
revocation/validity, source profiles, destination profiles, delegation and retention
rules. It is operational control metadata, **not an added receipt/manifest field**.
Any relevant policy change advances revision; no timestamp-only or process-local
counter. Revisions are never reused within an incarnation. Restoration of older
control state cannot roll back trust: a fresh incarnation and externally trusted
policy reconciliation are required before enabling workers. If freshness cannot
be established, capture/association stays blocked.

At request authorization, before signing, before each upload, at readback
acceptance and at SQL association, compare the assignment generation/fence with
authoritative current state and check deadline using a trusted clock. Any mismatch
is stale, even if the old receipt signature remains mathematically valid. The
verification observation records the exact generation, worker fence and clock.
Capture-time key validity remains governed by the frozen receipt contract; new
trusted use also applies current revocation and policy. Historical inspection
never creates current authority.

Association and local policy-generation changes must serialize on the same
authoritative fence inside PostgreSQL: lock/check current generation and worker
assignment through commit; policy update/revocation takes the conflicting fence.
A pre-transaction check alone is insufficient. All references and verification
metadata commit under that generation, with no network work under the lock.
If association wins first, it remains a historical stored association; a later
revocation still blocks new trusted use. If the policy change wins first, the
stale association rejects. Worker-takeover fencing follows the same rule.

This ordering is relative to the committed policy authority, not an instantaneous
guarantee about an external revocation notification not yet ingested. Deployment
must bound policy freshness and fail closed when its required feed is unavailable
or stale. In-flight object writes cannot be atomically revoked with SQL: a stale
worker may leave a protected unreferenced object, but cannot have it accepted or
associated. Short-lived credentials and restricted writes reduce that exposure;
no cross-store fencing/rollback guarantee is claimed.

### Deterministic failures and replay

| Condition | Required outcome |
|---|---|
| Interrupted/expired upload | No verified locator or association; record stage/cause. Retry exact intent only while authorized and before deadline; otherwise reauthorize with a new intent |
| Existing target contains wrong bytes | Integrity/conflict rejection, never overwrite or choose latest |
| Locator missing/unavailable or substituted profile/account/namespace/version | Availability or identity rejection; pinned exact lookup only. Network repair may allow revalidation, not reference mutation |
| Invalid signature, unsupported family/version or malformed canonical payload | Owner-verification rejection; no parser fallback or normalization repair |
| Revoked authority, disallowed profile or stale generation/fence | Current-trust rejection; old success/signature cannot bypass the fence |
| Historical source version disappeared or protection expired | Recovery availability fails even if manifest and receipt are intact; no physical-recovery claim |
| Lost SQL commit response | Read the authoritative set association first; exact compare and revalidate before reporting current availability. Object presence alone is not success |

Failure observations expose stable stage/category and safe typed identities, not
credentials or raw provider bodies. Missing any payload in the required set
prevents association; a partially successful upload batch has no recovery status.
Only provider-side exact-version byte verification establishes integrity;
metadata, location, Object Lock and valid signatures each prove different things.

### Freeze boundary and next implementation gates

The lifecycle, target-versus-locator distinction, immutable intent, delegated
authority and serialized generation fence above are the selected design. Future
contract tests must pin operational document bytes, profile canonicalization,
concurrent conditional writes, revoked/stale workers, lost commit responses,
deadline races and control-state rollback. The family-token allowlist must cover
only reviewed frozen payload families; unsupported large/supporting formats need
their own contract decision, not arbitrary uploads.

Before persistence implementation, review additive schema/permissions and the
transactional policy/assignment authority, provision signer/service identities,
approve immutable destination-profile revisions and retention horizons, and
qualify the provider's exact-version/conditional-create/protection capabilities.
Define deployment-specific maximum policy staleness and operation deadlines;
absence of approved limits blocks enablement rather than selecting permissive
defaults. No transport proof enables successor admission, publication or startup.
Existing v1/v2/FAI-521 bytes and guarantees remain unchanged.

This validates recovery evidence binding. It does not prove successful physical disaster recovery.

## RecoverySet v3 persistence implementation

The first storage foundation is isolated from the existing admission,
publication and startup consumers. It does not enable a v3 writer in application
composition. The successor contract owner verifies evidence; the PostgreSQL
adapter owns durable associations, not a second cryptographic validator.

### Storage and transaction boundary

The additive migration retains existing recovery tables and their serialized
records. Separate successor tables hold content-addressed supporting payloads,
exact-version locators, immutable manifest bindings, prepared v3 sets and their
two existing roots. PostgreSQL canonical bytes are an integrity comparison
cache, not a replacement for the off-host evidence store. A shared identity
registry prevents reusing a v1 identity for a different v3 record; it does not
convert the original record or recalculate its hashes.

Before insertion, a configured exact-version reader fetches the selected
off-host payloads. The adapter checks bounded size, raw SHA-256, canonical owner
encoding, domain-separated identity and the full owner evidence contract.
Authority, expected source scope, profile and verification clock come from an
independently configured trust resolver, never from request-supplied approval or
stored verification metadata. Private keys and provider credentials are not
persisted in these records.

One explicit PostgreSQL transaction checks the provisioned trust generation and
inserts or exactly compares the evidence graph and association. No provider I/O
occurs while holding its generation lock. Immutable constraints and foreign
keys protect references; the complete association either commits or rolls back.
An exact retry does not replace the winner. A different locator, receipt,
canonical payload or set record is a conflict, not an update. Creation and
verification metadata describe the original insertion rather than a mutable
last-success cache.

Every successful lookup re-fetches exact payload versions, compares stored
bytes and identities, and repeats owner verification against independently
selected trust. Missing off-host evidence or revoked trust cannot be repaired
by a successful past SQL record. Concurrent policy changes must serialize with
association on the owner-provisioned generation; production policy provisioning
and worker assignment remain deployment prerequisites, not implicit defaults.

### Migration, qualification and limits

The schema is additive and has no v3 backfill. Existing v1 hashes, wire bytes,
validators and FAI-521 guarantees remain unchanged. Downgrade must refuse rather
than delete successor evidence. Maintenance receives bounded persistence
permissions; normal application code is not granted a successor mutation path.
Canonical policy reconciliation must preserve those permissions on reapply.

The persistence qualification uses real PostgreSQL migrations and maintenance
connections with frozen signed contract vectors. Its exact-version byte reader
models already-uploaded evidence; it is not a new provider implementation or a
provider availability drill. The matrix covers restart through a separate
connection, exact retries, concurrent writers/readers, failed association
rollback, missing/tampered payloads and independent trust rejection.

Run the focused qualification with Docker available and PostgreSQL tests made
mandatory:

```sh
LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1 go test ./internal/recoveryset/postgres -run '^TestSuccessor' -count=2
go test ./internal/recoveryset/successor ./internal/recoveryset -count=1
```

No garbage collector is added. Database references cannot silently lose their
evidence, but provider versions need independent retention protection. Trusted
capture deployment, upload-intent execution, destination-profile provisioning,
worker/deadline fencing, policy freshness and provider retention qualification
remain required before production enablement. Admission, publication, startup,
physical restore, PITR and RPO/RTO measurement are outside this foundation.

This validates recovery evidence binding. It does not prove successful physical disaster recovery.

## Manifest v2 capture and signing qualification

This bounded qualification composes durable provider observations, a complete
managed closure, a source anchor, the canonical ManagedObservationManifest v2,
a detached signer receipt, and the existing RecoverySet v3 persistence path.
It qualifies evidence assembly and verification only; it does not change the
existing admission, publication, startup, restore, or recovery contracts.

### Evidence flow

Durable managed-data observations begin with a successful S3 write response.
The exact nonempty VersionID, profile identity, object key, size, SHA-256, and
UTC capture time are persisted in the managed-data PostgreSQL owner. Capture
reads the authoritative revision projection and resolves each file through an
explicit provider-profile binding. It then reads the stored observation and
replays that exact provider version, checking profile/account/endpoint/region/
bucket/prefix, returned VersionID, size, and fetched-byte digest. Latest-object
lookup, ETags, and post-write discovery cannot supply evidence.

The qualification source is intentionally instance-global: it captures every
ready managed revision in the control database under a PostgreSQL lock. An
independent policy resolver must authorize that complete provider-free
revision membership, its closure digest, the recovery-set identity, provider
profiles, authority registry, trust generation, worker fence, and deadline.
This prevents the capture worker from selecting its own scope or trust roots.

The verified revision/path membership is sorted into the provider-free managed
closure and its domain-separated digest. The capture service combines that
digest and the independently configured provider-profile digest with the
normalized existing recovery frontier to build and hash the source anchor.
The anchor digest is necessarily bound after PostgreSQL supplies the capture
restore point and WAL position; the resolver authorizes membership before that
capture rather than attempting to predict those source-owned values.
Manifest v2 then emits strict canonical bytes containing the set and anchor
identities, capture interval, closure, complete revision/file membership, and
the exact provider observations. Its capture record commits to the receipt
core digest. The final manifest digest is computed only after that core is
fixed; a detached signer receives the domain-separated receipt payload and
returns the Ed25519 signature. A trusted clock supplies verification time, and
the service re-resolves the independently owned policy immediately before
signing; a changed/revoked generation, fence, deadline, scope, profile, or key
fails closed. The authority registry checks authority/key identity, algorithm,
validity interval, revocation, and allowed provider-profile digest before the
evidence graph is accepted. Private keys remain behind the signer callback and
are not stored in capture results or recovery evidence.

The existing RecoverySet v3 persistence adapter fetches every canonical
payload by its configured exact-version locator, repeats canonical, domain,
profile, closure, anchor, manifest, receipt, and authority checks, and then
atomically stores the immutable evidence graph and prepared set association in
PostgreSQL. The transaction performs no provider I/O. A separate connection
can read back the same exact payloads and reverify them under independently
resolved trust.

### Managed-capture-core persistence boundary

This slice extends the `CreateSet3` bundle with the separately located,
canonical capture-core entry described by the frozen transport design. The
core remains embedded in the detached receipt for signature verification, but
the transport persistence path now requires the independent
`leapview.managed-capture-core` version-2 bytes and frozen domain digest. New
associations retain the core locator and verify its exact bytes and relation to
the receipt and manifest. The association verification metadata records the
resolved worker fence, and the same transaction locks the authoritative trust
generation row and rejects a changed generation or fence before inserting any
evidence. An explicit persistence marker distinguishes those
associations from the legacy embedded-only form. Historical v3 rows and
in-flight older writers remain compatible without backfill; they retain the
original embedded receipt-core behavior, are not presented as independently
transported core payloads, and cannot be silently upgraded by retry.

### Deterministic and failure behavior

Identical already-captured source facts and construction inputs produce
identical canonical documents and frontier commitment; collection ordering is
normalized without mutating the source. Exact retries of those saved documents
through `CreateSet3` are idempotent. A fresh PostgreSQL capture creates a new
restore point and must use a new set/capture identity; it is not a retry.
Missing observations,
incomplete closure, profile or namespace substitution, wrong or unavailable
versions, size/digest mismatches, stale capture boundaries, malformed
canonical bytes, invalid or revoked authority, signer errors, and signature or
binding mismatches fail closed with bounded categories. No latest fallback,
partial success, conflicting replacement, or dangling RecoverySet v3
association is accepted.

### Limitations

This qualification does not establish provider retention, restart durability of
the source system, PostgreSQL restore or PITR, physical provider recovery,
production signer/key deployment, or RPO/RTO. It also does not grant admission,
startup, publication, or activation authority. Standalone capture-core
transport is bounded to this persistence slice; upload intent execution,
provider retention and production evidence enablement remain separate. Signed
evidence does not prove physical disaster recovery.

Evidence persistence does not prove physical disaster recovery.

This validates recovery evidence binding. It does not prove successful physical disaster recovery.

## Managed-data S3 disaster-recovery seed qualification

The bounded seed harness composes the existing evidence owners without
performing restoration. It creates one disposable, versioned MinIO bucket and
one PostgreSQL control database, then seeds a single managed connection with
two explicit logical revisions. Normal S3 writes populate both revisions and
one revision also contains a multipart object. Every managed object remains
under `recovered-data/`; independently versioned sentinel objects remain under
`unrelated-sentinel/` and are compared byte-for-byte before and after capture.

The writer persists the exact VersionID returned by each successful PUT or
multipart completion together with its trusted provider profile, object key,
SHA-256, size, and capture time. The harness never infers a version from HEAD
or the mutable latest object. PostgreSQL then captures the complete ready
revision projection and recovery marker, the existing Manifest v2 service
resolves and re-verifies every exact version, and the existing signer and
`CreateSet3` path produce and persist the prepared RecoverySet v3 evidence.
The in-memory qualification key is never written to an artifact.

Run the isolated seed capture with Docker available:

```sh
task qualify:ubdr:managed-data-s3-dr
```

The command replaces only
`.tmp/qualification/ubdr/managed-data-s3/` and writes two private,
operator-facing reports:

- `recovery-point.json` records the scenario and RecoverySet identities,
  source-anchor and frontier commitments, Manifest v2 digest, capture interval,
  observation counts, revision identities, and logical scenario fingerprint.
- `baseline-object-inventory.json` records the two revision memberships,
  upload kind, exact managed-object versions, hashes and sizes, plus the
  independent sentinel version inventory.

Both reports use stable field and collection ordering and are never accepted
as recovery-admission input. Repeated runs are semantically deterministic:
logical identities, fixture bytes, membership, counts, and the scenario
fingerprint remain stable. Disposable bucket names, provider VersionIDs,
PostgreSQL restore points and capture timestamps remain authoritative per-run
values and therefore are not required to be byte-identical.

The qualification rejects missing observations, unavailable exact versions,
missing revision membership, sentinel namespace substitution, and digest
mismatches before a usable v3 association can be created. It does not restore
objects, execute provider recovery, activate state, exercise startup admission,
or measure RPO/RTO.

This validates recovery evidence binding. It does not prove successful physical disaster recovery.
