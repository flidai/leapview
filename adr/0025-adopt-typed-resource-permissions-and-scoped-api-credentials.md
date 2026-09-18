# ADR-0025: Adopt typed resource permissions and scoped API credentials

Status: accepted

Authority-flow decisions amended by
[ADR-0026](0026-preserve-authority-across-governed-operations.md);
typed resource actions and credential attenuation remain the foundation.

Decision date: 2026-09-17

Implementation: advanced partial — typed credentials, assignments, durable
grant primitives, and qualified private operation slices; compatibility removal
and complete surface coverage remain pending

Deciders: LeapView maintainers

Supersedes: none

Amends: [ADR-0017](0017-adopt-a-looker-aligned-semantic-access-contract.md),
API credential attenuation for control-plane administration only: replace
implicit inherited token authority and the use of `PROJECT_ADMIN` as a platform
token scope with explicit action/resource restrictions and distinct platform
scopes

Related: [ADR-0005](0005-use-project-wide-resource-graph.md);
[ADR-0007](0007-adopt-plan-driven-project-delivery.md);
[ADR-0015](0015-adopt-durable-audit-and-compliance-controls.md);
[ADR-0016](0016-adopt-standards-aligned-data-contracts-and-interchange.md);
[ADR-0018](0018-retain-project-as-the-durable-deployment-namespace.md);
[ADR-0021](0021-adopt-a-local-first-analytics-development-workflow.md)

## Implementation status

The contract and persistence foundations are implemented without claiming
complete rollout. LeapView now has the versioned `leapview.permissions/v1`
catalog, validated action/target pairs and prerequisites, named role
expansions, generated TypeSpec/SQL/presentation/documentation artifacts,
PostgreSQL typed-token persistence, and a typed personal-token picker. New
public token issuance requires an explicit permission array; omission fails,
an empty array creates an authentication-only credential, and a restricted
token cannot mint a broader child. Migration 022 revokes active legacy API
tokens with an explicit audit outcome because their generic capability lists
cannot be converted to resource pairs without widening authority.

Typed project assignments capture the exact, profile-pinned expansion of a
role at issuance time. New role-binding API and bootstrap writes use that
form, while historical capability bindings remain readable only for explicitly
unmigrated operations. Typed operation descriptors bind actions to server-owned
dashboard, semantic-model, source, model, pipeline, connection, project,
delivery, or instance resolvers. Evaluation unions coherent principal and
group assignments, requires every prerequisite pair independently, intersects
typed API-token ceilings, and rejects typed credentials on unmapped routes.
The deterministic operation-coverage matrix distinguishes qualified,
mapped-pending-qualification, intentionally legacy, and unsupported paths.

Project bootstrap establishes three explicit, composable assignments for the
claiming principal: `project_admin`, `editor`, and `release_operator`. A legacy
Owner or Admin binding may satisfy only the administrator prerequisite; it is
preserved rather than translated, and the typed editing and release assignments
are still recorded independently. Bootstrap verifies the exact project-bound
role expansion returned by the server and never treats administration as a
wildcard for resource mutation or delivery.

The reusable action/target mechanics live in `pkg/permissions` behind an
explicitly compiled, profile-pinned catalog. That package owns only opaque wire
types, shape and catalog validation, prerequisite closure, matching,
intersection, attenuation, and strict encoding. `internal/access` remains the
single owner of LeapView action definitions, presentation/delegation metadata,
project-graph identity, roles, grants, credentials, and live authorization; it
adapts its product catalog into the pure mechanics package. Public authority
envelopes carry `permissions.Pair` without exposing private access types and
are rebound to the active product catalog before execution.

Migration 025 adds immutable typed grants, typed role bindings, and typed policy
bindings without guessing meanings for historical rows. Migration 026 adds
independently durable exact-resource shares, execution grants, and bounded
grant-administration envelopes with resource UIDs, expiry, revocation,
idempotency, and audited PostgreSQL mutations. Trusted issuance constructs the
issuer and credential ceiling from current server-side authority; request DTOs
cannot supply those fields. Ambiguous legacy assignments deliberately remain
legacy rather than being widened, so downstream compatibility cannot be
removed until every affected surface is qualified.

