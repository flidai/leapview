# ADR-0026: Preserve authority across governed operations

Status: accepted

Decision date: 2026-09-17

Implementation: partial — restricted dashboard consumption and caller-authority
Pipeline refresh slices; delegated execution and lifecycle qualification pending

Deciders: LeapView maintainers

Supersedes: none

Amends: [ADR-0025](0025-adopt-typed-resource-permissions-and-scoped-api-credentials.md),
consumption entitlement, execution and grant delegation, creation and permission
evolution, reference-model emphasis, and implementation sequencing;
[ADR-0017](0017-adopt-a-looker-aligned-semantic-access-contract.md), retained-generation
eligibility under current security restrictions only, without changing authored
policy meaning or historical publication-evidence validation

Related: [ADR-0007](0007-adopt-plan-driven-project-delivery.md);
[ADR-0015](0015-adopt-durable-audit-and-compliance-controls.md);
[ADR-0021](0021-adopt-a-local-first-analytics-development-workflow.md);
[Resource authorization authority flow](specifications/resource-authorization-authority-flow.md)

## Implementation status

The restricted-dashboard slice now distinguishes saved-content consumption
from arbitrary query construction. Published dashboard execution requires an
exact `dashboard.read` credential pair and `semantic.consume` on every selected
SemanticModel in addition to the existing principal grants and semantic policy.
API, agent, Explorer, and preview query construction requires
`semantic.query`, whose catalog prerequisite independently requires
`semantic.consume`. Draft previews retain their authoring gate, and dashboard
interaction input is validated against the server-owned revision rather than
accepting a caller-supplied query shape.

The first asynchronous slice persists an immutable authority envelope with a
manual Pipeline refresh. It records an exact `pipeline.run` pair, actor,
execution principal, target, and non-secret browser-session or typed-token
evidence. The envelope participates in the job request digest and is
revalidated against live credential lifecycle and current exact resource
authority before worker admission. Unknown credential classes, missing
evidence, legacy token scopes, expiry, revocation, and changed permission
ceilings fail closed. Existing job kinds retain a migration sentinel; only
`refresh_pipeline` currently requires this envelope.

Delegated workload execution is represented in the envelope but intentionally
unsupported by the native revalidator. There is no execution-grant repository,
approved dependency/destination closure, scheduler authority, per-protected-unit
or output-boundary revalidation, grant-administration envelope, or completed
security-revision concurrency matrix yet. Release, deployment, managed-data,
agent, and approval jobs have not migrated to this authority contract. The
companion specification's ledger is authoritative for the remaining evidence;
these slices must not be described as security-complete.

## Context and problem statement

ADR-0025 establishes typed actions, exact resource scopes, credential
attenuation, and independent editing, publishing, sharing, execution, and
administration. These remain the foundation. Permission names alone do not
settle which authority governs a saved dashboard query, a privileged scheduled
pipeline, an access-administration mutation, or an old deployment after a
security change.

The existing ADR-0017 consumer, cache/lifecycle, and activation specifications
already bind semantic policy to coherent current attribute authority and exact
publication evidence. They must be extended to the new credential and execution
contract, not replaced by a second policy engine or security lifecycle store.

The decision is how authority persists, narrows, and is revalidated when an
operation crosses request, identity, revision, or deployment boundaries.

## Decision drivers

- Separate saved-content consumption from arbitrary query construction.
- Prevent executable-content editing and administrative APIs from becoming
  indirect credential or data-access bypasses.
- Make queued-work expiry, sharing lifetime, creation, and revocation predictable.
- Preserve immutable execution evidence without freezing mutable security state.
- Establish end-to-end evidence before claiming security completeness.

## Considered options

- Leave authority transitions to individual handlers. Rejected: locally correct
  action checks can still compose into privilege escalation.
- Treat dashboard access, ownership, worker credentials, and access management
  as implicit authority. Rejected: the caller's effective boundary becomes
  dependent on ambient privileges and undocumented defaults.
