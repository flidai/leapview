# Data-contract versioning conformance specification

Status: accepted

Profile: `leapview.contract/v1`

Last updated: 2026-09-04

Owners: LeapView maintainers

Governing decision: [ADR-0016](../0016-adopt-standards-aligned-data-contracts-and-interchange.md)

## Purpose

This mutable specification defines the resource-identity namespace, canonical
contract projection, serialization, digest, and change evidence required by
ADR-0016. It resolves two security-sensitive consequences of removing the
public Project resource: control-plane references cannot silently rebind, and
published contract immutability must be calculated from one deterministic
typed projection.

The terms **must**, **must not**, **should**, and **may** are normative. The
profile identifier is part of canonical bytes and conformance evidence.

## Resource identity

- **RID-01:** One LeapView instance has at most one active compiled source
  bundle. Deploying another source root creates a candidate replacement; it
  does not add a second public namespace. Concurrent independently deployed
  bundles require separate instances.
- **RID-02:** An authored `metadata.id` is portable and stable across repository,
  branch, directory, filename, symbolic-name, display-name, and source-root
  moves. None of those locations participates in canonical identity.
- **RID-03:** A candidate contains at most one resource with a given
  `metadata.id` across all six authored kinds. Cross-kind and same-kind
  collisions are rejected before graph construction.
- **RID-04:** First activation records `(instance identity, metadata.id)` with
  an immutable authored kind. That tuple is the complete resource identity;
  there is no second public or opaque resource UID. Control-plane grants,
  publications, audit records, and durable references store the instance
  scope, authored ID, and expected kind; they do not resolve by name or path.
- **RID-05:** A later candidate in the same instance with the same authored ID
  and kind updates the same resource identity. A kind change is never an update
  and is rejected.
- **RID-06:** Removing a resource tombstones its instance-qualified authored
  identity and suspends dependent control-plane references. Normal deployment
  cannot reuse the tombstoned ID or reactivate it implicitly.
- **RID-07:** Restoration of a tombstoned logical resource requires an explicit,
  audited control-plane restore operation. Restore retains the original
  `(instance identity, metadata.id)` tuple, recompiles and revalidates every
  dependent reference, and reactivates only references whose stored instance,
  authored ID, and expected kind still match.
- **RID-08:** Rollback reactivates the historical instance-qualified authored
  identities and generation recorded by the selected release. It cannot create
  a new identity or retarget a reference.
- **RID-09:** The same authored ID may exist in different instances because the
  instance identity is the namespace boundary. External ODCS, ODPS, DCAT, and
  lineage projections qualify it with a stable instance or tenant URI; the raw
  authored ID is never claimed to be globally unique.
- **RID-10:** Candidate planning reports created, updated, removed, tombstoned,
  restored, dangling, and collision outcomes before approval. An unresolved or
  ambiguously resolved control-plane reference blocks activation.
- **RID-11:** Candidate publish transitions carry the normalized set of
  DashboardPublication dependency bindings projected from the retained
  compiler manifest and graph. Live Grant references belong to the ControlStore
  and are never regenerated from a candidate manifest. The publication
  projector does not reload source files, resolve by display name, or infer a
  target kind from an ID's spelling. Legacy immutable transition rows may retain
  Grant evidence, which must be filtered before reference reconciliation.
- **RID-12:** ControlStore Grant target kinds are limited to the six authored
  analytics kinds and a Project target is rejected at that live boundary.
  Identity-ledger tombstoning suspends a Grant's durable target reference;
  active preparation revalidates and CAS-reactivates only a non-revoked Grant
  whose exact target and kind exist in the selected graph. An absent target
  remains suspended and denied. DashboardPublication dependencies require an
  existing authored graph target and retain that target's expected kind; a
  missing publication dependency is rejected before transition preparation.
- **RID-13:** Reference IDs are deterministic, readable, and collision-safe:
  `grant:<grantID>` for grants and a length-prefixed owner/target form for
  DashboardPublication dependencies. IDs longer than 255 bytes are rejected;
  hashing and truncation are not permitted.
- **RID-14:** PostgreSQL persists immutable durable-reference evidence with the
  transition. Normalize, sort, encode, decode, and exact-replay comparisons all
  include the reference set. Rollback copies the original publish references
  without rebuilding them and filters legacy Grant references before applying
  the current publication-owned set.
