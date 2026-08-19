# Map

Use a map for governed observations with geographic coordinates or a named
geometry asset. Geographic presentation and layer fields are typed and lower
directly into the renderer-independent map Visual IR.

Every preview on this page is generated from the YAML shown below against the
fixed documentation dataset.

## Choropleth

Join a result dimension to a pinned geometry asset and color regions by a
governed metric.

{{< visual id="state_order_map" >}}

```yaml visual-example=state_order_map
visuals:
  state_order_map:
    title: Orders by state
    description: Maps order count by Brazilian state.
    type: map
    query:
      type: aggregate
      dimensions:
      - state
      metrics:
      - order_count
      sort:
      - field: order_count
        direction: desc
      limit: 27
    presentation:
      type: geographic
      theme: auto
      layers:
      - kind: choropleth
        id: states
        geometryAsset: brazil_states
        join: state
        value: order_count
        tooltip: [state, order_count]
        color:
          kind: sequential
          palette: teal
          nullColor: "#d8dee4"
```

## Coordinate points

Bind latitude and longitude dimensions to semantic fields. The compiler owns
the geographic renderer, tile policy, and point styling.

{{< visual id="order_point_map" >}}

```yaml visual-example=order_point_map
visuals:
  order_point_map:
    title: Order locations
    description: Shows governed order locations with revenue context.
    type: map
    query:
      type: aggregate
      dimensions:
      - order_id
      - latitude
      - longitude
      metrics:
      - revenue
    presentation:
      type: geographic
      theme: light
      camera:
        mode: fit_data
        padding: 32
        maximumZoom: 9
      labels:
        density: automatic
        tooltipFallback: true
      layers:
      - kind: point
        id: orders
        latitude: latitude
        longitude: longitude
        value: revenue
        label: order_id
        tooltip: [order_id, revenue]
        size:
          minimumRadius: 5
          maximumRadius: 28
        stroke:
          color: "#ffffff"
          width: 1.5
          opacity: 1
```

## Heat

Aggregate a numeric value around each coordinate while keeping the query and
field references governed.

{{< visual id="revenue_heat_map" >}}

```yaml visual-example=revenue_heat_map
visuals:
  revenue_heat_map:
    title: Revenue concentration
    type: map
    query:
      type: aggregate
      dimensions:
      - latitude
      - longitude
      metrics:
      - revenue
    presentation:
      type: geographic
      theme: dark
      layers:
      - kind: heat
        id: revenue
        latitude: latitude
        longitude: longitude
        value: revenue
        heat:
          radius: 28
          intensity: 1.15
```

## Density

Emphasize the concentration of observations without requiring a value binding.

{{< visual id="order_density_map" >}}

```yaml visual-example=order_density_map
visuals:
  order_density_map:
    title: Order density
    type: map
    query:
      type: aggregate
      dimensions:
      - latitude
      - longitude
      metrics:
      - order_count
    presentation:
      type: geographic
      layers:
      - kind: density
        id: orders
        latitude: latitude
        longitude: longitude
        heat:
          radius: 22
          intensity: 1.35
```

## Reference boundary

Reference layers add immutable, content-addressed geometry without joining
query values into the shape.

{{< visual id="state_reference_map" >}}

```yaml visual-example=state_reference_map
visuals:
  state_reference_map:
    title: Brazil state reference boundaries
    type: map
    query:
      type: aggregate
      dimensions:
      - state
      metrics:
      - order_count
      limit: 27
    presentation:
      type: geographic
      basemap: blank
      layers:
      - kind: reference
        id: state_boundaries
        geometryAsset: brazil_states
        color:
          kind: sequential
          palette: blue
          nullColor: "#d8dee4"
        stroke:
          color: "#57606a"
          width: 1.5
          opacity: 1
        opacity: 0.12
```

## Paths

Group coordinate rows by a stable path field and order vertices
deterministically.

{{< visual id="state_order_paths" >}}

```yaml visual-example=state_order_paths
visuals:
  state_order_paths:
    title: State order paths
    type: map
    query:
      type: aggregate
      dimensions:
      - state
      - order_id
      - latitude
      - longitude
      metrics:
      - revenue
      limit: 100
    presentation:
      type: geographic
      controls:
        zoom: true
        reset: true
        compass: true
      layers:
      - kind: path
        id: state_paths
        latitude: latitude
        longitude: longitude
        path: state
        order: order_id
        value: revenue
        tooltip: [state, revenue]
        stroke:
          color: "#0969da"
          width: 3
          opacity: 0.9
        line:
          width: 3
          curvature: 0
        opacity: 0.9
```