Qualified paths now include private dashboard consumption and authoring,
governed semantic query entry points, project catalog discovery, selected
project/connection/managed-data administration, instance settings/audit, and
the manual/scheduled delegated-refresh foundation. Native delivery planning and
build also evaluate and persist a coherent compound permission projection,
then reauthorize it against the current candidate snapshot before physical
work. Complete publication/rollback reauthorization, sharing-administration,
agent/MCP, list/facet/autocomplete, public/embed, and all lifecycle-boundary
coverage remains governed by the ADR-0026 ledger. The Go catalog is the runtime
authority and generated checks prevent contract drift; completion still
requires the remaining partial ledger rows rather than an ADR-format or
schema-only claim.

## Context and problem statement

LeapView combines governed semantic queries, dashboard authoring, managed data,
pipelines, connections, and immutable project delivery. An API credential for
one workflow must be able to exclude unrelated operations the principal can
otherwise perform.

The existing contract has seven generic capabilities: `PROJECT_ADMIN`,
`RESOURCE_USE`, `RESOURCE_READ`, `RESOURCE_EDIT`, `RESOURCE_MANAGE`,
`RESOURCE_SHARE`, and `RESOURCE_PUBLISH`. Exact resource checks and immutable
authorization snapshots provide a useful foundation, but the generic vocabulary
does not independently express dashboard editing, semantic exploration,
pipeline execution, release operations, or administrative inspection.

The token UI groups those capabilities into five controls. Its read-only option
for administration, management, sharing, and publishing selects generic resource
read/use capabilities rather than a read action for the named operation. Empty
selection omits the allowlist, which means dynamic inheritance of current and
future principal authority. Sharing is selectable even though the grant API
requires project administration. A `PROJECT_ADMIN` token also passes the
credential restriction for instance administration when its principal already
holds the durable platform-admin role.

The question is which authorization contract should govern human roles,
resource grants, API credentials, and every application surface without
replacing the semantic policy or deployment architectures already selected.

## Decision drivers

- Express the minimum authority for a concrete integration or human workflow.
- Make UI selections, documented permissions, and enforced actions agree.
- Separate content consumption, arbitrary semantic querying, operational
  execution, sharing, release authority, and administration.
- Preserve exact resource identity, the bound Project/environment, immutable
  generation evidence, and current revocation checks.
- Keep semantic restrictions consistent across browser, REST, CLI, agent, MCP,
  export, and publication consumers.
- Use established reference models without adopting unnecessary hierarchy or
  claiming vendor compatibility.
- Make permission coverage, role expansion, and migration behavior testable.

## Considered options

- Retain the seven generic capabilities and repair only the labels. This fixes
  presentation but cannot independently restrict actions by resource type or
  separate exploration from pipeline and delivery execution.
- Copy Databricks dashboard ACLs. Their simple levels are useful presentation
  precedent, but editor publishing and combined deletion/access management do
  not preserve LeapView's independent controls.
- Adopt the complete Unity Catalog or Snowflake authorization hierarchy. Both
  offer mature governance, but their catalog/schema structure and ownership
  rules would introduce product concepts not required by LeapView's graph.
- Use coarse BI project roles as the API scope contract. Convenient defaults
  would become the maximum available granularity for automation.
- Adopt typed resource actions, named role presets, and independently scoped
  credentials using Unity Catalog and GitHub as references.

## Decision outcome

Choose typed resource actions with named role presets and independently scoped
credentials. Unity Catalog is the primary architectural reference; GitHub's
fine-grained personal tokens are the credential and token-UI reference. OAuth
RFC 9700 is the normative OAuth security baseline. LeapView owns its permission
contract and does not claim Databricks, Snowflake, GitHub, or Looker API
compatibility.

### Authorization and scope

Authorize an exact principal, action, resource, credential, and bound target.
For each required action/resource pair, access requires the intersection of:

1. The principal's effective grants and role assignments.
2. The credential's permitted action/resource pairs and target audience.
3. Applicable semantic, publication, and environment policies.

Authentication never supplies a resource grant. Groups contribute explicit
assignments; role names are deterministic bundles of actions, not special-case
authorization bypasses. Preserve exact graph-validated resource checks and
immutable generation-bound policy evaluation. Credential expiry, principal
disablement, and effective revocation remain live restrictions: serving an old
generation, replaying a command, using a cached result, or resuming queued work
must not restore revoked authority.

Project UID qualifies resource identity. Each instance remains bound to one
Project and environment under ADR-0018. A token's target restrictions verify
that binding; they do not introduce a browser Project picker or request-time
context switch. Creation is authorized on the containing Project for the
specific resource kind, since the new resource does not yet exist.

