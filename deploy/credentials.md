# Customer credential lifecycle and recovery

Connection and administrator-managed agent credentials use immutable logical
versions in the shared encrypted PostgreSQL store. Their encryption keyring lives
in a private runtime file outside that database. Saving or probing a version does
not make it active. Source credentials require the native candidate/publication
path; agent credentials require an immutable configuration revision. Both use the
same durable activation coordinator and admission barrier.

This is implementation guidance for the single-process lifecycle. It does not
accept the proposed [ADR-0027](../adr/0027-separate-credential-storage-activation-and-retirement.md),
qualify a managed deployment profile, or establish that an installation has an
independent key recovery copy. Keep those review and qualification gates explicit.

## Setup and the format boundary

1. Initialize the instance and retain its stable identity. Customer ownership must
   be declared explicitly; platform administration alone is not ownership.
2. Provision a private `credential-keyring-v1` file bound to that exact instance
   identity. Set `LEAPVIEW_CREDENTIAL_KEYRING_FILE`; mount it read-only for the
   runtime with the file ownership and permissions required by the keyring loader.
   Never place key bytes in source, PostgreSQL, command arguments, or public logs.
3. Stop the application and confirm its exit. Run `admin credentials setup --owner`
   against the same instance home and control database. The command takes the same
   home lock as the application, verifies initialization and key identity, and
   does not invent missing encryption keys.
4. Start the application with that keyring. Enter, validate, and explicitly activate
   customer credentials through the authorized lifecycle.

`LEAPVIEW_AGENT_CREDENTIAL_KEY` is an unused legacy setting. The new agent manager
cannot read its ciphertext. Legacy rows remain intact and fail with setup guidance;
there is no automatic import, dual reader, or destructive reset. Preserve the old
release, database, and recovery keys before crossing this format boundary. A
rollback across that boundary needs its separately reviewed recovery procedure.
Deployment-funded provider configuration remains operator-owned and is not
silently imported during the first administrator save.

## Activation and interrupted work

Connection activation endpoints accept an exact version, validation receipt,
expected binding revision, and caller-generated operation UUID. The operation is
reserved before candidate construction. Inspect that operation when a response is
lost. Before commit, retry requires a fresh successful validation of the same
version/configuration and current authority. After commit, retry installs only the
committed replacement. Abort is allowed only before commit.

For an existing enabled PostgreSQL connection, open **Credentials** on its
connection page. The control is available when the instance has configured
customer credential services and the caller can manage the connection. Save a
password draft for the connection's existing user and endpoint, select the saved
draft, then **Test draft**. Testing requires both manage and use authority.
**Prepare activation** builds its candidate without claiming that the credential
is in use. Test again when fresh validation is requested, then explicitly
continue activation. Only the server's completed and ready result confirms use.
The password field clears on submission and is never returned in page signals.

Retain the displayed operation ID before leaving the page. After a lost response
or reload, **Recover operation** reads that exact operation; it does not create
a replacement. Recover a committed operation to finish installation, or cancel
an uncommitted operation before choosing another draft. A completed operation
whose readiness has not yet been confirmed can also be recovered. Metadata reads
and explicit retries recheck current authority. Password submissions are never
automatically replayed.

This browser path manages credentials for an existing connection and active
project. It does not bootstrap the first production source password, change the
connection's endpoint or user, or retire historical credential versions.

Agent Settings uses the same lifecycle behind **Test connection** and **Save and
activate**. Testing the same pending proposal produces fresh validation for its
existing version and operation. **Cancel pending change** restores readiness of
the saved configuration before abandoning an uncommitted operation. A committed
change cannot be canceled; recover it or make another validated change.

Before commit, retry and cancellation belong to the original operation actor and
require that actor's current authority. Another administrator cannot take over a
pending operation. If the original principal was deactivated, restore its
authorized access through the normal access-management process before retrying
or canceling. Committed startup reconciliation uses the durable replacement and
current instance authority without requiring the original actor's session.

A durable committed pointer is not runtime readiness. The barrier remains closed
on uncertain commit or failed installation, and each process starts closed before
reconciling durable state. Provider cancellation is not evidence of cleanup: the
consumer must return and release its lease. Historical runs resolve their exact
configuration/version/destination; the newest key is never substituted.

## Key custody, rotation and restore

For each installation, retain a private operator record naming the custodian,
encrypted recovery-copy location, independent wrapping-key/access procedure,
restoration test date, stable instance identity, and supported backup/PITR window.
This repository deliberately does not fill those values with invented evidence.

Stop the application and confirm its exit. Install a keyring with a fresh active
write key and old keys retained for decryption, then run against the same instance
home and control database using independent maintenance database credentials:

```sh
leapview admin credentials rotate --batch-size 100
```

The command takes the application's home lock, checks durable instance identity
and all stored key commitments, and rewraps at most 100 envelopes (the accepted
batch size is 1–100). Its JSON result reports `rewrapped`, `complete`, and
`activeWriteKeyId`. Repeat the same command until `complete` is true; a full batch
may require one final empty invocation. If interrupted or an envelope fails, keep
the application stopped, correct the reported prerequisite, and rerun. Previously
committed envelopes are skipped. Each envelope update uses its expected revision
and appends a redacted audit record atomically. Encryption budget is consumed
independently even when that update fails.

Rewrapping preserves logical versions, their original authenticated bindings and
historical references. The command neither edits the keyring nor deletes keys.
Keep old keys for every supported database backup/PITR point even after current
rows use the new key. No automatic key deletion or upstream provider revocation
follows from local activation or rewrapping. This bounded maintenance operation
does not implement version retirement or prove an independent recovery copy.

Restore requires the selected database, its matching independent keyring copy,
and the same authenticated instance identity, with the old writer fenced. A
missing or mismatched key fails closed and must never trigger key regeneration
over existing encrypted records. Use a fresh write key, invalidate restored
validation authority, and establish current publication/configuration and provider
readiness before opening traffic. Database recovery cannot undo an upstream
credential revocation. A component test or a current-key health check alone does
not prove retained-backup decryption or installation-specific key custody.

Version retirement must account for retained serving/configuration history,
rollback references and resumable work, drain its consumers, and deny later use.
Do not treat activation of a replacement, encryption rewrapping, or elapsed time
as permission to delete a logical version or recovery key. Record any remaining
retirement and restore qualification gaps in D12/D13 until verified.
