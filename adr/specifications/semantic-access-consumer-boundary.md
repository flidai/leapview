# FAI-642 discovery and consumer boundary

Status: implementation under validation; publication and exhaustive consumer
qualification are not claimed complete. The durable boundary is described in
[semantic access consumers](../../docs/articles/architecture/semantic-access-consumers.md).

## Authority and responsibility

- FAI-619's immutable SemanticModel is the authored authority. Protection is
  derived from dataset filters and required grants, member requirements, and
  their compiled dataset/relationship/metric dependencies. A missing model,
  unknown member or inconsistent compiled snapshot is not a public asset.
- FAI-637 owns definitions, assignments, trusted mappings and principal/group
  resolution. Consumer composition must obtain coherent registry/control and
  effective-value evidence from that authority, never browser-supplied values.
- FAI-639 remains the only compiler/evaluator. Discovery projects its typed
  requirements and decisions in sorted dataset/member/binding order; it does
  not infer access by probing data or evaluate a second policy language.
- FAI-642 binds authenticated principal, target instance, serving generation
  and model identity to discovery and request planning. Direct references
  receive the same authorization as listed assets. Missing or stale authority
  fails closed, including paths that have only document-sharing authority.
- FAI-641 retains scan enforcement. Protected consumer execution must use its
  admitted planner and validate the resulting PlanIR/renderer envelope before
  database execution. Predicates remain before joins and aggregation.

## Consumer inventory

The shared boundaries cover dashboard query authorization, planning for the
semantic interface, Explore/catalog projection, and buffered/Arrow execution
in materialize. Dashboard, agent/MCP, export, suggestion, raw-value and background
paths must converge there or reject protected operations explicitly. Static
metadata/visual-spec projections need authorization before serialization, not
only before running a query. Source/Model authoring preview remains a distinct
resource-authorized capability and must not substitute for protected semantic
execution.

| Current path | FAI-642 boundary |
| --- | --- |
| Dashboard, semantic API, agent/MCP, suggestions and raw values | Shared query authorization and request-bound materialization; metadata is gated before serialization. |
| Explore and project catalog | Compiled-source discovery and filtered members; catalog authority comes from its exact serving lease. |
| Public/embedded dashboards | Publication identities cannot supply semantic attribute authority; protected execution is denied, while explicit public models retain existing behavior. |
| Protected byte/result reuse and bundles | Denied or bypassed until FAI-645 supplies lifecycle-qualified evidence; no invalidation implementation is added here. |
| Scheduled work and exports | Current refresh scheduling publishes state rather than querying dashboards; dashboard YAML export is authoring, not a data export. No new scheduled-query or data-export implementation is introduced. |

## Scope exclusions and dependencies

No second registry, compiler or evaluator; no provider adapter, lifecycle
transition, audit expansion, cache invalidation, production activation or
cutover. Reuse paths unable to prove protected authorization must fail closed;
FAI-645 owns policy-aware reuse/lifecycle integration. FAI-648 owns exhaustive
cross-consumer qualification and FAI-649 owns cutover. VAL-11 remains Partial.
Neutral activation verification must not fabricate a subject or bypass FAI-641;
its remaining design is a dependency, not implicit consumer authorization.

Historical implementation checkpoints are not qualification evidence for this
boundary. This implementation uses the FAI-619/636/637/639/641 foundations
already merged on main; it does not import stacked consumer or cutover work.
