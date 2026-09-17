# Service principals and API tokens

Non-human workloads should use a dedicated service principal and scoped credential. A token or service-principal secret authenticates an existing identity; it does not create identity or grant privileges by itself.

## Choose the principal type

Use a service principal for CI, deployment, data synchronization, scheduled integration, or another workload with its own lifecycle. Use a user API token only for a person's short-lived automation where individual ownership is intentional.

Never share one human token across several systems. Shared credentials remove attribution, make rotation disruptive, and allow one compromised workload to impersonate unrelated automation.

## Create and authorize a service principal

The [Access API](/docs/api/access) can create, update, list, and delete service principals and issue/revoke their secrets. Create one principal per workload boundary, then assign only the role or grants it requires.

Examples of separate identities include:

- a project deployer for one environment;
- a managed-data publisher for one project;
- a read-only semantic-query integration for one project;
- an MCP workload allowed to use the agent and only the required project resources;
- a monitoring principal for bounded synthetic queries.

Do not grant platform administration because a narrower workflow initially returns `403`. Inspect effective privileges and add the missing specific privilege at the correct scope.

## Handle the credential

Creation responses return new secret material once. Send it directly into the CI or deployment secret manager. Do not write it to project YAML, committed `.env` files, container layers, Terraform state/output, command examples, build logs, or plan artifacts.

CLI automation should inject service-principal credentials and exchange them for an ephemeral workload identity:

```sh
LEAPVIEW_TARGET=https://dash.example.com
LEAPVIEW_WORKLOAD_CLIENT_ID=sp_project_deployer
LEAPVIEW_WORKLOAD_CLIENT_SECRET=<secret>
LEAPVIEW_WORKLOAD_PROJECT=analytics
```

`LEAPVIEW_WORKLOAD_PROJECT` must identify the Project already bound to the target; it asserts that identity and cannot select or switch the server to another Project. The exchanged credential is bound to the target instance, Project, action allowlist, and short lifetime. It cannot be refreshed and is not persisted. The service principal's roles and grants remain authoritative, so the exchange scope can narrow access but cannot elevate it.

`LEAPVIEW_API_TOKEN` and `--token` remain a discouraged compatibility path. Avoid command-line secrets where process listings or shell history may expose them.

For a person, `leapview login <target>` uses browser/device authorization and the person's existing SSO or local browser session. It stores rotating CLI credentials in the OS keychain, never in the target profile. Browser sessions, CLI sessions, Desktop sessions, and workload credentials have independent client IDs and revocation lifecycles.

## User API tokens

An authenticated user can list, create, and revoke their tokens through `/api/v1/me/api-tokens`. New typed credentials carry a versioned permission profile and an explicit list of action-target pairs. The pair is the unit of authority: do not submit independent action and resource arrays, because combining them would create an unintended Cartesian product.

An empty `permissions: []` list creates an authentication-only token with no Project or resource authority. Omitting `permissions`, or sending it as `null`, is invalid; it never means “inherit everything.” The token can narrow the principal's effective authority, but it cannot elevate it. The response includes the catalog profile and the exact persisted pair list, including an explicit empty array for an identity-only token.

Targets make the audience visible:

- An exact resource pair names `scope: "resource"`, the bound `projectId`, `resourceKind`, and `resourceId`. It applies only to that resource.
- A Project-scoped operation names `scope: "project"` and `projectId`. This is used for actions such as Project administration, delivery operations, and creation, where the resource does not yet exist.
- A future-resource selection is explicit: use `scope: "project"`, `projectId`, `resourceKind`, and `includeFuture: true`. It applies to that resource kind in that Project as resources are created. A current-only selection is expanded into exact pairs when the credential is issued, so it does not silently widen later.
- An instance-scoped platform action names `scope: "instance"` and `instanceId` only. Instance authority is separate from Project authority.

`PROJECT_ADMIN` covers Project administration only. Platform actions such as `platform.access.manage` require the principal's current durable platform-admin role and a separate instance-scoped token pair. Project administration never implies platform administration, and a token pair never creates the durable platform role. In the legacy capability vocabulary, `PLATFORM_ADMIN` is likewise a separate option; it must not be inferred from `PROJECT_ADMIN`.

The permission catalog's role names and workflow presets are visible expansions, not hidden grants. Before issuing a credential, review the expanded action list, target scope, and expiry; a preset does not bypass pair, prerequisite, policy, or principal checks. See the generated [typed permission catalog](/docs/reference/permissions) for the profile, action descriptions, and role expansions.

The personal-settings picker receives only exact, already-authorized action-target pairs from the active authorization snapshot. Capability-only authority is not projected into typed permissions; an unavailable or unproven typed option set fails closed. Existing legacy capability rows remain a bounded migration and bootstrap concern, not a public issuance input.

The PostgreSQL typed-permission migration revokes active legacy API tokens because a capability-only list has no safe resource identity to convert without widening authority. It records that revocation in the security audit stream. Reissue a typed credential with explicit pairs and update each workload; do not expect an old legacy token to survive the migration. Legacy capability rows remain readable for audit and bounded bootstrap compatibility, but public token-creation APIs accept only typed permission pairs.

The same user can inspect browser sessions, API tokens, and authoring sessions through the Current User API. Revoke unused CLI sessions during credential or device incidents. Reuse of a rotated refresh credential revokes the entire CLI session family.

Row and column policies still apply because a CLI or workload credential authenticates the same governed principal. The short-lived credential adds an instance/project/action boundary; it does not bypass RBAC, row-level security, column policy, or source credentials held by the server.

## Rotate and verify safely

Use overlapping rotation where the secret API and workload allow it:

1. Create a new secret with the same intended scope.
2. Store it as a new secret-manager version.
3. Roll out the workload and verify authenticated operations.
4. Revoke the old secret.
5. Confirm old-credential attempts fail.
6. Review audit events for unexpected use during the overlap.

Do not extend overlap indefinitely. Record owner, purpose, creation, last rotation, expiry where supported, and revocation date.

## Respond to exposure

Revoke the credential immediately, then inspect audit and query events for the principal, the affected target's server-bound Project and resources, operations, and time window. Rotate downstream secrets that the workload could access, correct excessive grants, and issue a replacement only after the cause is contained.

Deleting or disabling a service principal is appropriate when the workload is retired. Remove role bindings and ownership references as part of the same change.

See [Current User API](/docs/api/current-user), [Access API](/docs/api/access), and [Automation and CI](/docs/cli/automation).
