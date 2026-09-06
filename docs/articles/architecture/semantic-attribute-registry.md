# Semantic attribute registry and control plane

FAI-637 adds the PostgreSQL control-plane half of the `leapview.semantic-
access/v1` profile. The registry defines what an attribute is; control rows
record which subjects receive canonical values and which trusted provider
claims may supply a value. FAI-639 adds the SemanticModel compiler/evaluator
boundary that qualifies authored policy against a registry snapshot and
evaluates it against a trusted effective-value snapshot. This is still not the
complete semantic query authorization engine: FAI-641 provides planner barriers,
while catalog filtering, consumer adapters, cache invalidation, and generation-
reference checks remain pending under
[ADR-0017](../../../adr/0017-adopt-a-looker-aligned-semantic-access-contract.md).

## Capability ownership and authority

The access capability owns the access-owned forward migrations, sqlc query
sources, domain contracts, repositories, validation, and audit projections.
The generic PostgreSQL migration runner owns only advisory locking, ordered
revision application, checksum evidence, and replay verification. Baseline
revision 1 is immutable; FAI-636 owns revision 2 and FAI-637 owns revision 3.

There are three distinct kinds of authority:

| State | Authority and purpose | Not implied |
| --- | --- | --- |
| `semantic_attribute_definition` and `semantic_attribute_registry` | Access control plane defines the stable name, logical type, shape, profile, stewardship metadata, lifecycle, revision, and registry digest. | Owning an attribute does not grant a principal access to data or assign a value. |
| `semantic_attribute_assignment` and `semantic_attribute_control_state` | Access control plane binds canonical values to a principal or group and maintains the control snapshot identity. Group membership is resolved from access membership state. | An assignment is not a semantic-model policy and does not by itself authorize a query. |
| `semantic_attribute_claim_mapping` | Access control plane records a source-bound provider/issuer/audience/claim name and its typed definition. The mapping has no value payload. | A mapping does not authenticate a claim or make an unverified request value trusted. |

`owner_kind`/`owner_id` on a definition is stewardship metadata for the
attribute record. It is deliberately separate from an assignment target and
from the durable platform role used to administer the control plane. An
instance-owned attribute may be assigned to subjects; a principal- or
group-owned definition does not make that subject an administrator.

The existing `principal.attributes` and `access_group.attributes` JSON
columns are legacy access metadata (including SCIM-facing metadata). They are
not registry definitions, assignments, claim mappings, typed values, or
authorization evidence. The FAI-637 tables are the only typed semantic
attribute authority.

## Platform-admin authorization and attenuation

The semantic-attribute HTTP endpoints are platform administration endpoints.
The generated transport contract describes them as requiring an authenticated
principal; the handler adds the stronger platform-admin guard. The request
flow is:

1. authentication resolves the canonical principal;
2. `RequestPlatformAdmin` rechecks the durable, instance-wide platform role
   through `PlatformAdminReader.IsPlatformAdmin`; and
3. the request credential, if present, can only attenuate that role.

A browser/session request with no API credential inherits the durable role. An
authoring credential is denied on this platform-admin surface. An API token
with nil capabilities inherits the role, an explicit empty capability list is
deny-all, and a non-empty list must contain `PROJECT_ADMIN`. A credential for
another principal is denied. No project authorization snapshot can create a
platform role. The explicit local-development `DevBypass` is accepted only by
the non-production development configuration; it is not a production
authorization path.

This flow means a credential may reduce a platform administrator's authority,
never elevate a non-administrator. Definition, assignment, mapping, preview,
and semantic-attribute audit routes all use this guard and fail closed when
the repository or durable role check is unavailable.

## Definition identity and lifecycle

Each `access.semantic_attribute_definition` row contains:

- an immutable UUID and case-sensitive SemanticModel identifier name;
- one logical type: `String`, `Boolean`, `Integer`, `Decimal`, `Date`, or
  `Timestamp`;
- a `scalar` or homogeneous `list` shape;
- profile `leapview.semantic-access/v1`;
- stewardship owner (`instance`, `principal`, or `group`);
- display name, description, and optional credential-free HTTPS documentation
  URL; and
- database-owned timestamps, lifecycle, and a positive definition version.

The UUID, name, type, shape, profile, and creation timestamp cannot change.
Metadata changes and enable/disable transitions advance the definition version
exactly once. Deletion is rejected. Disablement sets a database-owned
`disabled_at`; restoration clears it through the same guarded transition.

