# ADR-0022: Adopt dataset-local semantic authoring

Status: accepted

Decision date: 2026-09-11

Implementation: complete

Deciders: LeapView maintainers

Supersedes: none

Amends: [ADR-0006](0006-adopt-ossie-aligned-semantic-contract.md), native
semantic authoring placement, vocabulary, defaults, and pre-release authoring
freeze only

Related: [ADR-0010](0010-adopt-strict-typed-data-resource-contracts.md);
[ADR-0016](0016-adopt-standards-aligned-data-contracts-and-interchange.md);
[ADR-0017](0017-adopt-a-looker-aligned-semantic-access-contract.md);
[ADR-0019](0019-integrate-dbt-at-the-warehouse-contract-boundary.md)

## Context and problem statement

LeapView's current SemanticModel puts every metric and dimension at the top
level. A simple aggregation repeats its dataset in both `dataset: orders` and
`input: {field: orders.amount}`. A dimension over one local field requires a
bindings map and repeats a datatype already known by the physical Model. These
structures express the executable graph but make the common authoring case
unnecessarily verbose.

The product has not been released. This is the opportunity to improve its
authoring contract before consumers depend on the draft shape. The question is
which proven authoring conventions to adopt while preserving LeapView's
physical Model boundary, semantic governance, and native execution ownership.

