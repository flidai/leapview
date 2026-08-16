# OIDC

OpenID Connect provides interactive browser identity. LeapView identifies a person by the stable pair of issuer and subject. Email and display name are profile metadata and may change without creating a new identity.

## Before you begin

Choose one trusted OIDC provider, a stable provider ID, and the exact external HTTPS origin served by the trusted reverse proxy. Obtain a test user without administrator privileges and access to both provider and LeapView logs.

Roll out identity in this order:

1. Register one confidential web client with an exact callback URI.
2. Store its secret and configure the complete LeapView OIDC tuple.
3. Configure trusted proxy, host, cookie, CSRF, and clock boundaries.
4. Bind the test principal or provisioned group to a narrow role.
5. Validate configuration and verify login, logout, denial, and audit behavior.

## Register the client

Create a confidential web application in the identity provider. Register the exact public callback URL:

```text
https://dash.example.com/auth/{provider_id}/callback
```

The provider ID must contain only route-safe letters, numbers, dots, underscores, or dashes. Use one stable value; changing it changes the callback route and requires a coordinated provider update.

Configure LeapView:

```sh
LEAPVIEW_OIDC_PROVIDER_ID=entra
LEAPVIEW_OIDC_ISSUER_URL=https://login.microsoftonline.com/<tenant>/v2.0
LEAPVIEW_OIDC_CLIENT_ID=<client-id>
LEAPVIEW_OIDC_CLIENT_SECRET=<client-secret>
LEAPVIEW_OIDC_CALLBACK_URL=https://dash.example.com/auth/entra/callback
LEAPVIEW_OIDC_SCOPES="openid profile email"
```

Production validation requires the issuer and callback to use HTTPS and treats issuer, client ID, client secret, and callback as an all-or-none set. Store the client secret in the deployment secret manager.

## Configure the reverse proxy

Terminate TLS at a maintained trusted proxy and ensure the application sees the correct public scheme and host used by the callback. Set exact allowed hosts. Enable proxy-header trust only when that proxy overwrites client-supplied forwarding headers.

Secure cookies must remain enabled for browser auth. Clock synchronization matters for token and state validation on both LeapView and the identity provider.

## Understand identity mapping

The issuer URL and the token's subject claim form identity. Do not map identity by email alone: email addresses can be renamed or reassigned. Profile changes may update display metadata while privileges remain attached to the same principal.

LeapView intentionally does not treat OIDC group claims as the enterprise group source of truth. Use SCIM for directory users, groups, and membership, then use LeapView grants and role bindings for product authorization.

## Assign access

A successful login can still result in no visible project resource. OIDC proves who the user is; it does not grant product access. Bind a provisioned or known principal/group to an appropriate project role or explicit grant.

Test with a non-administrator user. An owner account can hide missing group provisioning or role binding because it already has broad access.

## Harden the provider

Use provider MFA and conditional access. Restrict who may use the client, protect client-secret rotation, and monitor provider-side sign-in events. Keep redirect URI lists narrow and remove retired callbacks.

When rotating the client secret, install the new value through the secret manager and coordinate restart without exposing either value. Verify login before revoking the old provider credential where the provider supports overlap.

## Validate the configuration

Start LeapView with production validation enabled before changing traffic. It must reject partial OIDC configuration, non-HTTPS production issuer or callback URLs, malformed provider IDs, and insecure cookie boundaries. Compare the configured callback with the provider registration character for character, including path and provider ID.

Validate that the reverse proxy overwrites forwarding headers, the application trusts only that proxy, and the public host is explicitly allowed. Keep the previous authentication path available to an operator until the new flow is verified.

## Verify identity and access

Use the non-administrator test account to complete these checks:

1. Sign in through the provider and confirm the expected issuer-subject principal is used.
2. Verify only explicitly granted projects, resources, and actions are available.
3. Sign out and confirm the browser session is no longer accepted.
4. Remove or suspend access and verify the next authorization check denies it.
5. Inspect audit and provider logs for the same event without raw tokens or secrets.

Repeat a login after changing only profile metadata such as display name; it should update the same identity rather than create a new one.

## Troubleshooting

If the provider rejects the request, compare the callback URL character for character. If callback reaches LeapView but state or cookie validation fails, check HTTPS, cookie security, allowed hosts, CSRF key consistency, proxy headers, and clock skew. If login succeeds but access is empty, inspect principal identity and grants rather than the OIDC handshake.

Preserve correlation timestamps and inspect both provider logs and LeapView audit/application logs without recording raw tokens.

## Next steps

Configure [SCIM provisioning](/docs/security/scim) for directory lifecycle, review [Roles, grants, and policies](/docs/security/authorization), and complete the broader [Authentication and authorization](/docs/enterprise-auth) checklist.

The configuration above shows the workflow. Use the generated [environment variable reference](/docs/configuration) for the complete OIDC, proxy, host, cookie, and production-validation contract.