```text
absent --register(v1)--> active
active --metadata update (v+1)--> active
active --disable (v+1)--> disabled
disabled --metadata update (v+1)--> disabled
disabled --restore (v+1)--> active
active/disabled --delete or type/name/shape/profile rewrite--> rejected
```

Registering an existing name with the same type and shape is an idempotent
replay; a different type or shape is a compatibility conflict. The stable
registry singleton advances `registry_revision` and replaces its SHA-256
`registry_digest` for each effective definition change. The digest is
recomputed from the ordered definition projection on reads, and a mismatch
fails closed. Replays do not advance either identity.

## Assignment and mapping lifecycles

Assignments are immutable incarnations of a definition/subject binding. A
subject is a principal or group, and each active definition/subject pair has
one active assignment. Values are canonicalized before storage; scalar values
have one element and list values are bounded homogeneous sets.

```text
absent --set(expected=0, v1)--> active
active --set(expected=current, canonical values changed, v+1)--> active
active --remove(expected=current, v+1)--> tombstoned
tombstoned --set--> new active assignment identity (old row retained)
tombstoned --restore or delete--> rejected
```

Tombstone time is database-owned and tombstones are retained. A new assignment
incarnation receives a new ID rather than reviving history. Assignment writes
carry `definition_id`, definition version, type, and shape so persisted state
cannot silently change meaning; database triggers and repository validation
reject disabled or incompatible definitions and invalid subjects.

A trusted claim mapping binds the exact source kind, provider, issuer,
audience, claim name, and definition. Source identity is closed to `saml`,
`oidc`, `embed`, and `service_token`; provider and claim names are exact and
bounded. Mapping rows have no value payload.

```text
absent --set(expected=0, v1)--> active
active --same identity replay--> active (no state advance)
active --remove(expected=current, v+1)--> tombstoned
tombstoned --set--> new mapping identity (old row retained)
tombstoned --restore or in-place identity rewrite--> rejected
```

Mapping identity includes the source tuple, exact claim, and target
definition. A desired remap is represented by a retained tombstone and a new
mapping row; it must not mutate an existing row in place. If simultaneously
resolved sources provide different canonical values for one definition, the
effective-value resolver returns a source conflict rather than selecting a
winner. Equal direct and trusted values may be represented as one effective
value with a combined source marker.

## Concurrency, control identity, and audit

Definition mutations lock the registry singleton. Assignment and mapping
mutations lock the independent `semantic_attribute_control_state` singleton.
The control state records profile `leapview.semantic-access/v1`, a monotonic
`control_revision`, and a deterministic SHA-256 `control_digest` over ordered
active and tombstoned assignment/mapping projections. The registry and control
identities are intentionally separate: a definition metadata/lifecycle change
advances registry identity, while assignment/mapping changes advance control
identity.

Updates use `If-Match`/expected versions at the HTTP and repository boundary.
Expected version zero creates; updates and tombstones require the current
positive assignment or mapping version. A stale version is a conflict. The
database requires each mutable update to advance exactly once, rejects
identity/type rewrites, and rejects control-state rewrites that do not advance
the revision with a new digest. Idempotent replays preserve versions and
identities.

Top-level repository mutations run the state change, control digest advance,
and audit append in one transaction. The `...Tx` variants accept a
caller-owned transaction and append the event before the caller commits. A
failed audit append rolls back the state mutation. Audit metadata records
stable IDs, definition/type/version, source identity, value count, control
revision/digest, actor, request, and correlation identity; it never
records canonical value payloads or raw provider claim values. The closed API
value envelope uses typed scalar fields, keeps integer/decimal precision, and
rejects maps, nested lists, executable values, nulls, and oversized lists.

## Trusted verifier boundary

`internal/access/trustedclaims` is an admission boundary, not an identity
provider. `Verify` accepts raw source evidence only together with a
source-bound cryptographic verifier. The source adapter owns signature, issuer,
audience, nonce, key, and token validation. The package enforces exact source
binding, trust identity, issued/expiry interval, fingerprint shape, exact
claim names, duplicate rejection, and scalar/homogeneous-list structure. It
returns an opaque envelope with private fields, copied values, and no raw
evidence; callers cannot forge an envelope by constructing its fields.

The four source names are vocabulary and boundary checks only in this slice.
No SAML, OIDC, embed, or service-token provider integration is claimed. The
durable mapping repository and effective resolver are intended to consume
claims only through the opaque `trustedclaims.Envelope`; wiring real provider
adapters through `Verify`, deriving a principal context, and passing that
context to semantic consumers remain pending.

