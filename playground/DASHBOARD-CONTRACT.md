# Dashboard YAML evaluation for agent authorship

The current review branch includes LeapView main `f072723d3389cfaae27cb6621eb624ebd4858ca2`, merged on 2026-10-08. Exact-source compiler evidence was refreshed and the unchanged trial submissions were regraded after this update; earlier qualification records below retain their original revisions. The initial pilot was authored on `51022499221ad4e23f514a6db7becc7a9ef38482`; its unchanged submissions were regraded after the main refresh. The local Playground example is `/#recipes/dashboard-contract`, served from an isolated preview process. This is a local review surface: structural validation is live; semantic/query/layout evidence comes from exact-input recorded Go compiler runs. No dashboard query execution is claimed.

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

## PR scope

The patch is in [PR #938](https://github.com/flidai/leapview/pull/938): scalar schema constraints, corrected starter query and agent guidance, real compiler evidence/corpus, exploratory legal-v1 evaluation records, regression coverage, the Playground review example, isolated preview builds, stable validation results and shared editor lifecycle/theme fixes. A later source-edit improvement should use visual/page/component IDs together with expected revision, reject stale or ambiguous edits and reuse the current reducer/application lifecycle. It should not introduce a parallel patch engine.

For a broader syntax change, evaluate agent tasks before choosing a design: create one page, add a KPI, change a time grain, move a component, add a filter, reuse a visual on another page, and edit after definitions are reordered. Measure valid first attempts, repair attempts, bytes edited and unchanged-query behavior. The nine-run discovery pilot is recorded separately and does not choose a winner among unimplemented canonical redesigns. A broader benchmark needs qualified prototypes and stronger repeated trials.

## Local verification: first phase

Focused compiler corpus/scorer regressions, eight fixture unit tests, six new browser tests, the existing production editor DOM suite and app/Playground/test TypeScript checks pass. Existing browser regressions retain real Monaco editing, bounded input, cyclic YAML recovery and state restoration. The earlier qualification on `464b9bcd4d9649c915122c79836ce75f73ed3ef5` passed generation/build, Go and database conformance, quality/architecture checks, frontend core and report viewer, then failed when the existing map-drag browser test exceeded its 30-second limit. The unchanged full Playground rerun passed all 246 tests (112 unit, 89 main browser, 45 expanded browser). The full dashboard-builder and chat suites also passed unchanged reruns. Data Explorer, project, admin and shared-editor groups passed before the data shard reached its cumulative 180-second deadline; focused saved-exploration checks passed unchanged. Later data attempts also encountered existing five-second browser timeouts. The remaining login, lineage, semantic graph, inspector and topology checks passed. The site shard also reached its cumulative deadline; its unchanged direct rerun passed all 75 tests. `task generated:check` completed successfully. Across these recorded runs, the required component groups have passing results on the refreshed revision. No assertions or time limits were weakened. A clean single `task ci` invocation has therefore not been achieved; timing failures remain a verification limitation. Earlier host Git-root issues were avoided using the clean temporary root and build-stamping workaround documented in the plan.

Recorded logs remain worktree-local under `.tmp/dashboard-ci/`: `latest-main-ci.log` (combined CI timing failure), `playground-recheck.log` (246 passes), `latest-main-ci-remainder-recheck.log` (builder/chat and data groups), `unreached-checks-final.log` (remaining data checks and site deadline), and `site-generated-final.log` (75 site passes and successful generated check). Exact-source evidence checking also verified all eleven compiler fixtures.

Local screenshots show the existing local prototype before this phase (not an untouched main baseline), the updated corrected example, the original semantic failure, an out-of-grid compiler failure, identical zero-span source with before/after schema selection, actual mobile viewport and app-controlled dark theme. A default-page Axe review across the existing WCAG tags reports no violations in either theme, with no browser errors or external requests; this does not replace manual accessibility review. The final captures were refreshed after qualification on `464b9bcd4d9649c915122c79836ce75f73ed3ef5`. Those first-phase screenshots are historical review evidence; the follow-up removes the unrelated fixture chart.

Changes are on `codex/dashboard-yaml-contract`. The user has authorized a PR for the reviewed fixes; Linear state remains unchanged.

## Interaction review follow-up

The user reported repeated flicker, frozen text and reloads. A failing two-server regression confirmed that browser tests overwrote the live preview bundle while its SSE stream retained a different build ID. Every server now owns its bundle, CSS, build identity and reload stream; test/review shutdown cleans only its own output. Tests also verify that opening another test/review server preserves an actual edited Monaco document and does not navigate the live browser.

A browser regression recorded six transient compiler-matching flashes during repeated controls. Exact-source evidence lookup now compares recorded bytes synchronously within the selected scenario. Integrity tests continue checking every recorded source digest. Structural results are cached by source and rules so unrelated control updates do not repeatedly parse YAML. Edited content still immediately loses recorded compiler status.

Shared editor regressions reproduced blank editors after reconnect and after disconnection during loading. Initialization now belongs to the connected lifetime, cancels stale work and recreates disposed editors while preserving edits, including empty text. Five production editor browser tests cover both loading orders and duplicate-listener prevention.

The default page now emphasizes the selected rules. Other schema results, document references and resolved intent expand on demand. The duplicate heading/explanation and unrelated fixture chart were removed. These are focused dashboard-contract review changes, not a redesign of other Playground examples. The before/after desktop captures use the same 1440 × 1000 viewport. The after review also includes the actual app dark theme and a 390 × 844 mobile viewport. Repeated controls produced one initial navigation and no additional navigation, no browser errors or external requests, and no mobile horizontal overflow. The configured light/dark WCAG scans found no violations; this is a bounded automated scan rather than proof of complete accessibility.


## Final interaction qualification

Qualification on main `6443c25befced1da531e4076c20b5a060d359a84` passes the focused compiler/schema/documentation and scorer tests, all eleven exact-source evidence checks, and regrading of all nine unchanged first submissions. Playground/test TypeScript checks, all five production editor browser tests, `task ci:lane:quality` (including architecture/dead exports) and `task generated:check` pass.

The 114 unit tests, eight dashboard-contract browser tests, two theme tests, server-isolation regression and 45 expanded browser tests have passing runs. Two initial combined DOM runs passed all 94 assertion tests, then exceeded the existing 30-second final-hook limit. Phase diagnostics showed page, browser and server shutdown completing when the main file ran separately. The test command now runs each DOM file in its own process to isolate native browser/server fixture lifetimes; temporary diagnostics were removed. A subsequent main-only run passed 82 of 83 tests but encountered an intermittent existing token-page startup timeout; an earlier probe encountered the same test waiting for its Select control. The unchanged token/navigation test passed its focused rerun. Thus all 253 individual Playground checks have passing results across recorded runs, but a single fully green `bun run test:playground` invocation has not been achieved. No assertions or time limits were weakened, and no unproven navigation change was made.

The new full `task ci` invocation passed generation/build, standard Go tests, application shards and MinIO checks. All four PostgreSQL application shards passed. The remaining PostgreSQL package sweep lost its disposable database during fixture creation (`conn closed`, then connection reset/refused); cleanup reported that container removal was already in progress. Its removal actor is unconfirmed. The affected `internal/app/deploymentpostgres` package passes when run alone through the unchanged required-conformance wrapper (206 seconds). This is not a green combined CI result. At that phase, PR #938 was draft and its hosted checks were skipped.

Final logs under `.tmp/dashboard-ci/` are `interaction-compiler-checks.log`, `interaction-generation.log`, `interaction-task-ci.log`, `interaction-postgres-isolated.log`, `interaction-quality.log`, `interaction-generated-check.log`, `interaction-types-final.log`, `interaction-playground-full.log`, `interaction-playground-isolated.log`, `interaction-teardown-main.log`, `interaction-playground-final.log`, `interaction-token-focused.log` and `interaction-browser-remainder.log`. Final app artifacts are `dashboard-bugs-before.png`, `dashboard-bugs-after.png`, `dashboard-bugs-after-dark.png`, `dashboard-bugs-after-mobile.png` and `dashboard-bugs-browser-review.json`. The JSON records zero scan violations in both themes, zero browser errors/external requests, one initial navigation through repeated controls, and no mobile overflow.


## Ready-for-review refresh

The user requested an up-to-date branch, passing checks and conversion from draft. Main `f072723d3389cfaae27cb6621eb624ebd4858ca2` merges without conflicts. Generation, all eleven real compiler fixtures, three oracle qualifications and regrading of all nine unchanged submissions complete successfully. Browser test assertions and time limits are unchanged. A full updated Playground run passes its unit tests and 82 of 83 main browser assertions, with the same intermittent startup symptom now occurring at the expanded-table case. Six focused interaction tests, including both that case and the token/navigation case, pass unchanged. Failure-only diagnostics now record asset request status, browser console errors and custom-element/render state if startup fails; the earlier logs did not identify the stalled stage. No unproven production navigation change was made.

That local `task ci` attempt stopped at an existing APIGen test timeout; the unchanged focused case passed. The ready-for-review candidate `04704ae73ace39d54a74f2d6d785e1efcb752648` subsequently passed its complete hosted CI, security and Nix workflows, including all 253 Playground tests in one invocation without a validation retry. Later candidates have their own results in the [PR checks](https://github.com/flidai/leapview/pull/938/checks). Earlier failure records above are historical.


## Startup and complete controls review

The reported two-to-four-second loading screen had a measurable cause: browser import compiled both AJV schemas synchronously, producing roughly 1.1–1.3-second main-thread tasks, and the route's static dependency graph included the full Monaco runtime. The Playground now generates standalone validators from the canonical schema during each build, loads Monaco when an editor mounts, minifies bundles, caches content-hashed chunks and precompresses built text assets. HTML and entry assets remain fresh, so cache reuse does not hide source changes. Browser validators retain the same current/baseline results and diagnostics; there is no new authored syntax or maintained schema copy.

A bounded Chromium measurement used the same 1440 × 1000 viewport and three fresh browser contexts for each version, with three cached reloads afterward. Servers were already built and running; no network or CPU throttling was applied. Values below are medians, not universal timing guarantees. The view milestone is the first rendered dashboard-contract workspace; the editor milestone is the visible, initialized Monaco editor.

| Measure | Earlier preview | Updated preview |
| --- | --- | --- |
| Fresh view | 1,679 ms | 338 ms |
| Fresh editor | 2,024 ms | 1,021 ms |
| Cached view | Not measured | 123 ms |
| Cached editor | Not measured | 330 ms |
| Fresh transferred assets | 7.36 MB | 1.13 MB |
| Cached transferred assets | Not measured | 0.21 MB |

The dashboard's initial JavaScript graph falls from 6.25 MB to 1.14 MB; editor assets arrive afterward. The editor still performs real initialization on its first use. Compression occurs during the build, not while serving requests. Encoding negotiation preserves original MIME types, separates gzip/identity cache entries and provides matching GET/HEAD metadata.

The interaction audit exercised all eleven scenarios against both schemas; source editing/reset, empty-source export, copied-link restoration and clipboard fallback; pinned iframe restoration/freeze/replacement/removal; disclosures and JSON keyboard scrolling; fixtures and manual checklist controls; width presets, preview/Escape, search, categories, favorites, skip focus and mobile navigation. In-app accessibility checks and full-shell desktop/mobile scans in both themes found zero configured WCAG rule violations, with two rules requiring manual review. This is a bounded scan, not proof of complete accessibility.

Two additional defects were fixed: an empty source no longer exports a placeholder comment, and the production editor now shrinks with its container. The previous mobile panel was 364 px wide while its editor stayed 640 px wide, clipping wrapped source. Updated resized and fresh mobile captures have matching 364 px widths and a visible wrapped description. Failed module loads expose an explicit reload action; recovery preserves pending shared YAML as well as current theme and width. Regression tests reproduce each of these failures before the fix.

A single complete local `bun run test:playground` invocation passes all 260 checks: 118 unit, 85 main browser, nine dashboard-contract browser, two theme, one server-isolation and 45 expanded browser tests. All six production editor DOM tests and app/Playground/test TypeScript checks pass. Assertions and time limits remain unchanged.

A subsequent catalog sweep discovers all 72 routes from the actual navigation and checks each in both themes: 144/144 render/readiness, browser-error, request, navigation and desktop-overflow checks pass. Eighteen representative screenshots across nine categories and both themes receive visual inspection; this does not exhaust every interaction or mobile layout in the catalog. Results are recorded as `pr938-catalog-review.json`.

Main then advanced to `44dddff737be423d0810618d6c51f0abae02eb64`. Its security-evidence timestamp conflict is resolved by rerunning the canonical live scanner; all six current JavaScript graphs have zero findings, including the newly added qualification npm graph. No Playground or editor source changes come from that main update. The complete local `task ci` run with the documented temporary-directory/build-stamping workaround stops at the existing APIGen test “rejects invalid command contracts before writing IR” exceeding its unchanged 30-second timeout; its focused local rerun also times out. The hosted APIGen lane passes on `0058e9dba`; final merged-candidate results remain separately tracked in the PR checks. This is not a green complete local-contract result.

Timing evidence and actual light/dark desktop/mobile captures are recorded in the task artifacts as `pr938-load-qualification.json`, `pr938-performance-after-*.png` and `controls-audit-after-*`. The current candidate's complete test results belong to the PR checks; the earlier qualifications above remain historical.


## Diagnostic responsiveness review

The follow-up merges main `5a900f73bbc8e8df05bbe2728f685b159ab9b39b` without conflicts. Review reproduced an editor freeze with a 13,409-character document containing 2,500 invalid components, below the existing input bound. Diagnostic deduplication searched the whole error list for each entry, and both schemas created all diagnostic rows even inside closed details. Deduplication now uses a set; comparison validation begins when opened, and additional diagnostics render in batches of 100. All distinct diagnostics remain available. Source changes invalidate cached validation and reset pagination.

A single local Chromium measurement with initialized Monaco at 1440 × 1000 updates the same invalid document in 308 ms after the fix versus 9,323 ms before. The default diagnostic DOM shrinks from 10,002 rows to three. This is a bounded local observation, not a universal latency guarantee. Light, actual dark theme and 390 × 844 screenshots show the corrected review surface; the check records one initial navigation and no mobile page overflow.

Three independent Astra reviews cover the UI, compiler/evaluation, and server/shared-editor changes. The UI reviewer also reproduced nonfinite YAML numbers being accepted because disabling AJV strict mode disabled its finite-number check. `strictNumbers: true` now explicitly rejects `.inf`, `-.inf` and `.nan` in both schemas; direct fixtures and generated-browser parity tests cover those values. The reviewers report no remaining actionable findings after this correction. Numeric schema formats remain annotations under the canonical contract; contextual date validity continues through the real compiler.

## Completeness follow-up — 2026-10-09

The audit adds a visible Edited marker to the current fixture name when source bytes change. A red browser run fails the missing indicator, and the corrected run covers actual Monaco editing, reset, schema switching, saved edited state and Copy link restoration. All ten dashboard-contract browser tests and Playground/test TypeScript checks pass. Paired captures use the same invalid stress document before/after the label change; normal light, dark and mobile captures retain Accepted results after Reset.

Four current-v1 compatibility regressions qualify a pinned nonempty revision: strict adapter compilation before/after YAML export, unrelated edit, restoration and JSON reread; equal compiled definitions/hashes for omitted versus explicit defaults while preserving distinct authored hashes; atomic application rejection of unsupported version/query/presentation source; and a valid presentation edit preserving the earlier revision and neighboring meaning. Both affected Go package suites pass. The fixture records its qualification baseline and fixed authored hash. This establishes current boundary behavior, without claiming an older released reader, database persistence or implemented future capability/version transitions.

A separate ten-task canonical corpus at `dashboard-design-evaluation/` starts the next benchmark prerequisite. Its source/prompt/resource hashes, expected seed validity and strict compiler-valid oracles are checked by the real project compiler. Eleven valid-but-wrong documents must fail exact intended-document comparison, including wrong neighboring queries, bindings, filter scope/dependency and placement. The deliberate repair seed must fail with the declared unknown dimension. Qualification JSON records the complete input/support digests and compiler binary/build/Git identity and explicitly reports zero agent trials. The existing nine submissions and frozen controls remain unchanged.

The broader A/B/C comparison and proposed 90 fresh attempts remain unexecuted: B/C prototypes and their common capability qualification must precede a fresh preregistration and trial dispatch. Adding a future presentation capability/query kind or changing a default is also an unimplemented experiment. This prerequisite does not establish optimal agent syntax, executed query results or responsive placement equivalence.

One complete follow-up local `task ci` invocation exits successfully with `TMPDIR=/var/tmp/leapview-dashboard-yaml-ci-temp GOFLAGS='-tags=duckdb_arrow -buildvcs=false'`. It includes 73 passing APIGen tests, Go application/package/external and PostgreSQL conformance, quality and frontend lanes, and generated-contract checks. Earlier timeout records remain historical; no assertions or time limits were weakened. The full log is `.tmp/dashboard-ci/re-audit-task-ci.log`. A final independent Astra review finds no actionable issues in the compatibility tests, corpus controls/prompts/oracles or Edited state.
