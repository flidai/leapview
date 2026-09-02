# ADR-0019: Integrate dbt through immutable build releases

Status: proposed

Decision date: 2026-09-02

Implementation: pending

Deciders: LeapView maintainers

Supersedes: none

Amends: none

Related: [ADR-0007](0007-adopt-plan-driven-project-delivery.md);
[ADR-0008](0008-isolate-ducklake-candidate-physical-state.md);
[ADR-0009](0009-separate-control-and-physical-transactions.md);
[ADR-0016](0016-adopt-standards-aligned-data-contracts-and-interchange.md);
[ADR-0018](0018-retain-project-as-the-durable-deployment-namespace.md);
[dbt release conformance](specifications/dbt-release-conformance.md);
[dbt build](https://docs.getdbt.com/reference/commands/build);
[dbt manifest](https://docs.getdbt.com/reference/artifacts/manifest-json);
[dbt run results](https://docs.getdbt.com/reference/artifacts/run-results-json);
[dbt on-run-start and on-run-end](https://docs.getdbt.com/reference/project-configs/on-run-start-on-run-end);
[dbt-duckdb external models](https://github.com/duckdb/dbt-duckdb#reading-and-writing-external-files);
[dbt-duckdb external materialization implementation](https://github.com/duckdb/dbt-duckdb/blob/67b43f1f86ef6b4252b184ebeb00b750a7e9a513/dbt/include/duckdb/macros/materializations/external.sql);
[Azure Login](https://github.com/Azure/login);
[DuckDB Azure extension](https://duckdb.org/docs/current/core_extensions/azure)

## Context and problem statement

LeapView can provide an attractive dbt adoption path if teams keep dbt as their
transformation and test framework while adding LeapView semantics, governed
querying, and dashboards. That path must work for a local dbt-duckdb project and
for production automation in which a CI job reads object-store inputs, runs dbt,
and publishes Parquet outputs for a separately operated LeapView instance.

The dbt manifest describes the parsed project graph, configuration, compiled
nodes, and relation metadata. It does not by itself prove that a selected model
was successfully built, that its tests passed, that the relation still denotes
the bytes inspected by the build, or that the physical schema matches the
logical declaration. `run_results.json` supplies per-node execution status, but
neither artifact makes a mutable relation or object-store prefix immutable.

Products that integrate dbt for metadata and semantic scaffolding can accept a
manifest as useful input. A deployment system has a stronger obligation:
candidate qualification and rollback must bind the active generation to exact
physical data. Reading a mutable relation, an unversioned Parquet key, a glob
under `latest`, or unrelated dbt artifacts would let external activity change
an already active LeapView generation without a LeapView plan.

LeapView also must not become a second dbt runner, own dbt credentials, or make
dbt an alternate native semantic authority. The integration boundary needs to
preserve dbt ownership of transformation while feeding the ordinary LeapView
compiler and delivery lifecycle with closed, reproducible input.

The question is what evidence and physical boundary LeapView consumes from dbt,
and how that input becomes Project-local LeapView resources without duplicate
authoring or mutable runtime dependencies.

## Decision drivers

- Let teams combine conventional dbt projects with LeapView without rewriting
  transformations or duplicating relation declarations.
- Keep dbt responsible for compilation, execution, tests, contracts, and
  creation of transformed outputs.
- Keep LeapView responsible for generated resource validation, semantic models,
  access policy, dashboards, governed queries, and atomic activation.
- Make every candidate ingest exact immutable dbt output bytes and bind the
  resulting managed physical state to correlated build evidence.
- Support production CI and target-owned object-store credentials without
  requiring LeapView to execute dbt.
- Allow build-once promotion, rollback without rebuild, and safe failure before
  activation.
- Preserve one generic boundary that can later admit other dbt adapters, dbt
  Cloud, warehouses, and object stores through qualified profiles.
- Keep the initial dbt-duckdb and Azure Blob path small enough to demonstrate
  end to end.

## Considered options

### Parse a dbt repository or manifest only

LeapView could compile selected dbt nodes into semantic scaffolding and resolve
their relation names at deployment. This is sufficient for metadata browsing,
but not for deployment: it cannot prove successful execution, test disposition,
physical schema, or which bytes a generation will query.

### Bind generated resources to mutable dbt relations

LeapView could combine manifest and run results, then query the current relation
named by each node. This avoids publishing another envelope, but a later dbt run,
manual replacement, environment switch, or object upload could mutate an active
LeapView generation outside planning and qualification.

### Make LeapView clone the repository and execute dbt

An embedded runner could control artifact correlation and copy results into
serving state. It would also make LeapView own adapter installation, package
resolution, arbitrary dbt macros, secrets, network access, scheduling, and
failure recovery. That duplicates mature CI and dbt execution responsibilities
and unnecessarily couples adoption to a LeapView-hosted build environment.

### Consume an immutable dbt build release

External automation can execute dbt, publish selected outputs under immutable
identities, correlate the dbt artifacts and physical objects in a small release
envelope, and ask LeapView to deploy that exact envelope. LeapView can validate
and lower the evidence into its normal local graph without executing dbt or
following mutable relation heads.

## Decision outcome

LeapView integrates dbt by consuming an **immutable dbt build release**. A dbt
release is a producer-created, content-identified deployment input containing:

- the exact `manifest.json` and `run_results.json` from one successful dbt build
  invocation;
- producer, repository, commit, adapter, command, and invocation
  provenance;
- separate explicit build and output selection policies, plus a result for every
  model and test the build policy requires;
- exact immutable physical outputs for those models, including portable storage
  aliases, content digest, versioned schema digest, row count, and provider
  identity evidence when available;
- a publication profile, provider-observed publication evidence, and a
  producer-guaranteed retention deadline; and
- a canonical release envelope written only after all outputs and evidence are
  complete.

The canonical release envelope is identified by digest and is the commit marker
for the producer transaction. A mutable channel pointer may help humans find a
release, but planning must receive or resolve to an exact release identity and
digest. Active or retained generations must never dereference `latest`, a branch
head, an unbounded object prefix, or another mutable alias.

The release separates **build selection** from **output selection**. Build
selection is the complete dbt graph needed to produce and test outputs on a
fresh target, including upstream models selected with graph operators. Output
selection is the smaller set of successfully built, relation-producing models
exported and lowered into LeapView. An internal staging model can therefore be
built without becoming a public LeapView Model. The importer reevaluates both
sets against the manifest and correlates build selection to the normalized
invocation arguments in `run_results.json`.

The release is a pinned **candidate-build input**, not serving state. Planning
stages and verifies the bounded JSON artifacts. In guaranteed mode, during
candidate construction a dbt-release importer reads each Parquet object into
target-owned bounded staging while computing its digest. In best-effort mode,
the same acquisition must finish before planning succeeds. The importer verifies
the complete staged bytes, row count, and schema and materializes each
output-selected model into the candidate's private DuckLake catalog.
Qualification reads that materialized state. Once sealed, the
DuckLake catalog and its physical pool are authoritative for serving, leases,
publication, and rollback under ADR-0008 and ADR-0009; queries do not reread the
producer's Azure objects.

### Ownership and trust boundary

The producer executes dbt and owns source access, package installation, macro
execution, intermediate state, tests, and output publication. LeapView does not
run repository code as part of release ingestion. Release artifacts are
untrusted bounded inputs: LeapView applies strict schema and artifact-version
allowlists, digest verification, size and graph limits, path controls, and
fail-closed parsing before candidate construction.

The producer's successful-build claim does not replace LeapView qualification.
LeapView correlates invocation identity, model identity, complete required-test
results, physical object identity, schema evidence, and exported row-set
equivalence. It then applies its own contract, connection, security, query, and
deployment checks. A release failing either producer evidence validation or
LeapView qualification cannot affect the active generation.

The initial producer profile rejects dbt `on-run-end` hooks from both the root
project and dependencies. Those hooks execute after model and test results and
could mutate a tested relation before export. LeapView detects their manifest
operation nodes and corresponding results under the pinned dbt artifact tuple;
an unrecognized hook representation fails closed.

The authenticated deployment request supplying the expected ReleaseDigest is
the v1 admission trust root for the release bytes. Repository, commit, workflow,
and toolchain fields are producer assertions retained as unverified provenance;
they cannot grant authorization or satisfy a policy requiring authenticated
build provenance. Such a policy requires a separately verified signed
attestation covering the ReleaseDigest and claims.

### Project and resource mapping

A dbt repository normally deploys to one LeapView Project selected under
ADR-0018. Project remains an issuer-assigned, target-claimed namespace and does
not become an authored dbt or LeapView resource. The release is an external
deployment input, not a native cross-Project publication under ADR-0018.

The producer dbt project may use packages or dbt Mesh public models from other
dbt projects. dbt resolves and materializes those dependencies before release
publication. LeapView consumes one completed consumer-project release in the v1
profile; it does not resolve upstream dbt Projects or merge independently
published releases while planning or serving. A future multi-release profile
requires a separate extension with deterministic identity, collision, and
atomic locking rules.

Selected dbt outputs lower deterministically into generated Project-local Source
and thin Model contracts. Their dbt package, unique ID, checksum, relation,
contract, invocation, release, and physical object identities remain provenance.
They do not replace Project UID, resource UID, authored contract identity, or
generation identity. Authors may override a bounded display or local-ID mapping,
but ambiguous mappings and collisions fail planning rather than retargeting an
existing resource.

Authors write LeapView Models only for transformations LeapView owns. A normal
dbt integration therefore contains dbt models and tests plus LeapView
SemanticModel and Dashboard source; it does not require handwritten mirror
Sources or Models for selected dbt outputs.

Every SemanticModel dataset resolves to an authored or generated Model in the
same LeapView Project candidate and generation. A dbt project-qualified node ID
is provenance inside the release; it is not a runtime cross-Project reference.

### Connections and environments

Release locations contain portable producer-defined storage aliases and exact
provider object paths, never LeapView binding names or credentials. The envelope
selects a versioned publication profile defining producer storage, commit, and
retention obligations. The delivery request separately selects a versioned
ingress profile and maps every alias to one target-owned logical connection
binding. Planning admits only an explicitly qualified publication/ingress pair,
resolves and authorizes the mappings in the destination Project and environment,
and records both profiles and mappings in the ReleaseLock. The binding used to
fetch `release.json` is a separate envelope-transport input and has no authority
over aliases inside the envelope. Candidate construction acquires and verifies
the complete bytes through the locked mappings before materializing them.
Producer and LeapView identities may therefore use different, least-privilege
credentials for the same storage service.

The initial importer is a new pinned-ingress capability layered on LeapView's
native Azure access; it is not an assertion that the ordinary direct-read Azure
Source connector already pins provider objects. Hashing and later opening the
remote path as two independent operations is forbidden. The importer stages and
hashes one byte stream, then materializes only from that verified target-owned
copy. Provider version IDs and conditional reads strengthen the acquisition but
do not replace the required content digest.

Ingress is resource-bounded after hashing as well as before it. The destination
locks limits for compressed and decoded bytes, rows, columns, nesting, row
groups, pages, values, expansion ratio, memory, temporary disk, CPU, and wall
time. Parquet metadata inspection and decoding run under those limits. A limit
breach fails the candidate without changing the active generation.

One completed release may be qualified and promoted through several LeapView
environments without rerunning dbt when the same physical data is intended.
Each destination builds and seals its own target-local candidate from the same
release unless an existing qualified candidate is being reactivated in the
same target.
When environments intentionally use different dbt inputs or outputs, they
produce distinct releases. Target switching never mutates an existing release
or generation.

### Physical immutability and lifecycle

Every output uses a create-only release identity and an exact object set. The
initial profile permits one Parquet object per output-selected model. Later
profiles may admit partition manifests, warehouse snapshots, tables with
time-travel identities, or other formats only when they prove equivalent closure
and retention.

The producer does not use dbt-duckdb's stock `external` materialization as the
v1 export contract. That materialization can encode a zero-row relation by
writing a synthetic all-NULL record and hiding it in a DuckDB view, while
LeapView consumes the Parquet object itself. The producer instead runs a
LeapView-qualified export phase after a successful `dbt build`. The export must
preserve the exact row multiset, including a true zero-row Parquet result with its
schema. The v1 DuckDB exporter reopens the Parquet and proves duplicate-
preserving multiset equality with the tested relation in both directions, in
addition to schema and row-count reconciliation. A future dbt-duckdb
materialization may be admitted only after it passes the same tests.

Every release declares a producer-guaranteed `retainUntil`. The initial Azure
publication profile requires at least 30 days from the provider-confirmed
successful publication of the release envelope, not from a producer-authored
timestamp. A delivery request chooses guaranteed acquisition before that
deadline or explicit best-effort acquisition. Post-deadline best effort is
admitted only by acquiring and verifying the exact locked objects during
planning; missing or changed objects fail closed. After a candidate has copied
the verified data and sealed, active generations, rollback windows, and query
leases root LeapView's catalog artifact and physical-pool objects rather than
the producer release. Longer producer retention remains valuable for audit,
disaster recovery, and later promotion but is not a serving correctness
dependency.

A producer may leave partial objects after a failed build, but it must not write
the release commit marker; abandoned uncommitted prefixes may be removed after
a bounded grace period. Published objects are never repaired in place. A
correction creates a new release.

### Semantic boundary and evolution

LeapView SemanticModel remains the native semantic and access-policy authority.
The initial profile does not require MetricFlow or consume the dbt Semantic
Layer. A future semantic-import profile may reuse dbt semantic artifacts, but it
must define explicit mapping, provenance, conflict, and policy rules without
silently creating a second runtime authority.

The architectural release boundary is independent of CI vendor, object store,
and dbt execution service. The first conformance profile and showcase use dbt
Core, dbt-duckdb, Parquet, GitHub Actions, and Azure Blob or ADLS because that
combination exercises the production boundary while remaining locally
reproducible.

Profile identifiers are wire contracts, not rolling documentation labels. A
profile may be refined while proposed, but once a conforming release is admitted
by a released LeapView version its envelope shape, canonicalization, selection,
schema normalization, digest inputs, and fail-closed acceptance semantics are
immutable. A breaking or digest-affecting change requires a new profile
identifier and explicit compatibility rules.

## Consequences

Teams can adopt LeapView beside dbt without migrating transformation code or
giving the LeapView service permission to execute repositories. CI can publish
once, LeapView can import and qualify exact data independently, and operators
can promote without rerunning dbt or roll back sealed generations without
accessing producer storage.

The active graph remains closed even though data was created externally. dbt
artifacts become useful provenance and validation evidence rather than being
mistaken for physical deployment proof. The same adapter architecture can later
support warehouse snapshots and dbt Cloud job artifacts.

The producer workflow gains explicit release duties: qualified full-replacement
export, true zero-row handling, unique create-only paths, artifact correlation,
schema and row-count inspection, hashing, final commit-marker creation,
retention, and abandoned-prefix cleanup. Large Parquet objects may make byte
hashing and target-owned staging expensive, and object-store ETags are not
universally cryptographic; profiles must define an integrity mechanism rather
than assume an ETag is a content digest.

LeapView must maintain hardened parsers for supported dbt artifact schemas and a
deterministic generated-resource mapper. Adapter and artifact upgrades are
therefore compatibility work, not an implicit best-effort behavior.

The first profile deliberately excludes incremental external models,
partition-set mutation, mutable warehouse relations, and imported MetricFlow
semantics. Those limitations make the production demo less broad but keep its
activation and rollback claims honest.

## Confirmation

- Golden releases prove canonical envelope hashing, strict version handling,
  bounded parsing, artifact digest verification, and manifest/run-result
  invocation correlation.
- Selection fixtures prove build and output selections are locked separately,
  upstream models build without becoming outputs, invocation arguments match,
  every required model and test is derived from the manifest, and exclusions,
  indirect-selection drift, state, or defer cannot omit required evidence.
- Hook fixtures prove an `on-run-end` operation that mutates a tested relation is
  rejected before export, including when introduced by a dependency package.
- Mapping fixtures prove deterministic Project-local Source and Model identity,
  collision rejection, provenance retention, and ordinary compilation of local
  SemanticModel and Dashboard resources.
- Export fixtures prove non-empty and zero-row relations retain their exact
  duplicate-preserving row multisets and versioned physical schemas without
  sentinel records across null, decimal, temporal, NaN, and nested values.
- Object-store conformance tests prove conditional create of exact paths,
  content and schema verification, single-stream verified staging,
  retention through `retainUntil`, unavailable-object failure, and no
  mutable-prefix resolution.
- Adversarial Parquet fixtures prove compressed and decoded resource limits,
  structural metadata bounds, timeout, cleanup, and unchanged active state.
- Delivery tests prove a failed dbt build, failing test, corrupt artifact,
  missing object, incompatible schema, changed storage-alias mapping or target
  binding, incompatible publication/ingress profile, expired guaranteed
  acquisition window, failed best-effort acquisition, or failed qualification
  leaves the active generation unchanged.
- Promotion and rollback tests prove the same release can build qualified
  candidates in multiple environments without rerunning dbt and that rollback
  selects the prior sealed LeapView catalog without reading producer objects.
- The production showcase uses GitHub Actions workload identity to read
  immutable Azure source data, runs one pinned classic dbt Core and dbt-duckdb
  build, exports exact Parquet locally, conditionally publishes immutable
  objects and correlated artifacts, and imports the exact release through a
  read-only LeapView Azure connection into a sealed DuckLake candidate.