## FAI-639 compiler/evaluator boundary

The generated TypeSpec SemanticModel contract remains the authoritative
portable input. The project compiler lowers it into `SemanticAccessPolicy` as
a target-independent, detached runtime model. Lowering validates the closed
shape, attribute and grant names, non-empty grant/filter lists, duplicate
references, known grant references, and exact literal kinds. JSON number
tokens retain their authored spelling until a registry type is available;
instance definitions, assignments, claims, and effective values never enter
the portable policy. `ExecutionSnapshot` deep-copies the policy's mutable
maps and slices.

`CompileSemanticAccessPolicy` then qualifies that portable policy against one
complete registry snapshot and one compiled semantic lineage. It recomputes the
FAI-636 registry digest before it binds stable
definition IDs and versions, requires active profile-v1 definitions with
valid stewardship ownership, canonicalizes grant values using the registered
logical type, rejects canonical duplicates, and validates that each access
filter names a directly bound compatible semantic dimension. Required grants
are propagated through dataset and relationship paths, dimensions, metric
dependencies, and referenced filter dimensions. The output is immutable
qualified policy metadata with deterministic canonical bytes and a policy
digest. Compilation is registry-qualified; it does not read assignment or
mapping values and it does not evaluate a principal.

Evaluation consumes two explicit inputs:

```text
compiled policy + effective attribute snapshot + current registry/control authority
                                  |
                                  v
                         SemanticAccessDecision
```

`SemanticAccessAttributeSnapshot` is a trusted handoff of already-resolved
effective values. It contains the instance and principal/actor identity,
complete registry and control snapshots, canonical values, each value's
definition identity/type/shape/source, and an effective-attribute digest.
`EvaluateSemanticAccess` recomputes all three digests, checks canonical ordering
and value digests, rejects invalid sources or definition mismatches, requires
the authority instance to match the target, and requires captured state to
match current authority. Claim-derived values additionally carry immutable
`SemanticAttributeClaimEvidence` created by the access capability from the
opaque verifier envelope, exact active mappings, mapped canonical values,
control identity, target/principal, and validity interval. Evaluation checks it
at the current authority observation time. The evaluator does not fetch
repositories, verify raw provider tokens, or make a browser value trusted;
malformed, expired, stale, missing, or tampered snapshots fail closed.

Direct-derived values (`direct` or `direct+trusted_claim`) require opaque
`SemanticAttributeDirectEvidence` from the access capability. It binds the
target instance and principal to the exact control snapshot, the principal plus
active-group subject closure, the active assignment IDs/versions, and the
effective value identities. An effective-value digest alone is not assignment
authority. Claim-derived values (`trusted_claim` or `direct+trusted_claim`)
retain the verified-envelope evidence: opaque claim evidence binds the same
control state and principal to the exact source, mapping IDs/versions,
validity interval, and verifier fingerprints. Raw assignments, values, and
claims remain outside these identity projections.

The subject closure used to seal direct evidence is trusted access-capability
input. FAI-642 composition must source it from the authoritative principal/group
resolver; consumer, request, and browser code must never provide or widen that
closure.

For a valid snapshot, scalar grant values use equality and list values use
overlap; required grants compose with logical AND. A dataset access filter is
lowered to a typed PlanIR predicate: scalar values become `compare` with `=`,
list values become `in`, and multiple filters become `and`. The PlanIR field is
the compiler-resolved physical field and each value is a typed string,
boolean, integer, decimal, date, or timestamp literal. No SQL, template, or
author-supplied operator is emitted. Dataset predicates and denials are
inherited by member decisions. Each multi-dataset dimension or metric decision
also carries `DatasetPredicates`, a complete map from every required dataset
(including relationship/dependency datasets) to that dataset's typed predicate;
`Predicate` is only the primary-dataset convenience view. FAI-641 must not
infer that one predicate covers a multi-dataset object.

The effective-attribute digest is a deterministic ordered projection of
definition ID/version/type/shape, value digest, and source; it excludes raw
values. Policy digest is deterministic canonical policy metadata. Decision
identity additionally binds the profile, target instance, semantic model and
generation, principal/actor, registry and control identities, effective
attribute digest, policy digest, grant results, filter evidence, and sorted
dataset/dimension/metric outcomes. When present, direct-assignment and
verified-claim evidence digests are also bound. Filter evidence carries value
digests and predicate kinds rather than unrestricted values. These identities
are inputs for later audit and cache partitioning, not evidence that cache
invalidation or consumer enforcement is wired.

