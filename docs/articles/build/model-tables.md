# Define model tables

A model table transforms permitted project sources into a project-owned analytical table. The goal is not merely to make SQL run; it is to establish a stable grain and field contract that semantic metrics can safely reuse.

## Before you begin

Complete [Connect a data source](/docs/guides/build/connect-data), stage a representative source revision, and profile its keys, nulls, and types. Write the intended output grain in one sentence before creating YAML.

Work in this order:

1. Declare named identity entities, the selected grain entity, permitted sources, and output fields.
2. Implement source cleanup and grain-preserving SQL.
3. Discover and validate the model resource.
4. Refresh it with representative data.
5. Verify keys, types, row counts, and repeatability.

## Design and create the table

### Start from the grain

Write one sentence before writing SQL: “One row represents one order,” “one customer,” or “one rating by one user for one movie.” Declare a primary entity (and any composite or foreign entities), select it as `grain.entity`, and determine which source joins preserve that identity.

For an order-grain table, joining raw order items directly would duplicate orders. Aggregate item-level values to `order_id` first, then join the one-row-per-order result.

### Create the resource

Create `dashboards/models/orders.yaml`:

```yaml
apiVersion: leapview.dev/v1
kind: Model
metadata:
  id: model:orders
  name: orders
  displayName: Sales orders
  description: One row per order with normalized purchase date and revenue.
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
  sources:
    - commerce.orders
  fields:
    order_id: {datatype: String, label: Order ID, description: Stable order identifier.}
    customer_id: {datatype: String, label: Customer ID}
    purchase_date: {datatype: Date, label: Purchase date}
    revenue: {datatype: Decimal, label: Revenue}
  transform:
    sql: |
      SELECT
        order_id,
        customer_id,
        try_cast(purchased_at AS DATE) AS purchase_date,
        round(coalesce(try_cast(amount AS DOUBLE), 0), 2) AS revenue
      FROM source."commerce.orders"
      WHERE order_id IS NOT NULL
```

The quoted source name is important because logical source IDs can contain dots. `spec.sources` declares lineage and bounds what the transformation may read. `spec.fields` documents the output; it is not a substitute for selecting those columns in SQL.

### Normalize deliberately

Use the model-table boundary for source-specific cleanup:

- cast weak source types to stable analytical types;
- normalize empty strings and sentinel values;
- choose how malformed values become null or are rejected;
- deduplicate with an explicit ordering rule;
- aggregate child rows before joining them to a parent grain;
- assign readable output field names.

Avoid silent lossy conversions. `try_cast` can keep a refresh running, but unexpected nulls must still be measured and reviewed. If malformed input should block activation, encode or validate that invariant explicitly.

## Validate the table

### Discover and validate

Ensure the project manifest discovers the model file:

```yaml
spec:
  models:
    include: [models/*.yaml]
```

Then validate the project:

```sh
leapview validate --project dashboards/leapview.yaml
```

Validation checks configuration shape and references. Data-level correctness requires a materialization or preview against actual source data.

## Verify the result

After deploying and refreshing in development, verify:

1. The output row count is plausible relative to the input.
2. `order_id` is non-null and unique.
3. `purchase_date` and `revenue` have stable types.
4. Failed casts and null rates are understood.
5. Joins have not multiplied the declared grain.
6. A repeated refresh over the same revision produces equivalent output.

Use the project resource browser and refresh history to inspect the table and its lineage. When several model tables are related, validate each table independently before declaring semantic relationships between them.

## Choose the materialization boundary

Keep reusable source cleanup and expensive cross-source shaping here. Put aggregations such as total revenue and average order value in semantic metrics so they remain filter-aware. A model table should only be pre-aggregated when its declared row grain is intentionally aggregated.

## Troubleshooting

If refresh multiplies rows, inspect each join against the declared grain and aggregate child records before joining. If casts silently create nulls, query the rejected source values and decide whether the resource should normalize or fail them. If validation succeeds but SQL fails, remember that structural validation cannot prove runtime column names or data types; test the transform against the staged revision.

## Next steps

Continue with [Build a semantic model](/docs/guides/build/semantic-model). The generated [Model configuration](/docs/config/model) remains the exact syntax reference.
