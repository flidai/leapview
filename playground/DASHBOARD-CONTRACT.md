# Dashboard YAML evaluation for agent authorship

Final local qualification uses LeapView `6443c25befced1da531e4076c20b5a060d359a84`, fetched from main on 2026-10-08. The initial pilot was authored on `51022499221ad4e23f514a6db7becc7a9ef38482`; its unchanged submissions were regraded after the main refresh. The local Playground example is `/#recipes/dashboard-contract`, served from an isolated preview process. This is a local review surface: structural validation is live; semantic/query/layout evidence comes from exact-input recorded Go compiler runs. No dashboard query execution is claimed.

## Recommendation

Retain the current canonical syntax for this bounded delivery. The nine-run exploratory legal-v1 pilot passed in all tested forms and does not establish a reason for a breaking rewrite; it is not the wider proposed A/B/C comparison. See [the reviewed plan](DASHBOARD-CONTRACT-PLAN.md) and [recorded evaluation](DASHBOARD-CONTRACT-EVALUATION.md). Keep one canonical, renderer-independent Dashboard document generated from TypeSpec. Optimize agent success through exact schemas, valid examples, stable identities and bounded edits. YAML line count is a secondary concern: shorter input that compiles to unintended behavior costs more than a few explicit fields.

The present contract already has named definition lists, stable IDs, closed query and presentation unions, semantic-member references and confined fragments. These are useful foundations, rather than reasons for a new DSL. The three `type` fields describe distinct domains: visual marks, query execution and presentation families. Removing them is a coordinated contract decision, not a formatting cleanup.

## Changes implemented locally

1. Layout defaults and page overrides now express `columns >= 1`, `rowHeight >= 1`, `gap >= 0` and `padding >= 0` in the canonical TypeSpec declaration. Placement coordinates and spans express minima of one. Generated JSON Schema and OpenAPI carry the same twelve bounds. The compiler already enforced these rules; structural validation now rejects invalid scalar values earlier.
2. The public dashboard starter now selects the actual `purchase_date` dimension with `grain: month` and `alias: purchase_month`. Its previous `[purchase_month]` reference is absent from the bundled sales semantic model. A regression test extracts and compiles the actual documentation block against bundled definitions.
3. The Playground now separates the selected source document from schema version. Switching validation rules preserves edited text. Eleven scenarios expose real recorded compiler outcomes, verified resolved query/layout intent, full supporting-file digests and compiler source/build fingerprints. Any source edit clears recorded compiler status until exact fixture bytes are restored. The historical structural schema still removes only the twelve added minima; it is not a historical compiler.
4. A reproducible Go evidence command uses the existing project compiler in isolated conventional source roots. Six invalid scenarios reject; five equivalent valid scenarios compile to the same resolved intent. Corpus tests gate stale recorded fixture data, compare actual monthly bindings, and verify deterministic evidence. The evidence checker distinguishes source drift from environment differences and tolerates commit/staging changes.
5. Nine fresh independent agent attempts tested explicit defaults, omitted defaults and confined fragments across creation, grain changes and reused-visual edits after definition reordering. All passed real compilation plus exact normalized document-intent and assigned-form checks, without repairs. Frozen inputs and first submissions are retained; the scorer detects oracle/submission changes and wrong-target/order differences. This small supplied-context pilot does not measure general agent performance or query results.
6. The dashboard guide now explains catalog members versus aliases, stable IDs and reused visual effects, omitted/default layout behavior, meaningful ordering and separate draft/schema/full-validation stages.
7. Visual review exposed an inherited Monaco theme bug: a dark-theme probe under a light root captured the light background. The shared editor now resolves panel and muted line-number colors after the actual theme changes. New browser regressions cover light → dark → light and line-number contrast; syntax, cursor and active-line colors retain the Shiki palette. Expanded compiler JSON is also keyboard-scrollable.

Before:

```yaml
query:
  type: aggregate
  dimensions: [purchase_month]
  metrics: [revenue]
  sort:
    - field: purchase_month
      direction: asc
  limit: 30
```

After:

```yaml
query:
  type: aggregate
  dimensions:
    - dimension: purchase_date
      grain: month
      alias: purchase_month
  metrics: [revenue]
  sort:
    - field: purchase_month
      direction: asc
  limit: 30
```

