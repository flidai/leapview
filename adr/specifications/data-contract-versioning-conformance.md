# Data-contract versioning conformance specification

Status: accepted

Profile: `leapview.contract/v1`

Last updated: 2026-09-01

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
- **RID-04:** First activation binds `(instance identity, metadata.id)` to an
  immutable instance resource UID and authored kind. Control-plane grants,
  publications, audit records, and durable references store the UID plus the
  expected authored ID and kind; they do not resolve by name or path.
- **RID-05:** A later candidate with the same authored ID and kind updates the
  same resource UID. A kind change is never an update and is rejected.
- **RID-06:** Removing a resource tombstones its UID and suspends dependent
  control-plane references. Normal deployment cannot assign the tombstoned ID
  to a new UID or automatically reactivate suspended grants or publications.
- **RID-07:** Restoration of a tombstoned logical resource requires an explicit,
  audited control-plane restore operation. Restore reuses the original UID,
  recompiles and revalidates every dependent reference, and leaves grants and
  publications suspended until separately authorized reactivation.
- **RID-08:** Rollback reactivates the historical UID and generation recorded by
  the selected release. It cannot create a new identity or retarget a reference.
- **RID-09:** The same authored ID may exist in different instances because the
  instance identity is the namespace boundary. External ODCS, ODPS, DCAT, and
  lineage projections qualify it with a stable instance or tenant URI; the raw
  authored ID is never claimed to be globally unique.
- **RID-10:** Candidate planning reports created, updated, removed, tombstoned,
  restored, dangling, and collision outcomes before approval. An unresolved or
  ambiguously resolved control-plane reference blocks activation.

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
  the profile, bytes, digest, resource UID, authored ID, kind, and version.
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
| Source root or file moves with unchanged authored ID | Same resource UID and contract digest |
| Two resources share an authored ID | Candidate rejected before graph construction |
| Resource kind changes under an existing ID | Candidate rejected |
| Deleted ID appears in a normal deployment | Tombstone-reuse rejection |
| Explicit restore is approved | Original UID restored; grants remain suspended |
| Description or tags change | Metadata diff only; digest unchanged |
| Field datatype or access grant changes | Digest changes; new version required |
| YAML key order, comments, aliases, or whitespace change | Digest unchanged |
| Equivalent decimal or timestamp spellings | Digest unchanged after typed normalization |
| Existing published version has different bytes | Candidate rejected |
| Canonical profile changes | New profile required; historical digest preserved |

## Evidence ledger

| Requirement range | Evidence | Status |
|---|---|---|
| RID-01–RID-10 | Identity registry, collision, tombstone, restore, rollback, and reference tests | Pending |
| CAN-01–CAN-07 | TypeSpec classification and generated projection checks | Pending |
| SRC-01–SRC-02 | Source canonical golden fixtures | Pending |
| MOD-01–MOD-03 | Model AST and quality-contract golden fixtures | Pending |
| SEM-01–SEM-03 | Semantic and access-contract golden fixtures | Pending |
| SER-01–SER-10 | Cross-language RFC 8785 and typed-normalization corpus | Pending |
| VER-01–VER-06 | Publication immutability, diff, and profile-version tests | Pending |

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