- **RID-15:** Production identity composition uses the PostgreSQL ledger and
  local/development composition has no substitute ledger. Candidate planning
  and readiness use that authority, canonical and worker publish/rollback pass
  through the durable identity coordinator before target-owned delivery, and
  startup fails readiness while a claimed transition requires replay. Absence
  from compiler-derived evidence never removes instance control state; complete
  reference-set reconciliation must be sourced from the live control stores.
  Candidate start and direct canonical plan creation may carry an explicit,
  restore-only intent containing exact authored IDs and a required reason; it is
  canonicalized, persisted immutably, and included in the existing plan
  evidence/digest before Build. Ordinary candidate publication cannot add
  restore data. The existing sealed publication activation consumes this
  evidence; there is deliberately no standalone restore endpoint or approval
  authority. Active runtime preparation projects current live role/grant state
  against the selected generation graph while immutable artifact authorization
  remains installation evidence. Private candidate preparation does not mutate
  or consume the live control store. This milestone does not claim semantic
  access migration.

## Canonical contract envelope

- **CAN-01:** Canonicalization starts from generated, validated TypeSpec DTOs,
  never YAML nodes, map iteration, source formatting, comments, aliases,
  anchors, default omission, or handwritten reflection.
- **CAN-02:** Every public DTO field is classified in TypeSpec as contract,
  descriptive, operational, secret, or derived. Generation fails when a new
  field lacks a classification.
- **CAN-03:** Canonical bytes contain `profile`, `apiVersion`, `kind`,
  `metadata.id`, `metadata.name`, authored contract version, compatibility
  policy, and the resource-specific contract projection below.
- **CAN-04:** Canonical bytes exclude display name, prose description, owner,
  domain, tags, documentation, provenance, AI context, source paths, comments,
  credentials, target bindings, runtime observations, deployment state, and
  derived lineage. Changes to excluded fields do not require a contract-version
  change but remain visible in candidate metadata diffs.
- **CAN-05:** Classification, critical-data-element markers, authoritative
  definitions, and deprecation guidance are included wherever ADR-0016 permits
  them. Their changes require a new contract version even when compatibility is
  otherwise non-breaking.
- **CAN-06:** Defaults are materialized before projection. An omitted field and
  an explicitly authored value are byte-identical only when the generated
  contract declares that value as the normative default.
- **CAN-07:** Unknown fields, generic extension bags, non-finite numbers, and
  values without a canonical typed representation are rejected before hashing.

## Resource-specific projection

### Source

- **SRC-01:** Source projection includes schema mode, declared field and nested
  structure, logical datatypes, nullability, field governance, freshness
  guarantees, and every stable authored identity used by those declarations.
- **SRC-02:** Source projection excludes Connection binding, physical location,
  credentials, discovered physical types, inferred observations, refresh state,
  and target-owned options. Inferred observations may be evidence but cannot
  mutate published canonical bytes.

### Model

- **MOD-01:** Model projection includes canonical output fields and nullability,
  entities, keys, grain, relationships promised by the Model contract, field
  governance, and the complete normalized quality-check definitions including
  stable IDs, types, severity, thresholds, and referenced fields.
- **MOD-02:** Result-affecting transformation logic is represented by the
  compiler's canonical analyzed logical-expression or SQL-AST projection.
  Whitespace, comments, harmless parentheses, and source formatting are absent;
  identifiers, operators, literals, casts, functions, and dependency IDs remain.
- **MOD-03:** Materialization location, runtime plan choices, cache settings,
  physical table names, and execution observations are excluded.

### SemanticModel

- **SEM-01:** SemanticModel projection includes datasets and bindings,
  relationships, entities, dimensions and time semantics, measures, metrics,
  filters, units, formats that affect returned consumer values, result-affecting
  expressions, field governance, and the ADR-0017 access contract.
- **SEM-02:** Every semantic collection is projected by stable identifier and
  every reference is lowered to a canonical stable resource or member ID before
  hashing. A file name, map insertion order, or display label cannot affect it.
- **SEM-03:** Discovery-only prose, AI context, display descriptions, and
  instance attribute values are excluded. Attribute names and authored access
  conditions are included.

