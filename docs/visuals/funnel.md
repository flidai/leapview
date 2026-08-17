# Funnel chart

Use a funnel chart to show ordered stages whose values usually decrease through a process.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Ordered conversion stages

Use actual sequential stages whose population narrows through one process. Numeric prefixes keep the business sequence explicit and stable.

{{< visual id="checkout_funnel" >}}

```yaml visual-example=checkout_funnel
visuals:
  checkout_funnel:
    title: Checkout conversion
    description: Shows progression from product visits to completed orders.
    type: funnel
    presentation:
      labels: {density: automatic, priority: [selected, anomaly, threshold], max_characters: 24, minimum_spacing: 6, tooltip_fallback: true}
    query:
      table: conversion_stages
      dimensions:
        stage: conversion_stages.stage
      metrics:
        conversions: null
      sort:
        - field: stage
          direction: asc
```

## Alternate dimension

Replace status with delivery buckets to reuse the funnel for an ordered operational distribution rather than a lifecycle stage.

{{< visual id="delivery_funnel" >}}

```yaml visual-example=delivery_funnel
visuals:
  delivery_funnel:
    title: Delivery speed funnel
    type: funnel
    query:
      dimensions:
        delivery_bucket: orders.delivery_bucket
      metrics:
        order_count: null
      sort:
        - field: delivery_bucket
          direction: asc
```

## Aligned labels

Set `presentation.align: left` to anchor the stages, keep labels visible, and use `presentation.sort` to control the visual stage order independently.

{{< visual id="status_funnel_left" >}}

```yaml visual-example=status_funnel_left
visuals:
  status_funnel_left:
    title: Left aligned status funnel
    type: funnel
    presentation:
      align: left
      sort: ascending
      labels: {density: automatic, priority: [selected, anomaly, threshold], max_characters: 20, minimum_spacing: 6, tooltip_fallback: true}
    query:
      dimensions:
        status: orders.status
      metrics:
        order_count: null
      sort:
        - field: value
          direction: asc
```
