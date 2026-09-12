# Build a semantic model

Semantic models give dashboards and integrations a shared business vocabulary. Build one after Models have stable fields and grain; otherwise semantic definitions will hide unresolved data-shaping problems.

## Before you begin

Refresh each input Model and verify its model materialization, declared grain, keys, types, and null behavior. Prepare trusted totals for at least one unfiltered question and one dimension-filtered question.

Build the model in this sequence:

1. Choose a coherent analytical domain and its datasets.
2. Add relationships whose endpoint keys are proven by data.
3. Define simple metrics beside their datasets and physical fields.
4. Compose derived metrics from named metrics.
5. Validate the resource, then verify representative business results.

## Design the semantic surface

### Choose the model boundary

A semantic model should serve a coherent analytical domain. Bind the Models needed for that domain as datasets, identify the datasets on which metrics aggregate, and define only relationships whose endpoint keys you can defend from data.

For a small Sales model, `orders` is the primary dataset and `customers` is a dimension reached through `customer_id`.

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
  datasets:
    orders:
      model: orders
      metrics:
        order_count:
          type: simple
          label: Orders
          description: Distinct orders in the filtered result.
          empty: zero
          format: integer
          agg: count_distinct
          field: order_id
        revenue:
          type: simple
          label: Revenue
          description: Sum of order revenue in the filtered result.
          empty: zero
          format: currency
          agg: sum
    customers:
      model: customers
  relationships:
    orders_customers:
      from: { dataset: orders, fields: [ customer_id ] }
      to: { dataset: customers, fields: [ customer_id ] }
  metrics:
    aov:
      type: ratio
      label: Average order value
      numerator: revenue
      denominator: order_count
      format: currency
```

### Define metrics from datasets

Every simple metric inherits its dataset and declares `agg`. Choose `count_distinct` when the business question counts stable identifiers; use `count` only when the dataset row grain itself is the intended count. For `sum`, `avg`, `min`, and `max`, provide a compatible `field`, or omit it when the field name equals the metric name.

Set empty-result behavior intentionally. `zero` is appropriate for additive counts and sums when no rows match; `null` may better represent an undefined average. Formatting is presentation metadata and does not turn a numeric result into a formatted string at the semantic boundary.

### Add metrics for derived values

Metrics compose named semantic values. Declare ratios with `type: ratio`, `numerator`, and `denominator`; the governed evaluator applies safe division semantics centrally. If a formula needs row-level conditionals or complex source parsing, move that logic into a Model first.

### Validate relationships

The `to` relationship endpoint must identify a declared primary or unique entity on the target dataset; the `from` endpoint may repeat that key. LeapView rejects relationships that do not preserve dataset grain. Sample the data, not just the schema: a declared entity whose fields contain duplicates can still multiply rows and inflate every metric that traverses the relationship.

Prefer one unambiguous relationship path. If the model needs role-playing dimensions or several paths between the same datasets, give each path an explicit design rather than relying on query order.

## Validate the semantic model

Place semantic model files beneath the source root's conventional `semantic-models/` directory and validate the source root:

```sh
leapview validate --source-root dashboards
```

Validation should reject unknown datasets, fields, metrics, filters, and malformed relationship definitions before deployment. Resolve every diagnostic at its source resource rather than compensating in a dashboard.

## Verify business results

Deploy to development and inspect the model:

```sh
leapview semantic-models describe sales \
  --target "$LEAPVIEW_TARGET" \
  --token "$LEAPVIEW_API_TOKEN"
```

Use the dataset, field, preview, explain, and query subcommands to test representative questions before building a dashboard. At minimum, compare unfiltered totals with a trusted baseline, filter by a dimension reached through each relationship, and verify empty-result behavior.

## Troubleshooting

Inflated metrics usually indicate an unproven relationship key or an ambiguous join path. Missing values after filtering often indicate incompatible relationship key types or null keys. If a derived metric is wrong while both base metrics are correct, test its zero and null cases separately and keep row-level cleanup out of the metric expression.

## Next steps

Continue with [Create a dashboard](/docs/guides/build/dashboard). See [Semantic Model configuration](/docs/config/semantic-model) and the generated [`semantic-models` CLI reference](/docs/cli/semantic-models) for exact operations.
