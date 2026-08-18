# Heatmap

Use a heatmap to show values at the intersections of two categorical dimensions.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Basic matrix

Provide two dimensions for the row and column axes and one metric for cell intensity, creating a compact comparison across both categories.

{{< visual id="state_status_heatmap" >}}

```yaml visual-example=state_status_heatmap
visuals:
  state_status_heatmap:
    title: State by order status
    description: Shows order status concentration by customer state.
    type: heatmap
    query:
      dimensions:
        state: orders.state
        status: orders.status
      metrics:
        order_count: null
      sort:
        - field: state
          direction: asc
        - field: status
          direction: asc
      limit: 120
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
      dimensions:
        category: orders.category
        status: orders.status
      metrics:
        order_count: null
      sort:
        - field: value
          direction: desc
      limit: 120
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
      labels: {density: automatic, priority: [selected, anomaly, threshold], max_characters: 12, minimum_spacing: 4, tooltip_fallback: true}
    query:
      dimensions:
        category: orders.category
        status: orders.status
      metrics:
        order_count: null
      sort:
        - field: value
          direction: desc
      limit: 80
```
