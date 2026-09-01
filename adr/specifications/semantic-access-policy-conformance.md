# Semantic access-policy conformance specification

Status: accepted

Profile: `leapview.semantic-access/v1`

Last updated: 2026-09-01

Owners: LeapView maintainers

Governing decision: [ADR-0017](../0017-adopt-a-looker-aligned-semantic-access-contract.md)

## Purpose

This mutable specification defines the public structure, compiler semantics,
runtime enforcement, and evidence required by ADR-0017. The governing ADR owns
the stable architectural decision: SemanticModel is the only authored policy
target and uses a Looker-aligned access-grant and access-filter model.

The terms **must**, **must not**, **should**, and **may** are normative. A
requirement is implemented only when its evidence is linked in the evidence
ledger. The profile identifier participates in generated schema, compiled
policy, cache, audit, and conformance evidence.

## Change control

- **CHG-01:** Editorial clarification, new non-normative examples, added test
  cases, and evidence links may update this document without changing the
  profile when accepted documents and authorization decisions are unchanged.
- **CHG-02:** Any change to public shape, attribute normalization, matching,
  list bounds, composition, planner placement, discovery, denial, or cache
  identity requires a new semantic-access profile version.
- **CHG-03:** A change that adds a policy concept, target, bypass, expression
  language, precedence model, or masking behavior also requires a new ADR that
  amends or supersedes ADR-0017.
- **CHG-04:** Historical compiled policies and audit evidence retain their
  original profile. A profile upgrade is planned, compatibility-classified, and
  never applied implicitly to an active generation.

## Canonical authored shape

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
      allowedValues:
        - sales
        - finance
    canViewPII:
      userAttribute: piiAccess
      allowedValues:
        - full
  datasets:
    orders:
      model: orders
      requiredAccessGrants:
        - canViewSales
      accessFilters:
        - field: region
          userAttribute: allowedRegions
  dimensions:
    region:
      datatype: String
      bindings:
        orders:
          field: region
    customerEmail:
      datatype: String
      bindings:
        orders:
          field: customer_email
      requiredAccessGrants:
        - canViewPII
  measures:
    revenue:
      dataset: orders
      aggregation: sum
      input:
        field: revenue
    cost:
      dataset: orders
      aggregation: sum
      input:
        field: cost
  metrics:
    grossMargin:
      type: derived
      expression: revenue - cost
      requiredAccessGrants:
        - canViewSales
        - canViewPII
