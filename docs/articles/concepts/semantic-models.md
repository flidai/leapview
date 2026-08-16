# Semantic models

A semantic model exposes business concepts independently from dashboard presentation. It selects project model tables and defines the dimensions, measures, metrics, and relationships that dashboards, CLI commands, API clients, and agent tools are allowed to query.

## Tables and field references

`spec.tables` lists the model tables participating in the model. Physical fields are addressed as `table.field`, for example `orders.purchase_date`. A semantic definition cannot make an undeclared table queryable simply by naming it.

Use stable table and field IDs. Labels and descriptions can evolve, but renaming an ID breaks dashboard queries and headless clients unless it is handled as a coordinated migration.

## Dimensions

Dimensions describe how results can be grouped or filtered: dates, categories, identifiers, geography, booleans, and other descriptive attributes. A semantic dimension may bind compatible fields at declared grains so users can ask a business question without choosing a physical join path themselves.

Good dimensions have:

- a clear type and label;
- a documented business meaning;
- bindings that resolve to known table fields;
- grains that do not imply invalid fan-out;
- stable null and formatting expectations.

Not every physical field needs a named semantic dimension. Dashboard table queries can select model-table fields directly where supported, while governed reusable groupings should be modeled explicitly.

## Measures

Measures define aggregations over a fact table:

```yaml
measures:
  revenue:
    label: Revenue
    fact: orders
    aggregation: sum
    input: {field: orders.revenue}
    empty: zero
    format: currency
```

The fact identifies the table grain being aggregated. The aggregation and input determine how values are computed. `empty` makes empty-result behavior deliberate instead of leaving each consumer to interpret a missing value. Formatting metadata communicates presentation intent without changing the numeric result.

Filtered measures should use declared semantic filters rather than embedding dashboard-specific conditions. If two teams mean different things by “revenue,” give the definitions distinct names and descriptions instead of silently changing a shared formula.

## Metrics

Metrics compose measures and other supported semantic expressions. The sample `aov` metric uses `safe_divide(${revenue}, ${order_count})` so division behavior is defined centrally. Metrics are useful for ratios and derived business values that should remain consistent across report pages and headless queries.

Keep expressions small and name their inputs clearly. If an expression requires extensive row-level cleanup, move that work into a model table first.

## Relationships

Relationships connect compatible tables using explicit fields and cardinality:

```yaml
relationships:
  - id: orders_customers
    from: orders.customer_id
    to: customers.customer_id
    cardinality: many_to_one
```

LeapView accepts only cardinalities that provably preserve the fact grain. For `many_to_one`, the `to` field must be the target table's declared primary key. For `one_to_one`, both fields must be their tables' declared primary keys, which makes the relationship safe in either direction. Reverse `many_to_one`, `one_to_many`, and `many_to_many` traversal is rejected because it can duplicate fact rows and corrupt measures. Primary-key declarations must still match the real data, so confirm uniqueness from data rather than naming convention.

Avoid multiple plausible paths between the same facts and dimensions. Ambiguous paths should be redesigned or rejected instead of letting query order determine results.

## Query consumers

Dashboard visual queries map result aliases to semantic dimensions and measures. The semantic-model CLI and API expose the same governed vocabulary for discovery, preview, explain, and query operations. That shared surface is the main benefit of modeling once: interactive and headless consumers cannot quietly diverge.

## Review checklist

Before publishing a model, verify that:

- every table exists in the project graph;
- relationship fields have compatible types;
- every relationship's `one` endpoint is the table's declared primary key, and those keys are unique in the data;
- measures identify the correct fact and aggregation;
- empty-result and formatting behavior are intentional;
- labels and descriptions are understandable outside the authoring team;
- representative grouped and filtered queries return expected values.

Continue with [Build a semantic model](/docs/guides/build/semantic-model) and use the generated [Semantic Model configuration](/docs/config/semantic-model) for exact syntax.
