# Roles, grants, and policies

LeapView authorization assigns privileges on securable resources to principals, service principals, and groups. Authentication and provisioning establish identities; the grant engine determines what those identities may do.

## Securable hierarchy

Securable objects include projects, dashboards, semantic models, sources, Models, datasets, tables, and columns. Objects participate in a parent hierarchy, so effective access may include inherited privileges as well as direct grants.

The authored project graph has exactly seven kinds: `project`, `connection`, `source`, `model`, `semantic_model`, `pipeline`, and `dashboard`. Groups, role bindings, grants, data policies, and dashboard-publication declarations are project inputs compiled into authorization and publication snapshots; they are not additional catalog graph nodes.

Review the effective privilege result rather than assuming a direct binding is the only source of access. The Current User and Access APIs expose effective-privilege views for this purpose.

## Project roles

Project role bindings apply reusable privilege sets such as viewer, member, editor, contributor, deployer, admin, or owner. Bind a stable group wherever access follows team membership:

```yaml
apiVersion: leapview.dev/v1
kind: RoleBinding
metadata:
  id: role-binding:analysts-viewer
  name: analysts-viewer
spec:
  role: viewer
  subject:
    kind: group
    group: analysts
```

Roles express common responsibilities. Owners and grant managers should be rare; routine project deployment should use a dedicated deployer identity rather than an owner token.

## Explicit grants

Use a Grant when one subject needs one privilege on a specific securable object outside the standard role shape:

```yaml
apiVersion: leapview.dev/v1
kind: Grant
metadata:
  id: grant:analysts-dashboard-read
  name: analysts-dashboard-read
spec:
  object:
    kind: dashboard
    id: dashboard:executive
  subject:
    kind: group
    group: analysts
  capability: RESOURCE_READ
```

Choose the narrowest object and privilege that supports the task. Avoid accumulating one-off direct user grants; they are harder to review and can survive team changes.

## Data policies

Data policies constrain analytical access beyond navigation or query permission. A row-filter policy limits eligible records; a column-mask policy changes exposure of a protected column. Policies target a securable object and may target a subject.

Policy expressions are part of the governed server query boundary. Apply them consistently to browser dashboards, headless API queries, agent tools, preview, and other data surfaces. Do not rely on hiding a dashboard component or browser column as a security control.

Test policies with representative users, including aggregates. Row filtering can change totals, distinct counts, relationship behavior, and whether an empty result is expected. Column masking must preserve safe types and must not leak the raw value through labels, tooltips, exports, or alternative datasets.

### Policy composition

LeapView composes every applicable policy before planning a raw, aggregate,
spatial, or multi-dataset query. The composition rules are independent of query
surface and repository traversal order:

| Applicable policies | Effective restriction |
| --- | --- |
| Multiple row filters for the same subject on the same protected object | AND |
| Row filters for different applicable subjects on the same protected object | OR |
| A global row filter plus subject-specific filters | Global filter AND subject alternatives |
| Row filters inherited from different protected objects | AND |
| A subject group containing only `{"allowAll": true}` | That subject alternative adds no row restriction; global and parent restrictions still apply |
| No applicable row policy | No additional row restriction |
| Candidate or preview restriction | AND with all active policy results |
| Equivalent masks on the same selected field | One mask |
| Different masks on the same selected field | Query rejected with the conflicting policy IDs |

Within one row-policy expression, entries in `filters` are also ANDed. Use
separate subject policies to express alternative entitlements; do not encode
authorization alternatives in reader-controlled dashboard filters. `allowAll`
is explicit so an empty or malformed expression cannot accidentally broaden
access.

Policy IDs remain part of the effective-policy fingerprint and query audit
identity. Composition errors name the relevant IDs, allowing an administrator
to resolve contradictory masks without relying on policy load order.

Policy expressions are compiled into a typed form during project validation,
serving-state snapshot assembly, or an audited API write. Invalid operators,
empty filters, ambiguous expression forms, and unsupported masks reject that
boundary before it becomes active. Existing stored policies are compiled when
first loaded and cached by their exact type and expression; an invalid stored
policy fails closed instead of reaching a query planner.

## Owners and administration

Ownership and platform administration are distinct from ordinary project-resource use. Keep the instance-wide `platform_admin` role, project `PROJECT_ADMIN`, and resource capabilities such as `RESOURCE_USE`, `RESOURCE_READ`, `RESOURCE_EDIT`, `RESOURCE_MANAGE`, `RESOURCE_SHARE`, and `RESOURCE_PUBLISH` separated according to operational responsibility.

A service principal used by CI should exist only on the target instances and receive the project and deployment/data privileges required by that pipeline. A read-only integration should not inherit project activation or grant management.

## Review access

Use this periodic review:

1. List active principals, service principals, and groups.
2. Reconcile SCIM membership and local groups.
3. Inspect effective privileges for sensitive project resources.
4. Find direct grants that duplicate or exceed role access.
5. Review owner, admin, deployer, and grant-manager assignments.
6. Review data policies against current semantic fields.
7. Remove or deactivate obsolete identities and revoke credentials.
8. Audit every binding, policy, and ownership change.

Validate project access resources before deployment and test with a non-owner principal afterward. See [Role Binding](/docs/config/role-binding), [Grant](/docs/config/grant), [Data Policy](/docs/config/data-policy), and the [Access API](/docs/api/access).
