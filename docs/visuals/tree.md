# Tree

Use a tree to show hierarchical paths when parent-child structure should remain explicit.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Operating model

Order dimensions from division to team and use workload to make the hierarchy useful without overcrowding a dashboard card.

{{< visual id="operating_model_tree" >}}

```yaml visual-example=operating_model_tree
visuals:
  operating_model_tree:
    title: Workload by division and team
    description: Shows the operating model and active workload across its teams.
    type: tree
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
      - division
      - team
      metrics:
      - active_work_items
      sort:
      - field: active_work_items
        direction: desc
```

## Three-level hierarchy

Add state as an intermediate ordered dimension and use `initialDepth` to limit the initial expansion so deeper nodes remain legible as the card resizes.

{{< visual id="category_state_status_tree" >}}

```yaml visual-example=category_state_status_tree
visuals:
  category_state_status_tree:
    title: Category, state, and status tree
    type: tree
    presentation:
      type: hierarchy
      orientation: vertical
      initialDepth: 2
      labels:
        density: automatic
        priority: [selected, anomaly, threshold]
        maxCharacters: 16
        minimumSpacing: 6
        tooltipFallback: true
    query:
      type: aggregate
      dimensions:
      - category
      - state
      - status
      metrics:
      - order_count
      sort:
      - field: order_count
        direction: desc
      limit: 120
```

## Alternate hierarchy

Replace the parent dimension with category to present a different two-level hierarchy using the same tree shape.

{{< visual id="category_status_tree" >}}

```yaml visual-example=category_status_tree
visuals:
  category_status_tree:
    title: Category and status tree
    type: tree
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
    presentation:
      type: hierarchy
```
