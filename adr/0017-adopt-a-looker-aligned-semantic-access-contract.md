# ADR-0017: Adopt a Looker-aligned semantic access contract

Status: accepted

Decision date: 2026-09-01

Implementation: pending

Deciders: LeapView maintainers

Supersedes: none

Amends: none

Related: [ADR-0006](0006-adopt-ossie-aligned-semantic-contract.md);
[ADR-0015](0015-adopt-durable-audit-and-compliance-controls.md);
[ADR-0016](0016-adopt-standards-aligned-data-contracts-and-interchange.md);
[Semantic access-policy conformance](specifications/semantic-access-policy-conformance.md);
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
          field: region
    customerEmail:
      datatype: String
      bindings:
        orders:
          field: customer_email
      requiredAccessGrants: [canViewPII]
```

### Attribute ownership and matching

The instance control plane owns the attribute registry, trusted SAML/OIDC and
embed-claim mappings, group and principal assignments, and the resulting
attribute values. SCIM may provision users and groups but does not define the
authorization meaning of an attribute. Analytics YAML may reference only a
canonical attribute name; it cannot declare values, identities, assignments,
or claim-extraction rules.

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

The control plane rejects deletion or type mutation of an attribute referenced
by an active or retained rollback generation. It may explicitly disable the
attribute as an audited emergency revocation; dependent semantic objects then
fail closed, become unhealthy, and invalidate affected caches. This is not a
semantic-evaluator bypass. Break-glass access is absent from this profile and
would require a separate control-plane security ADR.

### Enforcement boundary

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

The control plane must gain a typed, versioned attribute registry and trusted
claim-mapping lifecycle. Candidate preparation must prove that every referenced
attribute exists and is type-compatible without embedding instance values in
portable artifacts. Attribute changes must invalidate affected authorization
and result caches promptly and generate durable audit evidence.

Removing Source and Model policy targets means every consumer-visible query
must pass through SemanticModel. Any raw Source or Model preview remains an
authoring capability restricted by control-plane authorization and cannot be a
consumer data-delivery API. If a future raw-data product is required, it needs
its own governed consumption contract rather than reuse of internal resources.

## Confirmation

- TypeSpec is the sole public structural authority for `accessGrants`,
  `requiredAccessGrants`, and `accessFilters`; generated JSON Schema, Go DTOs,
  documentation, and browser types contain the same closed contract.
- The authoring registry rejects standalone `DataPolicy` resources and rejects
  access-policy fields on every resource other than SemanticModel.
- Schema and compiler fixtures cover the normative cases in the semantic
  access-policy conformance specification, including unknown fields, dangling
  grants, incompatible attributes, unbound dimensions, and prohibited identity
  or executable-string fields.
- Query authorization tests prove fail-closed scalar and list matching,
  all-grants and all-filters composition, discovery filtering, direct-reference
  rejection, parameterized planning, and identical enforcement for every
  semantic consumer.
- Extracted normative YAML examples parse and validate against the generated
  SemanticModel schema; cross-path golden fixtures prove identical typed
  canonicalization and the 1,024-value bound.
- Planner golden tests prove one effective security barrier per protected
  dataset occurrence before inner and outer joins, aggregates, totals,
  suggestions, self-joins, rollups, and coalesced multi-dataset plans, and prove
  unsafe rewrites fail closed.
- Compatibility fixtures cover every normative policy-change matrix row and
  prove tightening, widening, and indeterminate results trigger the required
  version and approval behavior.
- Registry lifecycle tests reject referenced attribute deletion and type
  mutation, prove explicit disablement fails closed, and prove no administrator
  or break-glass bypass exists in semantic consumers.
- Cache tests prove principals with different effective policies cannot share
  authorization-sensitive results and that attribute-version changes
  invalidate affected entries.
- Audit tests prove grant evaluation and row-filter application are attributable
  to the principal, semantic generation, attribute version, and policy identity
  without leaking unrestricted attribute values.
- Architecture tests prevent dashboards, APIs, agents, exports, and lower-level
  analytical runtimes from bypassing the governed semantic authorization path.
