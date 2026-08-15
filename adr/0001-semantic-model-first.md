# ADR-0001: Use a semantic-model-first BI contract

Status: accepted

Decision date: 2026-06-18

Implementation: complete

Deciders: LeapView maintainers

## Summary

LeapView uses a semantic-model-first BI contract:

```text
sources -> models -> semantic model -> dashboards
```

The browser UI and dashboard YAML query governed semantic models directly. LeapView does not expose a separate curated query layer between semantic models and dashboards in v1.

## Decision

Use one authored path:

- `sources` describe raw physical inputs.
- `models` describe DuckDB-backed model tables with light preparation.
- `semantic_models` define tables, fields, relationships, and measures.
- `dashboards` reference one semantic model and query its fields/measures.

Generated physical serving shapes are internal optimizations. They are not authored dashboard contracts and should not appear as primary authored assets.

## Product Vocabulary

| Term | Meaning |
| --- | --- |
| Connection | Global data-access configuration. Secrets are never shown. |
| Source | Raw file/table/object read through a connection. No business semantics. |
| Model | Light DuckDB preparation over one or more sources. |
| Model table | Semantic table exposed by a semantic model. |
| Field | Groupable/filterable semantic field on a model table. |
| Relationship | Governed join path between model tables. |
| Measure | Governed typed atomic aggregate owned by one fact table. |
| Metric | Governed arithmetic expression over measures and other metrics. |
| Semantic model | The governed business model used by dashboards. |
| Dashboard | Presentation layer that queries a semantic model. |
| Materialization | Internal generated physical serving structure. |

## Authored Shape

```yaml
sources:
  olist_orders:
    connection: olist
    path: olist_orders_dataset.csv
    format: csv

models:
  orders:
    sources: [olist_orders]
    sql: |
      SELECT
        order_id,
        customer_id,
        CAST(order_purchase_timestamp AS TIMESTAMP) AS purchase_timestamp,
        order_status AS status
      FROM source.olist_orders

semantic_models:
  olist:
    tables:
      - orders
      - customers

    relationships:
      - id: orders_customers
        from: orders.customer_id
        to: customers.customer_id
        cardinality: many_to_one

    dimensions:
      customer_state:
        type: string
        bindings:
          orders:
            field: customers.state
            path: [orders_customers]

    measures:
      revenue:
        fact: orders
        aggregation: sum
        input: {field: orders.revenue}
        empty: zero
        format: currency

      order_count:
        fact: orders
        aggregation: count
        empty: zero

    metrics:
      revenue_per_order:
        expression: safe_divide(${revenue}, ${order_count})
        format: currency
```

Facts are inferred from atomic measure ownership. Model-scoped queries may combine facts only through semantic dimensions with compatible bindings for every participating fact.

Dashboards query that model directly:

```yaml
semantic_model: olist

visuals:
  revenue_by_state:
    query:
      dimensions:
        state: customers.state
      measures:
        revenue:
```

## Rules

LeapView should force a safe default path:

1. Sources are raw-only and never define joins, measures, fields, or business logic.
2. Models are light preparation only: casts, cleanup, naming, and grain-alignment preparation.
3. Semantic models own fields, relationships, and measures.
4. Dashboards never reference SQL joins, physical files, source names, or generated serving structures.
5. Atomic measures declare one owning fact, one supported aggregation, and an explicit empty-value policy.
6. Metrics contain parsed arithmetic over measures and metrics; SQL aggregates and field references are rejected.
7. Multi-fact dimensions are conformed semantic dimensions bound to every participating fact.
8. Dimension bindings may follow safe `many_to_one` or `one_to_one` paths.
9. One-to-many, many-to-many, circular, ambiguous implicit, or missing paths are rejected.
10. Multi-fact aggregate queries pre-aggregate each fact and stitch results without joining fact rows.
11. Row/detail queries without measures must declare a table.
12. Authored model SQL uses `source.<name>` only; `raw.<name>` is internal runtime plumbing.
13. Semantic models remain the business-definition and curation boundary; models are not composed implicitly at runtime.

## Why This Shape

This keeps the product close to the Power BI mental model: a semantic model is the governed business layer, and measures are the simplified DAX-like contract. We get a clear star-schema workflow without adopting DAX, Power Query, or a general transformation framework.

The semantic model can still be optimized internally. LeapView may generate physical tables, rollups, or cached results for speed, but those are runtime concerns. Authors model the business graph and dashboard queries remain semantic.

## Future Extensions

If repeated query subsets become painful, add optional semantic views later. A view should be a DRY, permission, or curation layer over the semantic model, not a required v1 modeling layer.

If heavier transformations are needed, they should live upstream. LeapView can support small local SQL preparation, but it should not become a full transformation orchestrator.

## Confirmation

Configuration schemas, compiler tests, semantic-planner tests, dashboard
validation, and agent/query contracts must continue to reject dashboard access
to physical sources, authored joins, and generated serving structures. A change
that introduces another mandatory query layer or bypasses the governed semantic
model requires a superseding ADR.
