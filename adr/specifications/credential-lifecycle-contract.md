# Credential lifecycle contract

Status: proposed; D02 foundation and single-process component assumptions; D12 production lifecycle pending

Date: 2026-09-27

Governing proposal: [ADR-0027](../0027-separate-credential-storage-activation-and-retirement.md)

Historical research: [Step 1 inventory](credential-lifecycle-inventory.md), application
`8283839939dd630e2a1874927c62fdb8ccadfa8d`, and [PR #744](https://github.com/flidai/leapview/pull/744)
at `ac6b0986d52771776535b74e250774a94aecbe91`. Existing-code links are reuse points,
not evidence that the new contract is already implemented.

Current decision reference (1 October 2026): [PR #744's proposed ADR-0028](https://github.com/flidai/leapview/blob/b305bcc616892538a27bd3d255fe0b4e9fa66dff/adr/0028-share-an-open-deployment-stack-for-self-hosted-and-managed-leapview.md#decision-reconciliation-and-acceptance-boundary).
Its candidate-first managed deployment contract remains unreconciled with the
single-process lifecycle below. Historical research pins elsewhere in this
document remain evidence for their individual checkpoints, not current decisions.

### Delivery scope and shared decisions

[D02 / FAI-1019](https://linear.app/flid/issue/FAI-1019) owns PR #785's
credential ADR and foundation. [D12 / FAI-1029](https://linear.app/flid/issue/FAI-1029)
owns production activation, remaining consumers, runtime installation/restart,
rotation, retirement and key/backup recovery. D02 is Anand-owned; Ganesh owns D12.

The [foundation scope review](credential-foundation-review.md) records the retained
foundation, installed denial dependencies and preserved follow-up implementation.
The implementation checkpoints formerly appended below are preserved at
[059f61ff2](https://github.com/flidai/leapview/blob/059f61ff22601d04edbb0a11cf74c800287973b7/adr/specifications/credential-lifecycle-contract.md).
They are historical evidence, not a claim that those components remain installed.

Sections 1–8 propose the broader lifecycle contract; section 9 describes the
installed foundation. The proposal remains subject to maintainer review. In
particular, D01/D11 must resolve the single-process assumptions against Kamal's
candidate-first overlap before D12 composes activation. D10 must reuse the same
authorized, audited secret service for customer-secret bootstrap. Readiness,
saved drafts and successful validation grant no activation or retirement authority.

## 1. Scope boundary

Implement customer connection and customer-owned instance-agent credentials.
Deployment/bootstrap secrets and verification-only authentication retain their
separate ownership. Preserve the existing optional Infisical resolver's explicit
selection, scope and exact-version rules without adding an import framework.

V1 ownership decision (2026-09-27): persist one customer
owner declaration during instance setup. The scope resolver reads that declaration; secret-input requests
cannot choose an owner. Customer connection and agent credentials bind to it.
Platform-funded provider keys stay in deployment configuration. Do not infer the
customer owner from the administrator, Project ID, instance ID or an external
secret-store reference, and do not add per-provider-account ownership management.

There is no backward compatibility requirement: no legacy encryption adapter,
historical-data conversion, dual-read/write rollout, deprecated endpoint behavior
or image downgrade across an incompatible credential-format transition. Releases
within a declared compatible window still retain usable keys, versions and
acknowledged writes. New agent configuration revisions reference the new
credential store directly. Change callers and schemas together. Unsupported old
credential state fails with setup guidance; this proposal does not authorize
wiping an existing database. Previously saved agent credentials/configurations
and runs that require the old format are unavailable in the new release; setup
requires re-entering credentials and creating new configurations/publications.
Do not repoint old runs, mutate their evidence or discard unrelated data. Any
reset/removal is a separate explicit operation. New-format history, publications,
runs and backups still have their normal integrity and recovery requirements.

The foundation's component model assumes one app process, colocated River workers
and no overlapping deployments; this is not the managed target's release contract.
Reuse the [instance lock](../../internal/app/cli/serve.go#L71), with the same host
home/lock path shared across launches. Stop the old process before starting its
replacement; do not treat a DB lease timeout as proof of process death. A second
host or worker process is outside this profile. All credential consumers and
mutations must pass through this process/service; deployment qualification must
prove that container layouts and direct CLI paths cannot bypass that condition.
Online CLI commands call the running service; offline bootstrap/maintenance
commands require the app stopped and take the same instance lock. These limits
do not establish Kamal overlap safety or a qualified production lifecycle. The
activation, retirement and recovery sections below describe the proposed model
pending the shared decisions above, not completed D02 behavior.

## 2. Small data model

Use existing resource, publication, configuration, permission and audit records.
Add only lifecycle data that cannot be derived from them.

| Data | Contract |
|---|---|
| Credential version | Opaque ID; declared owner; stable deployment ID; exact target/project/environment/binding or instance-agent scope; purpose; provider/destination; creation metadata; retired flag. Logical value and scope are immutable. |
| Encrypted value | Version FK; format; key ID; nonce-prefixed ciphertext; envelope revision. Replace only for key rewrapping with expected-envelope-revision CAS. Never overwrite the logical secret in place. |
| Validation | Random receipt ID; version; actor/authority; canonical destination and non-secret configuration digest; expected binding/config/publication revision; expiry; consumed operation. |
| Change operation | Operation ID; exact version and receipt; expected predecessor; intended publication/configuration; initiator; phase; committed identity; bounded failure reason. One unfinished activation/retirement at a time for the instance. |
| Encryption budget | Key ID and consumed encryption reservations; no key bytes. |

An explicitly selected Infisical version retains its canonical external reference
and exact provider version, with no local encrypted payload. New PostgreSQL
resolver evidence uses the logical version ID, not a ciphertext digest.
Derive dependencies by querying serving/candidate/rollback references and agent
configuration/run history; do not add a generic dependency graph or holder table.
Lifecycle transitions and redacted audit commit in the same PostgreSQL transaction.

## 3. Encryption and inputs

Use AES-256-GCM through Go's `cipher.NewGCMWithRandomNonce`, a random 32-byte key,
and its nonce-prefixed payload. See [Go AES](https://pkg.go.dev/crypto/aes#NewCipher)
and [random-nonce GCM](https://pkg.go.dev/crypto/cipher#NewGCMWithRandomNonce).
Authenticate a fixed-order, length-prefixed tuple of format, deployment, owner,
resource scope, purpose, provider/destination, version ID and key ID. Reconstruct
that context from authoritative records. Reject unknown formats, missing keys,
wrong scope and authentication failure before releasing plaintext. Typed secret
bundles are limited to 16 KiB, without rewriting the submitted secret bytes.

The protected versioned keyring file names a stable deployment ID, active write
key and retained decrypt-only keys. Key IDs never rebind to different bytes.
The encryption budget stores a SHA-256 commitment to each random encryption
key, with unique material per deployment. Reservations reject changed material
under an existing ID and material reused under a new ID. These commitments are
internal key metadata, never digests of submitted customer credentials. Startup
requires keys for all retained envelopes and verifies configured keys against
recorded commitments before exposing the draft service.
Check ownership/permissions and prevent symlink substitution; replace atomically.
Operators provision initial keys only for an empty credential store;
existing state requires its named keys. Do not place keys in PostgreSQL or argv.

Provision the file using the operator's secure provisioning tooling. The format
below is a template, not a usable keyring; replace both placeholders before setup:

```json
{
  "format": "credential-keyring-v1",
  "deployment_id": "<durable instance ID from GET /api/v1/instance>",
  "active_write_key_id": "key-1",
  "keys": [
    {
      "key_id": "key-1",
      "key_base64": "<standard base64 of 32 cryptographically random bytes>",
      "state": "active_write"
    }
  ]
}
```

Exactly one key must be `active_write` and match `active_write_key_id`; retained
keys use `decrypt_only`. Key IDs and key material must each be unique. The file
must be regular, owned by root or the process user, and have mode `0400` or `0600`.
Neither the file nor any directory component of its configured path may be a
symlink. Mount the same protected file for offline setup and `serve`. Record the
instance ID before stopping the app; setup requires the app stopped under the
same home directory and instance lock.

Reserve an encryption count in PostgreSQL before every encryption, including
rewrapping and failed saves; a consumed reservation is never reused. Stop at a
conservative 2^31 uses per key, below Go's 2^32 bound. Restoring a database requires
a fresh write key so a restored counter cannot reset a retained key's budget.
This is one counter per key, not a distributed key-management service.

Secret inputs arrive in authenticated bodies or protected stdin/files. No
plaintext read/export API, logging, telemetry or browser replay of secret forms.
Read responses, jobs and audit contain IDs and authorized non-secret metadata/status only. Browser
commands retain CSRF protection and the same service authorization as the API.

Save is a draft-only operation: it creates an immutable version but cannot test,
publish or activate it. Do not route secret-bearing bodies through the existing
[raw-body SHA-256 replay digest](../../internal/app/api/protocol/protocol.go#L722).
Declare this new command non-replayable in its generated contract and browser
route; a supplied idempotency header must not enable body hashing. Preserve auth,
CSRF, limits and audit. A lost save response may leave a duplicate unactivated
draft on explicit resubmission; document that behavior and return/list draft IDs.
Do not automatically retry or claim exactly-once save. Validation also uses the
non-replayable policy: its five-minute receipt must not be served from the
24-hour replay store. Each explicit retry probes again and may create a new
receipt. Activation will take saved IDs and non-secret fields; its eventual
replay contract must distinguish historical results from current runtime status.
This requires checking both API and
[browser middleware](../../internal/app/api/protocol/protocol.go#L177).
The [generator](../../pkg/apigen/typespec/src/on-emit.ts) retains required replay
for ordinary POST commands. An explicitly authored `nonReplayable: true` command
emits `idempotency: "forbidden"`: synchronous authenticated JSON POST with required
transactional audit and no declared `Idempotency-Key` parameter. API and browser
transports reject any supplied idempotency header before reading the body,
including an empty header. Do not misuse the existing handler-owned
transactional-replay bypass to claim a guarantee this draft-only operation does
not provide.

The browser route binds one generated forbidden command in server code and
checks the caller's operation claim against it. It must use the dedicated
non-replayable middleware under session authentication, CSRF and ingress limits,
with current exact-resource authorization. Do not mix replay-required and
replay-forbidden commands on the same body-dispatched route. Its generated action
sends no idempotency key and sets `retry: 'never'`, `retryMaxCount: 0` and
`openWhenHidden: true`: the vendored Datastar runtime otherwise retries network
errors or resubmits when tab visibility changes. Impose a tight encoded-request
limit before JSON decoding in addition to the service's 16 KiB credential limit.

## 4. Validation and authority

Connection save/retire requires exact `connection.manage`; validation requires
both `connection.manage` and `connection.use`. Publication still requires its
existing independent authority and approvals. Instance-agent changes retain
`platform.settings.update`; status retains existing read permissions. Runtime
resolution needs governed workload authority, not a new general decrypt privilege.

Test the saved version through an isolated probe with the exact destination,
TLS/outbound policy and relevant non-secret configuration; use a 30-second
probe deadline and a five-minute receipt lifetime. Record success only if the
expected revision and actor authority still match. Testing must not activate a
pool; replace the old Test behavior and update callers without a compatibility
route. Changing destination requires explicitly supplied material as a new
version; never forward a kept secret to a different endpoint.

Receipt reservation and operation creation are atomic. Before authority commit,
recheck the actor, exact receipt, freshness and predecessor revision. If any
changed, block and require fresh authorized validation; do not make a durable
activation grant or automatically adopt another actor's proof.

## 5. Activation using existing runtime boundaries

Use one process-local admission barrier and mutation lock for credential-backed
work. Cover normal queries, retained generations, pools and cached clients,
main/resumed agent runs, title calls, background jobs and probes. Existing
publication/pipeline/rollback/configuration paths must use the same mutation
serialization and respect an unfinished operation under the existing target
transaction fence. Reuse pool leases and add bounded in-process tracking only
where missing, especially model calls. No PostgreSQL transaction spans provider I/O.
The administrative status/retry path remains available while work is paused.
Keep credential-free authorization-snapshot reads available: the existing
[resource authorizer acquires a runtime lease](../../internal/access/module/resource_authorization.go#L149),
so do not indiscriminately block every runtime-host acquire. Gate provider work,
not the read-only control-plane authority needed to authorize retry or abort.
Workers encountering the pause must wait or return a recognized retryable
infrastructure outcome before domain failure finalization. The existing
[agent job handler](../../internal/agent/module/jobs.go#L74) can otherwise mark a
valid queued/resumed run failed on an ordinary admission error.

1. **Save and validate B.** A remains authoritative; B is only a draft and receipt.
2. **Prepare.** Create the operation with expected-revision checks. Prepare a
   source candidate pinning B, or an agent configuration referencing B. Candidate
   preparation/probes are bounded and cannot admit ordinary work. Target drift
   invalidates preparation. Concurrent operations cannot reuse the receipt.
3. **Pause and drain.** Persist `switching` before closing normal admission. Hold
   mutation serialization through cutover; drain/cancel admitted provider work
   until no such work remains. Old idle pools/clients may remain until local
   publication, but the barrier must prevent all dispatch through them. A deadline
   or cancelled context is not proof that work ended. If draining cannot be confirmed,
   remain blocked; the operator may stop the entire process and confirm exit.
4. **Commit.** Recheck current authority, receipt and expected predecessor.
   In the existing publication/configuration transaction, commit the exact B
   identity, consume validation, record the operation's committed phase and audit.
   Keep admission closed. Source publication is authoritative; do not add an
   independent active-source-secret pointer. Agent revision and credential
   reference commit atomically, with no plaintext in configuration JSON.
5. **Install and reopen.** Install and check the exact committed runtime; retire
   and await closure of old idle pools/cached clients while admission stays closed.
   Existing runtime-host publication retires the old generation after its durable
   callback, so this completion wait follows commit rather than pretending that
   the current activation API already closes all old handles before commit. Persist
   completion before opening the local barrier. Report `in_use` only while the
   live process is ready for that committed identity; status combines the durable
   result with live readiness. Failure leaves admission closed and status blocked.

Reuse the [publication transaction](../../internal/deployment/postgres/repository.go#L3438),
[runtime-host activation](../../internal/runtimehost/manager.go#L876),
[pool draining](../../internal/analytics/connectionbinding/rotation.go#L585) and
[agent configuration](../../internal/agent/configuration.go#L190) seams.
They require integration and tests; none currently supplies this whole barrier.

Every process start begins closed, before workers or queries can dispatch.
Confirm the previous process exited, load durable publication/configuration,
rebuild clients and check current resource policy. An operation committed to B
can only reload B. Startup readiness has narrowly bounded resource-owned authority
for that committed state; it does not inherit an expired caller token or permit
arbitrary probe/output work. Missing authority or keys blocks readiness.

An unfinished pre-commit operation stays blocked until a current authorized
administrator retries with fresh validation or aborts. Abort cancels candidate
handles, confirms the predecessor is unchanged/authorized, rebuilds its clients,
records abort and then reopens. Request cancellation alone cannot reopen A.
If another publication won while staging, A is no longer the predecessor to
restore: remain closed until an authorized reconciliation loads and verifies
the actual current publication, then records abort. Never republish A implicitly.
After commit, rollback needs a separately authorized compensating publication or
configuration. No new background delegation or cleanup capability is introduced.
A restart never trusts an old `in_use` flag as evidence that clients are ready.

## 6. Retirement and crash outcomes

New-format historical pins remain meaningful: after B activation an authorized
A-pinned generation or resumable run may still acquire A. Never substitute B.
Expose these dependencies next to activation status. Clearing them requires the
existing explicit authority to cancel/retire those resources; do not erase history
to complete a password change.

Retirement serializes with activation/publication, pauses admission, checks all
references and drains local clients. If dependencies remain, report them and
leave A available unless separately disabled. Otherwise atomically mark A retired
with audit, then reopen. Every new dependency, resolve, publication, rollback and
run resumption must reject a retired version under the same serialization. A
retired record cannot be re-enabled; use a new validated version instead.
`retired_local` covers only this LeapView instance, not external clients or
source-side sessions. No upstream revocation is automatic.

| Interruption | Durable state | Recovery / administrator view |
|---|---|---|
| Save response lost | Possibly a saved draft; A unchanged | Inspect drafts; explicit retry may create another draft, never activation. |
| Test response lost or receipt expired | No usable proof for commit | Validate the exact saved version again. |
| Concurrent edit wins | Expected revision no longer matches | Reject stale intent; replan and validate. |
| Drain fails or pre-commit process dies | A remains authoritative; unfinished operation | Block; confirm process exit when needed; authorized retry or abort rebuilds A. |
| Commit succeeds, process dies before install | B committed; operation unfinished | Start closed, load exact B, check readiness, finish operation and open. |
| Local install succeeds, completion write fails | B committed; no completed operation | Remain closed; retry completion for exact B or reload it on restart. |
| Completion persists, response/open is lost | Completed operation; local readiness uncertain | Query current status; every restart rebuilds readiness before admitting work. |
| An A-pinned run resumes after B activation | Historical A reference still exists | Use exact A if authorized and not retired; block A retirement while needed. |
| Retirement crashes | A retired only if its transaction committed | Startup reads the flag; no new use of retired A. |
| Rewrapping crashes | Each envelope is entirely old or new | Resume rows still naming the old key; logical versions unchanged. |

## 7. Key rotation and restore of the new format

Use operator maintenance mode with the app stopped and its exit confirmed. The
maintenance command takes the same instance lock and calls the same encryption
service under operator authority. Install a keyring containing the new write key
and old decrypt-only keys. For each encrypted row, reserve budget, decrypt with
its exact old context, encrypt/decrypt-verify with the new key, then CAS ciphertext,
key ID and envelope revision together with audit. Restart resumes from key IDs;
there are no append-only orphan representations or multi-process keyring acks.
Logical credential IDs and historical configuration references do not change.
Retain old keys until no live row or supported backup/PITR point needs them.

Keep an independent encrypted keyring recovery copy. The deployment runbook names
its operator custodian, storage location, access and wrapping-key rotation steps.
Restore a supported new-format database plus matching keys with old hosts stopped;
use a fresh write key, invalidate validation receipts and open no runtime until
current provider credentials and publication/configuration readiness are checked.
Keep the stable deployment identity needed by authenticated encryption. Restore
cannot reverse an upstream password revocation. Missing keys require explicit
replacement credentials; they cannot recover historical plaintext.

PITR can lose operation/replay rows. Invalidate validation receipts on restore,
so a lost activation record cannot turn an old receipt into permission to execute
again. Retained replay records report historical results alongside current
resource status; they cannot establish live readiness. Never promise all retries
survive restore or silently resubmit a lost activation; inspect current state
and obtain fresh authorized validation for a new intent.
Keep restored clones isolated from provider dispatch until explicitly adopted.
Do not implement backup engines or support restoring the old credential format.

## 8. Implementation order and acceptance

1. **Storage/service foundation:** new schema, scoped encryption/keyring,
   permissions, redacted audit and draft-only save; tests for wrong context,
   missing keys and absence of secret body digests on API and browser paths.
2. **Consumers and activation:** PostgreSQL resolver, agent version references,
   isolated validation, existing publication/configuration integration, local
   barrier and startup recovery. Test every interruption above and every consumer,
   including title calls, queued jobs during the pause, pools and retained-generation
   reads. Prove credential-free retry/abort authorization remains available while
   the barrier is closed, and idle-client closure completes before reopening.
3. **Administration and operations:** shared UI/API/bootstrap commands, truthful
   statuses, dependency-aware retirement, resumable maintenance rewrapping and
   a fresh-host restore drill. Prove same-home process exclusion in each supported
   deployment adapter and reject unsupported formats/topologies.

These are ordered implementation slices after ADR review, not work performed by
this document. Reuse [lost-ack tests](../../internal/deployment/postgres/activate_lost_ack_test.go),
[pool retirement tests](../../internal/analytics/connectionbinding/rotation_retirement_test.go)
and [agent tests](../../internal/agent/configuration_test.go). Run focused red/green
checks, generated-contract checks when contracts change, and `task ci` for code
handoff. No runtime tests or recovery drills are claimed here.


## 9. Foundation integration boundary

The implementation in [internal/credential](../../internal/credential/service.go)
provides encrypted draft saves and metadata listing/reads, with PostgreSQL storage
and same-transaction audit. Connection draft routes use the active Project,
instance-bound target/environment and authoritative target binding. The server
resolves ownership, connector kind and destination digest; the request cannot
override them. Current exact `connection.manage` / `connection.read` permissions
and API credential attenuation are checked by the existing Access authority.
Drafts may be prepared before changing an auth-capable connector's authentication
mode. Metadata remains readable after mode or connector changes; later validation
must bind the exact current configuration before producing any success receipt.

The connection draft API is declared in [credentials.tsp](../../api/typespec/credentials.tsp):

- `POST /api/v1/projects/{project}/targets/{target}/connection-bindings/{connection}/credential-drafts`
  accepts only a `fields` object and returns an immutable version ID, creation time
  and `state: "draft"`. This means **saved**, never validated or in use.
- `GET` on the collection lists authorized metadata, with default limit 25 and
  maximum 100. `beforeVersionId` identifies a saved version within the same owner,
  deployment and resource scope; `nextBeforeVersionId` continues descending
  creation-time/version-ID ordering. Each page requires current authorization.
- `GET` on `/{version}` returns the same metadata for one version. These reads do
  not select ciphertext or decrypt credentials.

Save uses the generated non-replayable policy, rejects every supplied
`Idempotency-Key` before reading its body and never enters durable replay storage.
A lost response requires inspecting the list before explicitly submitting again;
another submission creates another unactivated draft. Strict, bounded JSON
decoding rejects duplicate/unknown fields and returns fixed diagnostics without
reflecting input. No credential browser form, CLI save command or agent tool is
exposed in this slice. Browser transport checks still use a synthetic endpoint.

The offline setup prerequisite now provides `admin credentials setup --owner <id>`.
After normal `admin initialize`, supply `LEAPVIEW_CREDENTIAL_KEYRING_FILE` pointing
to an independently provisioned private `credential-keyring-v1` file. Its
`deployment_id` must equal the already-persisted instance ID (available from the
instance API before stopping the app). The command takes the same instance lock
as `serve`, requires prior initialization, and declares one immutable customer
owner with redacted Access audit in one transaction. It never generates identity
or key bytes. Exact owner replays are no-ops; conflicting owners fail. No secret
material is accepted in argv or returned by this command.

Startup accepts both owner and keyring path absent as unconfigured customer
credential storage. Once either is configured, both are required and the private
keyring must match the durable instance identity. Platform-funded keys remain in
deployment configuration. The legacy agent credential key is not repurposed.

### Validation boundary

The internal [validation service](../../internal/credential/validation.go),
[isolated analytics probe](../../internal/analytics/module/credential_probe.go)
and immutable PostgreSQL validation receipts implement the validation engine.
Its first supported input is a PostgreSQL password draft. Other connectors and
connection-string bundles are rejected; a password cannot override the
server-owned endpoint. The existing target factory already rejects
connection-string bundles on its isolated preparation path.

Validation requires exact `connection.manage` and `connection.use` before
decryption and again after the probe. It compares the saved owner, purpose,
provider and destination with the current server-resolved scope. The caller
must supply the expected binding revision. The receipt records the exact draft
version, actor, binding identity/revision, non-secret configuration digest and
five-minute expiry. The digest must include authentication mode and the probe's
TLS/outbound policy identity, not just the endpoint hash. The probe has a
30-second deadline and must confirm cleanup before returning success. It does
not enter a shared pool directory or modify binding health, revision or active
runtime state. A successful receipt and its redacted audit commit together.
Cancellation or an unconfirmed close fails validation and produces no receipt;
the native close may still be completing after that failure. Before exposing
validation alongside activation, the admission barrier must account for these
isolated probes and their cleanup. A timeout alone is never evidence that their
connections have closed.

The post-probe configuration and permission checks are observations, not an
activation lock. A receipt can become stale immediately after those checks.
It is not an authorization grant: future activation must reauthorize the actor,
compare current scope/configuration/revision and atomically reserve/consume the
receipt with the operation as specified in sections 4–6. A saved receipt alone
never means the credential is in use or the old password can be revoked.

The public API exposes `POST /projects/{project}/targets/{target}/connection-bindings/{connection}/credential-drafts/{version}/validate`
with only `expectedBindingRevision` in its JSON body. The server-owned target
adapter resolves the full binding independently at probe time and binds the
configured TLS/outbound probe policy into the receipt. Validation rechecks live
caller credential evidence, including token revocation and current permission
attenuation or session revocation, as well as current exact-resource grants.
The generated route guard requires `connection.manage`; the service additionally
requires `connection.use` before decryption and after the probe.

A successful response contains only receipt/version IDs, binding revision,
validation/expiry times and `state: "validated"`. It does not change the draft's
stored state or report activation. The command forbids `Idempotency-Key` and uses
`Cache-Control: no-store`; explicit resubmission probes again. A lost response
can leave a committed receipt that was not returned to the caller.

The former pool-promoting `Test` route and browser action are removed without a
compatibility alias. Explicit Refresh retains its existing pool-refresh meaning,
and development profile bootstrap uses that explicit refresh operation. The
browser has no saved-draft selector yet, so it cannot expose the new validation
action truthfully until that UI exists. No runtime activation or receipt
consumption operation is added before its transaction contract is implemented.

Complete equivalent server-owned agent configuration scope before exposing
agent drafts. Do not infer ownership from the input channel or an administrative
role. No ordinary runtime consumer reads these drafts yet.

Before runtime use, complete sections 4–6. No legacy migration, automatic key
generation, activation or rewrapping command is included in the foundation.
Existing agent configuration is unchanged until the clean-break consumer
integration replaces it.

### Installed publication and runtime denial

Ordinary publication retains the composition-owned admission port and exact
local-pin continuity checks. The pending-operation reader runs at READ COMMITTED
after the target-lock wait and rejects every non-aborted operation, including a
committed record. Missing credential schema fails closed. The reader and its SQL
remain unchanged by the scope cleanup.

Active serving and refresh preflight reject local credential pins. No ordinary
runtime consumer decrypts a saved draft. Existing provider-only candidate and
refresh behavior uses its mainline implementation; the customer-credential
runtime factory, foreground resolver and lifecycle machinery are preserved for
D12 rather than composed in D02.

Migrations 048–050 implement foundation storage, owner declaration and validation.
Migrations 051–054 and the matching schema remain unchanged as the durable
operation-state boundary for the installed denial. The reader's minimum is
051/052; 053/054 are retained to avoid rewriting the recorded migration chain and
to preserve denial/constraint coverage for switching and committed states. This
schema exception supplies no phase writer, recovery coordinator or activation API.
Maintainers must review the exception with D02; it is not ADR acceptance.

The former activation access adapter, phase writers/recovery readers and
receipt-backed publication adapter are preserved at `339f364ca`. The remaining
runtime/lifetime implementation and unrelated CI/release/deployment fixes are
preserved at `059f61ff2` on `codex/credential-d02-scope-preserved-059f61ff2`.
These are recovery snapshots, not new delivery PRs or approved D12 interfaces.
See the scope review for the handoff and remaining maintainer decisions.
