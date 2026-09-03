# ADR-0005: Use a project-wide resource graph

Status: accepted

Decision date: 2026-08-15

Implementation: complete

Deciders: LeapView maintainers

Supersedes: workspace-scoped resource model (unrecorded)

Related: [Self-service dashboard builder and agent authoring](https://linear.app/leapstack/project/self-service-dashboard-builder-and-agent-authoring-c2670ffcbb2a)

## Context

LeapView currently uses workspaces as mandatory containers for Models,
semantic models, dashboards, pipelines, grants, and data policies. Workspaces
appear in authored identity, references, routes, authorization, serving state,
workload admission, and the browser shell.

That boundary does not match the product's deployment or execution model. A
LeapView instance has one process-wide analytical execution plane, one storage
and serving-state system, and one permanently bound environment. A project is
compiled and deployed atomically. A workspace therefore does not isolate
compute, storage, deployment, or failure domains. Treating it as though it does
creates a user-facing boundary without a corresponding operational boundary.

The mismatch is especially costly for shared dimensions and semantic assets.
Business entities such as customers, products, accounts, and dates normally
serve several analytical subjects. Mandatory workspace ownership either
duplicates those resources or forces artificial placement decisions. It also
makes authorization depend on repository structure when LeapView already has
explicit principals, groups, grants, and data policies.

The intended product is closer to one dbt project and one governed warehouse
graph deployed into each LeapView instance. Most mid-sized deployments should
need one development instance and one production instance, not several
containers inside each instance. Organizational domains can help discovery and
ownership without becoming another execution or identity boundary.

LeapView is not publicly released. We can replace the current workspace model
directly and do not need compatibility routes, aliases, migrations, dual reads,
or deprecated configuration forms.

## Decision

A LeapView instance serves exactly one environment and one active compiled
project graph. The project is the atomic authored, validated, deployed, and
activated unit.

The project graph contains these first-class resource kinds:

```text
Project
├── Connections
├── Sources
├── Models
├── Semantic models
├── Pipelines
└── Dashboards
```

Resources are project-wide. Workspaces are removed from the user-facing model
and from canonical resource identity. They are not retained as namespaces.

The Develop navigation presents the same model using concise product language:

```text
Develop
├── Data
├── Models
├── Semantic models
├── Pipelines
└── Connections
```

`Data` is the product area for sources, managed inputs, physical schema, and
data preview. `Source` remains the precise configuration and compiler term.
Dashboards and data exploration remain in Insights because they consume the
governed graph rather than define its engineering foundations.

## Deployment and isolation boundaries

- **Instance** owns compute, analytical storage, application state, secrets,
  operational limits, and hard isolation. Development, staging, and production
  use separate instances.
- **Environment** is the immutable serving identity permanently bound to an
  instance. It is not a request-time selector or an authored container.
- **Project** is one codebase and one complete dependency graph. A deployment
  compiles, validates, prepares, and activates the graph atomically.
- **Resource** is a connection, source, model, semantic model, pipeline, or
  dashboard in that graph.
- **Domain** is optional descriptive metadata. It is not a container, namespace,
  access boundary, deployment unit, compute boundary, or part of identity.

Independent compute, storage, deployment cadence, or failure isolation requires
a separate LeapView instance. If future customers need independently deployable
project graphs inside one instance, that requires a new architecture decision;
it must not be approximated by restoring workspaces.

## Resource graph

The principal dependency path is:

```text
Connection -> Source -> Model -> Semantic Model -> Dashboard
                         ^              ^
                         └── Pipeline ──┘
```

- A **connection** declares a logical data-access endpoint and adapter. Secret
  values and environment-specific bindings belong to the target instance, not
  the portable project contract.
- A **source** declares an external or LeapView-managed input relation, its
  physical schema, freshness expectations, and connection reference.
- A **model** defines a reusable transformation over sources or other models,
  together with materialization, contracts, tests, and lineage.
- A **semantic model** defines the governed query surface: datasets or entities,
  dimensions, relationships, measures, metrics, formatting, and policy-aware
  query behavior.
- A **pipeline** schedules or triggers ingestion and model refresh. It
  orchestrates graph resources but does not own or namespace them.
- A **dashboard** is a governed, versioned presentation document over semantic
  models. It owns semantic queries, filters, interactions, pages, visuals, and
  layout. It cannot introduce raw SQL, joins, credentials, grants, or deployment
  behavior.

Shared dimensions and other reusable models are defined once and referenced by
every permitted semantic model. No import or copy is required to cross an
organizational domain.

## Identity, metadata, and authorization

Every resource receives a stable canonical ID that is independent of its file
path, display name, domain, owner, and authoring origin. Project-authored
resources may also expose project-unique symbolic names for readable references;
the compiler resolves those references to canonical IDs. Interactive and agent-
authored dashboards receive the same kind of canonical identity.

Common optional descriptive metadata includes:

- display name and description;
- owner principal or team;
- domain;
- tags; and
- documentation and provenance.

Changing domain metadata does not rename, move, copy, redeploy independently,
or change the identity of a resource.

Authorization is explicit and object based. Principals and groups receive
capabilities on project resources; row- and column-level data policies constrain
governed queries. A domain value never grants access by itself. Repository
directories and symbolic names are organization aids, not authorization rules.

Dashboard visibility uses product-level sharing states such as private,
restricted, and organization-visible, plus explicit principal or group grants.
There is no `workspace-visible` state. Catalog, search, agent tools, and browser
navigation all return the authorization-filtered project graph.

## Dashboard authoring and publication

Dashboard delivery supports two authoring origins over one canonical document,
validator, compiler, and runtime:

- **Project-managed dashboards** are authored as files, reviewed in Git, and
  published through immutable project releases.
- **Instance-managed dashboards** are created by the builder or agent, use
  durable drafts, and publish immutable revisions in application state.

Both origins reference stable semantic-model IDs and compile against an exact
semantic serving-state generation. A project deployment revalidates affected
managed dashboards by dependency ID rather than by container activation.

Exporting an instance-managed dashboard to the project is a transfer or fork of
management, not an ongoing dual-write relationship. A transferred dashboard
becomes project managed and read-only in the interactive authoring store. A
fork receives distinct identity and provenance. This prevents repository and
database definitions from silently diverging.

Builder and agent clients operate through the same lifecycle commands. Agent
tools accept only authorization-filtered, catalog-returned resource IDs. Browser
and API routes address dashboards by stable ID rather than by workspace path.

## Standards alignment

No single external specification covers LeapView's complete graph. LeapView
uses standards at the boundary where each is strongest:

| Concern | Reference | Decision |
|---|---|---|
| Project resources, sources, models, dependency graph, tests, contracts, groups, and exposures | [dbt projects](https://docs.getdbt.com/docs/build/projects) and the [dbt manifest](https://docs.getdbt.com/reference/artifacts/manifest-json) | Primary conceptual data-engineering model and an interoperability source, not LeapView's authored schema |
| Semantic interchange | [Apache Ossie](https://ossie.apache.org/) | Compatibility target through import, export, validation, and extensions |
| Executable semantic behavior | [dbt semantic models and MetricFlow](https://docs.getdbt.com/docs/build/semantic-models) | Reference vocabulary and behavior for entities, dimensions, measures, metrics, and safe relationship planning |
| Plans, snapshots, and environments | [SQLMesh plans](https://sqlmesh.readthedocs.io/en/stable/concepts/plans/) | Internal deployment inspiration, not user-facing vocabulary |
| Runtime lineage events | [OpenLineage](https://openlineage.io/docs/spec/object-model/) | Interoperability format for job, run, and dataset lineage |
| Dashboard document and lifecycle | LeapView | Canonical LeapView-owned contract shared by files, builder, agent, compiler, and runtime |

LeapView does not adopt the dbt manifest as its source-of-truth authoring
schema. The manifest is a generated dbt artifact and carries dbt-specific
compilation and adapter concepts; it also does not define LeapView connections,
pipelines, managed data, or dashboard documents.

LeapView does not adopt Apache Ossie as its authoritative execution contract at
this stage. Ossie is the correct vendor-neutral semantic interchange direction,
but its current roadmap still includes stable identity, richer grain and
relationship semantics, governance, and a reference query language and engine.
LeapView retains its typed semantic compiler and query planner while keeping the
portable subset mappable to Ossie. LeapView-specific behavior travels through
versioned extensions where lossless mapping is not yet possible.

## Rejected alternatives

### Keep workspaces as mandatory containers

Rejected because they do not provide compute, storage, deployment, or failure
isolation. The boundary encourages duplicate dimensions and models, complicates
cross-subject analytics, and couples access control to content placement.

### Redefine workspaces as namespaces

Rejected because project-wide stable IDs, symbolic names, search, ownership
metadata, and RBAC already solve organization and collision concerns. A
mandatory namespace would retain most of the current complexity without an
operational reason.

### Make domains first-class containers

Rejected because domains are expected to change as the organization changes.
Putting them into identity or references would turn an organizational metadata
edit into a resource migration and recreate cross-domain sharing problems.

### Use dbt as LeapView's complete authored specification

Rejected because dbt is the closest data-engineering model but does not specify
the full LeapView connection, managed-data, pipeline, governed-query, dashboard,
and interactive publication lifecycle.

### Use Apache Ossie as the runtime semantic contract immediately

Rejected because the incubating interchange specification does not yet define
all execution, identity, governance, and query-planning invariants required by
LeapView. Compatibility is adopted without outsourcing runtime correctness.

## Consequences

The new model makes shared data and dimensions first-class, aligns navigation
with the actual dependency graph, and separates organizational description from
security and execution. Insights, builder, agent, API, and headless consumers
resolve the same governed catalog and semantic graph.

The implementation must remove workspace assumptions across configuration,
compiler ownership, persistence, serving generations, routes, API and CLI
contracts, access checks, workload admission, audit records, browser signals,
documentation, examples, and tests. Whole-project activation replaces per-
workspace activation. Workload fairness must use explicit workload class,
principal or group, and instance-wide limits rather than workspace identity.

Existing workspace configuration and state are disposable pre-release data.
Implementation must not add:

- workspace-to-project migration code;
- compatibility schemas or deprecated fields;
- `/workspaces/...` redirects or route aliases;
- dual workspace/project identity;
- read or write fallbacks; or
- hidden workspace IDs retained only for internal convenience.

Fixtures and local development state may be reset. Tests should positively
assert the project-wide model and reject the removed workspace forms so the old
boundary cannot return accidentally.

This decision supersedes workspace-scoped identity, ownership, authorization,
activation, and navigation described by existing concept and architecture
documentation. Those pages describe the current implementation until the
follow-up refactor replaces them; they are not the target architecture.

## Confirmation

Configuration, API, CLI, route, compiler, persistence, access, browser, and
architecture tests must reject workspace-scoped forms and exercise stable
project-wide resource IDs. No package may retain workspace identity as an
internal shortcut. The public concept documentation may describe this model
only after the implementation and generated contracts conform to it.
