# Project namespace conformance specification

Status: accepted

Profile: `leapview.project-namespace/v1`

Last updated: 2026-09-03

Owners: LeapView maintainers

Governing decision:
[ADR-0018](../0018-retain-project-as-the-durable-deployment-namespace.md)

Related decisions:
[ADR-0007](../0007-adopt-plan-driven-project-delivery.md),
[ADR-0008](../0008-isolate-ducklake-candidate-physical-state.md),
[ADR-0009](../0009-separate-control-and-physical-transactions.md),
[ADR-0016](../0016-adopt-standards-aligned-data-contracts-and-interchange.md),
[ADR-0017](../0017-adopt-a-looker-aligned-semantic-access-contract.md), and
[ADR-0019](../0019-integrate-dbt-at-the-warehouse-contract-boundary.md)

## Purpose

This implementation-facing specification defines how a portable source bundle
is bound to one durable Project and environment, how that identity is carried
through deployment and serving, and which multi-Project behaviors are excluded
from the first profile. The ADR owns the architectural choice.

The terms **must**, **must not**, **should**, and **may** are normative. A
requirement that has not been implemented is pending, not implicitly waived.

## Profile stability

`leapview.project-namespace/v1` is reserved while implementation is pending.
Its wire semantics become immutable when the first conforming claim or
generation is admitted by a released LeapView version. Until that freeze point,
the accepted architectural decision may be refined without claiming that an
unreleased contract is already interoperable. After the freeze point, editorial
clarification, additional evidence, and implementation organization may evolve,
but a change that alters a conforming claim, durable identity, authorization
result, or accepted/rejected input requires a new profile identifier and an
explicit migration decision.

## Mental model

| Concept       | Meaning                              | Durable evidence         |
| ------------- | ------------------------------------ | ------------------------ |
| Source bundle | Portable analytics source code       | `BundleDigest`           |
| Project       | Identity of one analytics product    | `ProjectUID`             |
| Environment   | Place that product runs              | Canonical environment ID |
| Generation    | Exact immutable release active there | `GenerationID`           |

Source says **what is defined**. Project says **which product owns it**.
Environment says **where it runs**. Generation says **which exact release is
active**.

## Scope

This profile covers:

- one durable Project and environment claim per server instance;
- destination binding of a portable source bundle;
- Project-scoped identity, authorization, audit, and deployment evidence;
- server-bound browser, query, search, agent, and release behavior;
- independent deployment of one Project to separate environment instances;
- one closed Project-local semantic graph per candidate and generation;
- explicit rejection of native cross-Project references and imports.

It does not define a multi-Project server process, a Project picker, Project
session switching, a generic Project CRUD control plane, output publications,
import locks, cross-instance sharing, or a data marketplace.

## Terms and identity

| Term            | Meaning                                                              | Durable identity                          |
| --------------- | -------------------------------------------------------------------- | ----------------------------------------- |
| Instance        | One LeapView server and admitted runtime boundary                    | `InstanceUID`                             |
| Project         | Durable namespace of one analytics product                           | `ProjectUID`                              |
| Project locator | Human-facing deployment locator                                      | Mutable alias; never a foreign key        |
| Portable bundle | Exact discovered source bytes                                        | `BundleDigest`                            |
| Environment     | Fixed deployment context such as dev, staging, or production         | Canonical environment ID                  |
| Resource ID     | Authored `metadata.id`, unique across kinds in one Project candidate | `(ProjectUID, ResourceID)`                |
| Resource UID    | Identity allocated by the bound instance on first activation         | Instance registry `ResourceUID`           |
| Generation      | One immutable active or retained Project graph                       | `(ProjectUID, Environment, GenerationID)` |

Display names, repository URLs, branches, directories, filenames, dbt project
names, and physical relation names must not replace durable identity.

## Ownership boundaries

| Concern                                                               | Owner                              |
| --------------------------------------------------------------------- | ---------------------------------- |
| Resource YAML and portable semantic meaning                           | Source repository                  |
| Project UID issuance and durable Project registry                     | Deployment authority               |
| Environment and singleton Project claim                               | Target instance                    |
| Connection endpoint and credentials                                   | Target binding                     |
| Plan, candidate, generation, and active pointer                       | Bound Project deployment lifecycle |
| Resource UID registry and tombstones                                  | Bound instance registry            |
| Process, network, credential, storage, upgrade, and failure isolation | Instance topology                  |

