# Repository and development workflow

LeapView is a monorepo containing the product application, browser components, resource examples, generators, deployment contracts, documentation, and independently deployable public site. Keeping these surfaces together lets one pull request update behavior and its contracts atomically.

## Important locations

- `cmd/leapview/` — application and CLI entry point.
- `internal/app/` — process composition, global routing, entrypoints, public-site composition, and tooling.
- `internal/<capability>/` — product behavior and its contracts, use cases, adapters, workers, and optional runtime construction.
- `internal/agent/contracts/typespec/` — capability-owned curated agent DTOs and portable tool-schema source contracts.
- `internal/platform/` — capability-agnostic technical mechanisms.
- `api/typespec/` — headless API source contract.
- `api/signals/` — UI signal source contract.
- `dashboards/` — complete example configuration-as-code projects.
- `web/components/` — product Lit components and renderer adapters.
- `static/` — built product browser assets.
- `adr/` — numbered, durable architecture decisions and their authoring template.
- `docs/articles/` — authored task and concept documentation.
- `docs/reference/`, `docs/api/`, and `docs/visuals/` — generated or catalogued reference inputs.
- `site/` and `internal/app/site/` — public site assets and Go HTTP server.
- `deploy/hetzner/` — supported single-node deployment contract.

Read the nearest `AGENTS.md` before editing. Preserve unrelated user changes in a dirty worktree.

## Choosing a package

LeapView uses a modular-monolith ownership rule:

- If a package mentions a product noun, place it in the capability that owns that language.
- If it implements a capability-agnostic technical mechanism, place it in `internal/platform`.
- If it assembles or exposes the application, place it in `internal/app`.

Capability modules are peers. `access`, `analytics`, `project`, `workload`, `runtimehost`, and `servingstate` are horizontal because several experiences and workflows use them, but they remain product capabilities rather than a shared technical layer. Depending on one requires an explicit contract and a declared dependency edge.

Keep capability HTTP, API, UI, persistence, and worker adapters beside their owner. Do not introduce generic `internal/api`, `internal/ui`, or `internal/modules` roots. Read the [Architecture overview](/docs/architecture) before creating a new top-level package or cross-capability dependency.

## Development loop

Use red-green-refactor for behavior changes:

1. Add or update a focused test that demonstrates missing behavior.
2. Run it and confirm the expected failure.
3. Implement the smallest coherent change.
4. Run focused tests until green.
5. Refactor while keeping tests green.
6. Run generated checks and the full CI gate.

Prefer package-level Go tests and focused Bun/Playwright tests during iteration. Use `task ci` before handing off substantial work.

## Managed development server

Use the worktree-safe commands:

```sh
task dev
task dev:status
task dev:logs
task dev:stop
```

The workflow stores process state beneath `.tmp/` and selects a worktree-local port. Do not kill unrelated processes or reuse persistent state from another worktree implicitly.

## Generation

Run:

```sh
task generate
```

It produces database code, configuration surfaces, API and UI-signal contracts, JSON Schemas, CLI docs, and the unified documentation catalog/search index. Individual generator tasks exist for focused work.

Do not manually edit a file marked generated. Change TypeSpec, CUE/config contracts, Cobra commands, configuration specs, or the owning generator. Agent provider schemas are generated from `internal/agent/contracts/typespec/main.tsp`; their readable and machine-readable presentation under `docs/reference/agent-tools/` is generated from the canonical runtime catalog. Generated implementation code, catalogs, and search indexes are build inputs and stay out of Git unless they are intentional public contract snapshots.

For PostgreSQL persistence, author every static DML/query leaf in the
capability-owned SQL files consumed by sqlc with `sql_package: pgx/v5`.
sqlc is typed SQL code generation, not an ORM: repositories and domain
services retain invariants, caller-owned transaction boundaries, state
machines, and error mapping. Raw SQL is limited to embedded schema/migrations,
validated dynamic identifier or result-shape SQL, and explicitly
analyzer-incompatible statements. Mark each exception adjacent to the call
with `sqlc-exception:<class>` or record it in a maintained capability
inventory, with its rationale and verification. Generation is local and
deterministic; sqlc Cloud is not used. Run `task db:generate` while iterating;
`task db:check` runs pinned generation twice, rejects nondeterministic or tracked
generated output, runs offline `sqlc vet` and `sqlc diff`, compiles generated PostgreSQL
consumers, and audits raw SQL with the AST-based `sqlcaudit` tool. It then
applies the clean product baseline to a disposable PostgreSQL 18 database and
runs the `sqlc/db-prepare` rule for every PostgreSQL query package. The derived
prepare config receives its short-lived database URI through an environment
variable; credentials and sqlc Cloud are not used. The audit enforces zero
static handwritten PostgreSQL SQL: every static `Exec`, `Query`, or `QueryRow`
call must be a generated sqlc method. Dynamic SQL is allowed only for a
narrowly justified ADR-0016 exception, marked adjacent to the call or recorded
in an exact capability inventory with rationale and verification.

Use `task docs:check` and `task config:check` to validate generated output. `task generated:check` detects drift in the public snapshots. CI verifies deterministic build-only inputs and shares them with downstream jobs.

## Browser assets

Product assets and public-site assets have separate builds. Component DOM tests use Playwright/Bun against focused bundles. Site tests exercise production lazy chunks and documentation routes. Design-token checks enforce the supported Primer-backed styling boundary.

When changing a component, test its cold/unupgraded layout, upgraded behavior, compact width, theme, and cleanup where relevant.

## Project and schema changes

Update the example dashboards when a contract changes. A feature is not complete if the code accepts it but schemas, generated references, examples, and docs disagree.

Validate example projects and generated YAML fences. Keep stable identifiers and provide migrations for intentional compatibility breaks.

## Final verification

The standard gate is:

```sh
task ci
task vuln
git diff --check
```

Review `git status` and the diff before committing. Keep product, tests, generated contracts, and documentation in the same pull request when they describe one behavior change.
