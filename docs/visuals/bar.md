# Bar chart

Use a bar chart to compare metrics across ranked categories with horizontal bars.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Ranked categories

Sort the metric descending to make the longest horizontal bar the leading category. This orientation works well for category labels of unequal length.

{{< visual id="categories" >}}

```yaml visual-example=categories
visuals:
  categories:
    title: Top product categories
    description: Ranks product categories by revenue.
    type: bar
    query:
      type: aggregate
      dimensions:
      - category
      metrics:
      - revenue
      sort:
      - field: value
        direction: desc
      limit: 10
    presentation:
      type: cartesian
```

## Alternate metric

Keep the bar contract and replace the dimension with delivery buckets to compare counts across an ordered operational grouping.

{{< visual id="delivery" >}}

```yaml visual-example=delivery
visuals:
  delivery:
    title: Delivery speed
    description: Compares order volume across delivery-speed buckets.
    type: bar
    query:
      type: aggregate
      dimensions:
      - delivery_bucket
      metrics:
      - order_count
      sort:
      - field: delivery_bucket
        direction: asc
    presentation:
      type: cartesian
```

## Stacked series

Use `query.series` for status and `presentation.stacked` to combine each status segment into one category total while preserving its composition.

{{< visual id="categories_by_status_bar" >}}

```yaml visual-example=categories_by_status_bar
visuals:
  categories_by_status_bar:
    title: Category revenue by status
    type: bar
    presentation:
      type: cartesian
    query:
      type: aggregate
      dimensions:
      - category
      metrics:
      - revenue
      sort:
      - field: value
        direction: desc
      limit: 60
```
