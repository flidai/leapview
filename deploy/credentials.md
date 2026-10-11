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

This connection editor manages an existing active project. First publication
uses the separate admitted-source page described below. Endpoint/user changes
and historical version retirement are separate operations. The standard project
administrator, editor and initial publisher roles do not include
`connection.manage`; source credential authority must be explicitly provisioned.

## First production source

The first source can be published without a development credential or environment
password. Claim the project, configure customer ownership and the private keyring,
and nominate an independent release approver through Access administration. The
source uploader, credential operator and publisher must be the same currently
authorized user. Their reviewer must be a different authorized principal.

Stop the application. Prepare a private JSON admission intent using the
[`FirstSourceAdmissionIntent` contract](../internal/credential/first_source_admission.go).
It names the real instance, project, environment, customer owner, operator,
connection and new binding, and includes the current authorization-policy
revision/digest. `version` is 1 and `expectedBindingRevision` is 0. The complete
PostgreSQL endpoint includes its existing database user, TLS mode and certificate
options. `credentialReference` names its logical project/environment/path/key;
it contains no password. Use a fresh operation UUID and retain this exact file.

Preview and apply against the same stopped instance and maintenance database:

```sh
leapview admin credentials admit-first-source --intent /private/first-source.json
leapview admin credentials admit-first-source --intent /private/first-source.json --apply
```

The command holds the application home lock and the unpublished target fence.
It checks the current owner, project claim, operator and policy, then atomically
records the exact binding and narrowly scoped credential grant with audit.
Replaying the identical applied intent returns its existing result. Conflicting
intent, an existing binding or an already published target fails closed. Preview
does not save a binding, grant or password.

Restart production and sign in as the admitted operator. Open
`/connections/{connection-id}/first-source`. The page shows the admitted endpoint
and user. **Save draft**, select its returned version, then **Test draft**. The
password clears immediately, is never returned in signals, and is never replayed
automatically. The real probe checks the exact destination and current authority.

When the admitted operator is also the claiming user, open **Personal settings →
API tokens** and create an ordinary personal API token with the needed delivery
permissions, the exact admitted connection's `connection.manage` and
`connection.use` permissions, and a short expiry. Use this bearer for the delivery
plan matching the returned `firstSourcePreparationId`. Issuance rechecks the live
browser session, unpublished admission, current claim roles and exact staged
grant. OAuth/device and initial publisher credentials do not gain access to the
customer credential. After publication, the normal permission picker resumes.

Upload the intended source using the same user's authenticated normal delivery
workflow. Enter its retained source digest, attestation digest and the delivery
plan's idempotency key on the first-source page. **Prepare first publication**
returns a preparation ID. Keep that ID and create the normal delivery plan with
`firstSourcePreparationId` set to it and exactly those source identities/key.
Build and publish that plan, request approval and have the independent reviewer
approve it. Neither saving, testing, preparing nor building activates a credential.
The approved worker commits the normal publication and credential journals
together, installs the exact runtime, and opens admission only after completion.

A lost browser response can be inspected with **Recover preparation** using the
same ID. Before commit, **Cancel preparation** drains work and records cancellation;
the consumed validation receipt remains consumed. Test again before preparing a
new operation. If a pending preparation needs fresh validation, test its same
version and **Renew preparation after test**. This does not change its immutable
source or publication intent. A failed or interrupted committed activation stays
closed until startup or the normal activation retry installs that exact committed
generation. After publication, use the existing connection credential editor.

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

Local version retirement is described below. Full-profile restore and installation-specific
key-custody evidence remain separately tracked in D12/D13.

## Inspect and retire a local version

Open the connection's **Credentials** panel, select the saved version and choose
**Inspect version dependencies**. The result identifies retained candidate,
release, activation and configuration references, including historical versions.
A live validation receipt also prevents retirement until it expires. Metadata
inspection requires current connection manage/use authority; it never returns the
password, ciphertext, key material or provider secret references.

**Retire version locally** is available after inspection finds no dependencies.
The command checks current authority and dependencies again under the publication
and version fences, drains provider work, records an immutable `retired_local`
state with its audit, and restores current clients before resuming work. A racing
validation, activation, release or configuration write cannot acquire a retired
version. An interrupted or repeated request can be resolved by inspecting that
same version; retirement is irreversible and saving a new draft is the way to
create a usable version again.

The API uses the same service:

- `GET /api/v1/projects/{project}/targets/{target}/connection-bindings/{connection}/credential-drafts/{version}/status`
- `POST /api/v1/projects/{project}/targets/{target}/connection-bindings/{connection}/credential-drafts/{version}/retire`, with an empty JSON object
- `GET /api/v1/agent/credential-versions/{version}`
- `POST /api/v1/agent/credential-versions/{version}/retire`, with an empty JSON object

Agent operations require current instance settings-update authority. Agent
settings and the configuration API expose the exact credential version ID without
its key. Every retained configuration revision remains a dependency because
resumable runs and configuration recovery select historical versions. Current
and previous native publications likewise remain dependencies even after a
replacement becomes active. The response bounds the dependency list at 100 and
sets `moreDependencies` when additional references prevent retirement. These
commands do not delete retained application history to make a version eligible.

Local retirement does not revoke a credential at its upstream provider, delete
encrypted envelopes, remove backups or release historical key-custody obligations.
The independent database-backup test restores a pre-rewrap PostgreSQL dump into a
separate database and reopens a separately retained key file: the replacement-only
key fails, while the historical key decrypts the selected stored version. This
proves that recovery path in the disposable fixture; an installation still needs
its own custodian, backup identity, protected independent recovery copy and
full-profile restore evidence.