## Instance claim and lifecycle

- **PRJ-01:** A target database instance must have at most one durable claim
  containing canonical `ProjectUID` and environment. A serving instance must
  have exactly one such claim.
- **PRJ-02:** A deployment authority must mint one opaque Project UID once, or
  receive it from an external canonical Project registry, before contacting a
  target. A target must not mint a Project UID as a deployment side effect.
- **PRJ-03:** An unclaimed target exposes one bootstrap operation authorized by
  an instance-administrator capability that does not depend on Project-scoped
  grants. Bootstrap requires the issuer-supplied Project UID and canonical
  environment and creates the singleton claim atomically.
- **PRJ-04:** Bootstrap records issuer identity, authenticated principal,
  Project UID, environment, target identity, time, and outcome in durable audit
  evidence. Repeating the identical tuple is idempotent. Any different Project
  UID or environment conflicts before repository access, planning, or candidate
  work.
- **PRJ-05:** After a claim exists, the bootstrap endpoint must not rename,
  retarget, or replace it. Reprovisioning or recovery is a separate
  authenticated, authorized, and audited operation.
- **PRJ-06:** One Project may be deployed to several distinct instances, for
  example separate dev, staging, and production instances. The deployment
  authority supplies the same exact Project UID to each bootstrap; every
  instance has its own bindings, plans, generations, approvals, and resource
  UID registry.
- **PRJ-07:** Each bound `(ProjectUID, environment)` has at most one active
  generation. Activation must not alter another instance's active pointer.
- **PRJ-08:** The initial local and single-repository experience may mint one
  default Project UID in durable local deployment state and bootstrap its
  target without authored Project YAML or a Project-selection step. Recreating
  local target infrastructure reuses that UID rather than minting a new Project.
- **PRJ-09:** Retaining Project identity does not require server-local list,
  create, rename, archive, or selection APIs for several Projects.

## Authoring, compilation, and deployment binding

- **BND-01:** Portable analytics source accepts exactly Connection, Source,
  Model, SemanticModel, Pipeline, and Dashboard as top-level kinds. It must
  reject `kind: Project` with migration guidance.
- **BND-02:** Discovery uses conventional resource directories beneath the
  selected root and must not require a LeapView root manifest, Project UID,
  environment, or target endpoint.
- **BND-03:** Portable validation may produce an unbound graph and
  `BundleDigest`. It must not allocate resource UIDs or infer destination
  identity from a repository name, path, remote, or dbt project name.
- **BND-04:** Before candidate construction, planning binds the target,
  canonical Project UID, environment, exact bundle digest, active base
  generation, target revision, and resolved target inputs.
- **BND-05:** A compiled graph must not contain a Project resource node. Every
  authored resource edge must close within the candidate graph.
- **BND-06:** Plan, candidate, generation, serving, query, cache, lineage,
  audit, managed-data, and physical-root evidence must carry Project UID where
  omission could permit collision, retargeting, leakage, or incorrect garbage
  collection.
- **BND-07:** Promotion replans the same portable bundle against a different
  target. It must not copy target credentials, approvals, target revisions,
  resource UIDs, or mutable active pointers from another environment.

## Resource identity

- **RID-01:** A candidate contains at most one resource with a given
  `metadata.id` across all six authored kinds. The uniqueness boundary is the
  bound Project candidate.
- **RID-02:** First activation binds `(ProjectUID, metadata.id)` and authored
  kind to an immutable resource UID in that instance's registry.
- **RID-03:** The same authored ID may occur in unrelated Projects. Separate
  instances allocate independent resource UIDs.
- **RID-04:** A resource UID is not portable across instances. Cross-instance
  evidence must qualify the instance, Project UID, authored ID, expected kind,
  contract digest, and generation as appropriate.
- **RID-05:** A later candidate on the same instance with the same Project UID,
  authored ID, and kind updates the same resource UID. A kind change is
  rejected.
