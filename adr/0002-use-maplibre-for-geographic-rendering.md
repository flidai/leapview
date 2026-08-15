# ADR-0002: Use MapLibre for geographic rendering

Status: accepted

Decision date: 2026-07-22

Implementation: complete

Deciders: LeapView maintainers

MapLibre is the sole geographic renderer for built-in LeapView visuals. ECharts owns Cartesian, proportional, hierarchy, network, and polar charts, but ECharts `geo` and `map` are not geographic fallback renderers.

## Context

LeapView maps are an analytical mapping subsystem rather than a static geographic chart. They combine a pinned vector basemap with governed data layers, persistent camera state, semantic and spatial selection, incremental updates, and spatial-windowed queries. The renderer must support zoom-dependent cartographic detail without transferring an entire world geometry or governed result to the browser.

ECharts `geo` is strong for thematic diagrams over one registered GeoJSON or SVG resource. It can place pie, scatter, line, and custom series in that coordinate system and provides excellent canvas and SVG export. It does not provide LeapView's vector-tile asset lifecycle, progressive basemap detail, or source-and-layer camera model. Using it beside MapLibre for built-in maps would create two projection, hit-testing, accessibility, selection, snapshot, and lifecycle implementations.

Neither renderer supplies authoritative world data. LeapView owns the map package: a pinned OSM-derived PMTiles archive, style, glyphs, sprites, provenance, license, attribution, and digests. The package is verified before serving or publication, exposed through same-origin immutable URLs, and never replaced at an existing content address.

## Decision

One geographic visual has one geographic camera and one renderer owner:

- MapLibre owns projection, camera, basemap sources and layers, geographic hit testing, clustering, data-layer ordering, spatial gestures, and map snapshots.
- The visualization IR remains renderer-independent. Authors cannot provide MapLibre styles, ECharts options, remote tile URLs, or callbacks.
- The governed runtime owns inline budgets and spatial-windowed queries. The browser requests a bounded viewport; it does not query arbitrary sources.
- ECharts remains the renderer for non-geographic charts and is not mounted as a second interactive canvas over MapLibre.
- Chart-like geographic glyphs such as pie or donut markers must become closed, typed geographic layers. They are rendered inside the MapLibre lifecycle through a LeapView-owned custom layer and reuse the shared formatting, theme, legend, tooltip, accessibility, and interaction contracts.

## Capability boundary

| Capability | MapLibre | ECharts `geo` |
|---|---|---|
| Registered static GeoJSON or SVG regions | Supported as controlled geometry layers | Native strength |
| Vector-tile basemap with zoom-dependent roads and labels | Native strength | Not a core capability |
| PMTiles Range loading and immutable same-origin assets | Native fit through its source model | Requires a separate subsystem |
| Camera-preserving source and selection updates | Native source/layer lifecycle | Adapter-owned option management |
| Point clustering and expansion zoom | Native GeoJSON-source capability | Custom product implementation |
| Viewport-driven governed queries | Natural camera integration | Separate product implementation |
| Pie and other chart glyphs at coordinates | Typed custom layer required | Native series composition strength |
| SVG and headless chart export | Additional product work | Native strength |

## Consequences

LeapView accepts MapLibre's WebGL, asset-packaging, and headless-export complexity in exchange for one scalable cartographic runtime. Map rendering must remain isolated behind the visualization host, recover from WebGL context loss, preserve its camera during compatible updates, and always expose an accessible tabular equivalent.

Adding ECharts `geo` is a new architecture decision, not an adapter shortcut. It requires evidence for a distinct product surface that cannot be expressed as a typed MapLibre layer and must not duplicate ordinary map behavior.

## Confirmation

Visualization schemas and architecture tests must keep renderer-specific options
out of the public IR, prevent ECharts `geo` from becoming a fallback geographic
adapter, and route built-in geographic visuals through the MapLibre lifecycle.
Any second built-in geographic renderer requires a superseding ADR.
