# ADR-0019: Integrate dbt at the warehouse contract boundary

Status: accepted

Decision date: 2026-09-03

Implementation: pending

Deciders: LeapView maintainers

Supersedes: none

Amends: [ADR-0018](0018-retain-project-as-the-durable-deployment-namespace.md),
dbt mapping and external-source examples only

Related: [ADR-0005](0005-use-project-wide-resource-graph.md);
[ADR-0007](0007-adopt-plan-driven-project-delivery.md);
[ADR-0008](0008-isolate-ducklake-candidate-physical-state.md);
[ADR-0010](0010-adopt-strict-typed-data-resource-contracts.md);
[ADR-0014](0014-adopt-an-asset-selected-refresh-pipeline-contract.md);
[ADR-0016](0016-adopt-standards-aligned-data-contracts-and-interchange.md);
[ADR-0018](0018-retain-project-as-the-durable-deployment-namespace.md);
[dbt model contracts](https://docs.getdbt.com/docs/mesh/govern/model-contracts);
[dbt marts](https://docs.getdbt.com/best-practices/how-we-structure/4-marts);
[dbt manifest](https://docs.getdbt.com/reference/artifacts/manifest-json);
[dbt run results](https://docs.getdbt.com/reference/artifacts/run-results-json);
[Lightdash dbt adapter](https://github.com/lightdash/lightdash/blob/a4cfeb9e4e70643d9a92588bb42ceaa13f1863d8/packages/backend/src/projectAdapters/dbtBaseProjectAdapter.ts);
[Omni dbt integration](https://docs.omni.co/integrations/dbt/setup);
[Cube dbt integration](https://docs.cube.dev/recipes/data-modeling/dbt);
[Rill underlying models](https://docs.rilldata.com/developers/build/metrics-view/underlying-model)

## Context and problem statement

LeapView should be easy to adopt beside dbt. Teams should be able to keep dbt as
their data-transformation framework and use LeapView as a separate BI serving
layer for governed models, semantics, access, and dashboards. The integration
must feel coherent in a local dbt-duckdb demonstration and in production
automation, without making dbt a LeapView runtime dependency.

dbt and LeapView have adjacent but different responsibilities. dbt transforms
source data into tested warehouse relations and business-facing marts. LeapView
connects to those data products, applies bounded consumer-side serving
adaptation through required Models, and defines the governed semantic and
presentation layers. Treating a dbt manifest as either physical data or a
second semantic authority would blur that boundary.

The earlier proposal made a dbt-specific immutable release envelope, correlated
artifacts, Parquet export, and target-owned ingestion mandatory. That design can
provide exact cross-environment data promotion, but it couples basic dbt
adoption to a new publication protocol. It also encodes generally useful
external-dataset revision semantics as dbt-specific behavior. Comparable BI
products instead use dbt artifacts primarily for discovery and scaffolding,
reconcile that metadata with physical warehouse relations, and retain their own
semantic or serving model.

The question is where the required runtime boundary belongs, which metadata
integration is useful in v1, and when LeapView should create its own physical
serving state.

## Mental model

dbt produces the warehouse data product. LeapView consumes that product and
turns it into a governed BI product:

```text
dbt sources -> staging -> marts
                         |
                 warehouse contract
                         |
LeapView Connection -> Source -> Model -> SemanticModel -> Dashboard
```

The boundary has three rules:

1. **dbt owns upstream and domain transformation.** It builds and tests
   warehouse relations.
2. **LeapView owns BI serving.** It owns bounded serving adaptation,
   materialization, semantics, access, querying, and presentation.
3. **The warehouse contract connects them.** A stable, target-resolved relation
   or object location, its schema, grain, freshness, and publication state are
   the required integration. dbt artifacts are optional authoring and
   provenance inputs.

This is a tight developer experience over a loose runtime coupling.

## Decision drivers

- Let a dbt-built warehouse connect through the same Connection, Source, Model,
  SemanticModel, and Dashboard graph as any other producer.
- Keep dbt responsible for compilation, execution, packages, credentials,
  upstream and domain transformations, tests, and warehouse materialization.
- Keep LeapView usable when dbt is absent, replaced, unavailable, or upgraded.
- Avoid duplicating business logic or physical data solely to create friendly BI
  labels.
- Keep LeapView Models focused on serving contracts and bounded adaptation,
  with physical materialization controlled by LeapView's compiler and runtime.
- Make dbt metadata import an adoption accelerator without making generated
  artifacts the runtime source of truth.
- Support a simple local dbt-duckdb showcase and an honest production workflow
  using GitHub Actions and Azure Blob or ADLS.
- Preserve Project-local, closed semantic graphs under ADR-0018.
- Defer generic immutable external-dataset publication until its producer,
  identity, retention, and promotion requirements justify a separate decision.

## Considered options

### Provide no dbt awareness

Teams could declare every dbt output as an ordinary Source and author all
LeapView resources manually. This keeps the runtime clean, but misses useful
adoption ergonomics: dbt already provides model identity, descriptions, types,
lineage, tags, contracts, and test metadata that can help scaffold and validate
the LeapView boundary.

### Treat the dbt manifest as LeapView's runtime authority

LeapView could derive its runtime graph from `manifest.json`. A manifest is a
generated description of a dbt invocation, not physical data and not proof that
a current relation contains the described result. It also does not define
LeapView serving materialization, access policy, dashboards, or the complete
LeapView semantic contract. Making it authoritative would introduce two
partially overlapping control planes.

### Make LeapView execute dbt

LeapView could clone repositories and execute dbt before every deployment or
refresh. It would then own adapter installation, package resolution, arbitrary
macros and Python, transformation credentials, network access, scheduling, and
failure recovery. Those are CI and transformation-platform responsibilities,
not BI serving responsibilities.

### Require an immutable dbt release protocol

A producer could publish correlated dbt artifacts and content-addressed Parquet
outputs for LeapView to ingest. This supports exact data promotion, but makes a
new publication envelope and hardened artifact parser prerequisites for the
ordinary case where a warehouse relation already provides the integration
boundary. The same facility would be useful for SQLMesh, custom pipelines, and
other producers, so it should not be designed as dbt compatibility.

### Integrate at the warehouse contract boundary

dbt builds a relation or versioned object set. LeapView declares it as a Source,
validates the expected input, materializes the required thin serving Model under
compiler and runtime policy, and builds its own SemanticModel. Optional dbt
metadata tooling can scaffold or check those authored resources, but serving
never requires the dbt repository or artifacts.

## Decision outcome

LeapView integrates dbt at the **warehouse contract boundary**. A normal dbt
deployment produces physical warehouse relations or files. LeapView consumes
them through its ordinary project resource graph and target-owned connection
bindings. There is no dbt-specific serving path and no mandatory dbt release
envelope.

### Published dataset contract

The handoff from dbt to LeapView is a **published dataset contract**. It
consists of:

- a stable, target-resolved locator assembled from the portable Source location
  and the target-bound Connection;
- the expected schema and compatibility policy;
- the producer-declared keys and grain that LeapView's Model will validate;
- freshness, revision, or watermark information when the consumer requires it;
  and
- evidence that the intended producer publication completed before a consuming
  refresh begins.

Publication-complete evidence may be successful orchestration, a committed
warehouse transaction, an atomic namespace or view swap, or completion of an
immutable versioned object location. This decision does not require a new
marker, envelope, or dbt artifact. The evidence has meaning only under the
selected connector and producer protocol; a successful dbt command alone does
not prove that a separate asynchronous publication completed.

The baseline contract does not require every dataset to have an immutable
revision. A mutable relation is valid when the producer's consistency guarantee
is sufficient. A coordinated snapshot or versioned location becomes mandatory
only when consumers require cross-relation consistency or reproducibility.

### Runtime and ownership boundary

The required runtime integration is:

- a LeapView Connection bound by the target to the warehouse or object store;
- a Source identifying a dbt-produced relation or exact object location and
  declaring LeapView's expected input schema and freshness;
- a required Model forming the Project-local serving contract over that Source;
- a SemanticModel defining dimensions, relationships, measures, metrics,
  labels, formatting, and access behavior; and
- Dashboards and governed queries over that SemanticModel.

A refresh intended to consume a new dbt output begins only after that output is
successfully published. Serving and deployments or refreshes unrelated to that
publication remain independent of dbt. LeapView does not compile the dbt
project, resolve packages, execute macros, own dbt credentials, or require a dbt
process while serving queries.

A dbt model contract and successful dbt tests are producer evidence. They do
not replace LeapView's Source compatibility checks, Model output contract,
quality checks, or candidate qualification. Conversely, LeapView does not
repeat upstream transformation tests merely to claim dbt integration. Each
system validates the contract it owns.

### Contract enforcement

[ADR-0010](0010-adopt-strict-typed-data-resource-contracts.md) remains the
normative authority for Source and Model validation. The following is a
non-normative summary included only to explain the dbt handoff:

- an inferred Source schema records observation without claiming a field
  contract;
- compatible and strict Source schema mismatches block candidate activation;
- declared freshness is evaluated during deployment or refresh, with
  `warningAfter` admitting a warning and `errorAfter` blocking activation;
- Model fields form an exact output contract checked before activation;
- entity and grain declarations imply the required identity checks; and
- Model checks warn or block according to their declared severity.

This ADR does not redefine those modes, thresholds, or outcomes. A change to
their enforcement belongs in ADR-0010 or a decision that explicitly amends it.

### Required, thin LeapView Models

A LeapView Model over a dbt mart should be thin and intentional. Appropriate
uses include:

- projecting a stable, narrower BI-serving schema;
- casting or normalizing values at the consumer boundary;
- adapting an unstable or opaque producer identifier;
- declaring and validating the grain, entities, and output contract LeapView
  will serve;
- materializing expensive reads for interactive performance;
- isolating dashboards from warehouse availability or concurrent mutation; and
- participating in LeapView's candidate validation, atomic activation, and
  rollback lifecycle.

LeapView Models should not reproduce substantial transformations already owned
by dbt. They also should not be created only to convert `snake_case` identifiers
into display text. Stable machine-facing field IDs remain suitable contract
identifiers; SemanticModel dimensions and metrics supply user-facing labels,
descriptions, and formatting. A SQL alias is appropriate when it creates a
clearer durable semantic identifier, not merely title casing.

A Model may project, rename, cast, or normalize fields without taking ownership
of upstream business logic. Aggregation, deduplication, or joining that creates
a different business grain from a dbt mart is upstream or domain transformation
and normally belongs in dbt. The general LeapView Model contract remains usable
without dbt; this restriction defines ownership for the dbt integration path.

A Source may feed a SemanticModel only through the Model boundary required by
the ordinary LeapView graph. A direct-source or pass-through Model is acceptable
when its value is the explicit serving contract rather than another business
transformation. Physical materialization remains compiler and runtime policy as
defined by ADR-0010; it is not an optional authored switch.

### Security boundary

The target resolves a least-privilege Source credential that is read-only
whenever the connector can enforce that mode. LeapView-owned DuckLake serving
state uses a separate target-owned write scope; a Source credential does not
gain write authority over either the producer system or LeapView's physical
pool merely because the Model materializes its data.

LeapView SemanticModel access rules govern consumption after materialization.
If a restriction must prevent the LeapView service itself from reading or
copying sensitive rows or columns, the producer must enforce it before the
handoff through a restricted relation, view, export, or source identity. A
downstream semantic access rule is not an ingestion filter or a substitute for
upstream minimization.

### Optional dbt metadata integration

LeapView may provide an explicit dbt import or synchronization command as an
authoring tool. The v1 metadata path may:

- read an explicitly supplied, supported `manifest.json`;
- select dbt models using documented selectors;
- use dbt unique IDs, relation names, descriptions, columns, tags, contracts,
  and lineage as provenance and scaffolding input;
- optionally read correlated run results as recent build and test evidence;
- query the target warehouse catalog and reconcile the declared relation and
  physical column types before proposing LeapView resources; and
- generate a reviewable Source and thin Model draft or report drift against
  existing authored resources.

This tooling is optional. It must use explicit artifact-version allowlists,
bounded fail-closed parsing, deterministic mappings, and collision detection.
It must distinguish dbt's declared metadata from warehouse-observed schema and
from LeapView-authored semantics. It must not silently overwrite authored
resource IDs, contracts, labels, metrics, policies, or dashboards.

The dbt package, node unique ID, checksum, relation, target, invocation, and
repository revision remain provenance. They do not replace Project UID,
resource UID, contract identity, Source location, or generation identity.
LeapView deployment and serving continue to work when no dbt artifacts are
provided.

### dbt Semantic Layer and MetricFlow

Importing or querying dbt Semantic Layer and MetricFlow definitions is out of
scope for v1. LeapView SemanticModel remains the only semantic and access-policy
authority. A v1 metadata importer may observe that these dbt resources exist,
but it does not silently create or reconcile LeapView metrics from them.

A future semantic-import profile requires a separate decision defining identity
mapping, supported behavior, conflict resolution, version compatibility,
provenance, access-policy ownership, and whether imported definitions remain
linked or become reviewed LeapView source. Supporting dbt physical models does
not imply dbt Semantic Layer compatibility.

### Repository and Project topology

The reference showcase uses one repository containing a dbt project and a
LeapView source bundle. A monorepo makes one pull request able to change a dbt
mart, its LeapView input contract, semantics, and dashboard together. It is a
developer-workflow recommendation for the showcase, not a runtime requirement.
Separate repositories may deploy the same contracts with coordinated CI.

One ordinary consumer dbt project normally feeds one LeapView Project in the
reference path. The dbt project may use packages or dbt Mesh public models from
other dbt projects; dbt resolves and materializes those dependencies before
LeapView reads the consumer-owned output. Alternatively, a LeapView Project may
declare several ordinary Sources produced by separately operated systems,
provided every Source enters the same closed Project candidate. Neither case
creates a live reference to another LeapView Project.

Every SemanticModel dataset still resolves to a Model in the same LeapView
Project candidate and generation under ADR-0018. Repository layout and dbt
project names do not create or replace LeapView Project identity.

### Environments, refresh, and immutability

Logical Connections and Sources remain portable. Each LeapView target owns its
endpoint, credentials, and environment-specific bindings. dbt targets may
produce different development, staging, and production relations; the
corresponding LeapView target binding selects the intended physical boundary.

An external Source observes its configured relation or object at discovery and
refresh time. A successful LeapView Model refresh materializes and validates
replacement serving state before activation; a failed refresh leaves the
previous state active. Atomic candidate activation makes the captured LeapView
state internally coherent, but it does not make upstream observations atomic.

The integration therefore distinguishes two consistency levels:

- **Ordinary relation consistency:** candidate construction reads each Source
  under the selected connector's guarantees. LeapView makes no additional
  point-in-time guarantee across several upstream relations. Once the required
  Models are materialized, the candidate activates atomically and serving reads
  that one LeapView state.
- **Coordinated dataset consistency:** when several marts must represent the
  same producer publication, the producer must expose a connector-supported
  read-consistent snapshot or transaction, an atomic namespace or view swap, or
  one immutable versioned object location containing the complete selected set.
  LeapView starts the consuming refresh only after that publication completes.

These levels describe the producer and connector contract; they do not create a
dbt-specific release envelope.

Teams requiring reproducible file refresh should publish versioned Azure Blob
or ADLS keys or prefixes and update the Source location through a reviewed
deployment. A GitHub Actions workflow may run dbt, publish those files, and
trigger LeapView deployment or refresh only after dbt succeeds.

Exact build-once data promotion across environments, content-addressed external
dataset releases, signed producer attestations, and producer retention
protocols are not part of this decision. They require a separate,
producer-neutral ADR for immutable external-dataset revisions. dbt may be one
producer for that future capability, but it is not a prerequisite for dbt
compatibility.

### Reference showcase

The reference implementation demonstrates the same boundary locally and in
production:

1. A dbt project transforms representative source data into one or more marts
   and validates its own contracts and tests.
2. Locally, dbt-duckdb materializes those marts and a post-build export writes
   the selected serving inputs to Parquet.
3. LeapView reaches the output through an ordinary Connection and Source,
   validates the input boundary, and materializes the required thin Model.
4. A LeapView SemanticModel supplies readable labels, governed metrics,
   relationships, formatting, and policy behavior to a Dashboard.
5. The repository can be used without dbt metadata import by declaring the same
   resources explicitly.
6. In production, GitHub Actions runs the same dbt build against source data in
   Azure storage, publishes the complete selected mart set to a versioned Azure
   Blob or ADLS prefix, and then triggers the normal LeapView plan, refresh, and
   activation path only after publication completes.

The demonstration must show that a failed dbt build never triggers LeapView,
and that a failed LeapView contract check or Model refresh leaves the previous
serving state active.

## Consequences

Teams can adopt LeapView beside dbt without installing dbt in the LeapView
service, granting LeapView transformation credentials, or adopting a new
artifact publication protocol. The same Connection, Source, Model, and
SemanticModel architecture works with dbt, SQLMesh, custom SQL, managed data,
and warehouse-native pipelines.

The separation makes ownership legible. dbt produces tested warehouse data;
LeapView validates what it consumes and owns its serving materialization and
semantics. A monorepo can still make the workflow feel integrated without
turning repository co-location into runtime coupling.

Optional manifest import improves scaffolding, lineage, and drift diagnosis,
but requires compatibility work for supported dbt artifact versions and
adapters. Because it is not authoritative, imported metadata can disagree with
the warehouse or authored LeapView contract; tooling must expose those
differences instead of choosing silently.

The baseline does not guarantee that two LeapView environments consume
byte-identical warehouse results. Mutable warehouse relations remain owned by
the producer, and versioned object paths remain an operational producer
contract. Customers requiring exact data promotion need the future generic
immutable external-dataset capability.

Materializing a thin LeapView Model duplicates data and adds refresh latency.
The required Model boundary still provides a stable serving contract; its
definition should remain a direct-source projection or minimal adaptation when
the warehouse already provides a stable, performant mart.

## Confirmation

- The reference monorepo runs dbt-duckdb and serves a LeapView Dashboard from a
  dbt-produced mart through the ordinary Connection, Source, Model, and
  SemanticModel graph.
- One documented local command or task runs the dbt-duckdb build and Parquet
  export and starts `leapview dev` against those outputs without production
  storage configuration or manual path rewriting between iterations.
- The same LeapView source bundle can be compiled and deployed from explicitly
  authored resources without supplying a dbt manifest or running dbt inside
  LeapView.
- Production workflow tests prove GitHub Actions triggers LeapView only after a
  successful dbt build and publication to the configured warehouse relation or
  versioned Azure location.
- Consistency fixtures prove ordinary multi-Source refreshes make no unsupported
  upstream snapshot claim, while a coordinated showcase consumes only a
  completed snapshot, atomic swap, or complete immutable versioned location.
- Contract fixtures prove producer schema drift, incompatible types, invalid
  grain, and Model quality failures stop candidate activation while the prior
  serving state remains active.
- Naming fixtures prove stable `snake_case` identifiers can render with authored
  human-readable labels without adding a cosmetic physical transformation.
- If metadata import is implemented, offline fixtures compare supported manifest
  declarations with authored Source contracts without requiring a warehouse
  connection. Separate physical fixtures use target catalog access or inspect
  produced Parquet to reconcile actual column types. Both paths cover bounded
  parsing, dbt selection, deterministic identity mapping, collisions, drift
  reporting, and protection of authored LeapView semantics.
- Architecture tests prove serving and refresh paths never require dbt
  binaries, repositories, manifests, run results, or dbt credentials.
- Security tests prove Source access is read-only where supported, serving-state
  writes use a separate scope, and semantic restrictions are not represented as
  pre-ingestion enforcement.
- Metadata fixtures prove dbt Semantic Layer and MetricFlow resources do not
  silently become LeapView semantic definitions in v1.
- Project tests prove every SemanticModel dataset resolves to a Model in the
  same Project candidate and generation, including when the producing dbt
  project uses upstream packages or dbt Mesh dependencies.
