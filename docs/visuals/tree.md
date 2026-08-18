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
      orientation: horizontal
      initial_depth: 2
      roam: true
      labels: {density: automatic, priority: [selected, anomaly, threshold], max_characters: 18, minimum_spacing: 6, tooltip_fallback: true}
    query:
      dataset: service_teams
      dimensions:
        division: service_teams.division
        team: service_teams.team
      metrics:
        active_work_items: null
      sort:
        - field: value
          direction: desc
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

## Three-level hierarchy

Add state as an intermediate level, use `initial_depth` to limit the initial expansion, and apply automatic label collision handling so deeper nodes remain legible as the card resizes.

{{< visual id="category_state_status_tree" >}}

```yaml visual-example=category_state_status_tree
visuals:
  category_state_status_tree:
    title: Category, state, and status tree
    type: tree
    presentation:
      orientation: vertical
      initial_depth: 2
      labels: {density: automatic, priority: [selected, anomaly, threshold], max_characters: 16, minimum_spacing: 6, tooltip_fallback: true}
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
