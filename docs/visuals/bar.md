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
      dimensions:
        category: orders.category
      metrics:
        revenue: null
      sort:
        - field: value
          direction: desc
      limit: 10
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
      dimensions:
        delivery_bucket: orders.delivery_bucket
      metrics:
        order_count: null
      sort:
        - field: delivery_bucket
          direction: asc
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
      stacked: true
    query:
      dimensions:
        category: orders.category
      series:
        field: orders.status
        alias: status
      metrics:
        revenue: null
      sort:
        - field: value
          direction: desc
      limit: 60
```
