# MFA security decision boundary

Status: accepted on 2026-08-25.

LeapView does not implement application-managed multi-factor authentication in the current authentication model. Deployments that require MFA for people must enforce it at the configured OIDC or Entra identity provider with provider-side conditional access. Local password authentication is not an MFA-capable production profile; retain it only where single-factor access is explicitly accepted or as a controlled, monitored break-glass path.

This is a deliberate fail-closed scope decision. It is not a claim that an upstream login satisfied MFA, and LeapView does not currently authorize sensitive operations from `acr`, `amr`, or other provider-assurance claims.

## Current boundary

Interactive browser authentication has two entry paths:

- OIDC verifies the authorization response, issuer, signature, state, and nonce, resolves the stable issuer-subject identity, and creates a browser session.
- Local authentication verifies the stored password credential and creates the same kind of browser session.

The durable browser-session record binds a token to a principal and expiration. It does not record authentication method, authenticator identity, assurance level, authentication time, or a step-up deadline. Desktop sessions add instance, profile, client, idle, and absolute-lifetime boundaries, but do not add authentication assurance. CLI authorization sessions and API tokens are separate credential classes.

Authorization therefore receives an authenticated principal, not evidence that a particular factor combination was completed. Adding a TOTP prompt only to local login would leave OIDC callbacks, desktop completion, session restoration, recovery, and sensitive-operation step-up outside a coherent assurance model.

## Why minimal local TOTP is rejected

A secure MFA implementation is a lifecycle and authorization feature, not an extra form field. A minimal TOTP secret and challenge would leave material gaps:

- no recent-authentication proof and confirmation ceremony for enrollment or removal;
- no encrypted authenticator storage, key rotation, device naming, or multiple-device lifecycle;
- no single-use recovery-code storage, recovery ceremony, or recovery notification;
- no challenge replay protection, clock-skew policy, attempt budget, or distributed rate-limit contract;
- no assurance attributes on browser, Desktop, or CLI sessions;
- no step-up policy for credential, principal, grant, token, or security-setting changes;
- no session revocation rule after authenticator reset or administrative recovery;
- no complete audit vocabulary for enrollment, challenge, recovery, reset, and factor removal.

Shipping only the challenge would create a bypassable control and could encourage operators to claim an assurance level the application cannot prove. The current architecture is safer when it names the limitation and delegates MFA to a mature external provider.

## Production operating decision

For a production deployment that requires MFA:

1. Use OIDC or Entra for human browser authentication.
2. Enforce phishing-resistant MFA or the organization's approved factor policy at that provider.
3. Apply provider conditional access to the exact LeapView client and intended users.
4. Verify the policy with a non-administrator account and preserve provider sign-in evidence alongside LeapView audit events.
5. Keep any local break-glass account individually attributable, vaulted, monitored, tested, and rotated after use.

Do not describe local password login as MFA. Do not treat API tokens, service-principal credentials, or a second network hop as a second human factor. If the application itself must cryptographically require and inspect a provider assurance value, the current release does not meet that requirement.

## Requirements before application-managed MFA

Future implementation starts only after the session and recovery boundaries are designed together. The minimum architecture must include:

- WebAuthn/passkeys as the preferred phishing-resistant authenticator, with an explicit decision on any TOTP fallback;
- durable authenticator identity and lifecycle records with protected secret material where applicable;
- recent-authentication enrollment, confirmation before activation, named devices, and safe factor removal;
- single-use hashed recovery codes and an attributable administrative recovery process;
- replay-safe challenges, bounded attempts, clock handling where applicable, and production rate limits;
- session evidence containing authentication method, assurance level, authentication time, and step-up expiry;
- one issuance policy shared by local browser, external browser, Desktop, and CLI completion paths;
- exact issuer-specific rules for accepted `acr` or `amr` values if upstream assurance is consumed, with missing or unknown values failing closed;
- step-up requirements for security-sensitive operations rather than only at initial login;
- revocation of affected interactive sessions after factor reset, recovery, or removal while keeping machine credentials an explicit independent class;
- audit events and operator notifications that contain stable identifiers but no authenticator secrets or recovery material;
- accessible enrollment, challenge, recovery, and device-management flows with tested browser and multi-device behavior.

Acceptance requires bypass tests across every session-creation path, recovery and factor-reset tests, replay and rate-limit tests, provider-claim tests, direct authorization tests for step-up operations, and an operational runbook. A future implementation must update this decision instead of silently weakening it.

## Revisit triggers

Revisit this boundary when a supported deployment requires local users with MFA, LeapView introduces step-up authorization for sensitive actions, or a provider-assurance contract must be enforced inside the application. Until then, external provider enforcement is the supported MFA boundary.

See [OIDC](/docs/security/oidc), [Local authentication](/docs/security/local-auth), and [Roles, grants, and policies](/docs/security/authorization) for the surrounding identity and authorization controls.
