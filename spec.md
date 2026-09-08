# LeapView architecture

This document records the accepted shape of the product. It is deliberately
short: implementation details belong in the package contracts and published
API documentation.

## Scope and identity

- A LeapView instance owns one process, one control-plane database, one
  analytical execution plane, and one configured environment.
- A project is the atomic authored, validated, compiled, deployed, and served
  unit. `ProjectID` is the canonical graph identity and appears in every
  serving, authorization, audit, and workload identity that needs a project.
- Environment is instance-bound configuration, not a request-time selector or
  a resource namespace. Development, staging, and production are separate
  instances when they need independent state or failure domains.
- Resource IDs are stable and independent of file paths, display names,
  domains, and deployment generations. Domains and folders are descriptive
  metadata only.

## Project graph

The project manifest discovers one immutable graph:

```text
Project
├── Connections
├── Sources
├── Models
├── Semantic models
├── Pipelines
└── Dashboards
```

The dependency direction is:

```text
Connection → Source → Model → Semantic Model → Dashboard
                              └── Pipeline (refresh orchestration)
```

Connections and sources provide governed inputs. Models provide reusable
transformations and materialize their outputs when refreshed. Semantic models
define datasets, dimensions, relationships, metrics, and policy-aware query
contracts. Pipelines schedule refresh work.
Dashboards compose governed semantic queries into pages, filters, visuals, and
layout. Shared resources are defined once and referenced by stable IDs.

Compilation validates the complete graph, resolves references, captures
dependency projections, and emits immutable artifacts. Runtime code consumes
those projections; it does not rediscover lineage by walking repositories or
constructing a second graph.

## Serving and authorization

Deployment prepares an immutable serving generation for the project and bound
environment. A generation includes the exact project digest, managed-data
revision, analytical snapshot identity, compiled runtime, and authorization
snapshot. Cutover publishes the complete generation atomically; readers lease
the referenced analytical snapshot until they drain.

The authorization snapshot is immutable after construction. It is validated
against the project graph and bound to `{ProjectID, Environment, GenerationID}`.
It contains canonical principal/group grants, captured project roles, and data
policies. Requests evaluate this installed snapshot and the authenticated
principal; they do not consult mutable role templates or infer access from
paths, folders, domains, or display names.

Authorization is enforced at every boundary: browser route, API operation,
CLI command, service use case, query planner, agent tool, and data execution.
Audit records use the same canonical project and serving identity. Credentials
and secret values never enter the graph, artifact, snapshot, cache, or audit
payload.

## Workload fairness

Admission is instance-local and bounded. Every request declares a workload
class, authenticated principal, canonical group set, operation, and resource
estimate. Interactive, background, refresh, control, and maintenance classes
have independent queue, concurrency, memory, and deadline policies.

Fairness is enforced across classes and principal/group identities while
requests remain project-bound. A principal or group may not monopolize shared
queues, retained results, cache budgets, or analytical capacity. The node may
lend idle capacity, but hard CPU, memory, or failure isolation requires a
separate instance. Durable jobs remain the source of truth; admission never
silently changes durable state.

## Canonical browser and API routes

Browser pages use unscoped product routes such as:

```text
/sources
/connections
/dashboards/{dashboard}
/dashboards/{dashboard}/pages/{page}
/chats/{conversation}
/admin/...
```

API resources are project-scoped where identity matters:

```text
/api/v1/projects
/api/v1/projects/{project}
/api/v1/projects/{project}/releases
/api/v1/projects/{project}/delivery/...
/api/v1/projects/{project}/connections/...
/api/v1/dashboards/{dashboard}
/api/v1/semantic-models/{model}/...
```

Generated API contracts are authoritative. Browser commands, API operations,
CLI clients, and agent tools share the same operation identity, authorization,
idempotency, audit, and error semantics. Unknown or retired paths are errors;
every caller uses the canonical route and operation contract.

## Ownership and composition

Capabilities own their domain contracts, use cases, and adapters. Platform
packages provide product-agnostic mechanisms. The application package composes
capabilities and global routing. Synchronous invariants use typed Go ports and
direct calls; durable asynchronous work uses bounded jobs and transactional
outbox records. Domain and use-case code does not import transport, persistence,
filesystem, analytical engine, or browser adapters.

The project graph, serving generation, installed authorization snapshot, and
workload controller are the only sources of truth for their respective
concerns. Generated artifacts and browser signal contracts are checked in or
reproducibly generated from their source contracts.
