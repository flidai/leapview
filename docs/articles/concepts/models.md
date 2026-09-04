# Models

Models are project-owned resources that transform permitted project sources. They form the stable boundary between physical input data and the semantic layer; their refreshed output is materialized analytical state.

A LeapView Model is a logical resource, not a physical table. A refresh creates a model materialization for that resource; documentation may call the resulting physical relation a materialized table when storage details matter. Neither physical term changes the resource's identity as a Model.

Raw inputs often contain transport-oriented names, weakly typed values, duplicate records, or joins that should not be repeated for every chart. A Model makes that cleanup and shaping explicit once.

## Contract

A Model declares named identity entities (primary, unique, or foreign), a selected grain entity, source dependencies, documented output fields, and a SQL transformation:

```yaml
apiVersion: leapview.dev/v1
kind: Model
metadata:
  id: model:orders
  name: orders
  displayName: Orders
spec:
  definition:
    type: sql
    sql: |
      SELECT order_id, try_cast(order_purchase_timestamp AS DATE) AS purchase_date,
        CAST(0 AS DECIMAL(38, 2)) AS revenue
      FROM source."olist.orders"
      WHERE order_id IS NOT NULL
  entities:
    order:
      type: primary
      fields: [order_id]
  grain:
    entity: order
  fields:
    order_id: {datatype: String, label: Order ID}
    purchase_date: {datatype: Date, label: Purchase date}
    revenue: {datatype: Decimal, label: Revenue}
```

The generated [Model configuration](/docs/config/model) is the exact field reference. Governed source/model relations can feed a transformation, and the compiler derives the resulting lineage so downstream semantic models see every exposed field.

## Grain and identity entities

The grain states what one row represents through `grain.entity`; identity entities declare the ordered field tuple that identifies or relates those rows. Use `type: primary` for the row identity, `type: unique` for alternate identities, and `type: foreign` for relationships to another entity. For example:

```yaml
entities:
  order:
    type: primary
    fields: [order_id]
  order_line:
    type: unique
    fields: [order_id, product_id]
grain:
  entity: order_line
```

This makes composite identity explicit instead of encoding it as a scalar grain value. Do not join a one-to-many dimension into an order-grain Model without deciding how the join changes grain. Duplicate rows will inflate sums and counts later, even if individual previews look plausible. Validate entity uniqueness, nullability, and expected row counts during development.

## What belongs in a Model

Good Model work includes:

- parsing timestamps and numeric strings into stable types;
- normalizing identifiers and missing values;
- deduplicating records according to a documented rule;
- joining source records needed by most consumers;
- deriving reusable physical columns;
- reducing expensive raw inputs to a supported analytical grain.

Business aggregations such as revenue, active customers, or conversion rate generally belong in semantic metrics. Keep them out of Model SQL unless the Model's declared grain itself is aggregated.

## Source namespace

Transform SQL reads permitted project sources through the source namespace. Quoted names are important when source IDs contain dots. The compiler derives lineage from governed SQL and keeps refresh planning accurate.

## Refresh and activation

Materialization builds replacement analytical state away from active serving state. A successful refresh validates and activates the new state. A failed or cancelled refresh leaves existing queries on the previous usable state.

That boundary does not make transformations automatically safe. A valid SQL statement can still produce the wrong grain, unexpected nulls, or an empty materialized output. Preview inputs, inspect output, and compare invariants before promoting a change.

## Design checklist

Before exposing a Model to the semantic layer, confirm:

- its name and field IDs are stable;
- one sentence can describe the grain;
- the primary entity fields are non-null and unique at the selected grain;
- field types do not depend on accidental source inference;
- every source dependency is referenced through the governed SQL namespace and
  appears in compiler-derived lineage;
- expensive repeated work is materialized once;
- the transformation has a bounded and understandable failure mode.

See [Define models](/docs/guides/build/models) for the full workflow and [Materialization and refresh](/docs/guides/data/refresh) for operations.
