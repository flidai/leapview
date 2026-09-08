# Saved exploration release QA evidence

Internal review report; this is not a published documentation page.

This checklist records the bounded evidence for current-query links, saved
exploration links, and governed CSV/Parquet export. It is an evidence map, not
a claim that every dashboard browser scenario has passed.

| Acceptance area | Evidence | Status |
| --- | --- | --- |
| Current query is canonical and does not read an unpublished repository copy | `internal/project/http/data_explorer_url_test.go`, `internal/project/http/exploration_exports_test.go`, `internal/app/saved_exploration_export_integration_test.go` | Covered |
| Saved latest-version lookup remains ID-only; exact revision guards apply to export requests | `internal/app/saved_exploration_api_integration_test.go`, `internal/project/http/saved_explorations_test.go`, `internal/project/http/exploration_exports_test.go` | Covered |
| Viewer/model denial does not disclose saved state or result bytes | `internal/app/saved_exploration_adapters_test.go`, `internal/project/http/exploration_exports_test.go` | Covered |
| Stale or incompatible URL state fails closed before execution | `internal/project/http/data_explorer_url_test.go`, `internal/project/http/data_explorer_projection_test.go` | Covered |
| Owner/viewer RLS and policy identity remain isolated through export | `internal/app/saved_exploration_export_integration_test.go` (wiring and DuckDB-backed parity tests) | Covered |
| Viewer column masks reach aggregate results before CSV/Parquet encoding | Real DuckDB regression in `internal/app/saved_exploration_export_integration_test.go`; planner matching and output-type regressions in `internal/analytics/query/aggregate_plan_ir_mask_test.go` | Security correction under review; integrated validation pending |
| CSV/Parquet output is complete, bounded, typed, and cancellation-safe | `internal/analytics/exploration/export/export_test.go`, `internal/analytics/exploration/export/benchmark_test.go` | Covered |
| Recovery guidance for stale revisions, denied access, cancellation, limits, and unavailable audit | [Exploration links and exports](/docs/guides/operate/exploration-sharing-exports) | Covered |

## Focused validation

Run the Go evidence with the release build tags:

```text
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go test ./internal/app ./internal/project/http ./internal/analytics/exploration/export -count=1
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go test ./internal/app -run 'TestSavedExploration(URLExport|Executor)' -count=1
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go test ./internal/project/http -run '^$' -bench CanonicalExplorationURLDecodeAndHydration -benchmem
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go test ./internal/analytics/exploration/export -run '^$' -bench EncodeGovernedResultCSVAndParquet -benchmem
```

The benchmarks report allocations and use fixed canonical state plus 128- and
1000-row results. Canonical authored exploration validation caps `limit` at
1000; the export endpoint's separate 10,000-row ceiling does not lift that
query limit. These are diagnostic measurements, not latency gates.

## Pending release evidence

The integrated explorer component suite passes, including current-query
sharing and private Save-as behavior. This does not replace the full mounted
monthly-revenue workflow through dashboard handoff and return navigation,
which remains pending with the dashboard integration. Final release also
requires integrated security validation and stable full CI. A timeout or
browser-worker shutdown is not treated as passing evidence.