- Make all asynchronous work depend forever on the initiating credential and
  all shares depend on the grantor. Coherent, but unsuitable for explicitly
  durable service workloads and ordinary persistent resource grants.
- Use explicit consumption, bounded durable grants and workload delegations,
  and coherent current-security checks at operation boundaries. Selected.

## Decision outcome

Preserve authority through explicit operation contracts enforced by one shared
in-process authorization module. A separately deployed policy service, generic
policy language, or vendor-compatible hierarchy is not required.

### Consumption is explicit

Add `semantic.consume` on an exact SemanticModel. A saved dashboard data request
requires `dashboard.read`, `semantic.consume` on every participating model, and
ADR-0017 row/member restrictions. The server resolves the published revision
and constructs queries only from its approved interactions. A dashboard ID is
not authority for caller-supplied arbitrary queries.

`semantic.read` remains metadata access. `semantic.query` authorizes arbitrary
governed query construction and requires independently satisfied
`semantic.consume`; it does not imply that grant. Draft data previews require
dashboard authoring authority and `semantic.query` plus `semantic.consume` for
the selected models. Editing alone cannot turn a consume-only Viewer into an
Explorer. Static draft editing need not execute data queries.

No action bypasses semantic policy. Public/embed and delegated publication
identities remain separate contracts; unsupported protected consumers remain
closed under ADR-0017's qualified profile.

### Execution has an explicit authority mode

Record the initiating actor, execution principal, approved executable revision,
target, parameter constraints, connection bindings, and output destinations.
Support two explicit modes:

- Caller-authority work remains dependent on the caller's current grants and
  live initiating credential, including expiry and revocation.
- Delegated workload work uses a separately authorized, revocable, bounded,
  expiring execution grant and the workload principal's current privileges.
  Its lifetime is explicitly independent of the initiating credential.

Creating or changing a delegation requires authority to delegate that specific
workload identity and operation. An edit or run permission does not confer it.
Approval binds the effective executable dependency closure, not just the top-level
pipeline file. Changing executable content, binding authority, identity,
parameters outside the approved envelope, or destinations requires new approval.
An editor cannot redirect an already approved privileged schedule.

Worker infrastructure credentials transport work; they do not supply missing
product authority. Trigger permission never automatically grants access to
data-bearing results, logs, or exports.

### Grant administration is bounded authority

`project.access.manage` permits an administrative operation, not arbitrary
privilege issuance. Check its grant contents, scope, recipients, and current
credential restrictions. Beyond ordinary bounded sharing, delegation requires
an explicit grant-administration envelope held by both principal and credential.
The intended action is `project.access.delegate`; its reviewed envelope identifies
the permissions and recipients that may be granted. Unrestricted delegation is
an intentional high-trust capability, visibly distinct from access maintenance.

Apply equivalent checks to role binding, group membership, controlled service
principals, credential issuance, and workload identity assignment. Trusted
attribute administration retains ADR-0017's separate durable role and scoped
credential requirements. Authored policy widening still requires security
approval; indeterminate classification still blocks publication.

Ordinary sharing creates an independent durable grant, bounded when issued.
Later loss of the sharer's authority does not automatically revoke it. Explicit
revocation, expiry if configured, recipient lifecycle, and current policy govern
continued use. This replaces the ambiguous lifecycle implied by the phrase
"bounded delegation" in ADR-0025; sharing is not transitive onward authority.

### Current security governs retained content

Analytics generations and current security evidence advance independently.
Reuse the existing Access registry/control revision and digest authorities;
extend coherent evidence to relevant grants, groups, credentials, delegation,
and retained-generation eligibility without duplicating lifecycle ownership.

Old generations, caches, queues, and rollback must satisfy current restrictions.
When a newer mandatory policy restriction cannot be safely enforced against
retained content, deny that content rather than interpreting historical approval
as continuing permission. Current eligibility is an additional fence; it does
not replace exact historical publication validation or splice new policy text
into an incompatible old graph.