```

## Public structure

- **STR-01:** `spec.accessGrants` is an optional identifier-keyed map. Keys are
  unique by construction and use the canonical SemanticModel identifier rules.
- **STR-02:** Each access grant is closed and requires exactly
  `userAttribute` and a non-empty `allowedValues` list.
- **STR-03:** `userAttribute` is a canonical control-plane attribute name. It
  cannot contain an email, principal ID, group ID, role, grant, publication,
  claim path, template, SQL fragment, or target-specific identity.
- **STR-04:** `allowedValues` contains typed scalar literals only. Values in one
  grant are homogeneous, canonicalizable, duplicate-free, and deterministically
  ordered after normalization.
- **STR-05:** `requiredAccessGrants` is permitted only on SemanticModel datasets,
  dimensions, measures, and metrics. It is a non-empty, duplicate-free list of
  access-grant names local to the same SemanticModel.
- **STR-06:** `accessFilters` is permitted only on a SemanticModel dataset. Each
  entry is closed and requires exactly `field` and `userAttribute`.
- **STR-07:** An access-filter field names a SemanticModel dimension, not a
  physical column, SQL expression, dashboard field, or external resource.
- **STR-08:** Access-policy fields on Connection, Source, Model, Pipeline, or
  Dashboard and standalone `DataPolicy` documents are rejected.
- **STR-09:** Unknown keys and generic extension bags are rejected at every
  policy node.
- **STR-10:** TypeSpec owns this structure and generates all public projections;
  handwritten Go, CUE, JSON Schema, or UI copies are not independent authorities.
- **STR-11:** `allowedValues` and every list-valued principal attribute contain
  at most 1,024 canonical values in profile v1. The bound is public
  authorization behavior and cannot vary by target or query path.
- **STR-12:** Every normative YAML example is extracted, parsed, and validated
  against the generated SemanticModel schema in CI.

## Control-plane attribute contract

- **ATT-01:** The target instance owns a typed, versioned attribute registry.
  Portable SemanticModel YAML references attribute names but contains neither
  registry definitions nor values.
- **ATT-02:** Attribute values originate only from authenticated control-plane
  assignments, trusted SAML/OIDC claim mappings, or cryptographically verified
  embed/service-token claims admitted by the control plane.
- **ATT-03:** Browser parameters, dashboard filters, query arguments, headers,
  unsigned tokens, and analytics YAML cannot create or override trusted
  principal attributes.
- **ATT-04:** Supported values are a scalar or homogeneous list over the
  approved semantic literal types. Maps, nested lists, raw JSON, SQL fragments,
  filter-expression strings, and executable values are rejected.
- **ATT-05:** Every resolved principal context carries a stable attribute-set
  version or digest suitable for authorization and cache identity.
- **ATT-06:** Candidate preparation resolves every referenced attribute against
  the target registry and rejects absent or type-incompatible declarations
  before activation.
- **ATT-07:** Runtime absence, emptiness, invalidity, expiry, or loss of trust
  fails closed even if candidate validation previously succeeded.
- **ATT-08:** Attribute assignment changes are audited control-plane operations
  and take effect without an analytics deployment.
- **ATT-09:** Deleting an attribute or changing its type is rejected while any
  active or retained rollback generation references it. Type evolution creates
  a new canonical attribute rather than mutating the existing definition.
- **ATT-10:** An administrator may disable an attribute as an explicit audited
  emergency revocation. Disabling never deletes the registry entry: every
  semantic object governed through a dependency on that attribute becomes
  unavailable, affected caches are invalidated, and health reports the disabled
  dependency until it is restored or a new SemanticModel generation removes
  the reference.
- **ATT-11:** Removing a principal assignment or trusted claim mapping is
  permitted and takes effect as a fail-closed value absence for affected
  principals. It cannot change the attribute's registered type or provide a
  fallback value implicitly.
- **ATT-12:** The control plane rejects an assignment containing more than 1,024
  canonical values. Runtime repeats the bound defensively and denies evaluation
  if persisted or token-supplied state violates it.

## Attribute value canonicalization

- **VAL-01:** Attribute names use the ASCII SemanticModel identifier grammar
  and are case-sensitive. Case folding, trimming, aliases, locale rules, and
  Unicode confusables do not participate in name resolution.
- **VAL-02:** Profile v1 permits String, Boolean, Integer, Decimal, Date, and
  Timestamp attribute values and homogeneous lists of those values. Float,
  binary, interval, JSON, map, object, nested-list, and null values are rejected.
- **VAL-03:** Strings are valid Unicode normalized to NFC, remain
  case-sensitive, retain leading and trailing whitespace, and reject C0/C1
  control characters without exception. A differently cased or spaced value
  is a different authorization value.
- **VAL-04:** Booleans have only the typed values `true` and `false`; strings
  such as `"true"`, integers, and truthy values are not coerced.
- **VAL-05:** Integers are signed 64-bit mathematical integers and canonicalize
  to base-10 with no leading plus or zeroes except `0`.
- **VAL-06:** Decimals are finite exact base-10 values. Canonicalization removes
  insignificant trailing zeroes, uses no exponent, and maps negative zero to
  `0`. Integer and Decimal remain distinct types even when numerically equal.
- **VAL-07:** Dates are valid Gregorian `YYYY-MM-DD` values. Timestamps parse
  RFC 3339, reject leap seconds, normalize to UTC `Z`, and use the shortest
  fractional form preserving nanosecond precision.
- **VAL-08:** Explicit null, YAML null, an absent value, and an empty list never
  match an allowed value and never mean unrestricted access. Null is invalid in
  `allowedValues` and attribute assignments.
- **VAL-09:** Lists have set semantics after element canonicalization: duplicate
  values collapse and remaining values sort by `(logical type, canonical
  bytes)`. Assignment order cannot change policy identity or cache identity.
- **VAL-10:** Grant values and filter dimensions require the same registered
  logical type. Profile v1 performs no numeric promotion, string parsing,
  timezone inference, locale conversion, or other cross-type coercion.
- **VAL-11:** One shared generated canonicalizer is used by control-plane input,
  claim ingestion, candidate validation, runtime evaluation, policy digesting,
  caching, and audit projection. Independent golden fixtures prove identical
  decisions across these paths.

## Access-grant evaluation

- **GRT-01:** A scalar attribute satisfies a grant when its canonical value
  equals one canonical `allowedValues` member.
- **GRT-02:** A list attribute satisfies a grant when at least one canonical
  element equals one canonical `allowedValues` member.
- **GRT-03:** Missing, empty, invalid, expired, untrusted, or type-incompatible
  attributes do not satisfy a grant.
- **GRT-04:** Multiple `requiredAccessGrants` on one object use logical AND.
- **GRT-05:** An empty required-grant list is structurally invalid; absence means
  that this mechanism imposes no additional grant requirement on the object.
- **GRT-06:** There is no implicit administrator, owner, service-principal, or
  publication bypass. Every principal on a semantic consumer surface satisfies
  the same authored requirements. Break-glass access is out of scope and does
  not exist under this profile; adding an operational bypass requires a separate
  control-plane security ADR and cannot alter semantic evaluation.
- **GRT-07:** Access-grant evaluation is deterministic and independent of map
  iteration, assignment order, repository paths, and display names.
- **GRT-08:** Grant names and results are included in normalized policy identity;
  raw sensitive attribute values are not included in logs or telemetry.
- **GRT-09:** Dataset requirements apply to every dimension, measure, metric,
  relationship traversal, raw-value request, and derived query over that
  dataset; a member cannot weaken its dataset's requirements.
- **GRT-10:** Required grants propagate through semantic dependencies. A
  derived metric, filtered metric, measure, or other semantic member cannot
  expose a denied dependency by omitting that dependency's grant.

## Access-filter evaluation

- **FLT-01:** The referenced dimension must be bound to the dataset containing
  the access filter and must resolve to a planner-governed field.
- **FLT-02:** A scalar attribute lowers to a typed equality predicate between
  the dimension and one bound parameter.
- **FLT-03:** A list attribute lowers to a typed membership predicate over bound
  parameters. After deduplication it contains at most 1,024 values; an oversized
  value denies evaluation before planning.
- **FLT-04:** Multiple access filters on one dataset use logical AND.
- **FLT-05:** Filters inherited through every dataset and relationship path used
  by a query also use logical AND. A join cannot weaken or replace a dataset's
  filter.
- **FLT-06:** Missing, empty, invalid, expired, untrusted, oversized, or
  type-incompatible attributes make the dataset unavailable and deny the query
  before execution.
- **FLT-07:** Access filters never accept an author-supplied operator, wildcard,
  regular expression, SQL fragment, template, subquery, function, or null-as-all
  convention in the initial profile.
- **FLT-08:** Filter values are parameters in the typed plan. They are never
  interpolated into authored or generated SQL.
- **FLT-09:** Access filters constrain suggestions, raw values, totals,
  comparisons, drill-through, spatial queries, exports, and every intermediate
  or coalesced query derived from the semantic request.
- **FLT-10:** Query planning retains normalized evidence of applied filters and
  their source policy identities without retaining unrestricted values.

## Planner security barrier

- **PLN-01:** Every protected dataset occurrence enters the semantic plan as a
  typed `SecurityBarrier` over that dataset's governed scan. The barrier owns
  normalized access-filter predicates and their bound parameters.
- **PLN-02:** The barrier restricts the dataset before joins, null extension,
  aggregation, windowing, totals, suggestions, projections, limits, sampling,
  spatial processing, caching, or consumer-supplied filters.
- **PLN-03:** An optimizer may push a barrier predicate into the protected scan
  only when it proves semantic equivalence. It may never pull the predicate
  above a join or aggregate, turn it into a final `WHERE` filter, or remove it
  because another predicate appears equivalent.
- **PLN-04:** Each alias, self-join, relationship traversal, subplan, rollup,
  materialized substitute, and coalesced query branch retains the barrier for
  every protected dataset it reads.
- **PLN-05:** Outer joins apply the protected side's barrier before the join so
  unauthorized rows cannot affect matching, null extension, counts, or
  aggregates. A post-join filter is not conformant evidence.
- **PLN-06:** Aggregate and derived-metric plans operate only over
  barrier-restricted inputs. Security filters cannot be represented solely as a
  HAVING clause or final projection.
- **PLN-07:** Rollups and caches may satisfy a protected scan only when their
  lineage and authorization identity prove that they contain no rows outside
  the same effective barrier.
- **PLN-08:** Plan validation fails closed if a protected dataset scan lacks
  exactly one effective barrier or if barrier lineage is lost during rewrite.
- **PLN-09:** Golden plan tests cover inner, left, right, and full outer joins;
  self-joins; many-to-many paths; totals; suggestions; derived metrics; rollups;
  and multi-dataset coalescing.

## Discovery and execution enforcement

- **ENF-01:** A dataset whose required grants fail is absent from catalogs,
  search, agent tools, builder choices, and reference resolution available to
  the principal.
- **ENF-02:** A dimension, measure, or metric whose required grants fail is
  absent from field catalogs and rejected when addressed directly by canonical
  ID, symbolic name, saved query, dashboard, API, or agent-generated request.
- **ENF-03:** Every dashboard, Explore, agent, export, scheduled execution, API,
  MCP, and embedded request enters one governed semantic authorization path.
- **ENF-04:** No consumer flag, route, token scope, saved artifact, raw SQL
  escape, `skip_nested_security` behavior, or internal caller may suppress
  grant or filter evaluation.
- **ENF-05:** A dashboard cannot widen SemanticModel access. Dashboard sharing
  and publication grant access to the document, not to otherwise denied data.
- **ENF-06:** Source and Model preview is an authoring capability controlled by
  control-plane authorization. It is not a consumer API and does not inherit a
  second authored policy language.
- **ENF-07:** Denial occurs before warehouse or DuckDB execution whenever the
  missing grant or invalid attribute is knowable before planning.
- **ENF-08:** Error responses disclose enough stable policy identity for an
  authorized administrator to diagnose failure without revealing inaccessible
  members or sensitive attribute values to the requester.
- **ENF-09:** Catalog and execution authorization calculate transitive semantic
  dependencies before exposing or planning a member, so discovery and direct
  execution reach the same decision.
- **ENF-10:** A dataset with an access filter is discoverable only when the
  principal has a valid, trusted, non-empty, type-compatible, in-bound value for
  every referenced attribute. Otherwise the dataset and its members are absent
  from discovery and direct execution is denied.
- **ENF-11:** A valid access-filter attribute makes the governed object
  discoverable even when the resulting authorized row set is empty. Discovery
  does not probe data to decide authorization.

## Compatibility and security-impact classification

- **CMP-01:** Compatibility class and security impact are independent outputs.
  Compatibility is `additive`, `behavioral`, `breaking`, or `indeterminate`;
  security impact is `none`, `tightening`, `widening`, or `indeterminate`.
- **CMP-02:** Classification compares authored contracts and the registered
  attribute types, not current users, groups, assignments, data contents, or
  observed query traffic. An apparently unused policy is not silently ignored.
- **CMP-03:** Any canonical policy change requires a new SemanticModel contract
  version. Additive and behavioral changes require at least a minor increment;
  breaking changes require a major increment. Reordering or duplicate removal
  that produces identical canonical bytes requires no increment.
- **CMP-04:** `backward` is satisfied only when the compatibility result is not
  breaking or indeterminate and deployment policy accepts every widening result.
  A version number never approves widening by itself.
- **CMP-05:** Tightening may be operationally desirable but is breaking when it
  can deny a previously valid consumer. Widening is never treated as a harmless
  compatibility improvement and requires explicit security approval.
- **CMP-06:** An indeterminate compatibility or security result blocks
  publication until the classifier or an explicit reviewed migration resolves
  it; it cannot be downgraded to behavioral by default.

| Authored change | Compatibility | Security impact |
|---|---|---|
| Add an unreferenced access grant | Additive | None |
| Remove an unreferenced access grant | Behavioral | None |
| Rename a grant and update its references | Breaking | Indeterminate |
| Add an `allowedValues` member | Behavioral | Widening |
| Remove an `allowedValues` member | Breaking | Tightening |
| Change a grant's `userAttribute` | Breaking | Indeterminate |
| Add a required grant to an existing object | Breaking | Tightening |
| Remove a required grant from an existing object | Behavioral | Widening |
| Add an access filter to an existing dataset | Breaking | Tightening |
| Remove an access filter from an existing dataset | Behavioral | Widening |
| Change an access filter's field or attribute | Breaking | Indeterminate |
| Add a new protected dataset or member | Additive | None |
| Add a new unprotected dataset or member | Additive | Widening |
| Reorder canonically unordered policy values | No canonical change | None |
| Delete or mutate a referenced registry attribute type | Rejected mutation | Indeterminate |

## Caching, lifecycle, and audit

- **LIF-01:** Authorization identity includes principal identity, actor identity
  where delegated, semantic generation, normalized grant outcomes, normalized
  filters, and attribute-set version or digest.
- **LIF-02:** Query and result caches cannot be shared across different
  authorization identities, even when generated analytical SQL would otherwise
  be equal.
- **LIF-03:** Attribute-value, registry, claim-mapping, policy, or semantic
  generation changes invalidate all affected authorization and result caches.
- **LIF-04:** Candidate planning classifies changes to access grants, required
  grants, and access filters and records affected dashboards, queries, agents,
  APIs, and published semantic members.
- **LIF-05:** Removal or weakening of a policy is never treated as descriptive
  metadata. Deployment policy may require explicit approval for access widening.
- **LIF-06:** Durable audit records bind request principal, delegated actor,
  semantic generation, dataset and member identities, normalized policy IDs,
  attribute-set version, decision, and denial reason.
- **LIF-07:** Audit payloads, logs, metrics, traces, and errors exclude raw
  unrestricted attribute values unless a separate data-classification decision
  explicitly permits a bounded projection.
- **LIF-08:** Control-plane attribute changes and authored policy deployments
  remain distinguishable audit events with a common correlation boundary.

## Deliberate exclusions

- **OUT-01:** The access-policy sub-contract has no authored SQL, Liquid, Go
  templates, regular expressions, functions, scripts, or general expression
  language. This does not restrict the SemanticModel's separately governed,
  closed metric-expression language.
- **OUT-02:** The initial contract has no principal, email, group, role,
  RoleBinding, Grant, service-principal, or publication references.
- **OUT-03:** The initial contract has no explicit deny rules, cross-model policy
  inheritance, reusable top-level policy resources, policy precedence, or
  consumer-specific overrides.
- **OUT-04:** The initial contract has no column masking. Failed member access
  makes the member undiscoverable and unqueryable.
- **OUT-05:** The initial contract does not claim Looker document compatibility
  or conformance; it claims only documented alignment with the three named
  Looker concepts.

## Qualification matrix

| Scenario | Expected result |
|---|---|
| Missing access-grant attribute | Grant fails and object is denied |
| Scalar grant attribute matches | Grant passes |
| List grant attribute has one matching element | Grant passes |
| Multiple required grants with one failure | Object is denied |
| Missing access-filter attribute | Query is denied before execution |
| Missing access-filter attribute during discovery | Dataset and its members are absent |
| Scalar access-filter attribute | Parameterized equality predicate |
| List access-filter attribute | Bounded parameterized membership predicate |
| Access-filter assignment contains 1,025 values | Assignment is rejected; defensive runtime evaluation denies |
| Multiple access filters | All predicates compose with AND |
| Dataset filter reached through a relationship | Filter remains enforced |
| Protected side participates in an outer join | Barrier filters its scan before the join |
| Optimizer rewrite loses or moves a barrier after a join | Plan validation denies execution |
| Denied member omitted from discovery | Member is also rejected by direct reference |
| Same query through dashboard, API, agent, and embed | Same policy identity and result boundary |
| Attribute-set version changes | Affected cache entries are not reused |
| Referenced registry attribute is deleted or changes type | Control-plane mutation is rejected |
| Referenced registry attribute is explicitly disabled | Dependent objects fail closed and affected caches are invalidated |
| Break-glass request reaches a semantic consumer | No bypass exists under this profile |
| Authored SQL, template, group, email, or mask | Structural rejection |
| Policy removal widens access | Impact is classified and approval policy applies |
| Required grant is added to an existing object | Breaking and tightening |
| Allowed value is added to a referenced grant | Behavioral and widening |

## Evidence ledger

| Requirement range | Evidence | Status |
|---|---|---|
| CHG-01–CHG-04 | Profile-version, historical-policy, and normative-change checks | Pending |
| STR-01–STR-12 | Generated TypeSpec, JSON Schema, DTO, extracted-YAML, and authoring-registry fixtures | Pending |
| ATT-01–ATT-12 | Control-plane attribute registry, mutation, disablement, claim-mapping, and trust-boundary tests | Pending |
| VAL-01–VAL-11 | Cross-path and independent typed-canonicalization golden fixtures | Pending |
| GRT-01–GRT-10 | Access-grant compiler and evaluator fixtures | Pending |
| FLT-01–FLT-10 | Semantic planner, parameterization, cardinality, and fail-closed tests | Pending |
| PLN-01–PLN-09 | Security-barrier IR validation and join, aggregate, rollup, and rewrite golden plans | Pending |
| ENF-01–ENF-11 | Catalog, dashboard, Explore, agent, export, API, and embed integration tests | Pending |
| CMP-01–CMP-06 | Policy-diff, compatibility, security-impact, version, and approval fixtures | Pending |
| LIF-01–LIF-08 | Cache partitioning, invalidation, planning, and durable-audit tests | Pending |
| OUT-01–OUT-05 | Negative schema, architecture, and documentation checks | Pending |

## Maintained verification

Implementation must add focused commands for the generated schema, extraction
and schema validation of every normative YAML example, semantic compiler,
security-barrier planner, query authorization, cache identity, compatibility
classifier, and control-plane attribute registry. The final combined change
must also pass:

```sh
task generate
task generated:check
task ci
```
