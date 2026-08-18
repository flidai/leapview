# KPI

Use a KPI for one governed current value. The canonical dashboard contract
keeps KPI presentation renderer-neutral; comparison, goal, trend, and status
configuration are not accepted as untyped YAML options.

Every preview on this page is generated from the YAML shown below against the
fixed documentation dataset.

## Current value

{{< visual id="total_orders" >}}

```yaml visual-example=total_orders
visuals:
  total_orders:
    title: Total orders
    description: Shows the governed order count.
    type: kpi
    query:
      type: aggregate
      dimensions: []
      metrics: [order_count]
    presentation:
      type: kpi
      displayUnits: auto
      tone: neutral
```

The exact value remains available in tooltips and accessibility detail. Keep
comparisons, goals, trends, and thresholds out of this canonical visual until
each capability has a typed Dashboard contract.
