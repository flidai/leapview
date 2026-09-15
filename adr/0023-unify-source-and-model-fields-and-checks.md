# ADR-0023: Unify Source and Model fields and checks

Status: accepted

Decision date: 2026-09-14

Implementation: complete

Deciders: LeapView maintainers

Supersedes: none

Amends: [ADR-0010](0010-adopt-strict-typed-data-resource-contracts.md), Source
and Model field placement, schema matching, freshness, and executable checks;
[ADR-0016](0016-adopt-standards-aligned-data-contracts-and-interchange.md),
Source and Model nullability authoring and shared rule projection only

Related: [ADR-0006](0006-adopt-ossie-aligned-semantic-contract.md);
[ADR-0008](0008-isolate-ducklake-candidate-physical-state.md);
[ADR-0020](0020-adopt-a-postgresql-centered-target-data-architecture.md);
[ADR-0022](0022-adopt-dataset-local-semantic-authoring.md)

## Context and problem statement

Sources and Models both represent datasets at a boundary in the project graph.
A Source exposes input data; a Model produces a dataset through a direct Source
definition or SQL transformation. Both need understandable expectations about
their structure and contents, but their authoring and enforcement differ today.

Before this decision, Sources declared `spec.schema.fields`, schema matching
modes, and a separate `spec.freshness`. Models declared `spec.fields` and
`spec.checks`. Model field declarations asserted existence and, when supplied,
datatype against the transformed output. Additional output fields were
discovered rather than rejected. Only Models had the general row-check
collection. Their primary and unique entities also implied uniqueness and
non-null checks.

Nullability had different meanings across these paths: Source declarations were
compared with discovered physical schema nullability, while Model field
nullability participated in contract metadata without creating a general
row-level non-null gate. Similar-looking declarations promised different things.

Contract versioning, canonical publication, and interoperability already apply
to both resource kinds. The problem is to unify authoring and validation without
confusing input guarantees with output guarantees or duplicating structural
assertions as verbose rules.

## Decision drivers

- Teach one model: **fields define shape; checks define rules**.
- Give Sources and Models the same syntax and semantics for dataset validation.
- Keep datatype expectations beside the field they describe.
- Validate the exact input or output to which a promise applies.
- Preserve typed rules, bounded execution, stable evidence, and publication policy.
- Keep entities, grain, transformations, and semantic modeling in their existing
  architectural domains.

## Considered options

1. Keep the current Source and Model shapes. This preserves inconsistent
   placement and requires separate explanations for equivalent expectations.
2. Put every assertion, including field existence and datatype, in `checks`.
   This is uniform internally but repeats field names and separates structural
   expectations from field documentation.
3. Share `fields` for shape and `checks` for rules on both resources. Compile
   both into common validation machinery while retaining resource-specific
   dataset preparation.

## Decision outcome

Choose option 3. Source and Model both expose `spec.fields` and `spec.checks`
using shared generated definitions. A contract remains broader than either:
`metadata.contract` retains version and compatibility, and field declarations
retain descriptions, labels, and governance metadata.

The implemented contract has identical validation meaning on either resource;
their connection/location or model definition remains separate:

```yaml
spec:
  fields:
    order_id:
      datatype: String
      description: Unique order identifier.
    revenue:
      datatype: Decimal
      description: Revenue after discounts.
  checks:
    - id: order_id_present
      type: non_null
      field: order_id
      severity: error
    - id: order_id_unique
      type: unique
      fields: [order_id]
      severity: error
    - id: has_orders
      type: row_count
      minimum: 1
      severity: error
```

### Fields define shape

Each declared field must exist in the dataset. An explicit `datatype` asserts
its logical type against the observed physical type using the shared portable
datatype mapping. An omitted datatype is discovered. Field declarations never
project, rename, cast, filter, or otherwise transform data.

Both resources use `spec.schema.mode` for field-set matching, with `compatible`
as the default and `strict` as the closed-field-set option. Compatible mode
allows undeclared columns and discovers them; strict mode requires the observed
field set to equal the declared set. Field order is not semantic. Strictness
controls field membership, not whether every datatype must be authored.
Omitting fields in compatible mode means discovery without authored shape
assertions; a separate `inferred` authoring mode is unnecessary. Discovery must
still establish a usable schema, and referenced fields must resolve.

Descriptions and governance metadata remain attached to fields, but changing
descriptive text does not itself create a data rule. Existing reviewed
contract-projection inclusion and exclusion policies continue to apply.

### Checks define rules

Use one shared typed dataset-check vocabulary on both resources. It includes
`non_null`, `unique`, `accepted_values`, `row_count`, `relationship`, and
`freshness`. Rule IDs are stable and unique within the owning resource; IDs,
default severity, thresholds, null handling, and result semantics do not change
with resource kind. Checks may reference discovered fields without requiring
redundant field declarations, but unresolved references fail validation.

Move freshness into this collection. Field-based freshness evaluates the
dataset's timestamp field on either resource. Revision-based freshness requires
authoritative revision evidence; a model build timestamp must not silently
substitute for source-data freshness. Unsupported evidence capabilities are
rejected explicitly, never interpreted as a passing check.

