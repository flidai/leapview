# Build a semantic model

Semantic models give dashboards and integrations a shared business vocabulary. Build one after model tables have stable fields and grain; otherwise semantic definitions will hide unresolved data-shaping problems.

## Before you begin

Materialize each input model table and verify its declared grain, keys, types, and null behavior. Prepare trusted totals for at least one unfiltered question and one dimension-filtered question.

Build the model in this sequence:

1. Choose a coherent analytical domain and its fact tables.
2. Add relationships whose cardinality is proven by data.
3. Define base measures on explicit facts and fields.
4. Compose derived metrics from named measures.
5. Validate the resource, then verify representative business results.

## Design the semantic surface

### Choose the model boundary

A semantic model should serve a coherent analytical domain. Select the model tables needed for that domain, identify the facts on which measures aggregate, and define only relationships whose cardinality you can defend from data.

For a small Sales model, `orders` is the fact and `customers` is a dimension reached through `customer_id`.

### Create the resource

Create `dashboards/semantic-models/sales.yaml`:

```yaml
apiVersion: leapview.dev/v1
kind: SemanticModel
metadata:
  id: semantic-model:sales
  name: sales
  displayName: Sales semantic model
  description: Governed order and revenue analysis.
spec:
  tables:
    - orders
    - customers
  relationships:
    - id: orders_customers
      from: orders.customer_id
      to: customers.customer_id
      cardinality: many_to_one
  measures:
    order_count:
      label: Orders
      description: Distinct orders in the filtered result.
      fact: orders
      aggregation: count_distinct
      input: {field: orders.order_id}
      empty: zero
      format: integer
    revenue:
      label: Revenue
      description: Sum of order revenue in the filtered result.
      fact: orders
      aggregation: sum
      input: {field: orders.revenue}
      empty: zero
      format: currency
  metrics:
    aov:
      label: Average order value
      expression: safe_divide(${revenue}, ${order_count})
      format: currency
```

### Define measures from facts

Every measure identifies its fact table and aggregation. Choose `count_distinct` when the business question counts stable identifiers; use `count` only when the table row grain itself is the intended count. For `sum`, `avg`, `min`, and `max`, provide a compatible input field.

Set empty-result behavior intentionally. `zero` is appropriate for additive counts and sums when no rows match; `null` may better represent an undefined average. Formatting is presentation metadata and does not turn a numeric result into a formatted string at the semantic boundary.

### Add metrics for derived values

Metrics compose named semantic values. `safe_divide` makes the denominator-zero case explicit and keeps the formula out of each dashboard. If a formula needs row-level conditionals or complex source parsing, move that logic into a model table first.

### Validate relationships

For `many_to_one`, the `to` field must be the target table's declared primary key and must be type-compatible with the `from` field. For `one_to_one`, both endpoints must be their tables' declared primary keys. LeapView rejects `one_to_many` and `many_to_many` relationships because they do not preserve the fact grain. Sample the data, not just the schema: a declared key that contains duplicates can still multiply fact rows and inflate every measure that traverses the relationship.

Prefer one unambiguous relationship path. If the model needs role-playing dimensions or several paths between the same tables, give each path an explicit design rather than relying on query order.

## Validate the semantic model

Ensure the project manifest includes semantic model files and validate the project:

```sh
leapview validate --project dashboards/leapview.yaml
```

Validation should reject unknown tables, fields, measures, and malformed relationship definitions before deployment. Resolve every diagnostic at its source resource rather than compensating in a dashboard.

## Verify business results

Deploy to development and inspect the model:

```sh
leapview semantic-models describe sales \
  --target "$LEAPVIEW_TARGET" \
  --token "$LEAPVIEW_API_TOKEN"
```

Use the dataset, field, preview, explain, and query subcommands to test representative questions before building a dashboard. At minimum, compare unfiltered totals with a trusted baseline, filter by a dimension reached through each relationship, and verify empty-result behavior.

## Troubleshooting

Inflated measures usually indicate a violated relationship cardinality or an ambiguous join path. Missing values after filtering often indicate incompatible relationship key types or null keys. If a derived metric is wrong while both base measures are correct, test its zero and null cases separately and keep row-level cleanup out of the metric expression.

## Next steps

Continue with [Create a dashboard](/docs/guides/build/dashboard). See [Semantic Model configuration](/docs/config/semantic-model) and the generated [`semantic-models` CLI reference](/docs/cli/semantic-models) for exact operations.
