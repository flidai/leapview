# Supported capabilities

This page maps the supported product surface. Generated configuration, CLI, API, and visual catalogs remain authoritative for exact current members.

## Configuration resources

LeapView supports these versioned YAML resource families:

- project discovery: Project;
- project data access: Connection and Source;
- workspace ownership: Workspace;
- analytical data: ModelTable and SemanticModel;
- presentation: Dashboard;
- access: WorkspaceGroup, WorkspaceRoleBinding, Grant, and DataPolicy;

All resources use the `apiVersion`, `kind`, `metadata`, and `spec` envelope. JSON Schemas and generated reference pages define required fields and accepted values.

## Data access and lifecycle

Connection kinds include managed data and supported object, HTTP, database, and DuckLake inputs as listed by the Connection schema. Source formats include the formats listed by the Source schema.

Managed data supports local planning, immutable content-addressed revisions, resumable local-backend upload, direct multipart object-storage upload, staged revision inspection, and atomic activation with a project deployment.

Model tables materialize source transformations into DuckLake-managed analytical state. Refresh builds isolated replacement state and changes the active serving pointer only after success. Storage cleanup reconciles active snapshots and query leases before deletion.

## Semantic and BI surfaces

Semantic models provide model-table datasets/fields, dimensions, measures, derived metrics, and explicit relationships. Supported aggregations and cardinalities are listed by the generated schema.

Headless operations cover semantic model and dataset discovery, field listing, row preview, aggregate query, and explain. Dashboard operations cover dashboard/page/component discovery, filter options, coordinated page query, visual data, and bounded table data.

## Dashboard presentation

Dashboards support report pages, deterministic grid placement, date-range, multi-select, and text filters, KPI cards, renderer-neutral chart visuals, data tables, matrices, pivots, conditional formatting, and semantic point/row selections.

The [visual catalog](/docs/visuals/overview) lists every documented registered chart type and renders a live example. Dashboard configuration lists accepted page component kinds and query shapes.

## Identity and access

Interactive identity supports local authentication, generic OIDC, and Azure/Entra configuration. SCIM provisions users, groups, membership, and active state. Service principals and API tokens support non-human workloads.

Authorization includes workspace roles, explicit grants on securable objects, effective-privilege evaluation, ownership, row-filter/column-mask data policies, and workspace audit/query event discovery.

## Operations

The CLI and API support project validation, target-aware planning, atomic deployment, refresh runs, backup/restore, bounded history maintenance, storage cleanup dry-run/apply, readiness checks, and managed revision inspection.

The supported Hetzner module provides a single-node production topology with Caddy, restricted SSH, local persistent state, scheduled backups, and health-checked image upgrade/rollback. It is not a high-availability deployment contract.

## Integrations

- Cobra-derived CLI command surface.
- TypeSpec/OpenAPI-derived HTTP API.
- Workspace search and catalog/lineage discovery.
- Dashboard and semantic headless BI queries.
- Principal-owned global agent conversations, messages, runs, events, and turns.
- An OAuth-protected, tools-only MCP endpoint at `/mcp` using the same twenty-tool governed catalog as the built-in agent—including bounded dashboard authoring tools for private drafts and exact revisions—with embedded PKCE consent or an external JWT issuer. See [Use the agent tool catalog](/docs/guides/integrate/agent-tools).
- Prometheus metrics protected by a bearer token.

## Boundaries

LeapView is dashboards-as-code: browser edits are not the durable authoring source. Browser clients do not receive unrestricted SQL or credentials. DuckLake snapshots are internal serving consistency and cleanup boundaries, not a general customer-facing time-travel/version browser. The provided single-node deployment is not horizontal high availability.

Use the generated [Configuration](/docs/config/project), [CLI](/docs/cli/reference), [API](/docs/api), and [visual](/docs/visuals/overview) catalogs to confirm exact support in the current version.