The current [dbt Semantic Layer YAML specification](https://docs.getdbt.com/docs/build/latest-metrics-spec)
places simple metrics beside their model, uses `type: simple` and `agg`, and
removes the older `type_params` wrapper. It supports model-level default metric
time with metric overrides. These are useful precedents for locality and
shallow definitions; they do not require adopting dbt's entire resource shape.
[Snowflake semantic views](https://docs.snowflake.com/en/user-guide/views-semantic/semantic-view-yaml-spec#metrics)
also distinguish table-level metrics from view-level derived metrics.

These are product specifications, not proof that every field is a universal
standard. The dbt latest specification is also distinct from its legacy
measure-based YAML. The references were reviewed on 2026-09-11.

## Decision drivers

- Put definitions beside the dataset that supplies their physical fields.
- Give each supported metric type one predictable authoring location.
- Borrow established terminology without implying unsupported execution.
- Keep physical identity, grain, and transformations reusable across semantic
  models.
- Preserve stable semantic references, explicit conformance, and authorization.
- Retain one native compiler and query engine, with external formats at adapter
  boundaries.
- Make defaults deterministic and errors explainable at the authored location.

## Considered options

1. **Keep the current native shape.** This avoids migration but retains repeated
   ownership and exposes normalization detail to authors.
2. **Adopt dbt/MetricFlow YAML verbatim as the native contract.** This maximizes
   surface familiarity but imports dbt-specific model and expression concepts,
   plus execution capabilities LeapView does not implement. Matching YAML alone
   would not establish compatible results. Running MetricFlow as a required
   sidecar would additionally move core product behavior outside LeapView.
3. **Keep a native contract and adopt selected authoring conventions.** Put
   simple metrics and local dimensions under datasets, retain explicit shared
   bindings, and normalize into the existing semantic graph. This requires a
   coordinated schema migration and a future adapter for actual interoperability.

## Decision outcome

Choose option 3. LeapView owns its YAML contract, compiler, and execution.
Adopt dataset-local authoring and MetricFlow's `simple` and `agg` vocabulary.
This decision changes authoring, not the supported metric algebra or query
results. The following rules define the target; they are not a claim that the
current parser already accepts it.

### Ownership and placement

| Concern | Authoritative location |
|---|---|
| Transformations, output fields and types, entities, keys, row grain | Physical `Model` resource |
| Physical Model reference and default metric time | `SemanticModel.spec.datasets.<alias>` |
| Local dimensions over that dataset's own fields | `datasets.<alias>.dimensions` |
| Simple metrics over that dataset's own fields | `datasets.<alias>.metrics` |
| Relationship declarations | `SemanticModel.spec.relationships` |
| Shared dimensions and dimensions requiring joined bindings | `SemanticModel.spec.dimensions` |
| Ratio and derived metrics | `SemanticModel.spec.metrics` |
| Reusable filters and semantic access-grant declarations | Existing SemanticModel-level locations |
| Chart selection, layout, and dashboard interactions | `Dashboard` resource |

Simple metrics are allowed only under datasets. Ratio and derived metrics are
allowed only at semantic-model level, including when all inputs belong to one
dataset. The latter is a deliberate LeapView restriction that makes placement
predictable; it is not a claim that MetricFlow requires the same restriction.
Top-level `metrics` may be omitted when the model contains only simple metrics.
Dataset member collections are optional; existing requirements on the complete
normalized model still apply.

A local dimension exposes an owning-dataset field. Its normalized binding is
`<dataset>.<field>` for that origin only. It does not become available through
every relationship automatically. A top-level dimension uses the existing
explicit per-origin `bindings` and relationship `path` contract, including a
single binding when a joined dimension needs one. Equal field names, labels, or
datatypes never establish conformance between datasets.

Physical entities and grain stay on Model. SemanticModel relationships reference
those declarations; local authoring does not duplicate keys or weaken the
existing uniqueness, nullability, cardinality, or fanout rules. A physical field
is not automatically exposed as a semantic dimension.

### Names, syntax, and defaults

Named collections remain YAML maps. Their keys are member names; no additional
`name` property or list-of-named-objects representation is introduced. Block
and flow YAML formatting express the same structure. Examples prefer block
formatting for substantial definitions and compact flow lists for short values.

Within one SemanticModel, metric names are unique across all dataset and
top-level metric maps. Dimension names are independently unique across all
local and top-level dimension maps. Nesting supplies ownership, not a new
public namespace or shadowing rule. Dashboard and metric references continue
to use names such as `revenue`, `order_status`, and `customer_country`.
Conflicts fail with both authored locations; there is no implicit override.

For example, two physical `status` fields can be exposed as `order_status` and
`refund_status`, each with `field: status`. A single shared semantic status
instead requires a top-level dimension with explicit bindings. Existing typed
member namespaces remain separate; this decision does not merge metric and
dimension names into one namespace.

| Property | Target rule |
|---|---|
| Simple metric `type` | Required, `simple` |
| Simple metric `agg` | Required; existing `sum`, `count`, `count_distinct`, `avg`, `min`, `max` vocabulary |
| Local `field` | Unqualified physical field identifier in the containing dataset; defaults exactly to the member's map key |
| Dimension `datatype` | Optional; inferred from the referenced Model's resolved logical field type |
| Explicit dimension `datatype` | Type assertion checked against the referenced field, not a cast |
| Shared dimension datatype | Every binding must resolve to the same logical datatype; an explicit assertion must match all bindings |
| Member `label` | Existing deterministic name-derived label when omitted |
| Simple metric `timeDimension` | Explicit override, otherwise the dataset's `defaultTimeDimension` |

Missing defaulted fields are errors. There is no fuzzy name matching, implicit
SQL expression, implicit join search, or query-time datatype inference. When
Model schema resolution is needed, candidate validation must resolve and check
types before activation. An unresolved or incompatible type blocks activation.
Schema observations cannot silently rewrite published contract bytes; canonical
identity and compatibility continue to follow ADR-0016.

Preserve existing time semantics: a time dimension declares its `nativeGrain`
and allowed `grains`, with the existing calendar and timezone rules. A default
or overridden time dimension must be valid for the metric's owning dataset.
Do not infer a time role merely from a field's name or temporal datatype.

Keep metric properties shallow. Existing `where`, `empty`, `timeDimension`,
`unit`, `format`, `label`, `description`, `aiContext`, `hidden`, and
`requiredAccessGrants` retain their applicable meanings. Reusable filters retain
their current typed predicates and qualified field references. Ratio metrics
retain `numerator` and `denominator`; derived metrics retain `expression` and
the governed `${metric}` reference syntax. No `type_params`, presentation
wrapper, arbitrary extension bag, or new general SQL `expr` is introduced.

Only the defaults specified here and existing documented semantic defaults
apply. In particular, `agg` has no default. Existing omitted `empty` behavior
remains zero for count/count-distinct and null for other simple aggregations.
Units, currency, formatting, filters, access grants, and aggregation behavior
are not inferred from names or broadly inherited from a dataset. Existing
dataset access gates and member access checks remain in force.

### Before and target examples

The following current fragment exposes one local dimension and simple metric:

```yaml
spec:
  datasets:
    orders:
      model: orders
  dimensions:
    order_status:
      datatype: String
      bindings:
        orders:
          field: orders.status
  metrics:
    revenue:
      type: aggregate
      dataset: orders
      aggregation: sum
      input:
        field: orders.amount
```

The equivalent target fragment is:

```yaml
spec:
  datasets:
    orders:
      model: orders
      dimensions:
        order_status:
          field: status
      metrics:
        revenue:
          type: simple
          agg: sum
          field: amount
```

Here is the target composition of local and shared definitions. The referenced
Models supply the named fields and their logical types, a foreign `customer`
entity on orders and refunds, and a primary or unique `customer` entity on
customers. Those physical declarations are omitted from this semantic resource.

```yaml
apiVersion: leapview.dev/v1
kind: SemanticModel
metadata:
  id: semantic-model:sales
  name: sales
  displayName: Sales
spec:
  datasets:
    orders:
      model: orders
      defaultTimeDimension: ordered_at
      dimensions:
        ordered_at:
          time:
            nativeGrain: day
            grains: [day, week, month, quarter, year]
        order_status:
          field: status
      metrics:
        revenue:
          type: simple
          agg: sum
          field: amount
          unit: USD
          format: currency
        order_count:
          type: simple
          agg: count_distinct
          field: order_id
    refunds:
      model: refunds
      metrics:
        refunded_amount:
          type: simple
          agg: sum
          field: amount
          unit: USD
    customers:
      model: customers
  relationships:
    orders_customer:
      from:
        dataset: orders
        entity: customer
      to:
        dataset: customers
        entity: customer
    refunds_customer:
      from:
        dataset: refunds
        entity: customer
      to:
        dataset: customers
        entity: customer
  dimensions:
    customer_country:
      bindings:
        orders:
          field: customers.country
          path: [orders_customer]
        refunds:
          field: customers.country
          path: [refunds_customer]
  metrics:
    average_order_value:
      type: ratio
      numerator: revenue
      denominator: order_count
    net_revenue:
      type: derived
      expression: ${revenue} - ${refunded_amount}
```

`ordered_at` demonstrates exact-name field defaulting. `average_order_value`
demonstrates a global metric with inputs from one dataset. `net_revenue` uses
two datasets, and `customer_country` explicitly supplies a binding for each
metric origin. No shared time dimension is declared for refunds: this example
does not make `net_revenue` groupable by the orders-only `ordered_at` dimension.

### Compiler and runtime boundary

TypeSpec remains the structural authority under ADR-0016, generating the public
DTOs and JSON Schema. Contextual compiler validation owns field resolution,
type assertions, member-name collisions, relationship paths, metric dependency
cycles, and access validation.

The compiler expands dataset ownership, field defaults, datatypes, and time
defaults into one native typed semantic graph before planning. Local dimensions
become ordinary explicit bindings; local simple metrics become the existing
aggregate operation with explicit input and ownership. The internal operation
does not need a cosmetic rename from `aggregate` to `simple`.

Contract projection must receive the resolved physical Model field types when
an authored dimension omits `datatype`. It materializes that type before
canonical bytes are formed, checks explicit assertions against supplied types,
and rejects unresolved fields instead of publishing an incomplete contract.

Keep one PlanIR and the existing Go/DuckDB execution path. Normalization retains
authored source locations for diagnostics and stable semantic member identity
for dashboards, policies, lineage, and contract projection. Planners must not
interpret YAML nesting or resolve omitted fields at query time. Descriptions
and AI context remain non-executable metadata.

Equivalent migrated definitions must retain existing exact-decimal and unit
behavior, null and empty-result semantics, ratio behavior, safe joins,
per-dataset preaggregation and stitching, filter application, and access
enforcement. Surface similarity to MetricFlow is not an assertion of identical
results across engines.

### Pre-release cutover and interoperability

Implement one coordinated replacement of the unreleased native
`leapview.dev/v1` authoring shape. This explicitly amends ADR-0006's authoring
freeze for this pre-release change; it does not relax its execution invariants
or create a standing exception for post-release breaking changes.

Update the authored schema, normalization, exporters, fixtures, examples, and
documentation together. Remove acceptance of top-level simple/aggregate
definitions, `type: aggregate`, repeated simple-metric `dataset`, `aggregation`,
and the `input` wrapper. Do not preserve a permanent second native authoring
syntax. Existing explicit top-level dimension bindings remain valid. A
one-time repository migration may rewrite draft YAML without making the runtime
a compatibility reader.

Retain the version-pinned Ossie boundary from ADR-0006. Native schema changes do
not silently change an external profile or namespaced extension's meaning;
adapters must continue honoring their pinned contract or introduce an explicit
new profile. Update native YAML export to emit the target structure.

MetricFlow ingestion is a separate future project. The architecture permits a
versioned authoring adapter that converts dbt/MetricFlow definitions into the
same native graph or generated native YAML, with explicit physical Model
bindings, provenance, and unsupported-feature errors. It does not require a
production MetricFlow sidecar or a second planner. A future workflow should
have one authoritative metric source; generated LeapView definitions must not
become independently edited copies expected to stay synchronized by hand.

This ADR does not select a supported MetricFlow release, artifact format,
conversion implementation, or compatibility guarantee. ADR-0019's warehouse
integration and semantic-authority boundary remain unchanged. Any future
synchronized import must explicitly settle authority and regeneration under
that boundary. SQL expressions, filtered input-reference objects, cumulative
and conversion metrics, offsets, time spines, and non-additive semantics are
not added as inert schema fields in this first pass.

### Basis and deliberate differences

The adopted precedents are model-local simple metrics, `simple`/`agg`, shallow
metric properties, and a default time dimension with explicit overrides.
The [simple metric reference](https://docs.getdbt.com/docs/build/simple) also
documents name-based default input resolution; LeapView restricts that input
to a physical field rather than importing arbitrary expression semantics.

Named maps, separate physical Models, explicit shared bindings, datatype
inference from the typed Model, and the strict local-simple/global-composed
placement rule are deliberate LeapView decisions. They are justified by its
existing resource and execution boundaries, not labeled MetricFlow compliance.

MetricFlow's [manifest normalization code](https://github.com/dbt-labs/metricflow/blob/89bc933a5d9c1eed73fb3e173f2fda93ba6f9714/metricflow_semantics/model/dbt_manifest_parser.py)
is implementation precedent for translating authored forms before execution.
It is not a dependency introduced by this ADR. Apache Ossie remains an
interchange reference rather than authority for native authoring UX.

## Consequences

Common definitions lose redundant ownership, bindings wrappers, and type
repetition. Authors can find field-based behavior beside its dataset while
shared semantics remain visible at model level. Stable public names keep
dashboard references independent of authoring placement.

The migration touches generated contracts, compiler normalization, native YAML
export, and all authored examples. Global uniqueness means authors sometimes
need explicit names such as `order_status`; nesting does not provide automatic
namespacing. Inferred types make trustworthy Model schema resolution necessary,
and explicit bindings remain more verbose than an automatically inferred join
graph.

LeapView still owns a schema and its maintenance. Borrowing terminology reduces
learning cost but does not deliver MetricFlow import, identical results, or
bidirectional compatibility. Keeping that distinction explicit avoids promising
capabilities before their semantic behavior has been implemented and tested.

## Confirmation

Implementation is complete when generated schema and compiler conformance
demonstrate the following:

- The target examples compile against physical Model fixtures. Dataset-local
  simple metrics work without a top-level metric map; wrong placement, old
  aggregate syntax, unknown properties, missing fields, and duplicate member
  names fail with useful authored locations.
- Omitted fields, types, labels, and time defaults resolve deterministically;
  explicit assertions are checked. Mixed shared-dimension types, unresolved
  types, invalid time references, and unsupported dimension origins fail.
- Explicit and shorthand forms of the same target definition normalize to
  equivalent typed semantics and canonical contract projections. Formatting,
  map order, and default omission cannot change semantic identity or results.
- Converted pre-change fixtures retain query results, decimal types, units,
  empty policies, one-sided multi-dataset groups, and fanout protection in
  focused planner and DuckDB integration tests. Authorization tests cover
  dataset gates, member grants, filters, and derived dependencies after nesting.
- Versioned Ossie round trips preserve the supported semantic graph; native
  export emits the new shape. Unsupported external behavior is rejected rather
  than silently dropped. No MetricFlow execution dependency or alternative
  runtime representation is introduced.
- Generated artifacts and public examples agree with the implemented schema;
  public documentation describes only capabilities that have actually shipped.
