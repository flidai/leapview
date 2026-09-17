# Resource authorization authority flow

Status: partially implemented; qualification ledger below remains authoritative.

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

Browser, REST, CLI, agents, MCP, workers, exports, and cache delivery call this
same module. Presentation may summarize its results but cannot grant authority.
Sensitive logs and diagnostic endpoints are also consumers. Internal audits may
record rejected identities that the requester is not allowed to discover.

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

| ID | Status | Required negative and lifecycle evidence |
| --- | --- | --- |
| AF-01 | Implemented for typed private dashboard/API paths | Dashboard read without consume denies data; consume without query cannot submit arbitrary queries; forged interaction/query payloads fail. Public/embed remains outside the qualified typed slice. |
| AF-02 | Partial | Draft preview requires authoring plus query/consume and preserves semantic policy. Full dependency-change and every authoring-surface matrix remains pending. |
| AF-03 | Pending | Restricted attributes change from all regions to EMEA during query/cache/coalescing; no later output boundary releases a global cached result. |
| AF-04 | Partial | Caller token/session evidence is checked at refresh dequeue/admission. Revalidation during running protected units and output, plus delegated-workload lifetime behavior, remains pending. |
| AF-05 | Pending | Editor changes pipeline SQL, referenced model, binding, destination, or run-as identity; approved privileged schedule cannot execute the changed closure. |
| AF-06 | Pending | Disabled workload principal, expired/revoked execution grant, or broadened role never yields authority outside the currently valid grant. Triggering does not expose privileged results/logs. |
| AF-07 | Partial | Personal-token issuance prevents restricted bearer and browser callers from widening typed authority. Role/group/service-principal/attribute/workload delegation escalation checks remain pending. |
| AF-08 | Pending | Ordinary share survives issuer loss as documented; explicit revocation works; no onward sharing or delete/recreate resurrection. |
| AF-09 | Pending | Old generation, rollback, and long reader obey current eligibility/restrictions; already committed activation reconciliation remains replay-safe. |
| AF-10 | Pending | Real concurrent security-write/mutation/dispatch/output tests establish commit and release ordering, bounded in-flight exceptions, and fail-closed authority/audit failures. |
| AF-11 | Partial | Catalog profiles, explicit prerequisites, exact/future targets, and typed-token immutability prevent silent credential widening. Resource create-only receipts and durable role migration remain pending. |
| AF-12 | Partial | Typed project catalog/search/detail paths filter exact read pairs and search continues across denied pages. Totals, facets, autocomplete, semantic-member discovery, dependency previews, and denial explanations still require complete coverage. |
| AF-13 | Partial | Permission storage and evaluation preserve pairs without Cartesian expansion. Coherent compound snapshots and complete delivery changed/dependency separation remain pending. |

Record endpoint coverage, concurrency fixtures, supported-profile limits, and
migration evidence with implementation. Documentation validation alone does not
qualify these cases or complete ADR-0017's existing qualification ledger.
