# Local authentication

Local authentication supports self-hosted browser login and a controlled break-glass path. Local users are administrator-created; LeapView does not provide public self-registration.

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

The one-shot offline initializer atomically binds the instance environment and creates a platform administrator with a forced-change temporary password plus a privilege-restricted publisher token that expires after 24 hours. It does not start an HTTP server or create an unrestricted bootstrap token. Until acknowledgement, rerunning the initializer returns the same credential bundle so an output-delivery failure is recoverable. After acknowledgement, a second initialization attempt fails.

The generic Compose controller and Hetzner provider recipe wrap this command and expose the result once through `leapviewctl first-login`, which deletes the credential file after printing it.

## Create local users

A principal with grant-management authority can create a local user through the Admin / Principals surface or `POST /api/v1/principals`. The response returns a temporary password once. Deliver it out of band and require the user to replace it at first sign-in.

Do not place temporary passwords in tickets, chat rooms, shell history, deployment output retained broadly, or automation variables. If delivery is uncertain, reset the password instead of forwarding the same value again.

## Reset access

Use `POST /api/v1/principals/{principal}/password-reset` to issue a new temporary credential. LeapView never reveals the previous password. A reset should force a password change and produce an audit event.

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
