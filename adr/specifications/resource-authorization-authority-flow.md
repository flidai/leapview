# Resource authorization authority flow

Status: implemented for the qualified private/native profile; the qualification
ledger below remains authoritative for unsupported and separately governed work.

Governing decisions: [ADR-0025](../0025-adopt-typed-resource-permissions-and-scoped-api-credentials.md)
and [ADR-0026](../0026-preserve-authority-across-governed-operations.md).
Action names below are intended catalog vocabulary, not a claim of a shipped
wire schema. Unsupported operations remain unavailable until qualified.

## Existing authorities and non-goals

Extend, do not replace:

- [Semantic consumer admission](semantic-access-consumer-boundary.md):
  server-resolved identity, metadata gating, and governed planner execution.
- [Cache, lifecycle, and audit](semantic-access-cache-lifecycle.md): coherent
  registry/control revisions, detached effective-policy evidence, current
  cache-hit/insertion/delivery checks, and fail-closed required audit.
- [Activation and cutover](semantic-access-activation-cutover.md): exact
  publication evidence, transactional activation fences, rollback review, and
  replay of committed activation outcomes.
- [Supported semantic profile](semantic-access-supported-profile.md): the
  current qualification boundary; this spec does not broaden its support claims.
- [Project delivery conformance](project-delivery-conformance.md): immutable
  plans, independent release acts, target binding, and physical-state lifecycle.

Access remains the security-state owner. No duplicate attribute registry,
global replacement lifecycle store, general policy language, or external policy
service is introduced. Reuse existing transaction/revision mechanisms and add
missing evidence there. Repository paths and dependency edges confer no grants.

## Shared operation contract

Every protected operation resolves a server-owned context containing actor,
effective principal, credential identity and validity, instance/Project/target,
exact typed resources, catalog profile, content/execution revision, required
action/resource pairs, and relevant current security evidence.

Evidence includes effective grant/group state, credential restrictions and
lifecycle, any delegation, and ADR-0017 registry/control, effective-attribute,
publication/policy, and enforcement-profile identities. Missing evidence denies;
unrelated analytics generation changes do not stand in for security revisions.
The result is a decision with enforceable obligations and bounded audit evidence,
not a reusable allow boolean detached from its context.

| Boundary | Required behavior |
| --- | --- |
| Single resource | Resolve exact kind/identity; intersect principal authority, credential pairs/audience, and applicable policies. |
| Compound operation | Resolve all pairs against one coherent security observation; no union of independently stale allow results. Revalidate at the relevant commit/output boundary. |
| Lists and search | Filter before returning visible items, counts, facets, autocomplete, or dependency previews. Page over authorized results; never expose an unfiltered total. Bind opaque cursors to query/context and reauthorize each page. |
| Semantic execution | Return a bound semantic context with row/member restrictions and admitted-plan provenance; an action allow cannot replace planner enforcement. |
| Sensitive mutation | Couple current authorization, expected resource/security versions, mutation, and required audit at commit, using transactions/locks or an equivalent proven fence. |
| Explanation | Use stable reason categories and authorized evidence only. Public errors must not distinguish a hidden object from a nonexistent one where that would leak its existence. |

Qualified Browser, REST, CLI-through-REST, Agent, MCP, worker, and cache adapters
call this same module. Generated Agent operations preserve action/resolver
metadata; supported custom tools and catalog/search preserve the typed
credential ceiling. Operations without an exact typed mapping, including custom
authoring, reject typed credentials and remain outside the qualified profile.
Presentation may summarize authorization results
but cannot grant authority. Sensitive logs and diagnostic endpoints are also
consumers. Internal audits may record rejected identities that the requester is
not allowed to discover.

## Dashboard consumption and previews