- **RID-06:** Removal, tombstones, restore, and rollback follow ADR-0016 within
  the bound instance and Project. They must not cross an instance or Project
  boundary.
- **RID-07:** Paths, display names, repository coordinates, Project locators,
  dbt names, and physical relation names must not be durable foreign keys.

## Public surfaces and authorization

- **API-01:** Project remains public identity in deployment, authorization,
  audit, lineage, catalog, and generation evidence.
- **API-02:** Browser, query, search, agent, and release requests use the
  server-bound Project and must not accept a client-supplied selector that can
  switch Project context.
- **API-03:** A deployment route containing `{project}` must resolve it to the
  already bound canonical Project UID before repository access or mutation. An
  unknown or different Project is a conflict, not a request to switch context.
  This rule applies after bootstrap; the narrow bootstrap endpoint accepts the
  issuer-supplied UID only while the target is unclaimed and uses
  instance-administrator authorization.
- **API-04:** Every request independently verifies the principal's capability
  against the explicit bound Project UID. A valid session, locator, or UID is
  not authorization.
- **API-05:** List, search, discovery, autocomplete, audit, lineage, and error
  surfaces expose only the bound Project and authorization-filtered resources.
- **API-06:** Cache and idempotency keys include Project UID plus every other
  result-affecting authorization, environment, and generation identity.
- **API-07:** Runtime discovery must fail closed if persisted active state
  spans more than the one claimed Project or environment.

## Environment and generation behavior

- **ENV-01:** Project identity is stable across environments; environment is
  target-owned and must not be inferred from a branch name.
- **ENV-02:** A generation belongs to exactly one Project UID and environment
  on one instance and is immutable after publication.
- **ENV-03:** Activation changes only the active pointer for the bound Project
  and environment after all plan, candidate, qualification, and transaction
  checks pass.
- **ENV-04:** Rollback selects a retained generation from the same bound
  Project and environment. Cross-environment rollback requires a new plan and
  deployment to that target.
- **ENV-05:** A source commit or bundle digest is useful provenance but does
  not by itself identify an active generation.

## Semantic composition

- **SEM-01:** Every SemanticModel dataset reference must resolve to a Model in
  the same Project candidate. A foreign Project qualifier is invalid.
- **SEM-02:** Every Model used by one SemanticModel must belong to the same
  immutable generation selected for query planning. Runtime resolution must not
  consult another Project's active pointer or catalog.
- **SEM-03:** A Model consuming a dbt-produced Source is Project-local and
  follows the same semantic reference, authorization, lineage, cache, and
  generation rules as every other authored Model. Optional dbt scaffolding does
  not create a separate runtime resource kind.
- **SEM-04:** Cross-dbt-project dependencies must be resolved and materialized
  by the producing consumer dbt project before ADR-0019's warehouse contract
  boundary. LeapView does not reproduce dbt Mesh resolution while compiling or
  serving a SemanticModel.
- **SEM-05:** Hosting several Projects in a future control-plane process must
  not weaken SEM-01 through SEM-04. Multi-Project hosting and cross-Project
  semantic references are independent capabilities.

## Isolation boundary

- **ISO-01:** Project is the logical product and authorization namespace. The
  server instance is the process, network, credential, storage-administration,
  upgrade, and failure boundary.
- **ISO-02:** Separate Projects or hard environments use separate instances in
  this profile.
- **ISO-03:** Documentation and product claims must not imply same-process
  multi-Project isolation, because this profile does not admit several active
  Projects in one runtime.

## Cross-Project boundary

- **XPR-01:** Authored resource references must not carry a foreign Project
  qualifier. The compiler rejects any resource edge that does not resolve in
  the bound candidate.
- **XPR-02:** The Source union must not add `projectOutput`, native LeapView
  publication, import-lock, or same-process zero-copy variants under this
  profile.
- **XPR-03:** Teams may share data through an ordinary external Source with an
  explicit data identity or through reusable source packages resolved into one
  closed bundle before compilation. The producer may be dbt or any other
  transformation system.
- **XPR-04:** Those mechanisms must not create a live runtime dependency on
  another LeapView Project or make another Project's catalog the serving
  authority.