The first example is structurally valid but fails semantic compilation. The second compiles against the bundled sales model. `columnSpan: 0` likewise passed the original structural schema but failed the compiler; the updated schema reports its exact field path and minimum. The Playground now shows the original source accepted structurally but rejected by a real recorded compiler run; corrected source exposes verified month/alias/sort/limit. Positive out-of-grid placement demonstrates a contextual rejection even when scalar schema checks pass.

## Agent-oriented evaluation

| Concern | Current assessment | Next improvement |
| --- | --- | --- |
| One contract | TypeSpec produces shared authoring types and sealed schemas. | Preserve this authority for every new field. |
| Stable identity | Visuals, pages and components have IDs; named lists make definitions self-contained. | Scope source edits by ID while keeping revision checks and atomic validation. |
| Query intent | Explicit query tags, semantic references and result aliases prevent accidental execution inference. | Supply minimal, compiler-tested examples per visual/query family. |
| Defaults | Omitted layout uses documented defaults; page overrides inherit omitted fields. | Show resolved defaults in tools without requiring repeated authored defaults. |
| Validation | Scalar rules are now aligned for layout. Compatibility and model binding remain contextual. | Generate static visual/presentation guidance from shared applicability rules; retain contextual compiler checks. |
| Large dashboards | Confined local fragments compose by identity and reject conflicts. | Let agents read/edit the relevant fragment rather than repeat entire dashboards. |
| Layout | Explicit coordinates are deterministic; compact reading order follows component sequence. | Offer layout commands that emit canonical coordinates and preview reading order. |
| Evolution | `apiVersion` and closed tagged unions give a defined contract boundary. | Add capabilities through canonical types; use explicit version decisions for changed meaning, no silent aliases. |

Do not add renderer option bags, arbitrary expressions, executable YAML, automatic deep merges or a second compact runtime grammar. Schema-valid is not compiler-valid: semantic references, filter targets, identity uniqueness, grid containment and overlap still need compiler validation. Formatting preferences should remain optional unless one form produces a measurable improvement in agent editing reliability.

## Reference-library evidence

The Flid library snapshots below were fetched on 2026-09-08. They provide prior art, not a guarantee of current upstream APIs.

| Source | Pattern worth retaining | Local primary-source reference |
| --- | --- | --- |
| Perses, revision `90a9876f232d2d2e8adc0e2a5961f26cc8075067` | Panel definitions separate from layout references. | `/srv/flid/reference-library/current/references/perses-spec/ts/src/dashboard/layout.ts` and `panel.ts` |
| Grafana, revision `307ef2b57ffa0956e7d28b2f1759a692f63199b1` | Elements and layout have distinct, explicitly typed roles. | `/srv/flid/reference-library/current/references/grafana/apps/dashboard/kinds/v2/dashboard_spec.cue` |
| Rill, revision `4f814a86196fac2ba9664b9237826582de2dad03` | Row intent can guide authoring tools and reading order. | `/srv/flid/reference-library/current/references/rill/docs/docs/reference/project-files/canvas-dashboards.md` |
| Power BI PBIR, catalog revision `1db56f98411b66b6` | Bounded per-object source files and public schemas aid programmatic edits. | `/srv/flid/reference-library/current/references/power-bi-docs/documents/developer/embedded/projects-enhanced-report-format.md` |

These patterns align with ADR-0011 (canonical document), ADR-0024 (named lists and identity) and ADR-0016 (standards boundaries). Keep LeapView's governed semantic queries and explicit coordinate contract when borrowing authoring ergonomics. In particular, the local Power BI article's preview status is stale and is not evidence for current product status.

## Proposed next PR scope

The local patch is a bounded first PR candidate: scalar schema constraints, corrected starter query and agent guidance, real compiler evidence/corpus, exploratory legal-v1 evaluation records, regression coverage, the Playground review example and the bounded editor theme fix exposed by that review. No PR has been created. A later source-edit improvement should use visual/page/component IDs together with expected revision, reject stale or ambiguous edits and reuse the current reducer/application lifecycle. It should not introduce a parallel patch engine.