## Normalization and serialization

- **SER-01:** The canonical projection is encoded as UTF-8 JSON using
  [RFC 8785 JSON Canonicalization Scheme](https://www.rfc-editor.org/rfc/rfc8785.html)
  after the typed normalizations in this section.
- **SER-02:** JSON object keys use RFC 8785 ordering. Collections declared as
  sets are deduplicated and sorted by their canonical element bytes. Ordered
  lists retain declared semantic order. TypeSpec must classify every collection
  as a map, set, or ordered list.
- **SER-03:** Identifiers and enumerations use their validated canonical spelling.
  General strings are Unicode NFC and remain case-sensitive; no whitespace is
  trimmed and forbidden control characters are rejected.
- **SER-04:** Integers use their mathematical base-10 representation with no
  leading plus or zeroes except `0`. Exact decimals remove insignificant trailing
  zeroes, use no exponent, and canonicalize negative zero to `0`. Approximate
  floats are not permitted in contract literals.
- **SER-05:** Booleans serialize as JSON `true` or `false`. Null is emitted only
  for a field whose generated contract distinguishes explicit null from absence;
  otherwise absence is represented by omission after default materialization.
- **SER-06:** Dates use valid Gregorian `YYYY-MM-DD`. Timestamps parse RFC 3339,
  reject leap seconds, normalize to UTC `Z`, and use the shortest fractional
  second form that preserves nanosecond precision.
- **SER-07:** URLs use RFC 3986 syntax normalization: lowercase scheme and host,
  uppercase percent hex, decode percent-encoded unreserved characters, remove
  dot segments, remove default HTTP/HTTPS ports, and use `/` for an empty
  authority path. Query-pair order and fragments remain significant.
- **SER-08:** Logical values that JSON cannot distinguish losslessly, including
  Decimal, Date, and Timestamp, use generated type-tagged canonical values so
  equal text in different logical types never hashes identically by accident.
- **SER-09:** The canonical digest is SHA-256 over the exact canonical bytes and
  is rendered as `sha256:` plus lowercase hexadecimal. Stored evidence retains
  the profile, bytes, digest, instance-qualified authored ID, kind, and version.
- **SER-10:** Independent fixtures in Go and one non-Go RFC 8785 implementation
  must produce byte-identical output and digests for the supported corpus.

## Version immutability and change handling

- **VER-01:** The first publication of a resource contract version stores its
  canonical bytes and digest immutably.
- **VER-02:** A candidate using an already published version must reproduce the
  stored profile and digest exactly. Any mismatch is rejected even when a
  compatibility classifier would call the change additive or descriptive.
- **VER-03:** A change to included canonical content requires a new semantic
  version. Excluded descriptive or operational changes do not require one.
- **VER-04:** A new version does not authorize activation. The normalized diff,
  compatibility class, security impact, affected graph, and deployment policy
  still determine approval or rejection.
- **VER-05:** Changing the canonical projection, normalization, or serialization
  requires a new profile identifier. Existing published bytes and digests remain
  verifiable under their original profile.
- **VER-06:** Editorial changes, added fixtures, evidence links, and stricter
  tests that do not change accepted documents or behavior may update this file
  without changing the profile. Any normative semantic change requires a new
  profile version and review of whether ADR-0016 must be amended or superseded.

## Qualification matrix

| Scenario | Expected result |
|---|---|
| Source root or file moves with unchanged authored ID | Same instance-qualified resource identity and contract digest |
| Two resources share an authored ID | Candidate rejected before graph construction |
| Resource kind changes under an existing ID | Candidate rejected |
| Deleted ID appears in a normal deployment | Tombstone-reuse rejection |
| Explicit restore is approved | Original identity restored; validated exact references reactivate |
| Description or tags change | Metadata diff only; digest unchanged |
| Field datatype or access grant changes | Digest changes; new version required |
| YAML key order, comments, aliases, or whitespace change | Digest unchanged |
| Equivalent decimal or timestamp spellings | Digest unchanged after typed normalization |
| Existing published version has different bytes | Candidate rejected |
| Canonical profile changes | New profile required; historical digest preserved |

## Evidence ledger

| Requirement range | Evidence | Status |
|---|---|---|
| PostgreSQL control authority prerequisite (FAI-609) | Production Admin uses a separately authenticated, one-connection migrator to apply the existing checksum-bound migration ledger, verifies the ledger through the runtime credential, and constructs the Access initializer only through `access/module`. PostgreSQL revision 009 owns immutable instance identity and environment binding, and production composition selects that authority while local/evaluation composition retains the embedded store. Focused Admin tests prove exact credential recovery replay and that production initialization does not create `leapview.db`; architecture tests reject direct application imports of `access/postgres`. | Implemented in this stacked layer; live PostgreSQL 18 fresh-target, role-grant, replay, and unavailable-target execution remain blocked by the unavailable container provider. Draft PR #386 was not merged and its physical-pool/general serving changes were excluded. |
| RID-01–RID-10 | The PostgreSQL identity-ledger package has collision, tombstone, restore, rollback, concurrency, and durable-reference tests. App lifecycle tests exercise exact candidate admission, publish/restore/rollback evidence binding, approval-before-identity ordering, and fail-closed delivery ordering against the coordinator contract. FAI-663 adds focused domain, module/API, direct canonical-plan, SQLite persistence, restart round-trip, changed-intent retry, and rollback-after-restore coverage for the explicit restore intent. FAI-616 adds the instance-qualified PostgreSQL live role/grant authority and one-time immutable-snapshot seed/projection boundary. FAI-617 projects current live controls into active runtimes, preserves immutable generation installation evidence, fences the complete durable-read-to-runtime-publish interval, and routes worker publish/rollback through the existing target-CAS activation callback with exact committed-retry target verification. | IMPLEMENTED; qualification PARTIAL because live PostgreSQL process execution was not run where Docker is unavailable. |
| RID-11–RID-14 | [`ProjectIdentityReferences`](../../internal/app/identity_lifecycle.go) is a pure retained-manifest-plus-graph projector for DashboardPublication dependencies. Focused app tests cover all six authored dependency kinds, missing targets, length-prefixed collision-safe IDs, the 255-byte fail-closed bound, and filtering of legacy Grant evidence on publish and rollback. [`transition_test.go`](../../internal/project/identityledger/transition_test.go) covers normalization, sorting, lifecycle-state rejection, and replay evidence; [`transition_repository_test.go`](../../internal/project/identityledger/postgres/transition_repository_test.go) contains PostgreSQL round-trip and immutable journal evidence cases. FAI-616 writes each live Grant row and durable target binding in one PostgreSQL transaction; identity-ledger tombstoning suspends the reference, and FAI-617 revalidates and CAS-reactivates only eligible current grants. | IMPLEMENTED; qualification PARTIAL because Docker-backed PostgreSQL replay was unavailable. |
| RID-15 | [`identity_conformance_test.go`](../../internal/platform/architecture/identity_conformance_test.go) proves the production identity builder selects PostgreSQL, local composition has no substitute ledger, candidate planning/readiness use the ledger, canonical publish/rollback route through the identity coordinator, restore reuses the existing transition/approval/digest authorities, and lifecycle code adds no second hash authority. [`postgres_access_conformance_test.go`](../../internal/platform/architecture/postgres_access_conformance_test.go) proves the shared identity pool is injected through the access module while concrete PostgreSQL repository, live control store, and MCP OAuth adapters remain capability-owned; it also rejects the former post-CAS worker reconciliation path. Runtime-host and factory tests prove immutable installation evidence remains distinct from the live reader projection, candidates skip live projection, same-generation preparations refresh it, and cutover tokens reject factory-overlap plus durable-read TOCTOU races. Worker job tests cover fresh and committed-retry publish/rollback ordering through the existing activation callback. FAI-663 delivery tests prove exact restore intent normalization, immutable candidate/plan persistence, direct-plan idempotency conflict handling, Build propagation, and rollback from a restored publication. | IMPLEMENTED; qualification PARTIAL because live PostgreSQL process execution was unavailable. FAI-648/649 semantic access migration remains open. |
| ADR-0016 public control boundary | FAI-616 removes the authored Project selector from the public access, query-audit, dashboard-authoring, authoring-session, and dashboard-publication contracts. Runtime adapters resolve the active internal serving Project where current persistence and authorization still require that deployment key; dashboard authoring fails closed when that resolver is unavailable, and generated response DTOs omit Project identity from lifecycle, serving, and fork evidence without rewriting authored document metadata. The completion layer adds one capability-owned PostgreSQL `ControlStore` on the existing access repository, instance-qualified role assignments and grants with monotonic revisions and atomic audit/reference writes, the canonical role catalog, generated grant handlers, effective-capability and batch projections, and conversion between live state and immutable runtime snapshots. FAI-617 wires the live projection into active preparation and restart/retry reconciliation, retains artifact authorization as immutable installation evidence, and prevents legacy transition Grant evidence from reviving revoked state. [`postgres_access_conformance_test.go`](../../internal/platform/architecture/postgres_access_conformance_test.go) rejects a SQLite substitute, cross-capability identity-ledger imports, and a second hash/canonicalization authority. | IMPLEMENTED, qualification PARTIAL. Focused domain, HTTP, module, snapshot, runtime-host, app-composition, generated-command, and architecture tests pass. Docker-unavailable PostgreSQL execution is BLOCKED. Transitional `DataPolicy` is DEFERRED until FAI-648 and remains owned by FAI-649. |
| ADR-0016 structural authority | FAI-619 completed the SemanticModel migration to [`api/data-resources/main.tsp`](../../api/data-resources/main.tsp). The generated [Go DTOs](../../internal/project/contracts/models.gen.go) and [JSON Schema](../../internal/project/contracts/gen/data-resources.schema.json) are the only production structural authority; the [architecture guard](../../internal/platform/architecture/architecture_test.go) rejects handwritten compiler DTOs and CUE copies. Qualified 2026-09-02 with `task generated:check`, `go test ./internal/platform/architecture -run '^TestSemanticModelStructuralAuthorityIsGeneratedFromTypeSpec$' -count=1`, `go test ./internal/project/contracts ./internal/project/schema -count=1`, and the focused `duckdb_arrow` compiler-lowering tests. | Implemented by FAI-619 |
| CAN-01–CAN-07 | FAI-620 added the explicit generated [`leapview.contract/v1` projection DTOs](../../api/data-resources/contract-projections.tsp) and reviewed [exclusion manifest](../../internal/project/contractprojection/exclusions.json). FAI-662 seals canonicalizable roots behind private projector-owned payloads and exposes strict publication read views that cannot satisfy the projection interface. The data-resource generator now checks both directions: every reachable authored leaf is projected or exactly excluded, every projection-only leaf is explicitly allowlisted, broad parent exclusions are rejected, and exclusion groups pin generated type/enum/requiredness fingerprints. New, renamed, removed, overlapping, unclassified, extra projection, and source-shape drift fixtures fail closed. | Implemented by FAI-620; integrity boundary hardened by FAI-662 |
| SRC-01–SRC-02 | The allowlist projector includes public schema mode and fields, freshness/SLA values, governance, authoritative definitions, and deprecation while excluding connector and physical location data. Golden leak-prevention and digest fixtures are in [`canonical_test.go`](../../internal/project/contractprojection/canonical_test.go). | Implemented by FAI-620 |
| MOD-01–MOD-03 | The Model projector includes entities, keys, grain, field governance, normalized checks, and a closed allowlist projection of the existing compiler-admitted pinned DuckDB AST. Authored SQL formatting, comments, parser locations, runtime plans, materialization, cache, and physical state are absent. | Implemented by FAI-620 |
| SEM-01–SEM-03 | The SemanticModel projector includes stable dataset, relationship, binding, dimension/time, filter, metric, format/unit, access-grant, and access-filter identifiers while excluding display, discovery, hidden-presentation, and AI fields. Exact typed scalar and internal-field leak fixtures run in `go test ./internal/project/contractprojection -count=1`. | Implemented by FAI-620 |
| SER-01–SER-10 | The sealed projection boundary uses the single production dependency `github.com/cyberphone/json-canonicalization` for RFC 8785, applies NFC/control-character, set, exact-number, date/time, and RFC 3986 URL normalization, and emits `sha256:` identities over exact bytes. RFC golden vectors and a language-neutral Go/Bun byte-and-digest corpus verify deterministic independent output. FAI-662 preserves those bytes and digests while rejecting zero-value, direct-JSON, mutable-root, and nested-unknown-field bypasses. The architecture guard proves existing graph/artifact/release digest paths do not import this boundary. | Implemented by FAI-620; sealed-input hardening implemented by FAI-662 |
| VER-01–VER-06 | FAI-622 added one canonical-byte [compatibility classifier](../../internal/project/contractversion/classifier.go) for structural, semantic, and security changes plus SemVer transition enforcement. Immutable [publication evidence](../../internal/project/identityledger/contract_publication.go) reuses the existing instance-qualified identity ledger and the FAI-620 canonical bytes/digest authority. PostgreSQL [revision 3](../../internal/platform/postgres/migrations/003_contract_publication_evidence.sql) stores authored version, profile, exact bytes, digest, database timestamp, and normalized validation evidence behind append-only triggers. FAI-662 [revision 8](../../internal/platform/postgres/migrations/008_contract_publication_integrity.sql) adds database-side equality checks for the SHA-256 digest and binds API version, profile, authored ID, resource kind, authored version, and build-metadata-free version baseline to the canonical JSON envelope. Exact retries replay the first row; build-metadata, content, digest, evidence, or envelope drift conflicts. The canonical `task test:go:postgres-conformance` lane now injects its application-owned required/optional decision into the platform harness and includes revision 008, exact-replay, conflicting-replay, and concurrency fixtures. Docker-free gate-policy and architecture tests pass; required live execution fails closed and remains BLOCKED where the Docker provider is unavailable. | IMPLEMENTED by FAI-622 and hardened by FAI-662; live PostgreSQL qualification BLOCKED by runner access, and deployment-plan consumption remains separate |
| ODX-01–ODX-07 | FAI-623 added the isolated [ODCS 3.1 export adapter](../../internal/project/contractodcs), pinned upstream schema and checksum, explicit mapping manifest, generated mapping/loss reports, sealed provenance extension, security exclusions, and a CI-only independent CLI oracle. The maintained [ODCS export conformance specification](odcs-export-conformance.md) records the exact document/export claim and known loss boundaries. | Implemented by FAI-623; export/document level only |

The FAI-622 boundary classifies and preserves contract evidence. FAI-662 closes
the projector, manifest, and database integrity gaps without changing the
generated wire DTOs, RFC 8785 implementation, compatibility classifier, or
graph/artifact/release digests. Feeding the result into affected-graph deployment review and consuming
explicit security approvals remains delivery-policy integration. FAI-623 adds
ODCS 3.1 Source/Model export only; ODCS import/round-trip, ODPS, DCAT, and runtime
transports remain capability-gated. The earlier STR-08 boundaries also remain: standalone
transitional `DataPolicy` rejection and contextual access-reference/type-
registry changes are not part of this milestone.

## Migration-chain remediation evidence (2026-09-06)

This entry supersedes earlier Docker-unavailable/migration-002 qualification
notes for the commands below; it does not mark the overall ADR qualified.
The focused `ganesh/postgres-migration-supersession` layer is based on FAI-641
dependency tip `fd4b095510ae` and changes migration infrastructure only.

| Requirement | Implementation | Verification / result |
| --- | --- | --- |
| Preserve historical migration integrity | Original 002 bytes unchanged; separately named replacement plus exact original/replacement tuples in `002_supersession.json`. | Frozen artifact/declaration tests and checksum comparison; original SHA-256 `42f0dc3dacbbf4fa06aef8e2fcbcb6d3559fd97e2907a18fc596c1c4c01152f1`. |
| Fresh initialization and recognized upgrades | One verify-before-skip runner accepts only a contiguous known prefix, selects replacement 002 only when pending, and preserves existing original tuples. | Live `TestMigrationSupersessionPostgreSQL18`: fresh, baseline-prefix, replacement and synthetic-original paths; replay preserves all revision rows and timestamps. |
| Atomicity, retry and concurrency | Caller-owned READ COMMITTED transaction, database-scoped advisory lock, stable built-in parameter/result types across rolled-back domain creation. | Live DDL/revision rollback and same-pool retry; lock timeout; waiting concurrent migrator skips after first commit; independent database proceeds. Focused race run passed. |
| Fail-closed lineage and privileges | Shared Apply/Verify history validation; revision 013 checks table ownership/keys/guards and effective grants, enables read-only runtime ledger verification, and fences ledger TRUNCATE. | Unknown IDs/checksums/future revisions, gaps, untracked/empty ledgers, unsupported isolation, disabled guard and excess privileges rejected; runtime Verify and immutable UPDATE/DELETE/TRUNCATE tested live. |
| Preserve generated contracts and architecture | No identity lifecycle, access control, projection, publication or planner implementation edits. | `task generated:check`, architecture and Admin package tests passed. |

Migration package execution now reaches an independent FAI-662 publication
integrity failure: revision 008's guarded constraint names collide with baseline
checks from 003, allowing malformed digest/profile/kind/version/baseline inserts;
the same test subsequently encounters SQLSTATE `42501` reading publication data.
The required PostgreSQL task passes pool and supersession tests before this
failure. Neither historical migration is changed here.

The broader identity PostgreSQL tests also expose SQLSTATE `42P08` in the existing
history append query and an invalid durable-reference fixture. Access PostgreSQL
tests report revision/identity-conflict expectations and SQLSTATE `42883`
(`platform.resource_id = uuid`). These are separately scoped qualification
blockers, not Docker skips; domain, architecture and Admin tests passed. No
publication, lifecycle or access behavior is repaired by this migration layer.

Final validation commands for this layer (PostgreSQL tests used
`LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1` and the existing Docker group):

| Command | Result |
| --- | --- |
| `go test ./internal/platform/postgres/migrations -run 'TestMigrationSupersessionPostgreSQL18\|TestSupersession\|TestHistorical\|TestApply\|TestVerify\|TestBaselinePostgreSQL18\|TestAccessAuthorityCompatibilityPostgreSQL18' -count=1` | Passed against pinned PostgreSQL 18; no Docker skip. |
| `go test -race ./internal/platform/postgres/migrations -run 'TestMigrationSupersessionPostgreSQL18\|TestSupersession\|TestHistorical\|TestApply\|TestVerify' -count=1` | Passed; rerun on final migration implementation. |
| `go test ./internal/platform/postgres/migrations -count=1` | Failed only in the pre-existing publication-integrity test described above; remediation matrix passed. |
| `go test ./internal/project/identityledger/... ./internal/access/... ./internal/platform/architecture ./internal/app/adminpostgres -count=1` | Domain, architecture and Admin packages passed; identity/access PostgreSQL failures described above remain. |
| `task test:go:postgres-conformance` | Pool and supersession tests passed; stopped at publication-integrity failure. |
| `task generated:check`; `task docs:check`; `go vet ./internal/platform/postgres/migrations`; `gofmt -l internal/platform/postgres/migrations`; `git diff --check` | Passed; no generated snapshot changes or formatting findings. |
| `task ci` | Completed with exit 201: frontend, APIGen and all four application shards passed; Go package sweep failed in `internal/access/postgres`, `internal/platform/postgres/migrations` (publication integrity), and `internal/project/identityledger/postgres`. Later CI steps were not reached. No green full-CI claim. |

## PostgreSQL qualification repair (2026-09-06)

The `ganesh/postgres-qualification-hardening` layer is stacked on
`ganesh/postgres-migration-supersession` at `e17c4931aa39`. This entry supersedes
the three PostgreSQL blockers above, not the remaining ADR capability boundaries.

| Requirement | Implementation | Verification |
| --- | --- | --- |
| RID history parameter typing | Explicit `platform.resource_id` casts on both repeated identity parameters in the handwritten history append query; no domain or lifecycle changes. | Live prepare checks domain parameter OIDs and rejects invalid IDs with domain SQLSTATE 23514; lifecycle, restore, rollback, reference reconciliation and instance isolation tests pass. |
| VER database evidence integrity | New revision 014 locks publications, inspects existing constraint definitions, preflights rows, and adds a validated CHECK for all seven intended 008 bindings. No historical constraint is renamed/dropped, no publication is rewritten, and revisions 002/003/008 are unchanged. | Fresh install and upgrades from 7/8/12/13 preserve valid evidence; digest/profile/kind/version/baseline corruption aborts with 23514, preserving rows, constraints and revision history. Runtime malformed inserts fail with publication CHECK violations. |
| Exact replay and concurrency | The existing publication advisory lock uses a JSON string tuple rather than PostgreSQL-invalid NUL-delimited text; it remains a transient lock key, not an identity/digest authority. | Live exact replay, concurrent replay/conflict, build metadata, monotonic version, immutability and independent-instance publication tests pass. |
| Qualification fixture correctness | Transition references use their own instance; access subtests have independent databases and exact audit counts/revision baselines; resource-ID assertions use domain rather than UUID casts; replay fixture scopes migrator role to the transaction. | Full identity/access PostgreSQL suites pass, including isolated role/grant lifecycles; assertions remain fail-closed. |
| Role mutation error semantics | Update/revoke recognize the scanner's translated not-found error so existing revision/revocation classification executes. | Live stale update reports revision conflict; repeated delete reports revoked; identity retarget remains rejected. This narrow product correction was explicitly approved after fixture repair exposed it. |
| Preserve projection and planner authorities | No projection, canonicalization, generated contract, compatibility classifier or planner implementation changes. | Architecture, contractprojection, contractversion, contracts/generate, PlanIR/query tests, generated checks and docs checks pass. |

Validation used the pinned PostgreSQL 18 Docker image and
`LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1`; these are executed tests, not skips.
The full migration, identity PostgreSQL and access PostgreSQL packages pass under
`go test -race ... -count=1`. The expanded `task test:go:postgres-conformance`
includes the correction matrix, history prepare/lifecycle, and access lifecycle.

| Final command | Result |
| --- | --- |
| `task test:go:postgres-conformance` | Passed every required Docker-backed gate, including revision 014 and the added identity/access tests; no skips. |
| `go test -race ./internal/platform/postgres/migrations ./internal/project/identityledger/postgres ./internal/access/postgres -count=1` | Passed all three packages. Publication replay/concurrency rerun after adding independent-instance evidence also passed. |
| `go test ./internal/platform/architecture ./internal/project/contractprojection ./internal/project/contractversion ./internal/analytics/query/planir ./internal/analytics/query -count=1` | Passed. |
| `go test ./internal/project/contracts/generate -count=1`; `go test ./internal/app/tools/configgen -count=1` | Passed. |
| `task generated:check docs:check`; targeted `go vet`; `git diff --check` | Passed; no generated snapshot changes. |
| `task ci` | Exit 201: admin browser test `personal API token permissions expose enforceable access levels` exceeded 5 seconds, then the shared browser closed and dependent tests failed. The Go sweep was interrupted; full CI is not qualified. No frontend files were changed. |
| `bun run test:admin-page` (isolated diagnostic retry) | Passed all 21 tests; original timeout did not reproduce. No timeout/assertion weakening or frontend fix was applied. |

Qualification assessment: FAI-662's scoped publication/projection/generation
acceptance is QUALIFIED by the evidence above. FAI-617 remains IMPLEMENTED /
PARTIAL at whole-milestone level: its live repository gates are qualified, but
this repair does not add an end-to-end production-process qualification.
FAI-641 remains IMPLEMENTED / PARTIAL for integrated semantic-access enforcement;
its existing focused planner/architecture tests pass without planner changes.
These assessments do not mark the entire ADR or downstream issues complete.
FAI-642/645/648/649/632 are not started. Revision 014 requires a publication-table
lock and may need a maintenance window; corrupt persistent data requires an
operator-reviewed recovery plan. Deployment history remains unknown, and upstream
Goose authority reconciliation remains separate.

Operational paths, exact lineage checksums and downgrade restrictions are in the
[migration authority guide](../../internal/platform/postgres/migrations/README.md).
Deployment status remains unknown; synthetic historical fixtures are not proof of
deployment. No downgrade or upstream-Goose adoption compatibility is claimed.
FAI-632 and downstream semantic-access milestones remain unopened by this work.

## Maintained verification

Implementation must add focused identity, canonicalization, cross-language
golden, and immutable-publication tests. Every normative YAML example must be
parsed and validated against the generated schema. The final combined change
must also pass:

```sh
task generate
task generated:check
task ci
```
