# LeapView component playground

From the repository root:

```sh
task playground
```

Open <http://127.0.0.1:4400>. The command installs the pinned Bun dependencies,
generates the canonical visualization/layout contracts and icon catalog, compiles the shared
CSS and browser components, then starts a loopback-only Bun static server. It
requires the repository's development tools (Task, Bun, Node/npm and Go for
contract generation), but does not build or start the Go monolith, authenticate,
connect to a database, or call application services. Dependency installation may
need internet access on the first run. No dependency upgrades are required.

Once contracts and dependencies are prepared, `bun run playground` starts the
same server. Set `PLAYGROUND_PORT=4401` to choose another port. Stop with Ctrl-C;
restart after changing source files to rebuild. Generated bundles stay under
`.tmp/playground`; production asset entrypoints are not changed.

## Reviewing examples

- Browse **Design tokens**, **UI components**, **Charts & data**,
  **Lineage & models**, **Tables & lists**, **Editors & content**,
  **Layout & identity**, and **Dashboard filters**, or filter
  their navigation by name. Every example has a stable hash link such as
  `/#charts/bar`, `/#graphs/asset-lineage`, `/#tables/windowed`, or `/#tokens/colors`.
- Change theme, preview width, and chart height to inspect responsive behavior.
- Use each example's controls to select fixtures, variants and supported states.
  Interactions use real component properties and events; event details are shown
  alongside the example. Use Tab, arrow keys, Enter and Escape in the production
  menus and date picker.
- **Clean preview** hides navigation, controls and usage notes for
  screenshots without resetting the current example. Press Escape to return.
  **Open default preview** opens a new tab with default fixtures and the current
  theme and viewport dimensions. `?preview=1#charts/bar` opens that view directly. Select a theme in
  the main playground to inspect its tokens and chart colors.
- Usage notes identify the production source, public inputs and emitted events.
  Inspect those files before extending an example; they own the interface.

## Structure and extension

`app.ts` owns navigation and the preview viewport. Each group module exports its
navigation entries and renders its examples; companion `*-fixtures.ts` modules
keep deterministic data separate from preview controls. Add an entry
with a stable ID and render the production component with its public properties.
Do not copy its markup/styles into the playground or introduce a parallel library.
Native buttons and fields use the production `settingsLayoutStyles` and settings
render helpers because the product does not wrap them in universal custom elements.

`chart-fixtures.ts` owns deterministic visualization envelopes and local table
responses. Extend these fixtures when adding a chart or edge case. Import the
production generated IR types and schema version, keep dataset references valid,
and use the existing visualization host. Do not hand-write an ECharts option
object: the product adapters must remain the code under review. Keep dummy values
and playground controls here; put reusable fixes in the production component.

`tokens.ts` reads CSS custom properties from the loaded production stylesheets
and displays their computed values. `static/app.input.css` and its imported
Primer primitives remain the source of truth. Add or fix tokens there, never in a
playground copy. The playground imports `static/theme.js` for theme behavior.

## Scope and limitations

This is a component workbench. Route shells, authentication, deployment workflows,
chat streaming, governed queries, and backend error handling are outside its
scope. Direct-property components need no Datastar runtime adapter. Table windows
are supplied in memory through the existing public event/envelope contract.
The map example uses local points and a blank basemap; production map asset
services, remote tiles and glyph servers are intentionally not needed. Screenshot
results for maps depend on browser WebGL availability.

The 26 production visual types are covered. Presentation controls cover
representative supported variations (including multiple/stacked series, legend
positions, label density, axes, tooltip fields and KPI modes), not every IR
property. All six supported geographic layer kinds are included: points, heat, density,
choropleth, reference boundaries and paths. Choropleth/reference fixtures use the
repository’s pinned IBGE geometry and production attribution. Remote raster tiles,
map asset services and glyph servers are excluded. Only states and
presentation options supported by the production component are exposed. A loading or error fixture represents a renderer/component state, not a
simulated network request. Data values are deterministic; focus, hover, theme,
viewport dimensions and browser font/rendering differences can affect screenshots.

See [COVERAGE.md](COVERAGE.md) for the component inventory and exclusions.

Graph examples import the product’s React Flow components, including their own
selection, expansion, relationship inspection and layout persistence. Code editing
uses the production Monaco runtime and a locally built worker/CSS. Windowed tables
answer the existing request contract in memory, including sort, reset version and
request sequence. Filter and composer events update local fixture state; they never
submit application commands. Drawer and modal examples use real focus behavior.

## Verification

After implementing a change, run `bun run test:playground` and
`bun run typecheck:playground` (typechecking requires `task ui-signals:generate`
in a fresh checkout), then `task ci` for the repository PR contract. Browser checks should load the
standalone server, exercise public interactions, resize the viewport, and reject
unexpected backend or external requests. Keep generated schema validation enabled
at the production visualization host boundary.

Implementation follows the repository's existing Bun builds and the official
[Bun bundler](https://bun.sh/docs/bundler),
[Lit reactive properties](https://lit.dev/docs/components/properties/),
[React Flow customization](https://reactflow.dev/learn/customization/theming),
[Monaco ESM integration](https://github.com/microsoft/monaco-editor/blob/main/docs/integrate-esm.md), and
[ECharts sizing](https://echarts.apache.org/handbook/en/concepts/chart-size/)
documentation. Flid's local Superset examples informed the gallery/interactive
fixture split; no Storybook dependency or copied implementation is used.
