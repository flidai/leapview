# Architecture overview

LeapView is a feature-oriented Go modular monolith with explicit generated contracts and focused browser components. The monolith keeps routing, authorization, project compilation, query execution, and lifecycle state in one deployable application while package boundaries preserve capability ownership.

## Modular monolith

The server has three kinds of top-level package:

```text
app
  │ composes
  ▼
capability modules (peers)
  │ use
  ▼
platform
```

- `app` owns process composition, global routing, CLI and process entrypoints, the public site, tooling, and global cleanup.
- Capability modules own product language and behavior: `access`, `admin`, `agent`, `analytics`, `dashboard`, `deployment`, `manageddata`, `project`, `refresh`, `release`, `runtimehost`, `servingstate`, and `workload`.
- `platform` owns capability-agnostic mechanisms such as HTTP, database and transaction primitives, filesystems, jobs, locking, observability, security, testing, and generic web transport.

All capability modules are peers. Some—especially `access`, `analytics`, `project`, `workload`, `runtimehost`, and `servingstate`—support several product experiences or workflows. That makes them horizontal product capabilities, not platform code or unrestricted shared dependencies. Modules collaborate only through explicit contracts and declared dependency edges.

`admin` is an interface module over capability-owned reads and mutations rather than a separate domain owner. Product HTTP, API, UI, worker, and persistence adapters live with the capability whose language they express. Generic protocol mechanics live in `platform`; global dispatch and application assembly live in `app`.

Offline initialization, retention, analytical cleanup, backup, and restore are Admin-owned use cases under `internal/admin/offline`. They depend on explicit Access, instance-state, retention, storage, archive, locking, and credential-recovery ports. `internal/app/adminoffline` only translates process configuration and wires those ports to concrete capability adapters and platform mechanisms.

`cmd/leapview` starts the application and CLI. Transport adapters parse HTTP or Datastar commands and invoke capability use cases. Capability code enforces authorization and lifecycle invariants through explicit ports. Capability-owned adapters implement SQLite, DuckLake, object-storage, filesystem, and external-connector behavior.

The process-facing `Application` surface is deliberately closed: handler,
start, shutdown, and fatal health only. A private lifecycle owner starts
components in dependency order, stops them in reverse order, and runs cleanup
closures once. Capability browser routes remain in each capability's `module`
package; the application router mounts those route sets and owns only global
process endpoints and cross-capability candidate composition.

Architecture checks enforce the declared capability graph as a directed
acyclic graph, validate every production import against that graph, prevent
the application router from reclaiming capability routes, and pin the narrow
process surface. These dependency and lifecycle invariants replace structural
field-count limits, which measured representation rather than coupling.

Avoid introducing a second path around capability use cases. Browser, CLI-backed API, and agent tools should converge on the same authorization and semantic boundaries.

## Configuration and deployment

Project YAML is loaded and validated as one graph. The project manifest discovers connections, sources, models, semantic models, pipelines, dashboards, access, and publications from flat include lists.

Project deployment compiles validated candidates into immutable artifacts and serving metadata, then changes the instance's serving pointers to accepted state. Runtime requests read the active deployment rather than mutable repository files. Managed-data revision pins move with the project candidate.

## Analytical storage

The platform SQLite database owns application state: identities, grants, environments, deployments, jobs, audit history, and active serving pointers.

One process-owned DuckDB instance is the sole client of a DuckDB-backed DuckLake catalog. DuckLake owns analytical table metadata, snapshots, schema evolution, statistics, and physical-file manifests; Parquet holds analytical data. Runtime generations produce snapshot-qualified plans and share bounded DuckDB connections for materialization and governed BI queries.

The active pointer is a LeapView concern; snapshot and file ownership are DuckLake concerns. Cleanup reconciles both before expiring metadata or deleting physical files.

## Query execution

Dashboard and headless handlers resolve the project resource, active deployment, semantic model, principal, data policies, filters, selections, sorting, and limits. The semantic query layer turns governed field/measure requests into bounded DuckDB work.

Hierarchical workload admission separates interactive reads from refresh writes with bounded project-fair queues and deadlines. Node-wide DuckDB, logical-result, and cache limits keep aggregate analytical work within the supported process envelope. Query cancellation and refresh generations prevent obsolete work from replacing newer state.

## Browser architecture

Go uses gomponents to render page shells and initial signal contracts. Datastar transports server-owned state and commands. Lit custom elements render application chrome, project/catalog pages, dashboards, filters, charts, tables, administration, data, and chat/agent surfaces.

Components bind to typed signal paths. They can keep ephemeral presentation state, but authoritative filters, selections, refresh state, permissions, and query results return from the server.

## Generated contracts

- TypeSpec under `api/typespec` defines the versioned headless API.
- TypeSpec under `api/signals` defines UI signal models.
- APIGen produces capability-owned Go server surfaces, typed clients, and models; a thin application route/metadata aggregate; platform-owned generic HTTP models; canonical OpenAPI; and the CLI operation registry.
- Capability CLI adapters use generated operation methods and concrete request/response types. Application composition injects a generic authenticated transport, so target resolution and HTTP mechanics remain shared without exposing string operation IDs or `any` bodies to capabilities.
- CUE/config-schema code exports JSON Schemas for YAML resources.
- The Cobra command tree generates CLI reference pages.
- Runtime configuration specifications generate Go accessors, environment reference, schema, and example environment.
- Documentation generation composes authored navigation with generated catalogs.

Change a source contract and regenerate; do not patch generated output as an independent authority.

## Deployment units

The product application and public documentation site are separate binaries in one monorepo. They share versioned contracts and examples but have independent HTTP packages and build outputs. This preserves documentation proximity without coupling production application availability to the marketing/docs site.

Read [Runtime architecture](/docs/architecture/runtime), [GitHub-hosted CI](/docs/architecture/github-hosted-ci), [Datastar signal flow](/docs/architecture/datastar), [Filter and slicer target architecture](/docs/architecture/filters-slicers), [Visualization target architecture](/docs/architecture/visual-plugins), [Geographic rendering](/docs/architecture/geographic-rendering), and [Storage architecture](/docs/storage-architecture) for deeper boundaries.
