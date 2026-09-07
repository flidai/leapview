# Semantic access discovery and consumers

FAI-642 connects authoritative access metadata to semantic discovery and
request execution. Resource RBAC remains necessary: semantic permission does
not grant document sharing, authoring, or access to another project.

## Discovery authority

The immutable SemanticModel carries dataset access filters and dataset/member
required grants. FAI-639 compiles their transitive relationship and metric
dependencies. Discovery uses these compiled requirements and the same typed
decision as query admission, not a second policy interpreter.

The shared query consumer returns sorted dataset, dimension-binding, and
metric records with required grants, dependency/filter datasets, ownership,
and qualified policy identity. Denied assets are omitted; direct references
receive the same decision. Missing models, unknown members, mismatched source
fingerprints, and invalid authority never become public assets.

## Request authorization

Application composition supplies the server instance, exact serving scope,
and authenticated principal. Access resolves the live principal and active
group closure with registry, control, and effective assignments in one owned
PostgreSQL repeatable-read, read-only transaction. Caller-owned transactions
are not accepted by this resolver.

The direct-assignment adapter uses existing canonical values and opaque
evidence. It does not accept browser claims or invent trusted-provider
evidence. Publication identities, development bypass, and workload admission
identities do not substitute for an authenticated semantic principal.

Each consumer pins policy/decision identity. Fresh observations must still
match it, preventing one response or plan from combining different valid
authorization snapshots. Changed, disabled, tombstoned, or malformed authority
fails closed. Discovery-only consumers cannot produce executable plans.

## Execution boundary

Request planners retain the activation-owned compiled model and consume the
existing FAI-639 predicates. [SecurityBarrier enforcement](/docs/architecture/semantic-access-planner)
remains before scans participate in joins and aggregation.

Materialization requires a bound consumer for protected queries, compares its
instance/project/environment/generation/model identity to the runtime, and
checks private admission provenance plus exact PlanIR, SQL, arguments, and
columns before execution. Arrow delivery rechecks authority before releasing
schemas and batches; buffered results are rechecked after database execution
before ownership is transferred to the caller. A copied or re-rendered
altered graph is not admission.

Dashboard queries, semantic API queries/explain, Explore, agent/MCP query
paths, and raw-value/suggestion paths converge on these shared boundaries.
Catalog and member projections are authorization-filtered. Whole-document
projections that cannot safely express partial visibility deny the entire
projection rather than expose denied members. Source/Model previews remain
separately resource-authorized authoring capabilities, never semantic bypasses.

## Deliberately unsupported paths

Protected result/immutable-byte reuse and bundles without a proven request
boundary fail closed. FAI-642 does not implement lifecycle-aware caching,
invalidation, or audit expansion; those remain FAI-645 responsibilities.

Neutral activation verification still needs its own trusted-context design;
it must not fabricate a principal or skip planner admission. Exhaustive
consumer qualification and production cutover remain FAI-648/FAI-649 work.
This boundary does not complete VAL-11, which remains Partial.
