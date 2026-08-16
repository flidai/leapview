# SCIM provisioning

SCIM synchronizes enterprise users, groups, memberships, and active state into LeapView. It does not authenticate browser sessions and does not replace LeapView authorization.

## Enable the service

Create a dedicated high-entropy provisioning token and store it in the deployment secret manager:

```sh
LEAPVIEW_SCIM_BEARER_TOKEN=<at-least-32-character-random-secret>
```

When configured, the SCIM base URL is:

```text
https://dash.example.com/scim/v2
```

Production validation enforces the token's minimum length. Do not reuse an interactive API token, deployment token, OIDC client secret, or metrics token.

## Configure the directory provider

Set the tenant URL to the SCIM base and use bearer-token authentication. LeapView exposes service-provider metadata, schemas, resource types, and CRUD/PATCH operations for users and groups.

Begin with a small test group. Provision a test user, update profile metadata, add and remove membership, deactivate the user, and confirm the resulting principal/group state before assigning production access.

## Identity lifecycle

SCIM users become ordinary user principals. SCIM groups are global directory groups that may receive access to resources in several projects. Membership changes affect effective access immediately.

LeapView preserves identity and audit continuity during deactivation. Setting `active=false` or deleting a SCIM user disables the principal, removes SCIM group memberships, and revokes sessions and API tokens. It is a soft disable rather than erasing historical attribution.

Administrators can also place an independent LeapView access block on an active principal. This emergency block revokes credentials and denies authorization, but does not alter the directory's `active` value. A later SCIM synchronization cannot clear the LeapView block; an administrator must remove it explicitly. Removing the block does not reactivate a principal that remains disabled by SCIM.

Directory profile changes should update mutable metadata without changing the principal identity. Configure immutable external identifiers correctly in the provider to avoid duplicate accounts.

## Separate provisioning and authorization

Use the directory as the source of truth for enterprise group membership. Use LeapView role bindings, grants, and policies as the source of product authorization.

This separation means:

- creating a group does not grant a project automatically;
- removing a user from a directory group immediately removes access inherited through that group;
- direct user grants remain effective until removed separately;
- OIDC group claims do not compete with SCIM membership;
- service principals remain managed through the LeapView access API.

The administration API therefore exposes SCIM profiles, groups, and memberships as read-only resources. Profile edits, group deletion, and membership changes must be made in the directory, while LeapView role bindings and grants remain writable.

Prefer binding stable directory groups to roles. Avoid granting every synchronized employee a default project merely because they exist in the tenant.

## Rotate the token

Plan rotation with the directory provider. Install a new LeapView token and update the provider in a controlled window; because the runtime exposes one configured bearer token, coordinate cutover so failed provisioning does not persist unnoticed.

After rotation, trigger a test update and verify it in LeapView. Revoke the old value in the secret manager and ensure it was not retained in provider diagnostics or operator shell history.

## Monitor provisioning

Alert on repeated SCIM failures, authentication failures, unexpected mass deactivation, membership churn, and directory/provider throttling. Preserve directory audit logs alongside LeapView logs to correlate a provisioning request with a product access change.

Regularly reconcile expected active users and groups. Pay special attention to direct grants that outlive group removal and disabled principals that still own securable objects or scheduled automation.

See [Authentication and authorization](/docs/enterprise-auth), [OIDC](/docs/security/oidc), and [Roles, grants, and policies](/docs/security/authorization). Use the generated [environment variable reference](/docs/configuration) for the exact SCIM runtime contract and the [Access API reference](/docs/api/access) for service-principal and grant operations.