Keep explicit resource grants and explicit Project-wide assignments. Repository
paths, domain metadata, tags, and graph dependency edges do not imply privilege
inheritance. Access collections or inherited scopes are deferred until their
membership, future-resource, and revocation semantics are separately specified.

### Permission families

The authoritative catalog must preserve the following independently grantable
actions. The names below define the intended action vocabulary; generated API
schemas and a versioned migration define the eventual wire contract.

| Family | Actions | Authorization scope |
| --- | --- | --- |
| Dashboard | `dashboard.read`, `dashboard.create`, `dashboard.update`, `dashboard.delete`, `dashboard.publish` | Exact Dashboard; creation on its Project |
| Semantic consumption | `semantic.read`, `semantic.query` | Exact SemanticModel |
| Development | Kind-specific read/create/update/delete for Source, Model, and SemanticModel definitions | Exact resource; creation on its Project |
| Pipeline | `pipeline.read`, `pipeline.create`, `pipeline.run`, `pipeline.update`, `pipeline.delete` | Exact Pipeline; creation on its Project |
| Connection | `connection.read`, `connection.create`, `connection.use`, `connection.manage` | Exact Connection or target binding; creation on its Project |
| Sharing | `resource.share` | Exact supported resource and bounded delegable actions |
| Delivery | `delivery.read`, `delivery.plan`, `delivery.build`, `delivery.publish`, `delivery.approve`, `delivery.activate`, `delivery.rollback` | Bound Project/environment and exact retained delivery evidence |
| Project administration | `project.settings.read`, `project.settings.update`, `project.access.read`, `project.access.manage`, `audit.read` | Bound Project |
| Platform administration | Distinct instance administration actions and credential scopes | Instance; requires durable platform authority |

Only expose actions backed by supported operations. Kind-specific actions and
their prerequisite rules must be explicit. A resource reader cannot mutate
business configuration, a dashboard editor cannot publish or delete merely by
editing, and a semantic query grant cannot authorize pipeline or delivery work.
Read operations may produce bounded supporting telemetry or cache artifacts;
classify authority by the operation's meaning, not solely by HTTP verb or the
presence of a storage write. Persisted delivery planning has its own action.

`semantic.read` permits authorized semantic metadata discovery;
`semantic.query` permits new governed analytical queries. Dashboard read permits
the approved dashboard's interactions and queries under their data policy; it
does not grant arbitrary semantic exploration. Dashboard access must not bypass
underlying semantic authorization, member restrictions, or row filters.

Source and Model development permissions do not introduce a raw-data consumer
API or authored data-policy target. ADR-0017 continues to govern semantic
consumption. Connection use permits authorized execution through a binding; it
does not reveal its credentials. Metadata responses must exclude secrets.

### API credentials and token presentation

API credentials attenuate their principal's authority. A token carries an
explicit target audience and explicit action/resource pairs. A pair may name an
exact resource or an explicitly selected, typed Project-wide scope. Preserve
pairing: separate resource and action arrays must not accidentally grant the
Cartesian product of their contents.

New token creation requires explicit permission selection. An empty selection
grants no Project/resource authority, and an omitted permission field cannot
mean full inherited access. Authentication or narrowly defined self-service
operations are a separate contract. Browser sessions retain their normal role
evaluation; they are not implicitly converted into restricted API tokens.

The token UI presents target/resource scope, allowed actions, and expiry. Common
workflow presets are visible expansions of those choices. Selecting Publish
selects the publish action; a read/write selector is offered only when both
levels correspond to real actions for that permission family. The count and
summary describe the restrictions actually persisted.

Future-resource inclusion requires explicit selection. New action definitions
never automatically enter existing token allowlists. Losing principal access
removes effective token access even before token expiry; gaining principal
access cannot exceed the token's stored restrictions. Token creation,
delegation, renewal, and exchange must not widen a restricted caller's
effective authority.

Credentials have an explicit expiry and support revocation independently of
analytics deployment. Use scoped service-principal credentials for unattended
automation. Prefer short-lived OAuth credentials where the existing authoring
and workload flows support them. RFC 9396 `authorization_details` is a reference
for structured OAuth resource/action consent, not a prerequisite for personal
token improvements or an assertion that LeapView already implements that RFC.