FAI-641 consumes the evaluator's typed predicates and installs security
barriers at protected scan occurrences before joins, outer-join null extension,
and aggregation. FAI-642 separately owns discovery, catalogs, and consumer
composition. FAI-639 does not perform that placement or enforce a query;
the existing generic/legacy access paths are not FAI-639 semantic-consumer
evidence.

## Registry, effective values, and consumers

The control-plane relationship is:

```text
definition registry (what/name/type/shape/lifecycle)
        +
direct assignments (principal/group -> canonical values)
        +
trusted mappings (source identity + exact claim -> definition)
        + authenticated claim envelope (future source adapter)
        |
        v
effective subject attributes (canonical values, source conflict checked)
        |
        v
FAI-639 policy compiler/evaluator
        |
        v
typed PlanIR predicates + decisions (planner handoff)
        |
        v
FAI-641 planner barriers
        |
        v
FAI-642 discovery and governed semantic consumers (pending)
```

FAI-637 provides the first three boxes, shared canonical value validation,
durable control identities, effective direct/group resolution behind the
opaque `trustedclaims.Envelope` boundary, and platform-admin management APIs.
FAI-639 consumes complete digest-verified registry/control snapshots, trusted
direct-assignment and verified-claim evidence, and an effective-value snapshot
at the compiler/evaluator boundary; it does not make repositories or raw claims
available to portable policy artifacts. It does not yet attach
predicates to governed scans, filter catalogs, or execute queries for
dashboards, Explore, agents, exports, APIs, MCP, or embedding. FAI-641 owns
the [planner barrier boundary](/docs/architecture/semantic-access-planner); FAI-642 separately
owns discovery and consumer integration. Existing generic/legacy access paths
therefore must not be described as FAI-639 semantic-consumer evidence.

## Immediate invalidation identity

FAI-637 establishes the durable inputs needed for immediate invalidation, and
FAI-639 computes the effective-attribute digest from an ordered,
profile-qualified projection of `(definition ID/version/type/shape,
valueDigest, source)`. It never contains raw values. Runtime trusted input
also binds its source credential/token fingerprint and validity interval.
FAI-639 does not publish cache events or implement consumer caches.

PostgreSQL registry and control readers use READ COMMITTED statement snapshots,
read state before and after the row projection, and retry once when the
revision, digest, or profile changes during the read. A second change or a
row/digest mismatch fails closed. Effective-value resolution applies the same
bounded before/after check across registry and control state. The persisted
registry/control digest wire projections are unchanged: snapshot admission
derives and rejects inconsistent lifecycle, owner/name, definition-reference,
and tombstone projections without adding those derived fields to the digest
wire.

Every authorization-sensitive cache key must include at least the instance
identity, semantic generation, principal identity, registry
`(profile, registry_revision, registry_digest)`, control
`(profile, control_revision, control_digest)`, effective attribute-set
identity, and normalized semantic policy identity. A committed definition
change invalidates by registry identity; a committed assignment or mapping
change invalidates by control identity and, when known, affected definition and
subject. A digest mismatch is a conservative immediate invalidation signal,
not permission to continue with stale state. Cache/event propagation and
consumer use of this identity are LIF- and consumer-integration work still
pending under ADR-0017.

## Current evidence and limits

The PostgreSQL registry tests cover deterministic registry identity, stable
registration/replay, metadata versioning, disablement, type immutability,
canonical values, and transactional audit evidence. The trustedclaims tests
cover the opaque verifier envelope, source binding, temporal checks, exact
claims, copying, fingerprints, and structural value rejection. FAI-639 tests
cover authoritative lowering, registry qualification, lineage propagation,
opaque direct-assignment and verified-claim evidence, trusted snapshot and
stale-authority rejection, complete multi-dataset predicate maps,
deterministic policy/decision identities, bounded reader checks, and typed
PlanIR predicate output. Snapshot admission preserves the digest wire formats
while validating derived lifecycle/name/tombstone consistency. FAI-641 adds
planner barrier validation and pre-join/aggregation execution coverage;
downstream consumer authorization remains FAI-642 work.

VAL-11 therefore remains **Partial**: the shared `internal/semanticvalue`
canonicalizer is used at registry/assignment/mapping ingress and FAI-639
compiler/evaluator paths, while generated canonicalization, complete
control-plane claim-ingestion equivalence, candidate validation, planner
evaluation, cache invalidation, and complete audit projection evidence remain
unqualified.
