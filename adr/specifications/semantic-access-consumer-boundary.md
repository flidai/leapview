# FAI-642 discovery and consumer boundary

Status: active within the qualified supported profile; exhaustive consumer
qualification is not claimed complete. The durable boundary is described in
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
| Dashboard, semantic API, and agent/MCP | Shared query authorization and request-bound materialization; metadata is gated before serialization. |
| Suggestions and raw values | Protected paths without named qualification reject or bypass unsupported reuse; no positive qualification is claimed. |
| Explore and project catalog | Compiled-source discovery and filtered members; catalog authority comes from its exact serving lease. |
| Public/embedded dashboards | Publication identities cannot supply semantic attribute authority; protected execution is denied, while explicit public models retain existing behavior. |
| Protected byte/result reuse and bundles | Denied or bypassed at this boundary unless the FAI-645 lifecycle-qualified cache path proves exact authorization identity. |
| Scheduled work and exports | Current refresh scheduling publishes state rather than querying dashboards; dashboard YAML export is authoring, not a data export. No new scheduled-query or data-export implementation is introduced. |

## Scope exclusions and dependencies

This FAI-642 boundary added no second registry, compiler or evaluator, provider
adapter, lifecycle transition, audit subsystem, or cache implementation. Reuse
paths unable to prove protected authorization still fail closed. FAI-645 now
supplies the qualified reuse/lifecycle integration, FAI-648 records the
supported consumer profile, and FAI-649 activates that profile. VAL-11 remains
Partial.
Neutral activation verification must not fabricate a subject or bypass FAI-641;
the unqualified path remains an explicit fail-closed exclusion, not implicit
consumer authorization.

Historical implementation checkpoints are not qualification evidence for this
boundary. This implementation uses the FAI-619/636/637/639/641 foundations
already merged on main; it does not import stacked consumer or cutover work.