| Operation | Explicit requirements |
| --- | --- |
| Published dashboard definition/shell | `dashboard.read`; redact unauthorized semantic metadata and secrets. This check alone cannot deliver data. |
| Published dashboard data/interaction | `dashboard.read` plus `semantic.consume` for every participating SemanticModel, current publication eligibility, and ADR-0017 row/member policy. |
| Standalone semantic discovery | `semantic.read` and member-discovery restrictions; metadata authority grants no data access. |
| Arbitrary governed semantic query | `semantic.query` plus `semantic.consume` on every participating model, with semantic policy. Discovery used to construct the query separately requires `semantic.read`. |
| Existing dashboard draft data preview | `dashboard.update` plus `semantic.query` and `semantic.consume` on selected models, with semantic policy. Unsaved creation preview uses `dashboard.create` on the Project in place of update. |

The server selects an eligible immutable published dashboard revision and
resolves its executable query closure. It validates interaction identifiers,
filter fields/operators/values, parameter types and bounds, drill destinations,
and participating models against that revision. The client cannot supply SQL,
additional projections, joins, or arbitrary query ASTs merely by naming an
accessible dashboard. Legitimate filters may change rows only within the
independently enforced row policy; allowed drills remain governed queries.

Adding a model to a dashboard does not grant the Viewer that model. Every data
request checks its actual participating set. Partial rendering may return
authorized tiles with non-disclosing unavailable states, never unauthorized
rows, member labels, or cached values. A multi-model query checks all inputs.

Dashboard interactions may expose only their approved, policy-filtered metadata
without granting standalone `semantic.read`. Loading another model's discovery
surface is a separate action. Download/export must declare its own supported
operation coverage and output limits; read is not permission for arbitrary bulk
extraction. Public/embed identities do not silently become the publisher.

Viewer presets explicitly select dashboard reads and model consumption scopes;
they do not infer grants along dependency edges. Explorer presets visibly add
query and discovery. Draft editing without query authority remains possible,
but data preview is denied. No workflow action overrides row/member policy.

## Asynchronous execution and delegation

Admission records an immutable operation envelope: actor, authority mode,
execution principal, exact Project/target, operation and action/resource pairs,
executable dependency digest, connection/binding identities, constrained
parameters, permitted destinations, output classification, approval identity,
expiry, and idempotency identity. Secret values and raw sensitive parameters
must not appear in audit; store protected execution inputs separately.

Caller-authority mode:

- Keep a reference/fingerprint to the initiating credential, not its bearer
  secret in a queue payload. Validate its current status, expiry, restrictions,
  and principal grants at admission, dequeue, each protected task/dispatch,
  and output boundaries.
- Expired/revoked credentials or removed authority stop new protected actions.
  A queued job does not acquire durable authority merely because admission
  succeeded. Resumption under a replacement credential is a new authorization
  decision, not substitution by the worker.

Delegated workload mode:

- An explicit execution grant names a service principal, exact workflow and
  revision closure, action/resource ceiling, target, inputs/destinations, allowed
  triggers, expiry, and revocation identity. It is independently durable after
  issuance; initiator token expiry or departure does not itself revoke it.
- Issuance requires the intended `workload.delegate` action, scoped to the exact
  execution principal and workflow, plus an explicit delegation envelope on
  both principal and credential covering all delegated authority. Merely having
  `pipeline.update`, `pipeline.run`, or `project.access.manage` is insufficient.
- Human/API triggers require current trigger authority at admission and
  dispatch, including an active actor, live credential, and `pipeline.run` (or
  the corresponding release action). After dispatch, the execution grant—not
  that trigger credential—governs continuation. Scheduled triggers instead use
  the explicitly configured scheduler principal and approved schedule grant;
  they cannot fall back to a departed user's identity.
- At every protected execution boundary, effective authority is the workload
  principal's current grants intersected with the execution grant and current
  applicable policies. A growing service-principal role cannot exceed the
  sealed grant. Revoking the grant or disabling the principal stops further
  protected execution even if transport credentials still work.

Changes to effective executable dependencies, connection/binding authority,
execution principal, target, or output destinations invalidate approval for the
changed revision. Parameter values within an explicitly approved constraint do
not require new approval. Secret rotation with unchanged binding identity and
authority need not alter executable content approval, but current credential
validity still applies. Binding changes must not be disguised as rotation.