The companion spec defines revocation boundaries for admission, commit, queued
execution, cache reuse, output, and side-effect dispatch. Already delivered data
and already dispatched irreversible effects cannot be retroactively revoked.
Reconciliation of a committed activation may replay its recorded outcome;
new data delivery still requires current authorization.

### Creation and evolution never silently widen authority

Creation on the Project confers no implicit owner/read/update grant and does
not expand the creating token. Follow-on access requires separately authorized
grants and matching credential scope. A create-only response is a minimal
creation receipt, not an implicit resource-read operation.

Prerequisites are additional checks, not implied grants. Version action meaning,
prerequisites, role expansions, and delegation envelopes, not just action names.
An authority-widening revision requires explicit reviewed migration/consent;
unknown or retired unsupported profiles fail closed. Retaining an old profile
must not retain a security bypass.

### Reference models and implementation order

Unity Catalog is the reference for typed securables and privileges, not the
overall hierarchy, inheritance, or ownership model. Looker informs the separate
consumption boundary; GitHub informs credentials; Databricks Jobs informs
execution identity; Kubernetes informs anti-escalation checks; Zanzibar informs
ordering of security and content changes. RFC 9700 remains the OAuth security
baseline and RFC 9396 informs structured resource/action consent. These are
selected precedents, not claims of compatibility or mandatory infrastructure.

Retain urgent token-selection/platform-scope corrections from ADR-0025. Before
hardening the full catalog and migration, qualify a restricted-token dashboard
Viewer end to end, then privileged pipeline execution. Both must exercise
current security changes, not just positive permission checks.

## Consequences

The same explicit authority rules govern interactive, asynchronous, delegated,
and retained-content operations. Restricted administrative credentials and
pipeline editing can no longer be described as safe solely from their immediate
endpoint scopes.

There are additional consumption and delegation concepts to explain, more
revision evidence to carry, and current authorization work on cache hits and
output paths. Create-only integrations need deliberate follow-on provisioning.
Revoking a sharer is not a substitute for reviewing grants they issued.
Emergency restrictions can intentionally make old content unavailable.

These are target decisions, not evidence that the current runtime implements
them. Existing qualified semantic-access claims remain limited to their
documented supported profile.

## Confirmation

The [companion specification](specifications/resource-authorization-authority-flow.md)
defines the operation contract, grant and execution lifecycles, consistency
boundaries, and negative acceptance matrix. Security completeness requires
evidence for both vertical slices, coherent concurrency tests, indirect
escalation tests, authorization-aware discovery, and versioned migration.
An ADR format check or documentation review cannot substitute for that evidence.

## Research basis

Official references checked on 2026-09-17, alongside the local authorities linked
in the companion spec. The resulting LeapView choices are design judgments:

- [Looker roles and permissions](https://docs.cloud.google.com/looker/docs/admin-panel-users-roles)
  distinguish data access, saved-content viewing, and exploration.
- [Databricks job identity](https://docs.databricks.com/aws/en/jobs/privileges)
  distinguishes job operation from Run as privileges and recommends service
  principals for production jobs.
- [Kubernetes escalation prevention](https://kubernetes.io/docs/reference/access-authn-authz/rbac/#privilege-escalation-prevention-and-bootstrapping)
  checks role contents and bindings in addition to ordinary write permission.
- [Zanzibar](https://research.google/pubs/zanzibar-googles-consistent-global-authorization-system/)
  provides precedent for respecting ordering between ACL and content changes,
  not a requirement to reproduce its distributed system.
- [Power BI semantic-model permissions](https://learn.microsoft.com/en-us/power-bi/connect-data/service-datasets-permissions)
  explicitly warns that withholding Build while granting Read is not a sensitive
  data security boundary. Workflow controls do not replace row/member policy.
- [RFC 9396 section 2.2](https://www.rfc-editor.org/rfc/rfc9396.html#section-2.2)
  informs structured authorization details; preserve distinct action/resource
  pairings through storage, consent, and evaluation.
