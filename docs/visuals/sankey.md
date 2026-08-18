# Sankey

Use a Sankey chart to show weighted flow between two categorical stages.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Basic flow

Map two dimensions to source and target nodes and one metric to link width, revealing how orders flow from status to delivery speed.

{{< visual id="status_delivery_flow" >}}

```yaml visual-example=status_delivery_flow
visuals:
  status_delivery_flow:
    title: Status to delivery speed
    description: Shows flow from order status to delivery-speed bucket.
    type: sankey
    presentation:
      type: hierarchy
      orientation: horizontal
      labels:
        density: automatic
        priority:
        - selected
        - anomaly
        - threshold
        maxCharacters: 18
        minimumSpacing: 6
        tooltipFallback: true
    query:
      type: aggregate
      dimensions:
      - status
      - delivery_bucket
      metrics:
      - order_count
      sort:
      - field: order_count
        direction: desc
      limit: 40
```

## Alternate flow

Replace the source and target dimensions to inspect category-to-status flow without changing the weighted graph contract.

{{< visual id="category_status_flow" >}}

```yaml visual-example=category_status_flow
visuals:
  category_status_flow:
    title: Category to status flow
    type: sankey
    query:
      type: aggregate
      dimensions:
      - category
      - status
      metrics:
      - order_count
      sort:
      - field: order_count
        direction: desc
      limit: 60
    presentation:
      type: hierarchy
```

## Spacious nodes

Increase `presentation.node_gap` when labels or links feel crowded, and tune `curveness` to keep parallel flows visually distinct.

{{< visual id="category_status_flow_spacious" >}}

```yaml visual-example=category_status_flow_spacious
visuals:
  category_status_flow_spacious:
    title: Spacious category to status flow
    type: sankey
    presentation:
      type: hierarchy
    query:
      type: aggregate
      dimensions:
      - category
      - status
      metrics:
      - order_count
      sort:
      - field: order_count
        direction: desc
      limit: 60
```
