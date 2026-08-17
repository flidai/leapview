# LeapView Project Overview

LeapView is a dashboards-as-code BI monolith. Go owns configuration compilation, security, deployments, managed data, DuckDB/DuckLake execution, and the Datastar SSE command loop. Gomponents renders page shells; Lit components render typed signal payloads in the browser.

## Architecture

- `dashboards/leapview.yaml` is the project entrypoint. It references project-wide connections, sources, models, semantic models, pipelines, dashboards, and access declarations.
- `internal/project/compiler/` loads, validates, and compiles the complete resource graph into deployable serving-state artifacts.
- `internal/deployment/`, `internal/servingstate/`, and `internal/runtimehost/` prepare immutable serving-state generations, activate the graph for the instance-bound environment, lease DuckLake snapshots, and drain readers safely during cutover.
- `internal/manageddata/` implements local and S3-backed ingestion, revisions, upload protocols, runtime views, retention, and binding resolution.
- `internal/analytics/model/` defines semantic models. `internal/analytics/query/` plans governed single- and multi-fact queries. `internal/analytics/materialize/` and `internal/analytics/duckdb/` execute and cache them.
- `internal/access/` owns principals, authentication credentials, RBAC, grants, data policies, groups, SCIM, sessions, service principals, and access auditing.
- `internal/app/` is the composition root and top-level HTTP router. Feature handlers live beside their domains under packages such as `internal/dashboard/http`, `internal/project/http`, and `internal/agent/http`.
- `github.com/Yacobolo/toolbelt/pagestream` provides the shared Datastar page/SSE transport, signal history, broker, tracing, and escaped action construction.
- `api/signals/main.tsp` is the source of truth for browser signal contracts. Generation produces feature-owned Go models and TypeScript types in `web/generated/signals/index.ts`.
- `internal/dashboard/ui/`, `internal/admin/ui/`, and `internal/agent/ui/` render gomponents document shells. `web/components/` contains Lit route and visual components.
- ECharts is the built-in chart renderer. TanStack powers table state and virtualization behind LeapView-owned signal/query contracts.

## Runtime Flow

1. `GET /` opens Insights; `GET /explore` opens Data Explorer; Develop uses `/sources`, `/models`, `/semantic-models`, `/pipelines`, and `/connections`.
2. Dashboard routes are `GET /dashboards/{dashboard}` and `/dashboards/{dashboard}/pages/{page}`. The server-bound project is never selected in browser routes.
3. Each page opens the canonical `GET /updates?...` Datastar SSE stream from `data-init`.
4. Browser components emit small domain events. Gomponents attributes translate them into CSRF-protected Datastar commands.
5. Domain handlers authorize the request, update stream state, execute governed DuckDB queries where needed, and publish typed signal patches through Toolbelt Pagestream.
6. Lit components subscribe to signal paths and render without ad hoc data-fetch APIs.

## Important Files

- `cmd/leapview/main.go` and `internal/app/cli/serve.go`: process startup and lifecycle.
- `cmd/leapview-site/main.go` and `internal/app/site/http/`: independently deployable public site startup and HTTP adapter.
- `internal/app/router.go`: canonical page, command, auth, admin, and API routes.
- `internal/project/compiler/compiler.go`: project compilation entrypoint.
- `internal/runtimehost/manager.go`: serving-generation and snapshot-lease lifecycle.
- `internal/analytics/materialize/runtime.go`: query execution, coalescing, and cache integration.
- `internal/analytics/query/planner.go`: semantic query planning.
- `internal/dashboard/runtime/`: dashboard query orchestration and signal payload construction.
- `internal/dashboard/ui/page.go`: governed dashboard document shell.
- `web/components/dashboard/dashboard-page.ts`: interactive report surface.
- `web/components/dashboard/table/report-table.ts`: BI table component.
- `docs/`: authored and generated public documentation; `site/`: site-specific browser source and static assets.
- `.github/workflows/ci.yml`: canonical parallel CI workflow.

## Development

- `task dev` builds, bootstraps, deploys, and starts the managed development server.
- `task ci` runs the fast pull-request contract locally; run it before a meaningful push while developing.
- `task ci:pr` runs that same PR contract locally, with bounded Go and frontend lanes.
- `task ci:full` runs static/race, desktop, route QA, and deployment validation in addition to the PR contract; the merge queue runs it against the exact candidate before merge.
- `task ci:nightly` adds dependency security scans to the full contract and runs automatically every day.
- `task ci:local` remains a compatibility alias for `task ci:full`. GitHub Actions runs the same contracts on ephemeral GitHub-hosted runners with correctness-independent remote caches.
- `task generate` regenerates sqlc, configuration, API, signal, and JSON Schema artifacts.
- `task generated:check` verifies intentional public contract snapshots are current.
- `task dev:status`, `task dev:logs`, and `task dev:stop` manage the worktree-local server.

Use focused tests during iteration and `task ci` before handing off substantial changes. Follow red-green-refactor for features and fixes. Prefer long-term correctness, simplicity, robustness, and scalability over minimizing implementation cost.