- **XPR-05:** A future native import requires a separate ADR defining contract
  version versus dataset release, authorization, revocation, retention,
  lineage, cycle behavior, integrity, ingress to a private candidate, and
  failure semantics.

## dbt mapping

- **DBT-01:** One ordinary dbt repository normally maps to one LeapView
  Project for the reference adoption path.
- **DBT-02:** The dbt project name, repository, commit, target, manifest, and
  invocation identifiers are producer provenance. They do not replace the
  issuer-assigned Project UID durably claimed by the target, environment, or
  generation identity.
- **DBT-03:** LeapView SemanticModels and Dashboards may live beside the dbt
  project in conventional directories and deploy without `kind: Project` or a
  Project manifest.
- **DBT-04:** Environment-specific dbt relations or versioned object locations
  are selected through explicit target Connection bindings and Source
  configuration under ADR-0019, not by mutating LeapView Project identity.
- **DBT-05:** The producer dbt project may depend on upstream dbt projects
  through packages or dbt Mesh. It materializes the consumer-owned warehouse
  outputs before LeapView refresh; upstream projects do not become LeapView
  Project references.
- **DBT-06:** A LeapView Project may consume several ordinary Sources produced
  by independently operated systems when they all resolve into the same closed
  candidate. Producer or repository cardinality does not create live
  cross-Project semantic references.

## Failure behavior

| Condition                                                      | Required result                                                                          |
| -------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| `kind: Project` appears in source                              | Authoring rejection with migration guidance                                              |
| Ordinary deployment targets an unclaimed instance              | Bootstrap-required failure before repository access                                      |
| Bootstrap lacks instance-administrator authorization           | Authorization failure without creating a claim                                           |
| Separate environments receive independently minted UIDs        | Treat as distinct Projects; block promotion and require reconciliation or reprovisioning |
| Requested Project or environment differs from the target claim | Conflict before repository access, planning, or candidate work                           |
| Persisted active state spans several Projects or environments  | Runtime admission failure                                                                |
| Same authored ID appears twice in one candidate                | Candidate rejection before graph construction                                            |
| Same authored ID exists in an unrelated Project                | Allowed; identity remains instance- and Project-qualified                                |
| Request supplies a different Project selector                  | Reject; never switch context                                                             |
| SemanticModel dataset names a Model in another Project         | Compile failure without foreign metadata disclosure                                      |
| Resource reference names another Project                       | Compile failure without foreign metadata disclosure                                      |
| Source uses `projectOutput` or another native Project import   | Schema or compile rejection                                                              |
| Optional dbt metadata is absent                                | Continue through explicitly authored Connection, Source, and Model resources             |
| Rollback names another environment or Project                  | Reject and require deployment to the intended target                                     |

## Evidence and conformance gates

| Requirement range | Required maintained evidence                                                                          | Status  |
| ----------------- | ----------------------------------------------------------------------------------------------------- | ------- |
| PRJ-01–PRJ-09     | UID issuance, pre-Project authorization, singleton claim, audit, idempotency, and environment tests   | Pending |
| BND-01–BND-07     | Generated authoring schema, discovery, unbound compile, binding, promotion, and graph tests           | Pending |
| RID-01–RID-07     | Uniqueness, instance registry, tombstone, restore, rollback, and projection fixtures                  | Pending |
| API-01–API-07     | Generated contracts, selector rejection, authorization, filtering, cache, and runtime-admission tests | Pending |
| ENV-01–ENV-05     | Activation, promotion, provenance, and same-environment rollback tests                                | Pending |
| SEM-01–SEM-05     | Project-local dataset resolution, generation closure, dbt lowering, and topology tests                | Pending |
| ISO-01–ISO-03     | Deployment topology and isolation-claim review                                                        | Pending |
| XPR-01–XPR-05     | Foreign-reference and unsupported-import rejection corpus                                             | Pending |
| DBT-01–DBT-06     | Reference deployment, upstream dependency, multi-Source closure, and cross-Project rejection tests    | Pending |

Implementation must update the project-delivery and data-contract versioning
conformance specifications where their current language conflicts with this
accepted profile. The final combined implementation change must pass:

```sh
task generate
task generated:check
task ci
```
