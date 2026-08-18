# Donut chart

Use a donut chart for part-to-whole comparisons that benefit from a central annotation.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Basic

Use one categorical dimension and one metric to show each status as a share of the whole, with an explicit center annotation identifying the represented total.

{{< visual id="orders" >}}

```yaml visual-example=orders
visuals:
  orders:
    title: Orders by status
    description: Breaks down orders by lifecycle status.
    type: donut
    presentation:
      center_label: Orders
      inner_radius: 0.54
      outer_radius: 0.76
    query:
      dimensions:
        status: orders.status
      metrics:
        order_count: null
      sort:
        - field: value
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
      dimensions:
        category: orders.category
      metrics:
        revenue: null
      sort:
        - field: value
          direction: desc
      limit: 8
```

## Center label

Set `presentation.center_label` to state the total represented by the ring, and adjust the typed inner and outer radii to control the ring diameters.

{{< visual id="orders_donut_center" >}}

```yaml visual-example=orders_donut_center
visuals:
  orders_donut_center:
    title: Orders donut with center label
    type: donut
    presentation:
      center_label: Orders
      inner_radius: 0.54
      outer_radius: 0.76
    query:
      dimensions:
        status: orders.status
      metrics:
        order_count: null
      sort:
        - field: value
          direction: desc
```
