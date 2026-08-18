# Donut chart

Use a donut chart for part-to-whole comparisons that benefit from a central annotation.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Basic

Use one categorical dimension and one metric to show each status as a share of the whole.

{{< visual id="orders" >}}

```yaml visual-example=orders
visuals:
  orders:
    title: Orders by status
    description: Breaks down orders by lifecycle status.
    type: donut
    presentation:
      type: proportional
    query:
      type: aggregate
      dimensions:
      - status
      metrics:
      - order_count
      sort:
      - field: order_count
        direction: desc
```

## Alternate metric

Replace the category and metric to compare revenue composition without changing the donut renderer or query shape.

{{< visual id="category_donut" >}}

```yaml visual-example=category_donut
visuals:
  category_donut:
    title: Revenue by category donut
    type: donut
    query:
      type: aggregate
      dimensions:
      - category
      metrics:
      - revenue
      sort:
      - field: revenue
        direction: desc
      limit: 8
    presentation:
      type: proportional
```
