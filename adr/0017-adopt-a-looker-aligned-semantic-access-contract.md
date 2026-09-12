# ADR-0017: Adopt a Looker-aligned semantic access contract

Status: accepted

Decision date: 2026-09-01

Implementation: active for the qualified supported profile

Deciders: LeapView maintainers

Supersedes: none

Amends: none

Related: [ADR-0006](0006-adopt-ossie-aligned-semantic-contract.md);
[ADR-0015](0015-adopt-durable-audit-and-compliance-controls.md);
[ADR-0016](0016-adopt-standards-aligned-data-contracts-and-interchange.md);
[Semantic access-policy conformance](specifications/semantic-access-policy-conformance.md);
[Semantic access activation and cutover](specifications/semantic-access-activation-cutover.md);
[Looker `access_grant`](https://docs.cloud.google.com/looker/docs/reference/param-model-access-grant);
[Looker `access_filter`](https://docs.cloud.google.com/looker/docs/reference/param-explore-access-filter);
[Looker access control](https://docs.cloud.google.com/looker/docs/access-control-and-permission-management);
[Lightdash user attributes](https://docs.lightdash.com/workspace-admin/user-attributes);
[Rill data access control](https://docs.rilldata.com/developers/build/metrics-view/security)

## Context and problem statement

LeapView's SemanticModel is the governed consumption layer. Dashboards,
Explore, agents, exports, embedded requests, and headless semantic queries must
all resolve datasets, dimensions, measures, and metrics through the semantic
planner. Source and Model describe ingestion and transformation; they are not
consumer authorization surfaces.

The current authored `DataPolicy` resource crosses this boundary. It can target
a Source, Model, or SemanticModel, names a principal, group, service principal,
or dashboard publication, and carries a separate row-filter or column-mask
expression. Consequently the data restriction is detached from the semantic
members it governs, identity records enter analytics source, and changing group
membership or publication state can require an analytics deployment. ADR-0016
removes that standalone resource and assigns identity, membership, grants,
sharing, and publication to the instance control plane.

Mature semantic products distinguish policy definitions from assignments.
Looker defines named `access_grant` conditions from user attributes, attaches
`required_access_grants` to Explores, joins, views, and fields, and connects an
Explore field to a user attribute through `access_filter`. Administrators own
the user attributes and their values. This matches LeapView's consumption
boundary and does not require a general policy language.

Rill places `access`, `row_filter`, `include`, and `exclude` under a metrics
view, which confirms the correct ownership location, but its values are SQL and
Go-template expressions. Lightdash similarly uses model-level SQL filters and
attribute templates. Copying either contract one-for-one would add executable
strings, dialect behavior, and injection-sensitive templating to LeapView's
governed query boundary. Cedar, OpenFGA, and SCIM address object authorization
or identity provisioning rather than semantic row filtering.

LookML is a product language, not a vendor-neutral YAML standard. LeapView can
therefore align with its proven three-part policy model but cannot claim Looker
document compatibility or conformance.

The question is which small authored contract should govern semantic dataset,
row, and member access while keeping identities in the control plane and every
query path fail-closed.

## Decision drivers

- Make SemanticModel the only authored data-access-policy target.
- Keep identity records, group membership, role assignments, grants, and
  attribute values out of analytics YAML.
- Express dataset, row, and semantic-member restrictions without arbitrary SQL,
  templates, scripts, or a general authorization language.
- Resolve every policy reference structurally and contextually before
  activation.
- Apply identical restrictions to every semantic query consumer, including
  discovery, suggestions, exports, agents, APIs, and embedding.
- Fail closed on missing, empty, invalid, stale, or untrusted attributes.
- Keep query planning deterministic, parameterized, auditable, and safe for
  authorization-aware caching.
- Prefer a small established semantic abstraction over a LeapView-specific
  policy framework.

## Considered options

### Keep standalone subject-bound DataPolicy resources

This retains the existing compiler path and permits direct policies for
Sources and Models. It also duplicates policies for groups, binds portable code
to instance identities, separates rules from the semantic fields they govern,
and lets repository deployment mutate administrative state. It conflicts with
the authored/control-plane boundary selected by ADR-0016.

### Copy Rill metrics-view security one-for-one

Rill covers resource access, row filtering, and field inclusion or exclusion
at the correct consumption layer. Its `access`, `row_filter`, and conditional
field rules are nevertheless SQL and template strings. Exact adoption would
make dialect parsing, template evaluation, quoting, and injection behavior part
of LeapView's public security contract and would bypass the closed semantic
planner.

### Use Lightdash model filters one-for-one

Lightdash has strong attribute-driven row and column behavior, but its rules
are centered on dbt or Lightdash models and use SQL templating. LeapView already
has a distinct SemanticModel consumption layer, so model ownership and
executable filter strings are both a poor fit.

### Adopt a general policy engine or warehouse row-level security

Cedar, OpenFGA, OPA, and warehouse policies can be useful enforcement or
integration layers. None supplies a portable semantic YAML contract covering
dataset discovery, semantic member visibility, semantic-field filtering, and
all LeapView query consumers. Making one authoritative would also split policy
meaning between the semantic compiler and an external engine or target.

### Adopt Looker's access-grant and access-filter model

Looker's model separates reusable attribute conditions, protection of semantic
objects, and user-specific row filters. LeapView can express the same concepts
as closed generated YAML, replace Looker's filter-expression strings with typed
scalar and list matching, and enforce them in the existing semantic planner.

## Decision outcome

LeapView adopts a Looker-aligned semantic access contract. SemanticModel is the
only authored policy target. The standalone `DataPolicy` resource is removed
as required by ADR-0016, and Source, Model, Dashboard, Pipeline, and Connection
do not gain authored access-policy blocks.

The initial contract has exactly three concepts:

- `accessGrants` defines named attribute conditions in the SemanticModel;
- `requiredAccessGrants` attaches one or more named grants to a dataset,
  dimension, measure, or metric; and
- `accessFilters` attaches a semantic dimension to a principal attribute at a
  dataset boundary.

The YAML uses LeapView's existing camel-case convention while preserving the
meaning of Looker's `access_grant`, `required_access_grants`, and
`access_filter`. Public documentation describes the contract as
**Looker-aligned**, never Looker-compatible or Looker-compliant.

An illustrative contract is:

```yaml
apiVersion: leapview.dev/v1
kind: SemanticModel
metadata:
  id: semantic_model:sales
  name: sales
spec:
  accessGrants:
    canViewSales:
      userAttribute: department
      allowedValues: [sales, finance]
    canViewPII:
      userAttribute: piiAccess
      allowedValues: [full]
  datasets:
    orders:
      model: orders
      requiredAccessGrants: [canViewSales]
      accessFilters:
        - field: region
          userAttribute: allowedRegions
  dimensions:
    region:
      datatype: String
      bindings:
        orders:
          field: orders.region
    customerEmail:
      datatype: String
      bindings:
        orders:
          field: orders.customer_email
      requiredAccessGrants: [canViewPII]
```

### Attribute ownership, stewardship, and matching

The instance access control plane owns the typed attribute registry, direct
principal/group assignments, trusted claim-mapping records, and resulting
effective values. `owner_kind`/`owner_id` on a registry definition is
stewardship metadata, not an access grant or an administrator role. Assignment
targets and trusted source identities are separate from definition ownership.
SCIM may provision users and groups but does not define the authorization
meaning of an attribute. Analytics YAML may reference only a canonical
attribute name; it cannot declare values, identities, assignments, or
claim-extraction rules.

FAI-637's PostgreSQL control state is split deliberately: the definition
registry has `(profile, registry_revision, registry_digest)` identity, while
assignments and mappings have an independent
`(profile, control_revision, control_digest)` identity. Definitions describe
name/type/shape/lifecycle; assignments bind canonical values to subjects;
mappings bind an exact source/provider/issuer/audience/claim to a definition
without persisting a claim value. Direct principal and active-group
assignments and claims from the opaque `trustedclaims.Envelope` may be resolved
together. A
source conflict is an error, never an implicit precedence decision.

Registry and control snapshot admission validates the full row projection in
addition to recomputing the persisted digest: lifecycle and stewardship/name
invariants, definition references and type/shape agreement, and assignment or
mapping tombstone state must be consistent. These derived checks do not change
the registry/control digest wire formats. PostgreSQL readers perform bounded
before/after state validation under READ COMMITTED and retry once; a second
change or a row/digest mismatch fails closed. Effective-value resolution also
checks registry and control state before and after resolution.

The initial value types are typed scalar and homogeneous list values supported
by the semantic field vocabulary. A scalar attribute satisfies an access grant
when it equals an allowed value. A list attribute satisfies it when at least
one element equals an allowed value. Multiple `requiredAccessGrants` use logical
AND. Missing, empty, type-incompatible, or untrusted values do not satisfy a
grant.

Profile `leapview.semantic-access/v1` defines the exact logical types,
canonical spellings, Unicode behavior, null handling, cross-type prohibition,
set normalization, and the fixed 1,024-value list bound. The same generated
canonicalizer governs control-plane input, claim ingestion, compilation,
runtime evaluation, cache identity, and audit projection.

An access filter maps a scalar attribute to equality and a list attribute to a
parameterized membership predicate. Multiple access filters use logical AND.
The referenced field must be a dimension bound to the selected dataset and the
attribute type must be compatible with the dimension datatype. Missing, empty,
invalid, or incompatible values deny the query; no wildcard, administrator
bypass, or implicit unfiltered fallback exists.

The control plane rejects deletion or type mutation at the registry identity
boundary. Generation-reference retention checks and dependent semantic-object
invalidation are required future integrations, not evidence supplied by
FAI-637. It may explicitly disable the attribute as an audited emergency
revocation; consumers must then fail closed. This is not a semantic-evaluator
bypass. Break-glass access is absent from this profile and would require a
separate control-plane security ADR.

### Enforcement boundary

#### Control-plane administration boundary

Registry, assignment, mapping, impact-preview, and semantic-attribute audit
operations are platform-admin operations. Authentication first resolves the
canonical principal. The request then checks the durable instance-wide
platform role through `RequestPlatformAdmin`/`IsPlatformAdmin`; a project
authorization snapshot cannot create that role. Request credentials can only
attenuate it: a session with no API credential inherits the role, an authoring
credential is denied, an API token with nil capabilities inherits, an explicit
empty capability list denies, and a non-empty capability list must include
`PROJECT_ADMIN`. A credential for a different principal denies. The explicit
development bypass is limited to non-production configuration. Repository or
role-check failure fails closed.

This is the administration gate for the control-plane state. It does not make
an administrator an unconditional semantic-consumer bypass; semantic
authorization uses the separate FAI-641 planner and FAI-642 consumer boundaries
for the qualified supported profile. Unsupported paths remain fail closed.

The following grant/filter rules are the decided target boundary. The
qualification ledger, rather than FAI-637 alone, identifies which consumer
combinations have executable evidence.

Access grants control both discovery and execution. A denied dataset or member
is absent from the authorization-filtered catalog and is rejected if addressed
directly by ID or name. Hiding a member in the UI is not enforcement.

Access filters lower through the typed semantic planner into bound parameters.
They are enforced for dashboards, Explore, raw-value and suggestion queries,
agents, exports, scheduled execution, APIs, and embedded requests. No consumer
may opt out, skip nested security, provide precompiled SQL, or substitute its
own filter expression.

Every protected dataset occurrence enters the plan behind a typed security
barrier at its governed scan. The barrier applies before joins, outer-join null
extension, aggregation, suggestions, totals, rollups, caching, and
consumer-supplied filters. An optimizer may push it into an equivalent scan but
may not pull it above a join or aggregate, reduce it to a final `WHERE`, or lose
it during rewrite.

A dataset requiring an access-filter attribute is absent from discovery when
that attribute is missing, empty, invalid, untrusted, out of bounds, or type
incompatible, and direct execution is denied. A valid attribute permits
discovery even if the authorized row set happens to be empty; discovery never
probes data to infer authorization.

Authorization identity, effective grant results, normalized access filters,
semantic generation, and trusted-attribute version participate in query and
result-cache identity. Durable audit records identify the policy inputs and
outcome without recording unrestricted sensitive attribute values.

#### Control-plane lifecycle state machines

The durable control-plane rows use explicit, forward-only state transitions:

```text
Definition:
  absent --register(v1)--> active
  active --metadata/disable (version +1)--> active/disabled
  disabled --metadata/restore (version +1)--> disabled/active
  active or disabled --delete or identity/type/shape/profile rewrite--> reject

Assignment:
  absent --set(expected=0, v1)--> active
  active --set(expected=current, values changed, version +1)--> active
  active --remove(expected=current, version +1)--> tombstoned
  tombstoned --set--> new active incarnation (new ID, version 1)

Trusted claim mapping:
  absent --set(expected=0, v1)--> active
  active --same identity replay--> active (no state advance)
  active --remove(expected=current, version +1)--> tombstoned
  tombstoned --set--> new mapping incarnation (old row retained)
  active or tombstoned --in-place identity rewrite/restore--> reject
```

Definition changes lock and advance the registry singleton; assignment and
mapping changes lock and advance the independent control singleton. Expected
versions are checked at transport and repository boundaries, stale versions
are conflicts, and database-owned timestamps/tombstones plus immutable IDs and
types prevent silent rewrites. Top-level mutations couple the state change,
digest advancement, and audit append in one transaction; explicit transaction
helpers preserve the same rule for callers that own the transaction.

### Deliberate initial limits

The initial contract does not include arbitrary access predicates, SQL row
filters, templates, regular expressions, group-name tests, explicit deny rules,
policy inheritance, reusable cross-model policy resources, or column masking.
Semantic member denial covers the initial column-security requirement.

If masked-but-visible values become a demonstrated requirement, a later ADR
must define a closed semantic masking extension, its aggregation behavior,
planner placement, export semantics, cache identity, and conflict rules. It
must not be added under the claim of Looker alignment.

The exact generated schema, evaluation rules, diagnostics, evidence, and
qualification matrix are maintained by the linked semantic access-policy
conformance specification under profile `leapview.semantic-access/v1`.
Normative changes to its shape, canonicalization, evaluation, planner,
discovery, cache, or compatibility behavior require a new profile; new policy
concepts, targets, bypasses, precedence, or masking require another ADR.

### Implemented authority and activation boundary

FAI-637 owns the PostgreSQL registry/control plane; FAI-639 compiles and
evaluates typed policy; FAI-641 places SecurityBarriers; FAI-642 routes the
qualified consumers; and FAI-645 binds cache, lifecycle, and audit evidence.
FAI-649 activates that already-qualified chain by sealing FAI-622 publication,
policy, approval, registry/control, compiled-policy, and enforcement-profile
identities into the delivery plan and revalidating them immediately before the
activation CAS. The exact cutover and rollback rules are in the linked
activation specification. Unsupported provider, consumer, and plan paths
remain fail closed rather than being activated implicitly.

The source names SAML, OIDC, embed, and service token are accepted as closed
mapping/verifier vocabulary only. FAI-637 does not claim an OIDC, SAML, embed,
or service-consumer adapter. The current effective-value resolver's claim
input is the opaque `trustedclaims.Envelope` boundary. FAI-642 supplies
request-bound direct/group principal context for qualified consumers; wiring a
real external provider through the verifier remains outside the supported
profile.

FAI-619 qualifies the generated structural SemanticModel contract and
compatibility lowering boundary. FAI-639 supplies compiler/evaluator behavior,
FAI-641 supplies planner barriers, and FAI-642 supplies consumer admission.
VAL-11 remains Partial until generated canonicalization and complete
control-plane/runtime equivalence are evidenced.

### FAI-639 compiler/evaluator boundary

FAI-639 preserves the authority split rather than moving instance state into
the portable project artifact. The generated TypeSpec SemanticModel contract
is lowered by the project compiler into the runtime `SemanticAccessPolicy`.
That lowering is target-independent: it validates the closed shape, names,
non-empty lists, duplicate references, and exact scalar/list literals (number
tokens retain their JSON spelling), but it does not resolve registry
definitions or principal values. The executable semantic-model snapshot
deep-copies this policy so serving state cannot be changed through authored
maps or slices after activation.

`CompileSemanticAccessPolicy` is the target-qualification boundary. It accepts
one target instance, semantic-model ID, semantic generation, compiled semantic
lineage, and a complete registry snapshot. It recomputes the canonical registry
digest and rejects inconsistent lineage, malformed or mixed registry identity,
duplicate names or IDs, missing/disabled or
incompatible definitions, invalid stewardship ownership, and filters that do
not name a directly bound compatible semantic dimension. It canonicalizes
grant values using the registered logical type, rejects canonical duplicates,
and propagates required grants through dataset, relationship, dimension,
metric, filter, and metric-dependency lineage. The result is immutable,
target-qualified policy metadata with a deterministic canonical policy digest;
it contains no principal or trusted-claim values and does not place a planner
barrier.

`EvaluateSemanticAccess` is deliberately separate from compilation. It
consumes a `SemanticAccessAttributeSnapshot` and the current
`SemanticAccessAuthority`. Both carry complete registry/control snapshots, not
unqualified digest strings; the evaluator recomputes their FAI-636/637 digests,
requires the authority instance to match the target, and rejects stale or mixed
state. It also recomputes the effective-attribute digest and checks every
value's definition/type/shape/source. Claim-derived values require an immutable
`SemanticAttributeClaimEvidence` produced from the opaque verifier envelope,
exact active control mappings, mapped canonical values, target/principal, and
validity interval; evaluation checks that evidence at the authority's current
observation time. Missing, malformed, expired, stale, or tampered state fails
closed. FAI-639 does not fetch repositories, verify raw provider tokens, or
construct a principal context itself.

Direct-derived values (`direct` or `direct+trusted_claim`) additionally require
opaque access-owned `SemanticAttributeDirectEvidence`. It binds the target
instance and principal to the exact control state, the principal plus active-
group subject closure, active assignment IDs/versions, and effective value
identities. A value digest by itself is not assignment authority. Claim-derived
values retain opaque verified-envelope evidence bound to the same control state
and principal, exact mapping IDs/versions and source identity, the envelope's
validity interval, and verifier fingerprints; raw values and claims are not
identity inputs.

The subject closure supplied when direct evidence is sealed is a trusted
access-capability input, not a consumer assertion. FAI-642 composition must
obtain it from the authoritative principal/group resolver and must not expose
the evidence constructor as a request or browser boundary.

Grant decisions use the profile's scalar equality, list-overlap, and logical
AND rules. A satisfiable dataset access filter becomes a closed typed PlanIR
predicate: scalar attributes produce `compare`/`=` with a typed literal, list
attributes produce `in` with typed literals, and multiple filters compose with
`and`. Relationship-bound members retain the complete dataset closure so later
planner enforcement cannot drop a joined dataset's filter. Field names remain governed references and values remain typed PlanIR
values; no SQL or template string is produced. Dataset predicates and denials
are inherited by member decisions. Every multi-dataset dimension or metric
decision carries `DatasetPredicates`, a complete map from each required dataset
(including relationship/dependency datasets) to its typed predicate;
`Predicate` is only the primary-dataset convenience view. FAI-641 must not
infer that one predicate covers a multi-dataset object.

The policy digest is over deterministic canonical qualified policy metadata.
The evaluator's decision identity additionally binds profile, instance,
semantic model and generation, principal/actor, registry and control
profile/revision/digest, effective-attribute digest, policy digest, grant
results, filter evidence (including value digests and predicate kinds), and
sorted object outcomes. Direct-assignment and verified-claim evidence digests
are bound when present. Raw effective values and provider claims are excluded
from both identity projections. These identities are an in-process handoff
and audit/cache input. FAI-639 does not itself own invalidation or caches;
FAI-645 consumes the identities at the existing result-cache and durable-audit
boundaries.

FAI-641 owns consumption of this decision and typed PlanIR
output in the governed planner. In particular, it must attach a security
barrier at every protected dataset occurrence before joins, outer-join null
extension, aggregation, suggestions, totals, rewrites, and execution.
FAI-642 separately owns wiring the same admission to catalogs and every
semantic consumer. The [planner boundary](../docs/articles/architecture/semantic-access-planner.md)
records the scan-occurrence and trusted-input contract. FAI-639's
compiler/evaluator tests are not evidence that those barriers, catalog rules,
consumer adapters, generation references, cache invalidation, or query
enforcement exist by themselves; the supported slices are evidenced by
FAI-641, FAI-642, FAI-645, FAI-648, and FAI-649.

### Immediate invalidation identity

The FAI-645 [cache, lifecycle, and audit boundary](specifications/semantic-access-cache-lifecycle.md)
extends these identities into protected buffered-result reuse and required
consumer audit. It preserves the existing registry/control lifecycle and
planner admission authorities; unsupported reuse paths remain closed. This
implementation boundary is not FAI-648 qualification or VAL-11 completion.

FAI-637 establishes durable invalidation inputs, and FAI-639 now computes the
effective-attribute, policy, and decision identities used to consume them.
Neither foundational slice publishes invalidation events or implements a
consumer cache; FAI-645 uses their committed identities to reject stale reuse.
The authorization-sensitive identity contract is the tuple
`(instance, semantic generation, principal, registry profile/revision/digest,
control profile/revision/digest, effective attribute-set digest, normalized
semantic-policy identity)`. FAI-639 materializes this contract in the
evaluator's deterministic decision identity, while FAI-645 binds it with exact
FAI-622 publication/policy evidence in protected result dependencies. The
effective attribute-set digest is an ordered
projection of definition ID/version/type/shape, canonical value digest, and
source; raw values and raw provider claims are excluded. Runtime trusted input
also binds its credential/token fingerprint and validity interval.

A committed definition change invalidates by registry identity. A committed
assignment or mapping change invalidates by control identity and, where known,
affected definition and subject. A digest mismatch is an immediate,
conservative invalidation signal, never permission to continue with stale
state. Fresh lookup/store/delivery guards consume this identity now;
unsupported suggestion, rollup, bundle, and opaque-byte reuse remains closed,
and the qualification ledger records the remaining exhaustive LIF cases as
Partial rather than treating them as supported.

Policy diffs report compatibility and security impact separately. The profile
matrix makes tightening changes such as adding a required grant or access
filter breaking, and makes weakening changes such as removing one explicitly
access-widening. A widening result always requires security approval; an
indeterminate result blocks publication. A semantic version never authorizes a
widening by itself.

## Consequences

Policy meaning becomes visible beside the governed semantic objects, and one
definition applies consistently to every consumption path. Identity teams can
change users, groups, and attribute values without rebuilding analytics code,
while changes to access grants or filters receive normal semantic-model review,
planning, compatibility classification, and deployment evidence.

The contract is substantially smaller and safer than Rill or Lightdash
templated SQL. It is also less expressive. Complex predicates, relational
entitlement tables, time-dependent policy, and masked values are not available
through v1 authoring and require modeled data, control-plane attributes, or a
future explicit decision.

FAI-637 supplies a typed, versioned attribute registry, direct assignment and
trusted claim-mapping lifecycle, typed ingress, durable revision/digest
identities, and transactional audit evidence. FAI-649 candidate preparation
proves that every referenced attribute exists and is type-compatible without
embedding instance values in portable artifacts. FAI-645 keeps authorization
and result-cache reuse partitioned by the resulting authority identity.

Removing Source and Model policy targets means every consumer-visible query
must pass through SemanticModel. Any raw Source or Model preview remains an
authoring capability restricted by control-plane authorization and cannot be a
consumer data-delivery API. If a future raw-data product is required, it needs
its own governed consumption contract rather than reuse of internal resources.

## Confirmation

- TypeSpec is the sole public structural authority for `accessGrants`,
  `requiredAccessGrants`, and `accessFilters`; generated JSON Schema, Go DTOs,
  documentation, and browser types contain the same closed contract.
- The accepted authoring contract rejects standalone `DataPolicy` resources and
  rejects access-policy fields on every resource other than SemanticModel. The
  public legacy API surface is removed, and FAI-649 rejects retained standalone
  policies on new activation while preserving their restrictions for explicit
  historical rollback.
- FAI-619's generated-boundary, structural, extracted-YAML, and compiler
  compatibility fixtures cover the migrated structural surface. FAI-639's
  compiler/evaluator fixtures cover the lowering, registry qualification,
  opaque assignment/envelope evidence, trusted snapshot checks, complete
  multi-dataset predicate maps, bounded reader/admission checks, deterministic
  identities, grant evaluation, and typed PlanIR predicate handoff. FAI-641
  and FAI-642 provide the qualified planner and consumer integration.
- The FAI-648 qualification ledger names executable evidence for the supported
  authorization, discovery, planner, cache, and consumer paths. Remaining
  Partial rows are unsupported or not exhaustively qualified and do not become
  implicitly available.
- Extracted normative YAML examples parse and validate against the generated
  SemanticModel schema; cross-path golden fixtures prove identical typed
  canonicalization and the 1,024-value bound.
- Planner tests qualify the plan shapes named by the supported profile and
  prove unsafe rewrites fail closed. Other join, rollup, substitution, and
  opaque reuse combinations remain unsupported rather than inferred from those
  tests.
- Compatibility fixtures qualify the named deterministic classification,
  version, widening-approval, and indeterminate-result slices. The matrix keeps
  unproven combinations Partial.
- Registry/control tests qualify the definition, assignment, lifecycle, replay,
  concurrency, digest, and transactional-audit slices named by the supported
  profile. External provider adapters and exhaustive lifecycle combinations
  remain outside that boundary.
- FAI-645 tests prove identity-partitioned result reuse and stale-state rejection
  for the qualified cache path. Unqualified reuse paths remain closed.
- Audit tests qualify durable redacted decision events, principal/actor/policy
  binding, and audit-failure denial; LIF-06 retains the explicitly documented
  evidence gap for the complete field set.
- Architecture tests guard the request-bound consumers named by the supported
  profile. Scheduled, export, embed, and other unqualified consumer paths are
  not claimed by this evidence.