For a broader syntax change, evaluate agent tasks before choosing a design: create one page, add a KPI, change a time grain, move a component, add a filter, reuse a visual on another page, and edit after definitions are reordered. Measure valid first attempts, repair attempts, bytes edited and unchanged-query behavior. The nine-run discovery pilot is recorded separately and does not choose a winner among unimplemented canonical redesigns. A broader benchmark needs qualified prototypes and stronger repeated trials.

## Local verification

Focused compiler corpus/scorer regressions, eight fixture unit tests, six new browser tests, the existing production editor DOM suite and app/Playground/test TypeScript checks pass. Existing browser regressions retain real Monaco editing, bounded input, cyclic YAML recovery and state restoration. The refreshed-main `task ci` invocation passed generation/build, Go and database conformance, quality/architecture checks, frontend core and report viewer, then failed when the existing map-drag browser test exceeded its 30-second limit. The unchanged full Playground rerun passed all 246 tests (112 unit, 89 main browser, 45 expanded browser). The full dashboard-builder and chat suites also passed unchanged reruns. Data Explorer, project, admin and shared-editor groups passed before the data shard reached its cumulative 180-second deadline; focused saved-exploration checks passed unchanged. Later data attempts also encountered existing five-second browser timeouts. The remaining login, lineage, semantic graph, inspector and topology checks passed. The site shard also reached its cumulative deadline; its unchanged direct rerun passed all 75 tests. `task generated:check` completed successfully. Across these recorded runs, the required component groups have passing results on the refreshed revision. No assertions or time limits were weakened. A clean single `task ci` invocation has therefore not been achieved; timing failures remain a verification limitation. Earlier host Git-root issues were avoided using the clean temporary root and build-stamping workaround documented in the plan.

Recorded logs remain worktree-local under `.tmp/dashboard-ci/`: `latest-main-ci.log` (combined CI timing failure), `playground-recheck.log` (246 passes), `latest-main-ci-remainder-recheck.log` (builder/chat and data groups), `unreached-checks-final.log` (remaining data checks and site deadline), and `site-generated-final.log` (75 site passes and successful generated check). Exact-source evidence checking also verified all eleven compiler fixtures.

Local screenshots show the existing local prototype before this phase (not an untouched main baseline), the updated corrected example, the original semantic failure, an out-of-grid compiler failure, identical zero-span source with before/after schema selection, actual mobile viewport and app-controlled dark theme. A default-page Axe review across the existing WCAG tags reports no violations in either theme, with no browser errors or external requests; this does not replace manual accessibility review. The final captures were refreshed after qualification on `464b9bcd4d9649c915122c79836ce75f73ed3ef5`. Those first-phase screenshots are historical review evidence; the follow-up removes the unrelated fixture chart.

Changes are on `codex/dashboard-yaml-contract`. The user has authorized a PR for the reviewed fixes; Linear state remains unchanged.

## Interaction review follow-up

The user reported repeated flicker, frozen text and reloads. A failing two-server regression confirmed that browser tests overwrote the live preview bundle while its SSE stream retained a different build ID. Every server now owns its bundle, CSS, build identity and reload stream; test/review shutdown cleans only its own output. Tests also verify that opening another test/review server preserves an actual edited Monaco document and does not navigate the live browser.

A browser regression recorded six transient compiler-matching flashes during repeated controls. Exact-source evidence lookup now compares recorded bytes synchronously within the selected scenario. Integrity tests continue checking every recorded source digest. Structural results are cached by source and rules so unrelated control updates do not repeatedly parse YAML. Edited content still immediately loses recorded compiler status.

Shared editor regressions reproduced blank editors after reconnect and after disconnection during loading. Initialization now belongs to the connected lifetime, cancels stale work and recreates disposed editors while preserving edits, including empty text. Five production editor browser tests cover both loading orders and duplicate-listener prevention.

The default page now emphasizes the selected rules. Other schema results, document references and resolved intent expand on demand. The duplicate heading/explanation and unrelated fixture chart were removed. These are focused dashboard-contract review changes, not a redesign of other Playground examples. Follow-up screenshots and final PR verification are recorded after qualification.
