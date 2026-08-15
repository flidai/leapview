# Geographic rendering

MapLibre is the sole geographic renderer for built-in LeapView visuals. ECharts
owns non-geographic analytical charts; ECharts `geo` is not a fallback renderer
for maps.

## Runtime boundary

MapLibre owns projection, camera state, basemap sources and layers, geographic
hit testing, clustering, spatial gestures, layer ordering, and map snapshots.
The visualization IR remains renderer independent: authors cannot provide
MapLibre styles, ECharts options, callbacks, or remote tile URLs.

LeapView owns the geographic asset package, including its pinned OSM-derived
PMTiles archive, style, glyphs, sprites, provenance, license, attribution, and
digests. Assets use immutable same-origin URLs and are verified before serving.

The governed runtime owns inline result budgets and spatial-windowed queries.
The browser requests a bounded viewport and never receives authority to query
arbitrary data sources. Chart-like glyphs on a map are closed, typed geographic
layers inside the MapLibre lifecycle rather than a second interactive canvas.

## Product guarantees

- Compatible data and selection updates preserve the current camera.
- Map rendering recovers from WebGL context loss without relaxing data policy.
- Every map exposes an accessible tabular equivalent.
- Shared formatting, theme, legend, tooltip, accessibility, and interaction
  contracts also apply to custom geographic layers.
- Geographic packages are content addressed and never replaced at an existing
  URL.

Adding a second built-in geographic renderer requires a new architecture
decision and evidence for a product surface that cannot be expressed as a typed
MapLibre layer.
