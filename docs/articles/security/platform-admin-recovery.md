# Platform administrator lockout recovery

`leapview admin access recover-platform-admin` is a production-only,
deployment-authorized emergency path. It restores the durable
`platform_admin` role to one existing, enabled user principal when the normal
platform-administrator surface is unavailable.

By default the command changes authorization only. For a complete lockout in
which the existing local administrator password is unavailable, apply can also
replace that principal's existing local password from an owner-only file. The
command never creates principals, issues tokens or sessions, or changes an
external identity provider. External IdP recovery remains provider-owned.

## Preview the exact target

Obtain the principal UUID and email from a trusted control-plane inventory. Use
a new UUID for each recovery attempt and retain it with the incident record:

```sh
leapview admin access recover-platform-admin \
  --principal-id <existing-user-uuid> \
  --expected-email <exact-user-email> \
  --operation-id <recovery-operation-uuid> \
  --acknowledge-offline-recovery
```

Preview is read-only. Confirm the JSON reports `mode: "preview"`, the exact
email and principal ID, and the expected `alreadyAdmin` state. Copy
`previousRevision` exactly; it is the compare-and-swap value for apply. The
output contains only bounded recovery evidence (identities and revisions),
not PostgreSQL URLs, passwords, tokens, or other secret material.

## Apply once, then verify

Run apply with the same target identity and operation ID, and with the exact
revision returned by preview:

```sh
leapview admin access recover-platform-admin \
  --principal-id <existing-user-uuid> \
  --expected-email <exact-user-email> \
  --operation-id <recovery-operation-uuid> \
  --expected-revision <preview-previousRevision> \
  --acknowledge-offline-recovery \
  --apply
```

The role grant, idempotency record, and `platform_admin.recovered` audit event
commit in one PostgreSQL transaction. A changed revision fails closed; run a
new preview and use a new operation ID. Repeating the exact apply command is
safe and returns the durable operation result instead of granting a second
binding.

## Recover an unavailable local credential

Use this only for an existing enabled user that already has a local
credential. Put a policy-compliant replacement password in a regular file
owned by the operator, with no group or other access. The file may contain one
trailing newline:

```sh
umask 077
printf '%s\n' '<vault-generated-replacement-password>' > ./recovery-password
chmod 600 ./recovery-password
```

Local authentication must be enabled for the recovered credential to be
usable. If the deployment normally uses only an external IdP, enable
`LEAPVIEW_LOCAL_AUTH=true` through the deployment's controlled configuration
for the recovery window. Then use the same preview/apply sequence and add the
credential controls only to apply:

```sh
leapview admin access recover-platform-admin \
  --principal-id <existing-local-user-uuid> \
  --expected-email <exact-user-email> \
  --operation-id <recovery-operation-uuid> \
  --expected-revision <preview-previousRevision> \
  --local-password-file ./recovery-password \
  --acknowledge-offline-recovery \
  --acknowledge-credential-reset \
  --apply
```

The command rejects symlinks, non-regular files, files larger than 4096 bytes,
and files readable by group or other users. Password replacement, session
revocation, the platform-role grant, and the audit event share the same
PostgreSQL transaction. Neither JSON output nor audit metadata contains the
password. Delete the file after securely transferring the value, sign in,
change the temporary password immediately, repair the normal IdP path, and
restore the intended authentication configuration.

If the target has no local credential, the command fails closed. Recover the
external IdP account or select an existing enabled local principal; never edit
credential or role tables directly.

After a successful apply, sign in through the configured authentication path,
verify platform administration, and review the corresponding audit event.
Follow [Local authentication](/docs/security/local-auth) for local break-glass
controls and [Authentication and authorization](/docs/enterprise-auth) for
identity boundaries.