Existing schedules remain pinned to the approved revision or stop; they never
follow an editor's unapproved latest revision. The resolved executable closure
includes referenced SQL/models and other executable dependencies, preventing
edits one level below the pipeline from bypassing approval. Execution adapters
must enforce connection and destination constraints, including code-level
egress; if they cannot, that privileged execution profile remains unsupported.

Run status may disclose bounded operational metadata. Reading data-bearing
results/logs requires separate current consumer authorization. For governed
analytical results, use the retrieving principal's semantic policy and result
identity; a broader service-principal result cannot be returned just because the
requester could trigger it. If safe reuse cannot be proven, re-execute under the
consumer's policy or deny. Export delivery to a noninteractive destination must
be explicitly approved for that audience; workload delegation alone is not
approval to declassify data or bypass the governed export contract.

## Grant administration and sharing lifecycle

For ordinary sharing, require `resource.share`, supported delegable actions,
exact resource/audience, and possession of every granted action within both
principal and credential authority. Do not confer administration, onward
sharing, or additional semantic data entitlement through a dashboard share.

Persist an independent grant with issuer/credential evidence, recipient, exact
action/resource pairs, catalog version, issuance time, optional expiry, and
revocation identity. Losing issuer authority later does not cascade-revoke it.
The UI must say so; support auditing and explicitly revoking grants by issuer.
Current recipient status, policy, resource lifecycle, and credential restrictions
still apply. Deleting and recreating a resource must not resurrect grants for its
previous identity.

Administrative issuance outside ordinary sharing requires
`project.access.manage` and `project.access.delegate`, plus the intersection of
the principal's and credential's explicit grant-administration envelopes. Those
envelopes bound action/resource pairs, role versions, recipient selectors,
targets, expiry limits, and whether any onward delegation may be issued.
Delegation of delegation is denied by default and cannot exceed the parent's
envelope or lifetime. Maintenance-only credentials can revoke authorized grants
but cannot issue authority from an absent envelope.

An intentionally unrestricted Project administrator envelope is visibly
high-trust and may enable indirect data access through controlled principals;
it is not a harmless metadata scope or a direct row-policy bypass. It does not
confer platform administration. Bootstrapping such envelopes is a separately
audited trusted administrative path, never a fallback for a restricted token.

Evaluate practical authority changes, not only the object being edited:

- Role creation/updates and binding check the expanded, versioned permission
  set and recipient scope. A binding cannot hide broader permissions in a role.
- Group membership changes check the permissions and delegation envelopes made
  effective through the resulting membership closure. Unknown closure denies.
- Creating a service principal grants no privileges. Giving it roles, creating
  its credentials, or assigning it to execution requires the corresponding
  bounded delegation; credential issuance itself cannot escape the issuer's
  authorized issuance envelope.
- Trusted attribute values/mappings and identity-provider configuration retain
  their separately scoped administrative checks. An access-management token
  does not implicitly authorize those operations. Such administration is
  high-trust because it may change effective data access.
