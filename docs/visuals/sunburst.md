# Sunburst

Use a sunburst to compare hierarchical levels as concentric part-to-whole rings.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Two-level hierarchy

Order two dimensions from parent to child and provide one metric for sector size to show category composition by status.

{{< visual id="category_status_sunburst" >}}

```yaml visual-example=category_status_sunburst
visuals:
  category_status_sunburst:
    title: Category and status hierarchy
    description: Shows category and status hierarchy by order count.
    type: sunburst
    presentation:
      labels: {density: automatic, priority: [selected, anomaly, threshold], maxCharacters: 12, minimumSpacing: 6, tooltipFallback: true}
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

## Alternate hierarchy

Replace the parent dimension with state to reuse the hierarchy contract for a geographic breakdown.

{{< visual id="state_status_sunburst" >}}

```yaml visual-example=state_status_sunburst
visuals:
  state_status_sunburst:
    title: State and status sunburst
    type: sunburst
    query:
      dimensions:
        state: orders.state
        status: orders.status
      metrics:
        order_count: null
      sort:
        - field: value
          direction: desc
      limit: 80
```

## Three-level hierarchy

Add a third ordered dimension for deeper nesting, set `initial_depth` to control the first visible level, and enable roaming for exploration.

{{< visual id="category_state_status_sunburst" >}}

```yaml visual-example=category_state_status_sunburst
visuals:
  category_state_status_sunburst:
    title: Category, state, and status sunburst
    type: sunburst
    presentation:
      initial_depth: 2
      roam: true
    query:
      dimensions:
        category: orders.category
        state: orders.state
        status: orders.status
      metrics:
        order_count: null
      sort:
        - field: value
          direction: desc
      limit: 120
```
