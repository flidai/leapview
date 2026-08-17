# ADR-0006: Adopt an OSSIE-aligned typed semantic contract

Status: accepted

Decision date: 2026-08-17

Implementation: pending

Deciders: LeapView maintainers

Supersedes: [ADR-0001](0001-semantic-model-first.md), authored semantic shape
and quantitative-member vocabulary only

Related: [ADR-0005](0005-use-project-wide-resource-graph.md);
[Apache Ossie core specification](https://github.com/apache/ossie/blob/main/core-spec/spec.md);
[MetricFlow semantic models](https://docs.getdbt.com/docs/build/semantic-models);
[Snowflake semantic-view YAML specification](https://docs.snowflake.com/en/user-guide/views-semantic/semantic-view-yaml-spec);
[Cube joins](https://docs.cube.dev/docs/data-modeling/joins);
[Cube multi-fact views](https://docs.cube.dev/docs/data-modeling/multi-fact-views)

## Context and problem statement

ADR-0001 established semantic models as LeapView's governed query contract.
ADR-0005 subsequently removed workspaces and made connections, sources, models,
semantic models, pipelines, and dashboards project-wide resources with stable
identity. It also selected Apache Ossie as the semantic interchange target and
MetricFlow as a reference for executable semantic behavior without making
either external format LeapView's runtime contract.

The current semantic authoring shape predates the project-wide refactor. A
semantic model lists project model names as tables, relationships repeat
qualified field strings and asserted cardinality, model grain is represented by
one primary-key string, and atomic measures and derived metrics occupy separate
query namespaces. Time grain, business identity, reusable eligibility
filters, and the boundary between AI guidance and executable semantics are
not modeled consistently enough to freeze as a public v1 contract.

Apache Ossie is the first credible vendor-neutral interchange specification in
this space. Its datasets, composite keys, directional relationships, portable
data types, AI context, and extensions improve several parts of LeapView's
authoring vocabulary. MetricFlow adds useful execution concepts such as named
entities, explicit metric time, fanout-safe join planning, and typed metric
behavior. Both specifications must generalize across runtimes, however, and do
not encode all of LeapView's conformed-dimension, multi-fact, empty-value,
authorization, and safe-planning invariants.

LeapView is not publicly released. The authoring contract can change directly
without compatibility aliases or migrations. The question is which semantic
contract should become the durable v1 authoring and execution boundary before
interchange support makes the existing shape expensive to change.

## Decision drivers

- Make grain, business identity, relationship safety, and metric population
  machine-checkable before query execution.
- Keep project models reusable across semantic subjects without copying their
  keys, fields, or transformation contracts.
- Give authors one concise vocabulary for every queryable quantitative member.
- Align portable concepts with Apache Ossie and executable concepts with proven
  MetricFlow patterns where doing so does not weaken LeapView's guarantees.
- Preserve LeapView's typed aggregations, conformed dimensions, explicit paths,
  empty-value behavior, and multi-fact pre-aggregation rather than recovering
  those semantics from arbitrary aggregate SQL.
- Ensure AI descriptions improve discovery but can never change governed query
  results.
- Leave room for cumulative, semi-additive, conversion, fiscal-calendar, and
  temporal-relationship behavior without requiring those features in the first
  implementation.

## Considered options

- Keep the existing `tables`, scalar primary key, qualified relationship
  endpoints, `measures`/`metrics`, and `fact` authoring contract.
- Adopt raw Apache Ossie documents as LeapView's only authored and runtime
  semantic contract.
- Adopt dbt/MetricFlow semantic YAML and execution vocabulary directly.
- Retain a typed LeapView contract while aligning its portable vocabulary and
  structures with Ossie and its execution concepts with MetricFlow.

## Decision outcome

LeapView will retain a native typed semantic contract and one internal semantic
representation. Native LeapView resources and supported versioned Ossie
documents may both compile into that representation. The native contract is
authoritative for execution; Ossie is an import, export, validation, and
extension boundary.

This decision freezes the authored and executable semantics of
`leapview.dev/v1`. Implementation remains pending, but implementation work does
not reopen the contract. A later incompatible or result-affecting semantic
change requires an explicit versioned contract and a superseding or amending
ADR.

### Models own identity and grain

A project `Model` owns its fields, logical data types, named entities, keys, and
row grain. A semantic model must not repeat a model's primary or unique keys.

Entities describe business identity and the fields that realize it:

```yaml
spec:
  entities:
    order:
      type: primary
      fields: [order_id]
    customer:
      type: foreign
      fields: [customer_id]

  grain:
    entity: order

  fields:
    order_id: {datatype: String}
    customer_id: {datatype: String}
    purchase_date: {datatype: Date}
    payment_status: {datatype: String}
    revenue: {datatype: Decimal}
```

The initial entity types are:

- `primary`: a complete, unique, non-null canonical identity eligible to define
  model grain;
- `unique`: a complete, unique, non-null alternate identity also eligible to
  define model grain;
- `foreign`: a repeated or nullable reference to an entity; and
- `natural`: a real-world identity suitable for historical validity modeling.

Entity keys are ordered field tuples and may therefore be composite. The
`grain.entity` value must reference a `primary` or `unique` entity on the Model
and defines that Model's exact row grain. A Model may declare multiple named
unique entities, including alternate external identifiers; declaring one does
not change the selected grain. Physical schema validation must confirm that all
entity fields exist and have compatible logical types. Runtime data checks
remain necessary to prove authored uniqueness and nullability claims.
`natural` alone does not prove uniqueness and is not eligible as grain or as a
relationship target unless the Model separately declares an equivalent primary
or unique entity.

### Semantic models bind project models as named datasets

A semantic model assigns stable semantic dataset names to reusable project
models:

```yaml
spec:
  datasets:
    orders:
      model: sales_orders
      defaultTimeDimension: purchase_date
    customers:
      model: sales_customers
```

The project resource ID, semantic dataset name, and display label are distinct.
Queries, relationships, dimensions, filters, and metrics use the semantic
dataset name. Changing a dataset alias is a semantic API change; changing a
display label is not.

References resolve names, not external IDs. `metadata.id` is stable project and
API identity; `metadata.name` is the authoring symbol within a project resource
kind; a dataset key is a local SemanticModel alias; an entity key is a local
Model symbol; and dimension, filter, and metric references are local
SemanticModel symbols. Consequently, `model: sales_orders` resolves the Model
whose `metadata.name` is `sales_orders`, while `model:sales_orders` may remain
that resource's external `metadata.id` without appearing in authored
references.

### Relationships are directional and key-proven

Relationships use structured endpoints. Named entity endpoints are preferred:

```yaml
relationships:
  orders_customers:
    from: {dataset: orders, entity: customer}
    to: {dataset: customers, entity: customer}
```

Ordered field-tuple endpoints are allowed when no meaningful entity exists:

```yaml
relationships:
  orders_customers:
    from: {dataset: orders, fields: [customer_id]}
    to: {dataset: customers, fields: [customer_id]}
```

`from` is the potentially-many side. `to` must resolve to a primary or unique
entity or an equivalent declared key. Authors do not declare normal
cardinality. Foreign-to-primary or foreign-to-unique is many-to-one;
unique-to-unique is one-to-one. Key arity and logical data types must match.

The compiler exposes only traversals that preserve the originating grain. It
rejects missing endpoints, unproven target uniqueness, incompatible key tuples,
cycles, unsafe reverse traversal, duplicate relationship definitions, and
ambiguous implicit path resolution. Multiple valid safe paths may coexist. An
omitted dimension binding path is inferred only when exactly one safe path
exists; otherwise the author must select a path explicitly. An explicit path
must form a complete safe route.

An aggregate metric's dataset is the query root. Traversal from that root across
safe many-to-one or one-to-one relationships preserves root rows: unmatched
root rows remain in the metric population and produce null joined dimensions.
A governed filter on a traversed dataset may require a match and
therefore remove those rows. Relationship authoring does not expose a free-form
join type; these row-preservation semantics are planner invariants.

Structured relationship endpoints reserve a compatible extension point for
future as-of relationships with fact time and target validity bounds. Equality
relationships are the only initial execution requirement. An equality join to a
customer row means the value on that joined row; it must not be described as the
value at purchase or event time unless a future as-of relationship actually
proves that temporal meaning.

### All quantitative members are metrics

The authored `measures` collection and `fact` property are removed. Every
queryable quantitative member is in one `metrics` namespace and has a tagged
type. The initial required metric types are:

- `aggregate`: a typed atomic aggregation over one dataset;
- `derived`: parsed arithmetic over other metrics; and
- `ratio`: a numerator and denominator evaluated with governed division
  semantics.

```yaml
metrics:
  order_count:
    type: aggregate
    dataset: orders
    aggregation: count_distinct
    input: {field: orders.order_id}
    empty: zero

  revenue:
    type: aggregate
    dataset: orders
    aggregation: sum
    input: {field: orders.revenue}
    empty: zero
    unit: BRL
    format: currency

  average_order_value:
    type: ratio
    numerator: revenue
    denominator: order_count
    unit: BRL
    format: currency
```

Metric types form a strict tagged union. An `aggregate` requires `dataset`,
`aggregation`, and `input`, and may add `where`, `empty`, and an explicit metric
time override. A `ratio` requires only `numerator` and `denominator` as its
executable fields. A `derived` metric requires `expression`. Common descriptive,
unit, and format properties do not affect the tag grammar, and fields belonging
to another tag are rejected.

Aggregate metrics retain the supported aggregate functions, typed input
validation, structured filters, and explicit empty-value policy. Derived
expressions may reference metrics and constants through deterministic LeapView
scalar functions and operators only; SQL aggregates, raw SQL, dataset fields,
and physical fields remain prohibited. Ratios give the planner explicit
numerator and denominator populations rather than requiring it to infer
division semantics from a general expression.

`safe_divide(a, b)` remains a portable LeapView expression function. It returns
null when either operand is null or the denominator is zero and otherwise
returns the promoted numeric quotient. It is not a SQL macro. Ratio evaluation
is defined as `safe_divide(numerator, denominator)`; v1 has no separate
zero-denominator option.

Cumulative, offset, semi-additive, and conversion metrics may be added as new
tagged metric types after their time and grain behavior is executable and
tested. They must not be approximated with dashboard calculations or arbitrary
aggregate SQL.

### Governed filters own population logic

Reusable structured filters define governed row eligibility:

```yaml
filters:
  captured_orders:
    field: orders.payment_status
    operator: equals
    value: captured

metrics:
  captured_revenue:
    type: aggregate
    dataset: orders
    aggregation: sum
    input: {field: orders.revenue}
    where: [captured_orders]
```

Named filters are reusable row-level Boolean expressions evaluated before
aggregation. An aggregate metric's `where` value is a non-empty list of filter
names combined with Boolean `AND`; omission means no filter. Authors express
other composition inside a filter with the structured `all`, `any`, and `not`
nodes. Every filter expression node is exactly one of a leaf, a non-empty `all`
child list, a non-empty `any` child list, or a `not` node with one child. Named
filter references occur only in metric `where` lists, not recursively inside
filter trees.

Leaf operators in v1 are `equals`, `not_equals`, `in`, `not_in`, `less_than`,
`less_than_or_equal`, `greater_than`, `greater_than_or_equal`, `is_null`, and
`is_not_null`. Comparison values must type-check against the field. `in` and
`not_in` require a non-empty value list. The null operators prohibit `value`;
all other operators require it, and null literal values are prohibited. A leaf
may include an explicit relationship `path`; omission follows the same
exactly-one-safe-path inference rule as a dimension binding.

Comparison with a runtime null produces unknown and does not select the row;
only `is_null` and `is_not_null` test nullness. A filter field must be safely
reachable from the metric's root dataset. A filter on a joined dataset
semantically requires a matching joined row, as specified by the relationship
row-preservation rule. Metric population, joins, filters, and calculations must
never be implied only by prose. The filter expression tree is compiled to a
typed predicate, validated, authorized, and rendered without raw SQL or template
languages. Filter values are typed literals in v1; parameter references require
a future explicit tagged value contract.

The `filters` collection contains governed, reusable semantic definitions. It
is distinct from ad hoc query or dashboard filter state, which constrains an
individual request and does not create or modify a semantic member.

Ratio and derived metrics do not accept `where`. Authors define a filtered ratio
by applying the same governed filters to its aggregate numerator and
denominator metrics. This makes population alignment explicit and independently
validatable.

### Portable logical types and explicit time semantics

Fields and conformed dimensions use the Ossie logical data-type vocabulary:
`String`, `Integer`, `Decimal`, `Float`, `Boolean`, `Date`, `Time`, `DateTime`,
`DateTimeTz`, and `Opaque`. `Opaque` requires an extension or runtime mapping
before operations that depend on its concrete type.

Temporal type and temporal role remain separate. A queryable time dimension
declares its native grain, allowed rollups, calendar, and effective timezone
where applicable:

```yaml
dimensions:
  purchase_date:
    datatype: Date
    time:
      nativeGrain: day
      grains: [day, week, month]
      calendar: iso8601
    bindings:
      orders:
        field: orders.purchase_date
```

`grains` is an allowlist. Every allowed grain must be no finer than the native
grain. Calendar and timezone determine truncation boundaries. Omitted grains
are not queryable. A dataset's `defaultTimeDimension` supplies metric time for
time-dependent behavior; an aggregate metric may explicitly override it.

Time densification, fiscal calendar resources, cumulative windows, and
period-offset execution are deferred capabilities. Empty aggregation policy and
the future creation of missing time buckets remain distinct concepts.

### Conformed dimensions remain fact-relative

Semantic dimensions remain named concepts with one validated binding per
participating metric origin. A binding resolves a physical field relative to a
semantic dataset and may declare an explicit safe relationship path. This
fact-relative binding model is retained because neither embedded dataset fields
nor implicit entity matching can safely represent conformed dimensions across
multiple facts.

### Multi-root queries aggregate before stitching

A query may combine metrics rooted in different datasets only through semantic
dimensions with valid bindings for every participating root. The planner
aggregates each root independently to the requested conformed dimension grain,
then combines the aggregate results with a null-safe full-outer stitch. It must
never join fact rows to one another.

A metric, dimension, filter, or calculation whose meaning cannot be resolved
consistently for every participating root makes the query invalid. This is a
normative execution contract, not optional planner optimization. LeapView
retains it in v1 because the existing planner already implements and tests the
behavior; runtimes that cannot honor it must reject the query rather than emit a
single-root approximation or a fanout-prone fact join.

### AI context is descriptive and non-executable

Resources and semantic members may define local `aiContext` containing
instructions, synonyms, and examples. AI context improves retrieval,
explanation, and authoring assistance only. Removing every `aiContext` value
from a valid project must leave compilation, authorization, generated plans,
and query results unchanged.

At resource level, `aiContext` is a top-level sibling of `metadata` and `spec`.
At member level, it is a sibling of the member's descriptive and executable
properties. This gives the annotation one consistent placement and maps it
directly to Ossie's first-class `ai_context` annotations.

Executable business meaning belongs in model transformations, fields,
relationships, filters, dimensions, metrics, and policies. A compiler,
planner, agent, or runtime must not use AI context to select fields, add filters,
choose join paths, or change calculations.

### Units, formats, and expressions are typed separately

`unit` describes semantic value meaning; `format` describes presentation. The
compiler recognizes ISO 4217 currency codes and may add other versioned unit
vocabularies. Counts are dimensionless unless a future entity-unit contract
defines otherwise. Arithmetic rejects known incompatible units, infers result
units where possible, and rejects an authored result unit that contradicts a
known inferred unit. Unknown units remain metadata until a vocabulary defines
their algebra.

### Ossie is a coexisting interchange format

Supported pinned Ossie versions may coexist with native LeapView semantic
resources and compile into the same typed internal representation. Ossie
datasets resolve to existing project Models; an import must not synthesize an
unreviewed connection, source, transformation, or materialization from a
dataset `source` string.

Import and export preserve LeapView-only behavior through versioned extensions.
Unsupported executable behavior is a compile error. Importers must never drop a
metric, filter, relationship, policy, or other result-affecting construct and
continue with a weaker model. Native extensions remain structured YAML; an
Ossie adapter serializes them into the interchange format's extension payload.

### Illustrative semantic model

```yaml
apiVersion: leapview.dev/v1
kind: SemanticModel
metadata:
  id: semantic-model:sales
  name: sales
  displayName: Sales
  description: Governed sales analysis.
aiContext:
  instructions: Revenue and order count use the governed captured-payment population.
  examples:
    - Show monthly revenue by customer state.
spec:
  datasets:
    orders:
      model: sales_orders
      defaultTimeDimension: purchase_date
    customers:
      model: sales_customers
  relationships:
    orders_customers:
      from: {dataset: orders, entity: customer}
      to: {dataset: customers, entity: customer}
  filters:
    captured_orders:
      field: orders.payment_status
      operator: equals
      value: captured
  dimensions:
    purchase_date:
      datatype: Date
      time:
        nativeGrain: day
        grains: [day, week, month]
        calendar: iso8601
      bindings:
        orders: {field: orders.purchase_date}
    state:
      datatype: String
      description: State associated with the customer.
      bindings:
        orders:
          field: customers.state
          path: [orders_customers]
  metrics:
    order_count:
      type: aggregate
      dataset: orders
      aggregation: count_distinct
      input: {field: orders.order_id}
      where: [captured_orders]
      empty: zero
    revenue:
      type: aggregate
      dataset: orders
      aggregation: sum
      input: {field: orders.revenue}
      where: [captured_orders]
      empty: zero
      unit: BRL
      format: currency
    average_order_value:
      type: ratio
      numerator: revenue
      denominator: order_count
      unit: BRL
      format: currency
```

## Consequences

The resulting native contract is smaller for consumers because every
quantitative query target is a metric, while remaining stricter for the compiler
because aggregate, derived, and ratio behavior is explicit. Dataset aliases
decouple business-facing semantic names from global project resource names.
Model-owned entities and grain make key safety reusable across all semantic
models. Ossie mapping becomes structural without forcing interchange-oriented
verbosity or arbitrary SQL into native authoring.

The change is intentionally breaking. Existing model primary keys, semantic
`tables`, relationship endpoints and cardinalities, `measures`, metric
expressions, dashboard measure bindings, API contracts, generated schemas,
fixtures, documentation, agent tools, and planner types must move to the new
vocabulary together. Because LeapView is pre-release, implementation must not
add compatibility readers, aliases, dual writes, or migration paths for the
removed forms.

Entity, time, filter, unit, and metric-type validation add compiler and
planner complexity. Import/export requires explicit version adapters and
round-trip tests. Some advanced semantic behavior remains deferred; the typed
extension points prevent authors from simulating those capabilities through
unsafe SQL or AI instructions in the meantime.

## Confirmation

- Generated configuration schemas require Model entities and grain, semantic
  dataset bindings, structured relationships, portable logical data types,
  filters, and tagged metrics; they reject removed `tables`, `measures`,
  `fact`, scalar-key, asserted-cardinality, and qualified-endpoint forms.
- Compiler tests prove entity key arity and type compatibility, target
  uniqueness, exact grain declaration, grain-safe and root-row-preserving
  traversal, deterministic path inference, explicit-path validation, name-based
  dataset resolution, and stable project resource identity.
- Semantic validation tests prove aggregate metric ownership and inputs,
  filter Boolean composition and null semantics, `where` population, strict
  metric tagged unions, empty-value rules, ratio population and governed
  division, derived-metric dependency acyclicity, time-grain compatibility, and
  basic unit inference.
- Architecture tests prove that `aiContext` is not consumed by compiler,
  authorization, planner, or runtime packages. Removing AI context from a
  fixture produces identical compiled plans and authorization artifacts.
- Planner tests cover root-row preservation, joined filters, single- and
  multi-fact aggregation, conformed dimensions, explicit selection among
  multiple valid paths, rejection of ambiguous implicit paths, empty results,
  filtered metrics, ratios, and null-safe pre-aggregate stitching without
  joining fact rows.
- Deployment validation prepares or explains representative plans for every
  relationship path and metric dependency graph against discovered schemas.
- Pinned Ossie fixtures validate against the official versioned schema. Native
  to Ossie to native round trips preserve portable semantics and versioned
  extensions; unsupported executable imports fail without partial compilation.
- Dashboard, API, CLI, catalog, agent, generated-contract, documentation, and
  example tests use only the unified metric vocabulary before implementation is
  marked complete.
