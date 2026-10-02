# Playground coverage

The gallery imports production implementations. It is a workbench for reusable
components, not a replacement for application route or service integration tests.
Only states supported by a component are shown; pending/error states are not
invented for controls that have no such interface.

| Group | Production coverage | Fixtures and interactions |
| --- | --- | --- |
| Design tokens | Production CSS colors, typography, spacing, borders, motion and other custom properties | Computed values for the selected production theme |
| UI components | Native settings buttons/fields, select menu, entity multiselect, date picker, filter menu, toast, spinner | Disabled/validation/empty/loading/error where supported; local selection, search, clear, keyboard and notification events |
| Charts & data | Every visual type in `docs/visuals/catalog.json` (26), through `lv-visualization-host` and production adapters | Ready/loading/empty/error, single point, nulls, long labels, dense data; supported series, legend, axes, labels, tooltip and size controls |
| Geographic layers | Point, heat, density, choropleth, reference and path | Deterministic point/path data and bundled pinned IBGE boundaries; production attribution, no remote tiles |
| Lineage & models | `lv-asset-lineage-graph`, `lv-semantic-model-graph` | Dependencies, execution statuses, many upstream models, empty graphs, composite keys and disconnected datasets; selection, scope, expansion, relationship inspection and persisted layout |
| Tables & lists | `lv-record-table`, `lv-windowed-table`, `lv-entity-list`, `lv-data-preview-table`, `lv-data-explore-table` | Deterministic rows, empty/error/loading as supported, sorting, window requests, columns, actions and local search |
| Editors & content | `lv-code-editor`, `lv-code-block`, `lv-config-viewer`, `lv-markdown-view`, `lv-visual-artifact`, `lv-chat-composer` | Local Monaco editing/read-only, highlighting/copying, outline/source parsing, Markdown, artifact states, composer draft/context events |
| Layout & identity | Drawer, avatar, brand/field icons, notification stack, one-time secret, empty state, page header, breadcrumbs, settings and entity-detail helpers; dashboard appearance and report view controls | Public properties, native form behavior, modal focus, local actions and production styling |
| Dashboard filters | `lv-filter-leaf`, `lv-filter-pane-card`, `lv-slicer`, `lv-filter-dock` | Dropdown/list/buttons/text/numeric/date/relative presentations; local mutation, clear/reset, editable/stale/pending and validation. Dock supports immediate/deferred application, Apply/Cancel, page/report resets and visible applied/draft state |

| Combined examples | Production filter + chart + table + KPI; drawer + select + date picker | Linked highlighting, local filtering/sorting, nested overlays, validation and focus restoration |

Sharing/reload preserve exposed fixture controls. Code snippets export public inputs.
The collapsed review panel provides a pinned interactive comparison, an opt-in
axe scan and a manual review checklist; it does not generate screenshot baselines.

## Deliberate exclusions

- **Full route shells and workflows:** app/navigation/catalog, project/asset/pipeline
  pages, admin/personal settings, authentication, the dashboard builder/report
  canvas, data explorer, chat manager/thread/drawer/history and saved-visual
  workflows. These compose components with route navigation, application signals,
  permissions or service commands. Their underlying reusable graphs, controls,
  tables, editors, drawers and composer are previewed directly.
- **Pipeline run-history wrapper and running clock:** the wrapper submits native
  filter forms and follows route links; durations depend on wall-clock time.
  The entity-list surface and graph execution statuses have deterministic examples.
- **Remote data and map services:** external tiles/glyphs, authenticated map assets,
  governed SQL, live streams, chat execution and distinct-option queries. Local
  fixtures exercise the same rendering contracts, not the backend behavior.
- **Internal utilities:** signal bridges, command senders, parsers, lifecycle,
  layout/resize helpers and authentication submission are not standalone visual
  components. Icon and asset helpers are exercised by their production consumers.
- **Exhaustive permutations:** supported representative variations are exposed;
  every possible chart IR property, dataset combination, domain signal and nested
  workflow is not a separate example. Production tests remain the contract checks.

## Adding coverage

Add deterministic inputs to the relevant fixture module, import the production
component in its group module, and expose a stable example ID. Bind public
properties and handle emitted events locally. Keep adapters narrow: answer the
existing public contract, do not patch private component state or recreate its
markup. Document the source, inputs, events and meaningful limitations beside the
example. Add focused browser tests for new behavior after implementation and source
review are complete, then run the checks documented in README.md.
