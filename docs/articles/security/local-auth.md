# Local authentication

Local authentication supports self-hosted browser login and a controlled break-glass path. Local users are administrator-created; LeapView does not provide public self-registration.

Local authentication is password-only. It is not an MFA-capable production profile. Deployments that require MFA for people must use an external provider that enforces it; see the [MFA security decision boundary](/docs/security/mfa-boundary). Do not retain a local account outside the documented break-glass controls and represent it as satisfying MFA.

## Enable the mode

Configure local auth with production security requirements:

```sh
LEAPVIEW_PRODUCTION=true
LEAPVIEW_LOCAL_AUTH=true
LEAPVIEW_CSRF_KEY=<at-least-32-character-random-secret>
LEAPVIEW_COOKIE_SECURE=true
LEAPVIEW_PUBLIC_URL=https://dash.example.com
LEAPVIEW_ALLOWED_HOSTS=dash.example.com
```

Validate the complete environment:

```sh
leapview config validate --production
```

The CSRF key protects CSRF state and OAuth state cookies. Store it in the deployment secret manager. Rotating it can invalidate security state and should follow a controlled maintenance procedure. Local sign-in and password forms are capped at 8 KiB, and authentication responses are marked `no-store`.

## Initialize the first administrator

Before the server starts for the first time, set `LEAPVIEW_BOOTSTRAP_ADMIN_EMAIL` and run:

```sh
umask 077
leapview admin initialize --format json > initial-credentials.json
leapview admin initialize --acknowledge-credentials
```

The one-shot offline initializer atomically binds the instance environment and creates a platform administrator with a forced-change temporary password and an initial project-claim token that expires after 24 hours. It does not start an HTTP server or create an unrestricted bootstrap token. Until acknowledgement, rerunning the initializer returns the same credential bundle so an output-delivery failure is recoverable. After acknowledgement, a second initialization attempt fails.

`bootstrap-project` exchanges the claim for a publisher scoped to the claimed project. The publisher can establish the initial project policy and stage data before the administrator changes the temporary password. This exception requires privately recorded issuance provenance; naming an ordinary token like an initial publisher does not grant it. Acknowledging the publisher handoff revokes the claim while allowing the publisher to finish data staging.

The first successful password change or reset permanently ends this publisher exception. A later administrator reset blocks that publisher through the normal password-change gate; exchanging or rotating credentials cannot restore the exception. After the password requirement is satisfied, the publisher remains subject to its normal scope and expiry. Existing installations without recorded initial-setup provenance must complete the normal browser password-change flow.

Initial publisher metadata cannot be edited through generic token management. While the claim remains usable, retry the explicit exchange to replace the publisher. Generic rotation produces an ordinary token without the setup exception. Use separately scoped credentials for ongoing publication.

The generic Compose controller and Hetzner provider recipe wrap this command and expose the result once through `leapviewctl first-login`, which deletes the credential file after printing it.

## Create local users

A principal with grant-management authority can create a local user through the Admin / Users surface or `POST /api/v1/principals`. The response returns a temporary password once. Deliver it out of band and require the user to replace it at first sign-in.

New local passwords must contain at least 12 Unicode characters and no more than 1024 UTF-8 bytes. LeapView also rejects passwords found in a version-pinned offline corpus of common and breached credentials, including case variants. The check uses only embedded one-way hash prefixes: no password candidate or hash is sent to an external service. Passwords remain opaque values and are never trimmed or normalized before storage. A successful change revokes the principal's browser, desktop, CLI authoring, and MCP OAuth sessions; personal API tokens remain independent credentials and must be rotated or revoked separately when their exposure is suspected.

Do not place temporary passwords in tickets, chat rooms, shell history, deployment output retained broadly, or automation variables. If delivery is uncertain, reset the password instead of forwarding the same value again.

## Prepare the first independent reviewer

A protected target needs a reviewer before its first publication. After claiming the project and changing the initial password, the original claiming administrator can use Admin / Users to assign **Release approver** to a different active user. Do this before inspecting or building the candidate so its immutable authorization snapshot includes that reviewer.

This initial nomination is available only through the claiming administrator's authenticated browser session, while the canonical owner binding remains present and the target has no active publication. It can grant only the exact Release approver role to another user. API tokens, other platform administrators, self-nomination, group assignments and broader roles do not receive this authority. The owner does not acquire approval permission.

Nomination uses the normal short-lived grant administration envelope, policy revision check and audited role-binding mutation. A PostgreSQL target lock serializes it with first activation, including on a target that has not yet created its first plan. After activation, ordinary current-project grant authority applies. The reviewer signs in, changes their temporary password and approves the exact candidate with a separately scoped credential; independent approval is still required.

### Stage an explicit workload grant

The default owner and publisher roles do not include pipeline execution. An installation operator with local control-plane access can stage an exact resource grant when a workload requires that additional authority:

```sh
leapview admin access stage-grant \
  --project project:example --id refresh-orders \
  --principal PRINCIPAL_ID --kind pipeline --resource pipeline:orders \
  --action pipeline.run --expected-revision 4 --operation-id refresh-orders-v1
```

The command previews by default. Add `--apply` to persist the grant and its audit event together. It checks the immutable Project claim, bound environment, active recipient, and expected policy revision. Reuse the operation ID only for an identical retry. The command accepts exact, delegable resource actions; it cannot create project-wide or future-resource grants, and it does not impersonate an application user or issue a credential.

Staging changes only the target-owned policy. Plan and publish a candidate with that policy revision, obtain independent approval, and activate it before the grant becomes runtime authority. Admission verifies the resource against the candidate's graph. After activation, the recipient may issue a token limited to that exact permission. Existing grants retain their recorded representation; legacy capabilities do not implicitly become typed permissions.

## Reset access

Use `POST /api/v1/principals/{principal}/password-reset` to issue a new temporary credential. LeapView never reveals the previous password. A reset forces a password change, revokes interactive sessions, and produces an audit event.

When a principal is no longer trusted, deactivate the principal and revoke sessions and API tokens rather than only resetting the password. Review direct grants and group membership as part of offboarding.

## Authorization behavior

Local users map to ordinary user principals. They use the same sessions, roles, grants, API tokens, data policies, and audit events as OIDC or SCIM identities. Authentication proves the principal; it does not grant project-resource access by itself.

Local groups use `provider: local` in the runtime access model and can be represented in project access declarations. Prefer group-based project bindings for teams and direct user grants only for exceptions.

## Operate a break-glass path

If policy requires emergency local access alongside OIDC:

- keep the account individually attributable;
- store recovery material in an access-controlled vault;
- test sign-in and required privileges on a schedule;
- alert on any use;
- rotate the password or token after an incident;
- review all actions in the audit trail;
- do not embed the credential in automated health checks.

A shared permanent owner password is not a break-glass design. It removes attribution and tends to escape controlled storage.

## Review checklist

Confirm secure cookies, allowed hosts, TLS, rate limiting at the trusted edge, strong secrets, temporary-password delivery, session revocation, and audit retention. Use [Authentication and authorization](/docs/enterprise-auth), [Roles, grants, and policies](/docs/security/authorization), and the generated [environment reference](/docs/configuration).
