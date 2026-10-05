# LeapView component playground

From the repository root:

```sh
task playground
```

Open the local address printed at startup. `task playground` installs the pinned Bun dependencies,
generates the canonical visualization/layout contracts and icon catalog, compiles the shared
CSS and browser components, then starts a loopback-only Bun static server. It
requires the repository's development tools (Task, Bun, Node/npm and Go for
contract generation), but does not build or start the Go monolith, authenticate,
connect to a database, or call application services. Dependency installation may
need internet access on the first run. No dependency upgrades are required.

Once contracts and dependencies are prepared, `bun run playground` starts the
same server. Set `PLAYGROUND_PORT=4401` to choose another port. Stop with Ctrl-C. Source edits rebuild automatically and reload connected previews
on the same port. Builds are serialized; an in-page notice shows build failures
and recovers after the next successful save. Fixture controls, theme, and size
survive reload when browser session storage is available. Restart after changing
the server or build-helper implementation; contract/package changes may also need
the normal generation or dependency-install command. Generated bundles stay under
`.tmp/playground`; production asset entrypoints are not changed.

## Reviewing examples

- Browse **Design tokens**, **UI components**, **Charts & data**,
  **Lineage & models**, **Tables & lists**, **Editors & content**,
  **Layout & identity**, **Dashboard filters**, and **Combined examples**, or filter
  their navigation by name. Every example has a stable hash link such as
  `/#charts/bar`, `/#graphs/asset-lineage`, `/#tables/windowed`, or `/#tokens/colors`.
- Expand a category to see its examples and count. The current category opens
  automatically; searching opens matching categories and hides empty ones. On small
  screens, use **Browse** to open navigation; selecting an example closes it.
- Star an example beside its title to keep it in **Favorites**. **Recent** keeps the last five visited examples; both lists stay in this browser and pinned comparisons do not change them.
- Use the sun/moon button to switch between light and dark mode. The choice
  persists through the production theme setting; existing theme-specific preview
  links still work. Change preview width and height to inspect responsiveness.
- Use each example's controls to select fixtures, variants and supported states.
  Chart **Display options** holds presentation settings.
  Interactions use real component properties and events; expand **Usage & events**
  below the preview for source references, public inputs, and live event details. Use Tab, arrow keys, Enter and Escape in the production
  menus and date picker.
- **Preview** hides navigation, controls and usage notes for
  screenshots without resetting the current example. Use **Exit preview** or
  press Escape to return. Expanded visuals block background controls; close the
  visual with its × button or Escape before leaving Preview.
  Preview stays in the current tab. `?preview=1#charts/bar` still opens a preview
  directly, with default fixtures; optional theme and viewport parameters work
  for shared links.
- **Copy link** captures the current fixture controls, theme, size, and Preview mode.
  Links contain a versioned, validated state snapshot; legacy hash links still work.
  Links over 8,000 characters are rejected with a Copy component code alternative.
  Native production-internal state (open popovers, graph positions, table scroll,
  editor selection, and transient event logs) is not serialized. Selection and
  sort in the linked dashboard are included. Edited code is included in content
  links; keep the editor's content appropriate for sharing.
- Expand **Code & review** for current public-input snippets and authored chart YAML
  links. Snippets use property bindings and production imports; wire their public
  events in the owner. Native controls require the listed shared Lit styles.
- **Pin comparison** keeps a reference fixture in an embedded preview in the same
  tab. Change the live example above to compare options/themes; Replace comparison
  refreshes the reference. Its captured theme does not change the saved preference.
  This is an interactive visual reference, not a pixel-diff baseline system.
- **Fixtures & states**, inside Code & review, lists the options exposed by the current fixture controls. Refresh it after changing conditional options. These are available variants, not a claim that every permutation has passed tests.
- **Check accessibility** loads the existing axe-core dependency on demand and
  reports findings for the current rendered preview. It never runs automatically.
  Results are a point-in-time aid, not proof of accessibility; use the manual
  checklist for keyboard, screen readers, focus, canvas charts, and visual states.
- **Combined examples → Linked dashboard** connects a status filter, bar, region
  table and KPI. Select regions with a chart click or the table's keyboard controls;
  highlights preserve totals, while status filtering changes the underlying rows.
- **Combined examples → Drawer form** combines the production drawer, dropdown,
  date picker, and validation. **Dashboard filters → Filter dock** adds immediate
  or deferred Apply/Cancel and page/report scope resets with local acknowledgements.
- Usage notes identify the production source, public inputs and emitted events.
  Inspect those files before extending an example; they own the interface.

For table review, compare headings with the values below them: text stays left,
numeric columns use their declared right alignment, and record cells are centered
vertically beside multiline asset names. Sort indicators must not shift headings.
For lineage review, switch the Scope dropdown at a narrow preview width; the
selected asset stays in view. **Fit** shows all included nodes in Full graph,
while Focused path keeps the selected neighborhood readable. In the semantic
graph, **Related / All** keeps keyboard focus on the field toggle after resizing
the nodes.

## Structure and extension

`app.ts` owns navigation and the preview viewport. Each group module exports its
navigation entries and renders its examples; companion `*-fixtures.ts` modules
keep deterministic data separate from preview controls. Add an entry
with a stable ID and render the production component with its public properties.
Do not copy its markup/styles into the playground or introduce a parallel library.
Use `exampleDetails` and `exampleChromeStyles` from `example-chrome.ts` to keep
usage information and event logs in the shared disclosure below each preview.
Keep fixture controls and meaningful selection feedback visible. Mark the fixture control container with `data-fixture-controls` so the review panel can discover its labeled selects and checkboxes without duplicating option lists.
Expose `getExampleState()` and `restoreExampleState()` on the playground renderer
for sharing/reload. Whitelist bounded controls and reconstruct fixtures; never
restore arbitrary envelopes or production private state from URLs. Await the
initial Lit update before restoring controls that `willUpdate` resets.
`getExampleCode()` returns public input examples. `example-code.ts` can capture
public Lit properties and authored slots without traversing shadow/runtime UI.
Keep non-JSON or imperative API examples explicit. The reload client dispatches
`playground-before-reload` so the shell can save its snapshot to session storage.
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

In the linked dashboard recipe, the height control is a minimum chart height.
Wide layouts also reserve room for the compact KPI and regional table beside the
chart; narrow layouts stack the three views.

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

The new workflows also draw on Flid's Superset component stories and ECharts
event documentation, the official [ECharts event/action contract](https://echarts.apache.org/handbook/en/concepts/event/),
[ECharts ARIA guidance](https://echarts.apache.org/handbook/en/best-practices/aria/),
[Bun filesystem watching](https://bun.sh/guides/read-file/watch), and
[axe API](https://www.deque.com/axe/core-documentation/api-documentation/).

## Optional visual and accessibility review

See [browser-review/README.md](browser-review/README.md) for screenshot comparison
and the catalog accessibility sweep using the existing Playwright dependencies.
These commands are opt-in and do not change repository CI or security policy.
