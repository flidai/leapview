# ADR-0018: Retain Project as the durable deployment namespace

Status: accepted

Decision date: 2026-09-02

Implementation: pending

Amended by: [ADR-0019](0019-integrate-dbt-at-the-warehouse-contract-boundary.md),
dbt mapping and external-source examples only

Deciders: LeapView maintainers

Supersedes: none

Amends: [ADR-0016](0016-adopt-standards-aligned-data-contracts-and-interchange.md),
public Project identity and deployment scope only;
[ADR-0005](0005-use-project-wide-resource-graph.md), Project graph-root
representation only

Related: [ADR-0007](0007-adopt-plan-driven-project-delivery.md);
[ADR-0008](0008-isolate-ducklake-candidate-physical-state.md);
[ADR-0009](0009-separate-control-and-physical-transactions.md);
[ADR-0015](0015-adopt-durable-audit-and-compliance-controls.md);
[ADR-0017](0017-adopt-a-looker-aligned-semantic-access-contract.md);
[ADR-0019](0019-integrate-dbt-at-the-warehouse-contract-boundary.md);
[Project namespace conformance](specifications/project-namespace-conformance.md);
[dbt projects](https://docs.getdbt.com/docs/build/projects);
[dbt project dependencies](https://docs.getdbt.com/docs/mesh/govern/project-dependencies);
[Lightdash project configuration](https://docs.lightdash.com/self-host/customize-deployment/environment-variables);
[Lightdash multi-dbt-source merge](https://github.com/lightdash/lightdash/blob/a4cfeb9e4e70643d9a92588bb42ceaa13f1863d8/packages/common/src/dbt/manifest.ts);
[Omni dbt integration](https://docs.omni.co/integrations/dbt/setup);
[Rill projects](https://github.com/rilldata/rill/blob/a21215b66badc27ae2a16e6885614c6bdedae883/docs/docs/guide/administration/project-settings/project-settings.md);
[SQLMesh multi-repository guide](https://github.com/SQLMesh/sqlmesh/blob/839c5b2caface322045441d41c74f528a508998e/docs/guides/multi_repo.md)

## Context and problem statement

ADR-0016 correctly removes `kind: Project` from portable analytics source. A
repository root and conventional resource directories are enough to compile
Connection, Source, Model, SemanticModel, Pipeline, and Dashboard resources.
LeapView does not need an authored include-only manifest to discover them.

That authoring decision does not remove the need to identify the analytics
product being deployed. Project already scopes deployment claims, serving
state, authorization, audit, connection bindings, managed data, and active
generations. Removing that identity would make an instance identifier or source
location stand in for product identity and would make the same portable bundle
difficult to promote coherently through separate environments.

The existing runtime has a deliberately smaller topology than a multi-project
BI control plane. One server instance is durably claimed by one Project and one
environment. Browser, query, search, agent, and release surfaces use that
server-bound Project and reject request-time Project selection. Runtime-host
discovery rejects active scopes spanning several Projects. Preserving Project
identity therefore does not imply changing one process to host several active
Projects.

The ecosystem supports a closed semantic Project boundary but does not supply
one universal process topology. dbt resolves package or public-model
dependencies into a consumer project's graph before that project builds.
Lightdash hosts several BI Projects in one control-plane instance, scopes
content and authorization by Project, and its guarded multi-dbt-source path
merges manifests into one combined Project explore set. Omni formally connects
one dbt project per database connection and cannot combine separate connections
in one query. Rill explicitly keeps resources inside one Project and describes
a Project as one deployed instance. SQLMesh combines several repositories into
one centrally planned graph.

These products differ on repository cardinality and process hosting, but agree
on the important boundary: semantic queries execute against one resolved,
internally coherent project graph. They do not make live references between
independently deployed BI Projects a prerequisite for LeapView v1.

The question is how LeapView retains a durable Project identity without
restoring an authored Project resource, changing the server-bound runtime, or
prematurely choosing a cross-project sharing protocol.

## Mental model

LeapView uses four separate concepts:

| Concept       | Meaning                                                          |
| ------------- | ---------------------------------------------------------------- |
| Source bundle | Portable analytics source code                                   |
| Project       | Durable identity of one analytics product                        |
| Environment   | The place that product runs, such as dev, staging, or production |
| Generation    | The exact immutable release active in that environment           |

The deployment relationship is:

```text
source bundle + Project + environment
                  |
                  v
          immutable generation
```

Source answers **what is defined**. Project answers **which analytics product
owns it**. Environment answers **where it runs**. Generation answers **which
exact release is active**.

## Decision drivers

- Keep the single-repository path free of a LeapView Project manifest.
- Preserve a stable product namespace across source revisions and environment
  promotion.
- Align the decision with the existing server-bound Project and environment
  runtime rather than introducing multi-project process hosting.
- Keep authored resource IDs portable and unique only inside their Project.
- Keep every candidate and active generation a closed, reproducible graph.
- Require every SemanticModel dataset to resolve to a Project-local Model in
  the same candidate and generation.
- Retain Project-aware authorization, audit, target binding, and lifecycle
  evidence without accepting a request-time Project selector.
- Defer cross-project sharing until its physical, version, retention, and policy
  semantics are independently justified.
- Preserve dbt adoption ergonomics: one ordinary dbt project and its LeapView
  semantic files can deploy to one automatically bound LeapView Project.

## Considered options

### Remove Project completely

Instance, source bundle, and generation identities could replace Project. This
would minimize vocabulary but erase the durable product scope already carried
through deployment and authorization. The same product promoted through
separate target instances would have no stable issuer-assigned, target-claimed
namespace distinct from its repository or current release.

### Restore `kind: Project`

An authored Project resource could carry includes and identity. This would
again mix portable source with destination lifecycle and authorization state.
The compiler already discovers the six authored resource kinds without it.

### Retain Project and host several Projects in one server process

This is a valid future topology for a shared control plane, but it changes the
runtime, browser context, routing, authorization partitioning, caches, quotas,
credential isolation, physical-pool fencing, and failure model. It is not
required to preserve Project identity and contradicts the current singleton
Project claim.

### Retain Project and add versioned cross-project imports

Native sharing would require a publication model, separate contract and dataset
versions, import locks, entitlement and revocation behavior, candidate ingress,
retention, cycle handling, and cross-catalog physical ownership. dbt Mesh uses
environment-resolved relations rather than immutable dataset locks, Rill does
not share Project objects, and SQLMesh plans one combined graph. Selecting a
LeapView protocol now would couple the namespace decision to an unproven second
architecture.

### Retain Project with one bound Project per instance

Project remains the durable product and authorization namespace. Portable
source remains Project-free. A target instance claims one Project and one
environment and serves one active immutable generation. This preserves the
existing topology and leaves sharing as an independent future decision.

## Decision outcome

LeapView retains **Project as the durable control-plane identity and deployment
namespace of one analytics product**. Project is not an authored resource kind,
include manifest, compiler graph node, request-time selector, or substitute for
an environment or generation.

### Authoring and deployment binding

Portable source contains only Connection, Source, Model, SemanticModel,
Pipeline, and Dashboard resources. There is no `kind: Project` and no required
`leapview.yaml`. Compilation may validate a portable source bundle and compute
its digest without knowing a destination Project.

Planning binds the exact source-bundle digest to one target instance, canonical
Project UID, and environment. The target instance durably claims that Project
UID and environment. A later request naming another Project or environment is a
conflict; changing the claim requires an explicit instance reprovisioning or
recovery operation outside ordinary deployment.

The deployment authority is the canonical Project UID issuer. It mints one
opaque Project UID once, or receives it from an external Project registry,
before contacting any target instance. The same exact UID is supplied when
bootstrapping that Project's development, staging, and production targets. A
target never independently invents Project identity as a side effect of
deployment.

An unclaimed instance exposes one narrow bootstrap operation authorized by an
instance-administrator capability that exists before Project-scoped
authorization. Bootstrap receives the issuer-supplied Project UID and target
environment, creates the durable singleton claim atomically, and records the
issuer, principal, and audit evidence. Repeating the same tuple is idempotent;
any different tuple conflicts. After a claim exists, the bootstrap capability
cannot select or switch Project context.

The first local or single-repository setup may mint one default Project UID in
durable local deployment state and bootstrap its target automatically. Routine
browser and query routes therefore need no Project prefix or picker. Ordinary
deployment APIs may carry a Project locator, but it must resolve to the already
bound Project UID before work begins and cannot switch the server's Project.

### Project, environment, and generation

One Project may be deployed to several target instances, normally one for each
environment. Each instance remains permanently bound to one environment and
one Project. Each bound `(Project UID, environment)` has at most one active
generation.

Promotion sends the same portable source identity to a different target and
creates a new target-specific plan and candidate. It does not copy approval,
credentials, target bindings, or a mutable active pointer from another
environment. Rollback selects a retained generation in the same bound Project
and environment.

### Resource identity

Authored `metadata.id` is unique across the six authored kinds within one
Project candidate. It is not required to be unique across unrelated Projects.
The bound Project UID qualifies resource, contract, lineage, audit, cache, and
authorization identities wherever omission could cause collision or leakage.

Resource UIDs and tombstones continue to follow ADR-0016's instance registry.
This ADR does not make a resource UID portable across instances. Project UID,
authored resource ID, expected kind, contract digest, and generation evidence
together preserve meaning across environment promotion without treating a file
path, repository URL, dbt name, or display name as durable identity.

### API, browser, and authorization boundary

Project remains a public identity in deployment, authorization, audit, and
catalog contracts. A server exposes only its bound Project through browser,
query, search, agent, and release surfaces. Client-supplied browser or query
Project selectors are rejected rather than used to switch context.

Every request authorizes the principal against the explicit bound Project UID
before returning data or metadata. Cache, idempotency, lineage, audit, and
serving identities include Project UID when required for safe partitioning.
This ADR does not require a server-local multi-Project CRUD interface or
Project-selection session state.

### Closed semantic boundary

A SemanticModel may compose several datasets, but every dataset reference
resolves to a Model in the same Project candidate and generation. A
SemanticModel cannot qualify a Model by another LeapView Project, query another
Project's active catalog, or make another Project's availability part of its
serving path.

Cross-project transformation belongs upstream of this boundary. A consumer dbt
project may use dbt packages or dbt Mesh public models from producer projects,
then build and publish its own coherent release. LeapView ingests the completed
consumer release, lowers its selected outputs to Project-local Models, and
compiles SemanticModels only after those Models enter the private candidate.
The latest dbt Semantic Layer format likewise does not support direct
cross-project semantic-model references.

This closed-graph rule is independent of process topology. A future LeapView
control plane may host several Projects in one process, as Lightdash does,
without allowing one Project's SemanticModels to resolve live resources from
another Project.

### Isolation boundary

The Project claim supplies logical product and authorization identity. The
server instance supplies the process, network, credential, storage
administration, upgrade, and failure boundary. Because v1 binds one Project per
instance, Project-aware identity does not imply that several tenants safely
share one runtime.

### Cross-project boundary

The v1 compiler and planner reject direct references to resources in another
Project. There is no `projectOutput` Source location, output-publication
protocol, import lock, or same-process zero-copy import in this decision.

Teams may share data through an ordinary external Source with an explicit data
identity, through the immutable dbt release ingestion defined by ADR-0019, or by
resolving reusable source packages into one closed bundle before compilation.
Those paths do not create a live edge to another LeapView Project.

A future native Project import requires a separate ADR. It must distinguish
contract version from dataset release, materialize verified producer data into
the consumer's private candidate, make the consumer's sealed catalog the
serving authority, and define authorization, revocation, retention, lineage,
and failure behavior. Project identity alone does not select those semantics.

### dbt mapping

One dbt repository normally maps to one LeapView Project. The dbt project name,
repository, and commit remain producer provenance and may seed a suggested
human-facing locator, but they do not replace the issuer-assigned Project UID
durably claimed by the target.
Environment-specific dbt releases and LeapView bindings remain target inputs.

The producing dbt project may already depend on other dbt projects. dbt, not
LeapView, resolves that dependency graph and materializes consumer-owned
outputs before the immutable release boundary. The v1 profile does not merge a
set of independently published dbt releases into one LeapView deployment. A
future multi-release profile would need one deterministic collision policy and
one atomic release lock before it could produce a closed candidate.

The reference adoption path is therefore simple: keep dbt transformations and
tests in their normal project, keep LeapView SemanticModels and Dashboards in a
conventional subdirectory, and deploy the combined portable source to the
server-bound LeapView Project.

### Product and catalog projection

ADR-0016's Bitol data-product, DCAT, and lineage projections use one Project
generation as the product release boundary. External identifiers qualify
resources by stable instance, Project, generation, and resource identities and
remain authorization-filtered.

## Consequences

Users get one stable mental model without additional authoring ceremony. A
repository supplies source, Project supplies durable product identity,
environment supplies the target, and generation supplies the active release.
The dbt-plus-LeapView demonstration needs no Project YAML or Project picker.

The decision preserves the runtime's singleton Project claim, server-bound
browser behavior, and closed generation model. It restores Project to public
deployment and authorization identity without committing LeapView to a shared
multi-Project process.

The closed semantic boundary follows the established product pattern. Rill
keeps resources inside a deployed Project, Omni keeps formal dbt integration
inside one connection, Lightdash compiles one combined explore set per Project,
and SQLMesh combines repositories before planning. LeapView's one-Project-per-
instance topology is a v1 isolation choice, not a claim that every BI product
uses the same process topology.

Operators continue to deploy separate instances for separate Projects or hard
environments. That costs more infrastructure than multi-Project process
hosting, but it keeps process, credentials, storage administration, upgrades,
and failures honest and already matches the implementation.

Native cross-project reuse is deferred. Teams use external data boundaries or
closed source packages until a separately researched sharing protocol justifies
its publication, import, retention, and policy complexity.

## Confirmation

- Authoring schemas accept exactly Connection, Source, Model, SemanticModel,
  Pipeline, and Dashboard and reject `kind: Project`; compilation requires no
  Project manifest or destination identity.
- Project-claim tests prove one target database instance can bind exactly one
  canonical Project UID and environment, the same claim is idempotent, and a
  conflicting claim fails before candidate work.
- Bootstrap tests prove a deployment authority mints or receives one Project UID
  before target contact, all environments claim that exact UID, only an
  instance administrator may initialize an unclaimed target, and ordinary
  Project authorization is never used before the claim exists.
- Runtime-host, router, browser, query, release, search, and agent tests reject
  request-time Project switching and active scopes spanning several Projects.
- Plan, candidate, generation, serving, audit, lineage, authorization, cache,
  managed-data, and physical-root evidence retain the bound Project UID.
- Resource fixtures prove authored-ID uniqueness inside one Project candidate,
  same authored IDs in unrelated Projects, stable instance resource UIDs,
  tombstones, restore, and same-Project rollback.
- Promotion tests prove the same bundle can plan independently against separate
  Project/environment targets without copying target-specific state.
- Compiler fixtures reject qualified foreign Project references and the
  `projectOutput` Source variant. No cross-project publication or import
  capability is implied by Project identity.
- SemanticModel fixtures prove every dataset resolves to a Project-local Model
  in the same candidate and generation, including Models generated from the
  selected dbt release.
- dbt dependency fixtures prove producer-project provenance may include upstream
  projects while LeapView receives one completed consumer release and never
  performs live cross-project resolution.
- The dbt showcase deploys one ordinary dbt project and its LeapView semantic
  source into one automatically bound Project without authored Project YAML.
