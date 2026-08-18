# Pie chart

Use a pie chart for a small number of categories that form a meaningful whole.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Basic

Use one categorical dimension and one metric for a part-to-whole comparison, sorting by value to keep the largest sectors easy to find.

{{< visual id="status_pie" >}}

```yaml visual-example=status_pie
visuals:
  status_pie:
    title: Orders by status pie
    type: pie
    query:
      type: aggregate
      dimensions:
      - status
      metrics:
      - order_count
      sort:
      - field: value
        direction: desc
    presentation:
      type: proportional
```

## Rose sectors

Set `presentation.rose_type: radius` to encode values through sector radius as well as angle, and show labels so the less familiar form remains readable.

{{< visual id="status_pie_rose" >}}

```yaml visual-example=status_pie_rose
visuals:
  status_pie_rose:
    title: Orders by status rose pie
    type: pie
    presentation:
      type: proportional
      labels:
        density: automatic
        priority:
        - selected
        - anomaly
        - threshold
        maxCharacters: 24
        minimumSpacing: 6
        tooltipFallback: true
    query:
      type: aggregate
      dimensions:
      - status
      metrics:
      - order_count
      sort:
      - field: value
        direction: desc
```

## Compact labels

Move labels inside the sectors for a compact presentation; keep the category count low enough that internal labels do not collide.

{{< visual id="category_pie_inside" >}}

```yaml visual-example=category_pie_inside
visuals:
  category_pie_inside:
    title: Compact category pie
    type: pie
    presentation:
      type: proportional
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
      - category
      metrics:
      - revenue
      sort:
      - field: value
        direction: desc
      limit: 6
```
