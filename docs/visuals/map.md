# Map

Use a map for governed observations with coordinate dimensions. The canonical
map contract lowers one geographic point layer from the latitude and longitude
query dimensions; dashboard-only spatial interactions are documented in the
[Filters and interactions guide](/docs/guides/build/filters-interactions).

Every preview on this page is generated from the YAML shown below against the
fixed documentation dataset.

## Coordinate points

Bind latitude and longitude dimensions to semantic fields. The compiler owns
the geographic renderer, tile policy, and point styling; visual YAML does not
accept renderer-specific `geo` or layer objects.

{{< visual id="order_point_map" >}}

```yaml visual-example=order_point_map
visuals:
  order_point_map:
    title: Order locations
    description: Shows governed order locations with revenue context.
    type: map
    query:
      type: aggregate
      dimensions: [order_id, latitude, longitude]
      metrics: [revenue]
    presentation:
      type: geographic
      labels:
        density: automatic
        tooltipFallback: true
```

Maps expose exact governed values in tooltips and accessibility detail. Keep
regional comparison, reference boundaries, heat and density layers, and path
geometry out of this canonical visual until a typed Dashboard contract exists
for each capability.