Use `non_null` for authored absence-of-nulls assertions. Remove authored field
`nullable` from Source and Model rather than retaining two ways to express the
same promise. Continue recording and displaying observed physical nullability
as schema evidence. A physically nullable column containing no null rows can
pass a non-null rule; a declaration alone does not prove that the rows pass.

Model entities and grain retain their domain meaning. Primary and unique
entities continue to derive uniqueness and non-null checks automatically.
The validation plan records their origin and avoids duplicate execution where
the same assertion is implied and authored. An explicit warning cannot weaken
an entity's required invariant. Sources do not gain Model entities or grain
merely because the check vocabulary is shared.

### Placement determines the dataset

Source fields and checks apply to the relation exposed by the source reader,
after reader decoding and before Model SQL. Model fields and checks apply to
the resulting dataset after its transformation, before serving activation.
They do not evaluate dashboard-filtered or aggregated query results.

Checks are not inherited across graph edges. For example, a deduplicating
Model may pass uniqueness while its Source fails it. Conversely, a join can
violate Model uniqueness even when input keys pass. Each resource owns its
contract version and its validation evidence.

Source validation must describe the same captured input or consistent source
snapshot consumed by transformation. Re-reading a mutable external source
after checking it does not establish that guarantee. Resource preparation must
provide an appropriate snapshot, captured relation, or equivalent consistency
evidence; unavailable guarantees must be reported rather than assumed.
Relationship checks require authorized, resolved dataset references and a
defined consistent set of inputs, including when checking across resources.

Compile both resource kinds into one typed validation plan and evaluator.
Resource adapters supply the relation, observed schema, snapshot/revision
identity, and capabilities. Rules cannot introduce arbitrary SQL, connector
access, credentials, or an independent query path. Preserve execution budgets
and the distinction between success, warning, blocking failure, and unavailable
or timed-out evidence. Warnings may qualify; required failures or missing
required evidence cannot authorize activation.

### Contract evolution and implementation boundary

Canonical projections and version classification must include the new shared
shape policy and checks. ODCS export and mapping/loss reports must describe
their supported semantics without expanding the existing conformance claim.
Publishing a compatible contract and validating a dataset remain separate steps.

Implement this as a coordinated pre-release authoring conversion: move Source
fields to `spec.fields`, normalize inferred mode to compatible discovery, move
freshness to checks, and translate `nullable: false` into stable non-null rules.
Remove `nullable: true` without generating a rule. The conversion from physical
nullability comparison or metadata to row validation is an explicit semantic
change, not a mechanically equivalent rename. Generated definitions, compiler,
fixtures, native export, projections, and documentation must move together;
do not retain parallel legacy authoring aliases. Historical publication evidence
remains immutable and subject to its existing profile and replay guarantees.

## Research basis

The Flid reference library's ODCS and dbt documentation and current official
documentation were reviewed on 2026-09-14.
[dbt model contracts](https://docs.getdbt.com/docs/mesh/govern/model-contracts)
establish output shape, while [dbt data tests](https://docs.getdbt.com/docs/build/data-tests)
can assert contents on both Sources and Models.
[ODCS quality rules](https://bitol-io.github.io/open-data-contract-standard/latest/data-quality/)
attach rules to datasets or fields within the broader contract. These support
the shape/rules distinction; the shared LeapView placement and matching policy
are native design decisions, not a claim of verbatim dbt or ODCS syntax.

## Consequences

Authors learn one field and rule vocabulary and can move a check between input
and output intentionally. Datatype declarations remain concise. A common
evaluator reduces divergent semantics and gives diagnostics a consistent form.

The change requires more than adding Source checks: authoring, canonical
identity, compatibility classification, export, and examples all change.
Source row checks may add scans and require capturing data or holding a
consistent source session. Strict Model shape validation is newly available.
Nullability conversion can change which candidates qualify and must be visible
in conversion diagnostics. Shared syntax does not make unavailable revision or
snapshot capabilities exist.

## Confirmation

Implementation is complete when the following are demonstrated:

- Generated schemas accept the same field and check fragments on both resources
  and reject removed paths, unknown rule types, and duplicate rule IDs.
- Shared fixtures prove identical field existence, datatype, discovery, and
  compatible/strict matching behavior on Source and Model relations.
- Rule conformance covers passing, warning, blocking, empty, unavailable,
  timeout, and budget outcomes on both resource kinds, with stable evidence.
- A transformation fixture proves that input and output checks are independent:
  filtering or deduplication can repair input, and a join can break output grain.
- Non-null checks scan actual contents irrespective of physical nullable
  metadata; derived entity checks retain their strength without duplicate work.
- Source consistency, relationship authorization, and freshness capability
  tests reject mismatched snapshots and missing or substituted evidence.
- Conversion fixtures cover old schema modes, freshness thresholds, and both
  nullable values; canonical publication and supported ODCS mappings preserve
  declared semantics or report an explicit incompatibility or unsupported case.
- Public documentation and generated examples change only when implementation
  ships; this ADR alone does not establish runtime support.
