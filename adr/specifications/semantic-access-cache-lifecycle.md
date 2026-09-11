# Semantic access cache, lifecycle, and audit boundary

Status: implementation in progress (FAI-645).

This boundary extends ADR-0017 on the merged FAI-619/636/637/639/641/642
foundations. It does not activate protected authoring, qualify every consumer,
or complete VAL-11.

## Existing authorities

- Access owns typed registry definitions, assignments, mappings, and their
  monotonic revision/digest pairs. Mutations use expected versions and
  caller-owned PostgreSQL transactions; durable audit participates in commit.
- The semantic compiler owns normalized policy and decision identities. The
  request-bound consumer re-reads coherent authority and rejects decisions
  that no longer match its pinned identity.
- The planner owns pre-relational SecurityBarrier placement and admitted-plan
  provenance. Cache reuse cannot substitute for admission or revalidation.
- Result identity and resultcache own cache keys, retention, generations, and
  coalescing. FAI-642 currently bypasses protected shared-result reuse and
  rejects protected opaque byte/bundle reuse.
- FAI-622 owns immutable contract publication, compatibility/security
  classification, policy evidence, and widening-approval evidence. The
  Project adapter validates historical evidence against exact canonical bytes
  before detaching its identity for analytics reuse.
- Canonical Access audit owns durable authorization evidence. Query-history
  logging is not a substitute for required security audit persistence.

## Cache boundary

Extend existing result dependencies with detached, value-free semantic
authorization evidence: serving scope/generation, principal and actor,
profile, policy/decision digests, registry/control revision and digest,
effective-attribute digest, trusted evidence identity where supported, and the
exact FAI-622 publication/policy/approval evidence digests. Public dependency
serialization must retain its existing meaning.

A cache hit requires fresh validation of the same admitted policy and plan.
Repeat validation before insertion and delivery, including coalesced waiters.
Changed authority invalidates eligibility immediately; neither TTL nor an
unchanged SQL string may preserve access. Existing cache invalidation mechanisms
must prevent stale in-flight work from repopulating invalidated entries.
Physical eviction is a retention optimization, not the security authority.

No separate compiled-policy cache exists at this boundary. A consumer's pinned
compiled policy is request-local and must continue failing closed after an
authority change. Unsupported shared suggestion, rollup, bundle, and opaque
byte paths remain bypassed or denied until they carry equivalent evidence.

## Lifecycle boundary

Reuse FAI-637's state machines; do not create a second lifecycle store:

- Definitions: create active; metadata update; disable; explicit re-enable.
  Each effective change advances the definition and registry revisions.
  Definition deletion and identity/type/shape rewrites remain rejected.
- Assignments: create/update; tombstone removal; a later set creates a new
  incarnation rather than restoring the tombstone in place.
- Mappings: create; identity-preserving replay; tombstone removal; a later
  creation retains the historical tombstone.

Expected-version conflicts reject stale writes. Serialized registry/control
updates and coherent read snapshots prevent mixed authorization observations.
Replays must retain existing revision semantics and durable audit behavior.
Disable/re-enable or remove/recreate cannot reuse an older decision merely
because effective values happen to match again.

## Audit boundary

Extend existing canonical audit with bounded policy/decision evidence,
principal/actor, serving identity, affected semantic targets, source,
correlation, and deterministic reasons. Never include raw attribute values,
claims, predicate literals, SQL parameters, or unrestricted error payloads.
Required security audit failure must deny the corresponding authorization or
release; optional query-history logging cannot turn that failure into success.

Registry/control mutation events remain distinct from authored policy
deployment evidence. Use their existing durable event and revision identities;
do not invent a second audit store or lifecycle sequence.

Consumer observations use `semantic_access.consumer_bind`,
`semantic_access.discovery`,
`semantic_access.authorization`, `semantic_access.plan_admission`,
`semantic_access.plan_validation`, and `semantic_access.plan_invalidation`.
Production composition requires the canonical recorder for protected models;
the pure planner library can still be used without a persistence adapter.
Successful admission is retained only after its required observation persists.
Invalidation records describe the rejected pinned decision, not an invented
replacement decision when fresh authority cannot be obtained.
Constructor denials without a decision explicitly mark decision evidence
unavailable and omit policy/decision digests. Available pinned evidence is
not a claim that stale authority remains valid. Discovery records hidden
denials as well as visible assets; security admission records denied members
and sorted participating datasets. Ordinary query syntax/rendering diagnostics
remain with existing query-history tooling.
If a trusted serving scope/principal or the recorder itself is unavailable,
composition still denies access; it cannot manufacture a canonical audit
identity or promise persistence through an unavailable audit sink.

Canonical audit preserves its existing append-only row IDs and deterministic
intent digest. Repeated observations may produce distinct rows with the same
intent; this is not an exactly-once delivery or event-deduplication protocol.
Fresh authority reads and required audit writes remain on protected cache-hit
paths. Result caching saves analytical execution, not authorization work;
this slice does not claim reduced authorization latency or qualification of
its operational throughput.

## Dependencies and explicit limits

The older unpublished FAI-645 cache/lifecycle and policy-evidence branch
ancestry was not imported. Current FAI-622 publication authority is consumed
through one narrow adapter that calls historical publication validation and
copies only its immutable identity and evidence digests; cache code does not
classify contracts or approve widening. Protected cache reuse fails closed
when this exact publication evidence is unavailable. Selecting the active
publication for a serving generation remains FAI-649 activation/cutover work,
so this slice exposes the validated injection boundary without inventing a
latest-publication lookup or activation rule.

FAI-648 owns exhaustive qualification. FAI-649 owns production activation,
approval/cutover, and legacy DataPolicy removal. Provider adapters, new consumer
integrations, UI/admin workflows, and VAL-11 completion are excluded.
Already delivered rows cannot be retracted; revalidation guards subsequent
lookup, admission, storage, and output boundaries, not atomic retroactive
revocation of external data.

## Validation evidence

Capability-specific evidence includes protected dependency identity
partitioning and detached serialization, guarded cache reuse and coalesced
waiters, consumer audit persistence failures, and PostgreSQL lifecycle
revision/audit/concurrency checks. The PostgreSQL checks exercise real
transactions rather than interpreting a Docker skip as a pass.

The current-main reconciliation adds exact FAI-622 genesis/update replay,
widening-approval binding, tamper rejection, and publication/policy identity
rotation evidence. Final repository-wide validation is recorded with the
FAI-645 handoff; this document does not turn FAI-645 checks into FAI-648
qualification or claim that FAI-649 activation selection is complete.
