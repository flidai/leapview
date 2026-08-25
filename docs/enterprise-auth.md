# Authentication and authorization

LeapView separates authentication, provisioning, authorization, workload identity, credentials, and auditing. Use this page to choose the security workflow or concept you need.

## Choose how people sign in

- Use [Local authentication](/docs/security/local-auth) for self-hosted users and a controlled break-glass path.
- Use [OIDC](/docs/security/oidc) for interactive enterprise browser login.
- Use the [MFA security decision boundary](/docs/security/mfa-boundary) to distinguish provider-enforced MFA from application-managed assurance, which is not currently supported.
- Use browser/device authorization for `leapview login <target>`; CLI credentials remain separate from browser and Desktop sessions.

Both sign-in modes resolve an ordinary LeapView principal. Authentication proves identity; it does not grant project-resource access.

## Provision identities and workloads

- Use [SCIM provisioning](/docs/security/scim) to synchronize enterprise users, directory groups, memberships, and active state.
- Use [service principals and short-lived workload identity](/docs/security/tokens) for CI, deployment, data publishing, monitoring, and other non-human workloads. API tokens are a compatibility path.

OIDC subject identity, SCIM directory state, and service-principal lifecycle remain distinct so that sign-in, provisioning, and automation can change without becoming authorization shortcuts.

## Govern access

Read [Roles, grants, and policies](/docs/security/authorization) to understand securable hierarchy, project-resource roles, explicit grants, ownership, and row or column data policy. LeapView authorization remains authoritative regardless of how the principal was authenticated or provisioned.

Use [Audit events](/docs/security/audit) to investigate security-sensitive changes and correlate them with governed query activity.

## Look up exact contracts

Use the generated [Environment variable reference](/docs/configuration) for authentication and security settings, the [Access API reference](/docs/api/access) for principals and grants, the [Current User API reference](/docs/api/current-user) for sessions and user tokens, and the [Audit API reference](/docs/api/audit) for event operations.
