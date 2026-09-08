# Saved exploration release QA evidence

Internal review report; this is not a published documentation page.

This checklist records the bounded evidence for saved exploration, governed
export, shared visualization and dashboard handoff. It is an evidence map, not
a claim that every dashboard browser scenario has passed.

| Acceptance area | Evidence | Status |
| --- | --- | --- |
| Current query is canonical and does not read an unpublished repository copy | `internal/project/http/data_explorer_url_test.go`, `internal/project/http/exploration_exports_test.go`, `internal/app/saved_exploration_export_integration_test.go` | Covered |
| Saved latest-version lookup remains ID-only; exact revision guards apply to export requests | `internal/app/saved_exploration_api_integration_test.go`, `internal/project/http/saved_explorations_test.go`, `internal/project/http/exploration_exports_test.go` | Covered |
| Viewer/model denial does not disclose saved state or result bytes | `internal/app/saved_exploration_adapters_test.go`, `internal/project/http/exploration_exports_test.go` | Covered |
| Stale or incompatible URL state fails closed before execution | `internal/project/http/data_explorer_url_test.go`, `internal/project/http/data_explorer_projection_test.go` | Covered |
| Owner/viewer RLS and policy identity remain isolated through export | `internal/app/saved_exploration_export_integration_test.go` (wiring and DuckDB-backed parity tests) | Covered |
| Viewer column masks reach aggregate results before CSV/Parquet encoding | Real DuckDB regression in `internal/app/saved_exploration_export_integration_test.go`; planner matching and output-type regressions in `internal/analytics/query/aggregate_plan_ir_mask_test.go` | Covered; feature PR #536 passed local CI and all 13 remote checks |
| Mounted authenticated monthly shell, save/reopen, viewer execution, and export | `internal/app/saved_exploration_mounted_monthly_workflow_test.go`; real DuckDB rows, saved spec/revision, policy isolation, audit and admission assertions | Covered; primary race-enabled run repeated three times |
| Monthly table, line-chart and pivot presentation | Canonically validated projection cases in `TestSavedExplorationMonthlyPresentationIRProjection`; existing browser visualization suites | Projection and browser-component coverage; not a single full browser journey |
| Independent dashboard append, durable replay, stale revision rejection, publish and Explore-back | `internal/project/http/explore_dashboard_live_test.go`; mounted routes with real SQLite authoring repository/service and fixture runtime/compiler ports | Covered; focused and race-enabled tests passed |
| Published authored source uses owner/visibility-aware authorization before revision reads | `internal/dashboard/authoring/sourceadapter/adapter_test.go` | Covered for private owner, organization recipient and denied private outsider |
| Private authored dashboards cannot be copied using project-reader access alone | `TestForkInstanceAuthorizesAuthoredSourceBeforeRevisionOrCreate` | Covered; no revision read or target creation on denial |
| Dashboard browser transport uses the generated request envelope and recovers from failures independently of query execution | `web/components/data/data-explorer-dashboard-picker.dom.test.ts`, `internal/project/ui/data_explorer_test.go` | Covered, including an actual Datastar POST body, 403/409, network failures and refresh serialization |
| Chat visual handoff retains canonical query, effective bounds and scoped filters | `internal/agent/tools/visual_provider_test.go`, `internal/app/exploration_model_wiring_test.go` | Covered; active-generation callback and compiled-model agreement fail closed |
| Older or malformed optional chat metadata preserves the visual without a handoff action | `internal/agent/transcript_test.go`, `web/components/shared/visual-artifact-layout.dom.test.ts` | Covered, including actionless chart/table sizing |
| Query updates and saved reopen refresh context while history retains validated return links | `internal/project/http/data_explorer_browser_test.go`, `web/components/data/data-explorer-agent-context-explore.dom.test.ts`, URL/return unit tests | Covered; includes out-of-order suppression and hydration/edit/save history |
| CSV/Parquet output is complete, bounded, typed, and cancellation-safe | `internal/analytics/arrowquery/export/export_test.go` (declared decimal/date/timestamp and all-null schema retention), `internal/analytics/arrowquery/export/benchmark_test.go` | Covered |
| Recovery guidance for stale revisions, denied access, cancellation, limits, and unavailable audit | [Exploration links and exports](/docs/guides/operate/exploration-sharing-exports) | Covered |

## Focused validation

Run the Go evidence with the release build tags:

```text
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go test ./internal/app ./internal/project/http ./internal/analytics/arrowquery/export -count=1
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go test ./internal/app -run 'TestSavedExploration(URLExport|Executor)' -count=1
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go test ./internal/project/http -run '^$' -bench CanonicalExplorationURLDecodeAndHydration -benchmem
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go test ./internal/analytics/arrowquery/export -run '^$' -bench EncodeGovernedResultCSVAndParquet -benchmem
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go test ./internal/app -run '^$' -bench 'BenchmarkSavedExploration(Read|MonthlyProjection)$' -benchmem
```

The benchmarks report allocations and use fixed canonical state plus 128- and
1000-row results. Canonical authored exploration validation caps `limit` at
1000; the export endpoint's separate 10,000-row ceiling does not lift that
query limit. These are diagnostic measurements, not latency gates.

## Enforced resource budgets

Canonical validation caps query rows at 1,000 and each dimension, metric,
filter, and sort list at 100. Visualization projection respects the effective
row limit; pivot projection falls back with warnings above 64 column values
or 4,096 cells. Export independently caps rows at 10,000 and retained/encoded
bytes at 32 MiB without raising the canonical query limit. Saved-list API
pages default to 50 and cap at 200. These are resource bounds, not latency
guarantees. Benchmark timings depend on the host and fixture; in-memory
saved-read measurements do not represent database or full authorization cost.

## Pending release evidence

The integrated explorer component suite passes, including current-query
sharing and private Save-as behavior. The mounted monthly workflow now covers
the authenticated shell/update/save/reopen/export path. Mounted dashboard
handoff, published-source return navigation and authoring transport recovery
also pass. Chat visual-artifact handoff, context freshness and return navigation
have focused regression coverage. Final release still requires stable full CI on the combined
handoff/release checkpoint. A timeout or browser-worker shutdown is not treated
as passing evidence.
