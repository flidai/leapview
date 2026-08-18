# Semantic models

A semantic model exposes business concepts independently from dashboard presentation. It binds project Models as named datasets and defines the dimensions, metrics, filters, and relationships that dashboards, CLI commands, API clients, and agent tools are allowed to query.

## Datasets and field references

`spec.datasets` gives each participating Model a stable semantic name. Physical fields are addressed as `dataset.field`, for example `orders.purchase_date`. A semantic definition cannot make an undeclared dataset queryable simply by naming it.

Use stable dataset and field IDs. Labels and descriptions can evolve, but renaming an ID breaks dashboard queries and headless clients unless it is handled as a coordinated migration.

## Dimensions

Dimensions describe how results can be grouped or filtered: dates, categories, identifiers, geography, booleans, and other descriptive attributes. A semantic dimension may bind compatible fields at declared grains so users can ask a business question without choosing a physical join path themselves.

Good dimensions have:

- a clear type and label;
- a documented business meaning;
- bindings that resolve to known dataset fields;
- grains that do not imply invalid fan-out;
- stable null and formatting expectations.

Not every physical field needs a named semantic dimension. Dashboard table queries can select model-table fields directly where supported, while governed reusable groupings should be modeled explicitly.

## Aggregate metrics

Metrics define aggregations over a dataset:

```yaml
metrics:
  revenue:
    type: aggregate
    label: Revenue
    dataset: orders
    aggregation: sum
    input: {field: orders.revenue}
    empty: zero
    format: currency
```

The dataset identifies the population being aggregated. The aggregation and input determine how values are computed. `empty` makes empty-result behavior deliberate instead of leaving each consumer to interpret a missing value. Formatting metadata communicates presentation intent without changing the numeric result.

Filtered metrics should use declared semantic filters rather than embedding dashboard-specific conditions. If two teams mean different things by “revenue,” give the definitions distinct names and descriptions instead of silently changing a shared formula.

## Derived and ratio metrics

Derived metrics compose other metrics and supported semantic expressions. Declare ratios explicitly with `type: ratio` and named inputs:

```yaml
metrics:
  aov:
    type: ratio
    numerator: revenue
    denominator: order_count
    label: Average order value
    format: currency
```

The governed evaluator applies safe division semantics (including empty or zero denominators) centrally; authors do not need to embed a `safe_divide` expression. Metrics are useful for ratios and derived business values that should remain consistent across report pages and headless queries.

Keep expressions small and name their inputs clearly. If an expression requires extensive row-level cleanup, move that work into a model table first.

## Relationships

Relationships connect compatible datasets using structured endpoints:

```yaml
relationships:
  orders_customers:
    from: {dataset: orders, fields: [customer_id]}
    to: {dataset: customers, fields: [customer_id]}
```

LeapView derives cardinality from declared entities and keys. The `to` endpoint must be a primary or unique entity (or an equivalent declared key), while the `from` side may repeat. Unsafe reverse traversal is rejected because it can duplicate dataset rows and corrupt metrics. Key declarations must still match the real data, so confirm uniqueness from data rather than naming convention.

Avoid multiple plausible paths between the same datasets and dimensions. Ambiguous paths should be redesigned or rejected instead of letting query order determine results.

## Query consumers

Dashboard visual queries map result aliases to semantic dimensions and metrics. The semantic-model CLI and API expose the same governed vocabulary for discovery, preview, explain, and query operations. That shared surface is the main benefit of modeling once: interactive and headless consumers cannot quietly diverge.

## Review checklist

Before publishing a model, verify that:

- every dataset resolves to a project Model;
- relationship fields have compatible types;
- every relationship's `to` endpoint is a declared primary or unique key, and those keys are unique in the data;
- metrics identify the correct dataset and aggregation;
- empty-result and formatting behavior are intentional;
- labels and descriptions are understandable outside the authoring team;
- representative grouped and filtered queries return expected values.

Continue with [Build a semantic model](/docs/guides/build/semantic-model) and use the generated [Semantic Model configuration](/docs/config/semantic-model) for exact syntax.