- Authored policy changes follow ADR-0017's
  [security-impact invariant](../0017-adopt-a-looker-aligned-semantic-access-contract.md#immediate-invalidation-identity):
  widening requires security approval, indeterminate classification blocks,
  and semantic versioning alone is never approval. A generic edit scope cannot
  substitute for this check.

Check the resulting authority and audit in the same commit fence as the
mutation. Concurrent role, group, credential, or envelope changes cannot turn a
previously bounded grant into an unchecked escalation.

## Security consistency and revocation boundaries

Security changes commit through Access-owned versioned state and durable audit.
Coherent decisions include all relevant revisions/digests; a deployment pointer
is not the security clock. Do not copy mutable grants/attributes into a serving
generation and then treat that copy as current authority.

| Boundary | Effect of a committed security change |
| --- | --- |
| New request/admission | Observe current relevant state, including changes committed before admission; stale local snapshots or TTL-based allows cannot authorize. |
| Mutation/cutover | Serialize the final authorization fence with relevant security writes; if revocation wins the commit order, reject the mutation. A check followed by an unguarded write is insufficient. |
| Queued work/task start | Revalidate the applicable caller or delegated mode before starting each protected unit. Admission evidence alone is not permission to run. |
| Cache lookup/insertion/coalesced delivery | Require fresh matching security and admitted-policy evidence for each recipient. Changed attributes, grants, credentials, eligibility, or policy prevent reuse and stale in-flight repopulation. |
| Result retrieval/stream output | Authorize retrieval and every bounded output batch at its server-side release boundary. A batch already authorized and handed to transport may finish; stop subsequent releases after revocation. |
| External side-effect dispatch | Fence a bounded dispatch authorization with current security state and record its identity. Already authorized/dispatched irreversible effects may complete; retries reconcile idempotently and new effects need fresh authority. |

Implementation must specify bounded batch/dispatch units and demonstrate their
race behavior. An entire unbounded stream cannot be one preauthorized batch.
No guarantee is made to retract data already released to transport or cancel an
external effect already dispatched. Security-state read failure denies new
protected work/output; availability is not permission to serve stale data.

Retained generations must pass current security eligibility in addition to
historical publication validation. A mandatory policy tightening records a
current eligibility restriction for affected semantic identities/targets as part
of its security transition; old leases and rollback cannot evade that fence.
The initial safe behavior is to deny revisions whose restrictions cannot be
proven compatible. Do not invent arbitrary policy intersections or inject new
compiled policy into old graphs. Emergency blocking may deny a model outright
until an eligible revision is available, without adding a policy language.
Lifting such a restriction is itself a security-widening administrative action
requiring explicit approval; rollback permission alone cannot lift it.

Rollback preserves current grants, groups, attributes, credentials, delegations,
and eligibility. It validates retained publication evidence and current rollback
approval, then repeats the current security/cutover fence. Replaying the outcome
of an already committed activation for internal reconciliation remains allowed
under the existing activation spec; it does not authorize new user-visible data
delivery or a new external effect.

## Creation, catalog evolution, and delivery scope

Creation checks the typed create action on the bound Project. It grants neither
ownership authority nor read/update/share/publish. Return only the new identity
and bounded status receipt; subsequent reads need their own permission. A
preexisting explicitly future-inclusive Project scope may already cover the new
object, but a create-only or exact-resource token does not expand. Provisioning
follow-on grants is a separate explicitly authorized operation, not an implicit
creator exception.

Pin credential action semantics and assignment role expansions to supported
catalog profiles. Define prerequisites as checks for separately held permissions.
UI presets may offer their explicit expansion, with visible consent, but runtime
authorization must not silently insert grants. Removing a prerequisite or
broadening an existing action can widen authority even without a new action name;
it needs the same reviewed migration as a role expansion. Unsupported profiles
deny or require migration, never alias silently to the latest definition.

Store consent and evaluate action/resource pairs without a Cartesian-product
expansion. Future-resource selection, delegation envelopes, recipient selectors,
and role version changes must retain their declared scope through migration.

Delivery distinguishes the exact authored change set from read dependencies and
the work actually executed. Creates/updates/deletes require their kind-specific
actions; publication and release transitions require their independent actions
and target approvals. Dependencies require read/use/run authority appropriate
to their actual use and execution identity, not update authority merely because
they are graph-reachable. Rebuilding a dependent resource is execution work, not
necessarily an authored edit. Plans declare these categories and required pairs;
execution must not discover an unchecked extra dependency after approval.

## Qualification sequence and acceptance evidence

The status column records the 2026-09-17 implementation milestone. `Implemented`
means the stated narrow case has executable evidence; `Partial` means only the
listed slice exists. It does not waive the remaining clauses in that row.

1. Correct misleading token selection and platform scope leakage as isolated
   security work; establish enough catalog coverage for the first slice.
2. Qualify a restricted-token dashboard Viewer through server interaction
   validation, semantic policy, cache reuse, and live revocation.
3. Qualify a privileged pipeline with an approved revision closure, explicit
   workload identity, bounded destinations, and expiry/revocation behavior.
4. Expand operation/catalog coverage and complete the versioned migration only
   after these cross-boundary invariants have executable evidence.

Public and embedded dashboard consumers are a separately governed surface. They
remain outside the qualified private typed-permission profile until dedicated
public/embed consumer, stream, cache, and output evidence exists. This
qualification boundary records evidence scope only; it does not change public
behavior, publication controls, or their existing fail-closed safeguards.

| ID | Status | Required negative and lifecycle evidence |
| --- | --- | --- |
| AF-01 | Implemented for typed private dashboard/API paths | Dashboard read without consume denies data; consume without query cannot submit arbitrary queries; forged interaction/query payloads fail. Public/embed remains outside the qualified typed slice. |
| AF-02 | Implemented for private dashboard authoring paths | Generated authoring operations distinguish Project-scoped create/fork from exact-dashboard read/update, body-dependent edit/publish/archive actions use coherent typed principal/group authority plus credential attenuation, dependency changes require semantic metadata authority, and draft data preview requires update plus query/consume. Public/embed and non-dashboard authoring surfaces remain outside this slice. |
| AF-03 | Implemented for protected materialization paths | Real PostgreSQL tests commit an attribute restriction during cache reuse and a coalesced flight, reject stale insertion/delivery, and stop the next bounded Arrow output batch. Other result/export transports remain outside this slice. |
| AF-04 | Implemented for native Pipeline refresh | Caller token/session and delegated-grant evidence is checked at worker admission and refresh prepare, execute, publish, and output boundaries. Native scheduling selects exactly one live grant for the instance, Project, and Pipeline UID; the grant supplies the execution principal and there is no scheduler-identity fallback. Missing selection/revalidation, ambiguity, expiry, revocation, or evidence drift fails closed. Other job kinds remain unmigrated. |
| AF-05 | Implemented for native Pipeline refresh | Queue admission binds parameter, binding, run-as principal, environment, destination, trigger, workflow revision, and complete closure evidence to the canonical generation-bound Pipeline plan. Every protected boundary rebuilds the plan from the active artifact and exact-matches the evidence; missing evidence and editor/source/artifact drift deny. |
| AF-06 | Partial | Current execution-grant lookup, workload-principal lifecycle, permission-ceiling intersection, expiry/revocation, fingerprint, exact target, and closure evidence have negative tests. Separate result/log/export consumer authorization remains pending. |
| AF-07 | Implemented for project role binding and bounded grant administration | Personal-token and durable-grant issuance prevent request-supplied issuer/ceiling authority, require coherent principal/group plus credential evidence, and deny onward delegation. Public administration-envelope issue/revoke is project-bound; role binding creation locks and exact-matches the envelope's actor, recipient, role version, target, permission expansion, and lifetime. Platform group, service-principal, credential, and trusted-attribute mutations remain distinct high-trust instance operations with typed platform actions, not project-envelope fallbacks. |
| AF-08 | Implemented for durable-share issuance/revocation lifecycle | PostgreSQL and HTTP tests prove exact resource UID binding, issuer-independent lifetime, recipient/group lifecycle, expiry, explicit audited revocation, idempotency, server-derived issuer/ceiling, and no onward delegation. Downstream use of a durable share as an authorization source remains unsupported. |
| AF-09 | Implemented for the current registry/control fence | Real PostgreSQL activation/rollback tests serialize current semantic registry revision changes, deny stale or unavailable eligibility evidence, produce no stale rollback activation evidence, and replay an already committed activation outcome safely. A future per-semantic-identity retained-eligibility restriction remains separate work. |
| AF-10 | Partial | Real PostgreSQL AF-03 and AF-09 races establish cache/coalesced/output-batch and activation/rollback ordering, including fail-closed unavailable evidence. External dispatch and the remaining mutation/output authorities still need real concurrency fixtures. |
| AF-11 | Implemented for the typed catalog/profile lifecycle | Catalog profiles, explicit prerequisites, exact/future targets, typed-token immutability, immutable PostgreSQL assignments, exact role-binding deletion, and bounded create receipts prevent silent widening. Ambiguous historical assignments remain read-only legacy evidence and fail closed on typed routes; obsolete public generic-grant mutations are removed. |
| AF-12 | Partial; qualified project catalog and bounded Agent/MCP projection | Shared project discovery filters before items, authorized totals and derived projections, signs context-bound cursors, and reauthorizes continuation pages. Agent chat, catalog, get, search, and resource resolution retain exact typed credential ceilings and never return unfiltered counts. Credential-bound Agent pagination plus unlisted facets, autocomplete, semantic-member discovery, dependency previews, and denial explanations remain unsupported until separately mapped. |
| AF-13 | Partial; native planning/build qualified | Permission storage/evaluation and durable grant ceilings preserve pairs without Cartesian expansion. Production native planning evaluates authored changes, the complete candidate dependency closure, connection bindings, and delivery transitions under one coherent snapshot; it persists the exact projection and build reauthorizes it against the current candidate snapshot. Publication/rollback still validate persisted evidence structurally without an independent compound-resolver call. |

### Executable evidence ledger

The following tests are the evidence for the status changes above. A row not
listed here retains only its previously documented evidence.

| IDs | Executable evidence |
| --- | --- |
| AF-02 | `internal/dashboard/api/operation_contract_test.go`, `internal/dashboard/queryauthz/semantic_consumption_test.go`, `internal/dashboard/authoring/accessadapter/adapter_test.go`, `internal/dashboard/module/routes_test.go`, `internal/agent/tools/authoring_contract_test.go`, and `internal/app/project_authorization_typed_test.go` |
| AF-03, AF-10 | `internal/analytics/materialize/adr0026_qualification_test.go` uses the real PostgreSQL registry/control authority at cache, coalesced-flight, and bounded Arrow-batch release boundaries. |
| AF-04, AF-05, AF-06 | `internal/refresh/module/authority_capture_test.go`, `internal/refresh/run/service_test.go`, `internal/app/job_authority_test.go`, `internal/app/postgres_refresh_scheduler_test.go`, `internal/access/postgres/durable_grants_test.go`, and `internal/platform/jobs/module/module_test.go` cover unique scheduled-grant selection, canonical closure drift, current grant/principal revalidation, and prepare/execute/publish/output fences. |
| AF-07, AF-08, AF-11 | `internal/access/http/durable_grant_handler_test.go`, `internal/access/http/role_binding_handler_test.go`, `internal/access/durable_grant_service_test.go`, `internal/access/durable_grants_test.go`, `internal/access/postgres/durable_grants_test.go`, and `internal/project/api/contracts_test.go` cover trusted issuance, envelope consumption, attenuation, resource UIDs, bounded receipts, lifecycle, audit, replay, and revocation. |
| AF-09, AF-10 | `internal/deployment/module/native_coordinator_af09_test.go` exercises PostgreSQL activation/rollback concurrency, stale evidence, unavailable authority, and committed replay. |
| AF-12 | `internal/access/discovery_test.go`, `internal/project/catalog/catalog_test.go`, `internal/agent/module/catalog_scope_test.go`, `internal/agent/module/apigen_typed_scope_test.go`, and `internal/agent/http/credential_scope_test.go` cover filtered totals/projections, continuation reauthorization, and Agent/MCP credential attenuation. |
| AF-13 | `internal/deployment/compound_authority_test.go`, `internal/app/deploymentpostgres/native_create_plan_postgres_test.go`, `internal/release/module/native_candidate_artifacts_test.go`, `internal/access/typed_operation_test.go`, and `internal/access/module/resource_authorization_typed_test.go` cover coherent compound evidence, lifecycle replay/tamper rejection, production wiring, and exact pair preservation. |

Record endpoint coverage, concurrency fixtures, supported-profile limits, and
migration evidence with implementation. Documentation validation alone does not
qualify these cases or complete ADR-0017's existing qualification ledger.