Platform scopes are distinct from Project scopes. A Project token cannot
administer the instance even when its owner is a platform administrator. A
platform scope cannot manufacture the durable role. This amends only
ADR-0017's API credential gate; its semantic administration ownership,
authoring-credential restrictions, and fail-closed data-policy behavior remain.

### Roles, sharing, and publication

Offer Viewer, Explorer, Editor, and Project Admin presets, with independent
Publisher, Release Approver, Release Operator, and Auditor presets. Assignments
are scoped and composable. Viewer covers approved content consumption; Explorer
adds governed semantic querying; Editor adds authoring. Publishing, deletion,
sharing, and release administration require their explicit actions. Project
Admin controls Project administration without becoming a platform role or a
semantic-policy bypass. The catalog documents every preset's exact expansion.
Do not retain synonymous role names without an explicit compatibility purpose.

Sharing is bounded delegation. A sharer may grant only the allowed delegable
actions on the exact resource, within the configured audience, and within the
authority of both principal and credential. Ordinary sharing cannot confer
Project administration, unrestricted onward delegation, or additional semantic
data privileges. Full access administrators are explicitly trusted to assign
privileges; they are distinct from bounded sharers. Ownership or stewardship
metadata alone is not an implicit grant.

Public or embedded exposure requires explicit publication authorization and
audience policy separate from sharing with named principals. Dashboard content
publication does not implicitly authorize anonymous exposure. Any delegated
query identity must be explicit, bounded, and audited under the existing
publication contract; viewer policies cannot silently become publisher access.

### Delivery and administration separation

Preserve plan, build, publication, approval, activation, and rollback authority
as separate decisions. Publisher automation does not require Project access
administration. Check the relevant action on every affected resource as well
as the bound target, without treating the dependency graph as inherited access.

Protected environments retain independent approval and activation controls,
separation of duties, exact candidate/plan evidence, and rechecks immediately
before cutover. A publisher cannot approve its own protected release merely
because it also holds another preset. Immediate-policy targets may still
activate automatically through explicitly authorized execution under ADR-0021;
this decision does not add a mandatory human approval to every development run.
Promotion cannot transfer target credentials or approvals between environments.

### Catalog, enforcement, and migration

Maintain one authoritative machine-readable permission catalog for action
identity, valid resource kinds, scope, descriptions, prerequisites, delegation,
role expansions, and operation coverage. Generate or mechanically validate the
token picker, API documentation, role documentation, and enforcement contracts
against it. Compound operations declare every required pair; polymorphic
commands resolve their concrete action before execution. REST, browser, CLI,
agent, and MCP entry points use the same policy decision boundary.

Unknown actions, invalid kinds, missing required scope, and unavailable
authorization fail closed. An authenticated-only declaration cannot substitute
for a domain action check. A displayed permission must have enforcement
coverage, and an authorization decision must be explainable without exposing
resources the caller cannot inspect. Audit security-relevant mutations under
ADR-0015, including grant and credential changes and release decisions.

Implement a versioned migration from the generic capabilities and existing
roles. Preserve exact principal/resource/target associations and historical
audit meaning. Define mappings for each old action by resource kind and
operation; do not mechanically translate every `RESOURCE_USE`, `RESOURCE_EDIT`,
or `PROJECT_ADMIN` into all new actions. Existing dynamically inherited tokens
need an explicit inventory and conversion or revocation policy. Do not silently
turn them into unrestricted new scopes or relabel their behavior as restricted.
Compatibility handling is bounded migration work, not a permanent second
permission system.

The implementation sequence is to correct token selection and separate
platform scopes, establish catalog coverage, split query/operational/delivery
actions and implement bounded sharing, then complete grant/role/credential
migration. Exact endpoint matrices, compatibility mappings, and rollout evidence
belong in companion specifications and implementation work. Acceptance of this
ADR does not certify that the current runtime already satisfies it.

## Research basis

Official documentation and Flid reference sources were reviewed on 2026-09-17.
Product behavior supplies precedent; only the cited IETF documents are protocol
standards, and conformance requires implementation evidence.

