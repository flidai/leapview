# Heatmap

Use a heatmap to show a metric across two categorical dimensions.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Basic matrix

Provide row and column dimensions plus one metric for compact categorical intensity.

{{< visual id="state_status_heatmap" >}}

```yaml visual-example=state_status_heatmap
visuals:
  state_status_heatmap:
    title: State by order status
    description: Shows order status concentration by customer state.
    type: heatmap
    query:
      type: aggregate
      dimensions:
      - state
      - status
      metrics:
      - order_count
      sort:
      - field: state
        direction: asc
      limit: 120
    presentation:
      type: cartesian
```

## Alternate dimensions

Replace the row dimension with product category to reuse the same matrix contract for a different categorical relationship.

{{< visual id="category_status_heatmap" >}}

```yaml visual-example=category_status_heatmap
visuals:
  category_status_heatmap:
    title: Category by order status
    type: heatmap
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
      limit: 120
    presentation:
      type: cartesian
```

## Cell labels

Use the renderer-neutral `presentation.labels` policy when exact cell values matter. `automatic` preserves priority labels and suppresses collisions responsively; full values remain available in tooltips.

{{< visual id="category_status_heatmap_labels" >}}

```yaml visual-example=category_status_heatmap_labels
visuals:
  category_status_heatmap_labels:
    title: Labeled category status heatmap
    type: heatmap
    presentation:
      type: cartesian
      labels:
        density: automatic
        priority: [selected, anomaly, threshold]
        maxCharacters: 12
        minimumSpacing: 4
        tooltipFallback: true
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
      limit: 80
```
