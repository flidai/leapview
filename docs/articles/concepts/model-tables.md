# Model tables

Model tables are project-owned analytical tables built from permitted project sources. They form the stable boundary between physical input data and the semantic layer.

Raw inputs often contain transport-oriented names, weakly typed values, duplicate records, or joins that should not be repeated for every chart. A model table makes that cleanup and shaping explicit once.

## Contract

A model table declares a stable identity, primary key, grain, source dependencies, documented output fields, and a SQL transformation:

```yaml
apiVersion: leapview.dev/v1
kind: Model
metadata:
  id: model:orders
  name: orders
  displayName: Orders Fact
spec:
  primaryKey: order_id
  grain: order_id
  sources:
    - olist.orders
  fields:
    order_id: {label: Order ID}
    purchase_date: {label: Purchase date}
    revenue: {label: Revenue}
  transform:
    sql: |
      SELECT
        order_id,
        try_cast(order_purchase_timestamp AS DATE) AS purchase_date,
        0::DOUBLE AS revenue
      FROM source."olist.orders"
      WHERE order_id IS NOT NULL
```

The generated [Model configuration](/docs/config/model) is the exact field reference. Real transformations can use several declared sources and should expose every field needed by downstream semantic models.

## Grain and key

The grain states what one row represents; the primary key identifies that row. Document both even when they use the same field. For example, an order table may have `grain: order_id`, while an order-item table may use an item key and a grain composed conceptually of order plus product line.

Do not join a one-to-many dimension into a fact table without deciding how the join changes grain. Duplicate fact rows will inflate sums and counts later, even if individual previews look plausible. Validate key uniqueness and expected row counts during development.

## What belongs in a model table

Good model-table work includes:

- parsing timestamps and numeric strings into stable types;
- normalizing identifiers and missing values;
- deduplicating records according to a documented rule;
- joining source records needed by most consumers;
- deriving reusable physical columns;
- reducing expensive raw inputs to a supported analytical grain.

Business aggregations such as revenue, active customers, or conversion rate generally belong in semantic measures and metrics. Keep them out of model SQL unless the table's declared grain itself is aggregated.

## Source namespace

Transform SQL reads permitted project sources through the source namespace. Quoted names are important when source IDs contain dots. Depend only on sources declared on the model table; this keeps lineage and refresh planning accurate.

## Refresh and activation

Materialization builds replacement analytical state away from active serving state. A successful refresh validates and activates the new state. A failed or cancelled refresh leaves existing queries on the previous usable state.

That boundary does not make transformations automatically safe. A valid SQL statement can still produce the wrong grain, unexpected nulls, or an empty table. Preview inputs, inspect output, and compare invariants before promoting a change.

## Design checklist

Before exposing a table to the semantic layer, confirm:

- its name and field IDs are stable;
- one sentence can describe the grain;
- the primary key is non-null and unique at that grain;
- field types do not depend on accidental source inference;
- every source dependency is declared;
- expensive repeated work is materialized once;
- the transformation has a bounded and understandable failure mode.

See [Define model tables](/docs/guides/build/model-tables) for the full workflow and [Materialization and refresh](/docs/guides/data/refresh) for operations.
