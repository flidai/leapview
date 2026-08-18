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
