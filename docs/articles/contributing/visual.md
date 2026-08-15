# Add a visual type

A visual type is complete only when resource validation, semantic query shape, server payload, renderer adaptation, interactions, documentation, examples, and tests agree. Start by deciding whether the requirement is truly a new type, a new renderer-neutral option, or only an adapter improvement for an existing shape.

LeapView compiles every built-in visual into the versioned IR described by the [visualization architecture](/docs/architecture/visual-plugins). Charts, KPIs, tables, matrices, and pivots share visual identity, page placement, envelopes, commands, interactions, and headless APIs. New product semantics must remain renderer-independent, closed, typed, and suitable for immutable specifications and validated inline or windowed data.

## Define the product contract

Specify:

- the visual type ID and compatible page component kind;
- the renderer-independent specification kind and mark;
- required and optional dimensions, measures, series, or table input;
- sorting and cardinality rules;
- stable payload fields and formatting behavior;
- whether point selection is supported and which datum fields identify it;
- empty, invalid, and partial data behavior.

Add the type to the closed authoring contract and canonical TypeSpec IR, then update compiler capability validation. Update tests that reject unsupported combinations. Regenerate Go, TypeScript, JSON Schema, and configuration reference artifacts.

Do not begin by adding a raw ECharts option. The server and non-ECharts consumers need to understand the product meaning first.

MapLibre exclusively owns geographic visuals. New map behavior must be a closed geographic IR layer rendered within the existing MapLibre camera and interaction lifecycle, not an ECharts `geo` fallback or a second canvas overlay. See [Geographic rendering](/docs/architecture/geographic-rendering).

## Produce the server payload

Extend compilation and data shaping so a valid semantic query becomes a `VisualizationSpec` plus validated inline or windowed `VisualizationDataState` inside one envelope.

Test:

- valid minimum and representative payloads;
- missing or excessive dimensions/measures;
- result alias handling;
- typed null/zero/false values;
- deterministic sorting and bounds;
- filtered and empty results;
- selection mappings where supported.

Keep renderer-specific data transformations out of Go unless they are part of the stable product payload.

## Extend browser adapters

Regenerate the canonical browser types from `api/visualization`; never add a parallel handwritten visual model. Implement ECharts behavior in the matching semantic translator under `web/components/dashboard/visualization/adapters/echarts/`, or update the owning TanStack, HTML, or MapLibre adapter. Register an engine only through `web/components/dashboard/visualization/registry.ts` and the common `RendererAdapter` lifecycle.

The adapter must handle theme tokens, resize, update without remount when safe, clear, and dispose. Avoid global listeners or renderer instances that survive component disconnect.

Do not add renderer-native options to YAML or the IR. Add a typed product-level presentation field, validate it during compilation, and translate it inside the owning adapter.

## Implement interactions

If selection is supported, map library events back to the original source datum and let shared interaction code build semantic entries. Test rapid replacement, clear/toggle behavior, and identity containing the correct field, fact/grain, and scalar type.

For transformed structures such as graph edges, Sankey links, or hierarchy nodes, prove that only data with a real source mapping can become a semantic selection.

## Add a showcase example

Add a representative visual definition and page component to the visual showcase workspace. Use data that exercises meaningful labels, multiple series or edge cases, and empty/null behavior without making the example unbounded.

The example should demonstrate the recommended query shape, not every possible option.

## Add documentation

Create or update the visual Markdown under `docs/visuals` and register it in `docs/visuals/catalog.json`. Each live example pairs a chart shortcode whose `id` is the example ID with a `yaml visual-example=example_id` fence containing exactly one `visuals.example_id` definition. IDs are globally unique. Documentation generation compiles and executes these queries against the fixed visual-example semantic model, so the displayed YAML and rendered payload cannot drift.

Use pattern-oriented section names such as “Basic,” “Multiple series,” or “Threshold bands,” rather than repeating the title inside the rendered visual. Explain the visible effect of the relevant YAML fields. The generator derives each page's API summary and each example's key-field callout from these definitions; it also rejects non-finite values, incomplete geographic coverage, and region identifiers that do not belong to the selected documentation map.

Run `task docs:generate`; the unified catalog will add the route and search entry. The generator rejects orphaned Markdown and broken internal links.

## Test end to end

Run focused Go report/runtime tests, TypeScript adapter/interaction tests, the chart-showcase DOM test, and site tests. Verify light/dark/system themes, compact width, focus modal, show/copy data, CSV export, selection clear, loading, empty, error, resize, and disconnect cleanup as applicable.

Finish with:

```sh
task generate
task docs:check
task ci
```

Do not expose the new type publicly until the generated dashboard schema, browser registry, live documentation route, and production bundle all recognize it.