- [Unity Catalog permissions model](https://docs.databricks.com/aws/en/data-governance/unity-catalog/access-control/permissions-concepts):
  typed securable objects, scoped privileges, and explicit creation boundaries.
- [Databricks dashboard ACLs](https://docs.databricks.com/aws/en/security/auth/access-control/#dashboard-acls):
  simple access levels, with edit/publish and manage/delete combinations that
  LeapView deliberately separates.
- [Databricks privilege reference](https://docs.databricks.com/aws/en/data-governance/unity-catalog/access-control/privileges-reference):
  grant administration can enable self-granting and must be treated as trusted
  authority, not harmless metadata access.
- [GitHub personal access tokens](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens):
  resource selection and fine-grained permissions restrict the owner's access.
- [RFC 9700 section 2.3](https://www.rfc-editor.org/rfc/rfc9700.html#section-2.3)
  and [RFC 9396](https://www.rfc-editor.org/rfc/rfc9396.html): audience,
  resource, and action restrictions for OAuth credentials and structured consent.
- [Snowflake access control](https://docs.snowflake.com/en/user-guide/security-access-control-overview)
  and [semantic-view privileges](https://docs.snowflake.com/en/user-guide/security-access-control-privileges):
  centralized grant administration and separate metadata/query privileges.
- [Looker roles](https://docs.cloud.google.com/looker/docs/admin-panel-users-roles):
  permission sets, model access, development, and deployment distinctions.
- [Databricks dashboard data permissions](https://docs.databricks.com/aws/en/dashboards/share/embedding):
  individual and publisher data permissions are different execution identities.
- Flid's Lightdash reference at revision
  `35906ad9e116df59d2d59da2f58e45e9b39eaff8`,
  `docs/authorization-scopes.md` and
  `packages/common/src/authorization/rolePresets.ts`: scope migration, parity,
  and visible preset expansion.
- Flid's Rill reference at revision
  `4f814a86196fac2ba9664b9237826582de2dad03`,
  `docs/docs/developers/build/metrics-view/security.md` and
  `docs/docs/guide/administration/users-and-access/roles-permissions.md`:
  shared data-policy enforcement and distinct production/development authority.

## Consequences

Integrations can receive the specific actions and resources they need, and
human-facing presets remain understandable. Shared catalog coverage prevents
UI labels, token restrictions, and backend checks from drifting independently.
The existing graph, serving snapshots, semantic planner, and release evidence
remain the enforcement foundation.

The permission catalog and migration are larger than a UI correction. Endpoint
coverage, compound actions, role expansion, existing tokens, and every entry
point must be reconciled. Restricted credentials may need replacement, and
clients must receive actionable scope diagnostics. Scope growth requires review
and compatibility evidence; generic administrator shortcuts cannot compensate
for missing actions.

Fine-grained actions do not remove the trust placed in access administrators,
and token restrictions do not replace row/member policies. Hierarchical access
collections and a new general policy engine remain outside this decision.

## Confirmation

- Catalog validation rejects unknown actions, invalid action/kind/scope pairs,
  cyclic or unsatisfied prerequisites, and unsupported UI permissions.
- Generated role and UI contracts match enforcement, including creation,
  compound operations, polymorphic commands, and all delivery actions.
- A dashboard reader can perform only approved interactions under semantic
  policy; it cannot use arbitrary semantic queries. An Explorer cannot run
  pipelines or build releases through its query permission.
- A dashboard editor cannot publish, delete, share, or modify Source/Model
  definitions without the relevant additional actions.
- A token with read on resource A and update on B gains neither update on A
  nor read on B unless those pairs are explicitly included or required by
  declared, validated prerequisites.
- Empty or omitted new-token permissions do not inherit authority. Future
  resources and new actions do not enter a token except as expressly allowed
  by its persisted scope. Restricted token issuance cannot expand its caller.
- Project credentials cannot pass platform administration checks, including
  when the token owner holds both roles. Platform credentials without the
  durable platform role also deny.
- Sharing tests reject out-of-audience recipients, nondelegable privileges,
  broader data access, and unauthorized onward delegation. Public exposure
  requires its separate authorization.
- Protected release tests preserve independent approval/activation, affected
  resource checks, candidate binding, and environment isolation. Immediate
  targets operate through explicit authorized policy without a new manual gate.
- Expiry, principal disablement, credential/grant revocation, cached results,
  queued work, and retained-generation rollback cannot restore stale authority.
- Browser, REST, CLI, agent, MCP, export, and supported publication consumers
  produce equivalent decisions for the same authority and semantic context.
- Migration fixtures preserve intended access and audit identities or produce
  explicit conversion/revocation outcomes; they never silently broaden grants.
