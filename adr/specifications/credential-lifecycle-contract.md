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

The 1 October migration roadmap separates D02 ([PR #785](https://github.com/flidai/leapview/pull/785))
from D12 completion. Freeze further activation/runtime feature growth in D02
while maintainers review this scope. This ledger records the existing diff and
proposed allocation; it neither extracts code nor accepts the lifecycle decision.

The [foundation scope review](credential-foundation-review.md) gives concrete
keep/split candidates, dependencies of installed denial checks, assurance-owner
handoffs and the decisions needed before the next implementation boundary.

| Work | D02 disposition | Completion responsibility |
| --- | --- | --- |
| Owner/keyring setup, encrypted immutable drafts, scoped metadata/save APIs, validation receipts and transactional audit | Retain as the credential foundation under review. Offline owner/keyring setup is not customer-secret bootstrap or first publication. | D02; D10/D12 compose customer-secret bootstrap through the same authorized, audited service. |
| Existing access checks, preparation/switch/commit records, pins, source/pool cleanup and publication fences | Review as explicit prerequisites; decide which stay in D02 and which need a separate PR. Some restrictive safeguards are installed; receipt-backed activation is not composed into production. | D11/D12 for combined ownership, drain, publication, installation and restart guarantees. |
| Remaining app activation authority callback and production coordinator, source/agent consumption and administration UI | Defer further feature work; reuse the existing publication-admission adapter and reviewed prerequisites. Saved, validated or sealed does not mean in use. | D12 after D01/D02 decisions and the D10/D11 shared contract. |
| Rotation, retirement, independent key recovery and retained-backup decryption | Required follow-up, not completed by draft-storage tests. | D12 implementation; D13 profile/recovery qualification. |
| Accumulated CI/browser/security, retained release-provenance and demo-readiness fixes | Separate keep/split review; no removal is implied by this ledger. | Their owning CI, release and deployment reviews; none proves Nix adoption or managed lifecycle acceptance. |

Before D12 implementation crosses these shared boundaries, record the reviewed
contract and named credential/deployment owners for:

- **D10 bootstrap:** the shared authorized/audited customer-secret service and
  bounded setup authorities; no parallel raw-SQL secret bootstrap.
- **D11 process ownership:** shared home/storage access, worker and mutation
  authority, candidate readiness, admission and draining across overlapping
  processes. A process-local barrier cannot account for another process.
- **D11/D12 publication and restart:** serialization, committed-version
  installation, recovery and the evidence for `in_use` and safe local retirement.
  Readiness alone grants none of these mutation or retirement authorities.
- **Release/recovery:** preflight classification of incompatible credential
  formats, maintenance/recovery for those transitions, and retained versions,
  keys and acknowledged writes within a declared compatible rollback window.

These are pending interface decisions, not a new coordination protocol. A
stop-first profile cannot replace Kamal overlap without a reviewed amendment to
the deployment ADR and roadmap and distinct qualification. Independent host and
build work may proceed; full managed acceptance requires the combined gates.

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

### Runtime reader boundary

The internal [runtime reader](../../internal/credential/runtime.go) resolves a
PostgreSQL password only through a mandatory server-owned runtime authority.
Its input is the exact serving identity and connection resource. The authority
must obtain the committed version and full scope from publication evidence and
current governed workload authority; a caller-selected version, the newest draft
or a validation receipt cannot supply that authority.

The reader verifies the identity/resource match, then reads only the authorized
deployment, owner, resource and version. It compares the complete authenticated
encryption binding, rechecks authority after storage I/O, and decrypts only that
exact envelope. On exit, the decrypted byte buffer is zeroed and callback map
entries are removed. Go cannot erase strings copied by a consumer; trusted
consumers must not log or retain plaintext beyond the admitted client lifetime.
Errors from storage, decryption and the callback do not expose their raw
diagnostics. This is an internal read primitive, not a public decrypt API.

Those authority observations do not prevent changes during credential use. The
eventual lifecycle integration must hold admission for the full consumer lifetime,
including any pool or native client created from the credential, and drain and
close those consumers before cutover. The callback is not evidence of retirement
or readiness. The foreground check described below composes a request-bound
reader and holds a generation lease through temporary-pool cleanup. It has no
production caller; ordinary queries cannot resolve saved drafts through this reader yet.

The publication evidence extension below records an explicit local credential
version. The remaining integration must implement admission and recovery,
then connect the reader to the source runtime. Preserve exact pins for
retained generations and keep source publication as the sole activation
authority. Agent configuration references require their own equivalent
integration. Do not add an independent active-source-credential pointer or infer
the provider from an ambiguous version string.

### Publication pin and commit proof boundary

Candidate, runtime-host and release binding evidence now carries an explicit
`credentialVersionId` for a local PostgreSQL password. It is a canonical nonzero
UUID and is mutually exclusive with the external provider version. Public/no-auth
connections cannot carry a local credential pin. The exact ID participates in
each binding fingerprint and survives native seal assembly and API metadata
serialization. Local pins require the current provenance format, version 6;
there is no automatic conversion or new compatibility reader for version 5.

The serving evidence adapter checks local pins against delivery's committed
generation lookup. It matches target, project, environment, generation, candidate
identity/revision, serving artifact digest and the sealed binding fingerprint.
The lookup requires a committed publication for the exact generation; candidate
provenance, qualification, a ready release or a pending publication alone is
insufficient. Historical committed generations remain valid commit evidence
after the active pointer advances, preserving exact references for retained work.

This is proof of durable publication, not current permission, retirement status,
admission or live readiness. The source runtime explicitly rejects local pins
until the runtime authority and activation lifecycle are integrated; a local UUID
never falls through to an external resolver. No draft-to-candidate selection,
receipt reservation/consumption, lifecycle operation or `in_use` response is added
by this slice. Those are the next coupled activation changes under sections 4–6.

### Transactional preparation boundary

The internal preparation primitive now reserves an immutable validation receipt
for one exact intended source publication. It records an operation ID, expected
target revision and predecessor generation, and intended candidate, generation
and publication IDs. The receipt remains the authoritative source for actor,
credential version, owner/resource scope, binding revision, destination and
configuration digest; the preparation does not duplicate those values.

`PrepareActivationTx` requires a caller-owned PostgreSQL transaction and a
mandatory authority callback. The eventual coordinator must use that same
transaction to lock and recheck the current target/predecessor, ownership,
binding/configuration and actor permissions. The store compares the full saved
receipt, checks database wall-clock freshness, and writes its unique reservation
with redacted audit. A savepoint rolls back this work on any error; only the
caller can commit the outer transaction. A repeated request conflicts rather
than creating or replaying an activation. The read method returns the exact
historical preparation without renewing the receipt.

Preparation, switching, commit and cancellation remain internal; no production coordinator creates
these records yet. One unfinished preparation is permitted per deployment.
An explicit authorized cancellation preserves the original intent and permanently
reserves its receipt while releasing the deployment slot for a new operation
with a new receipt. The commit primitive described below consumes that reservation
with its exact publication; completion is not implemented yet. Prepared intent does not mean a candidate has been qualified,
the predecessor is still current, or a credential is in use.

### Publication fence and preparation cancellation

All fresh native publication activations require a composition-owned admission
port, including direct repository calls, rollback, and refresh paths. Production
installs the credential pending-operation check independently of customer keyring
setup. Under the existing locked delivery target, it reads the pending preparation
in the same transaction and refuses the active-pointer change if one exists.
Missing admission or unavailable credential storage fails closed. An exact
committed replay only reads prior publication evidence; it is not a second
activation or a readiness result. The optional semantic/qualification hook cannot
replace this mandatory fence.

Source preparations require their deployment and target identities to match the
instance-bound delivery target. Preparation, cancellation and the pending check
require READ COMMITTED: their statements must see changes committed while waiting
for the target lock. A repeatable snapshot must not silently miss a newly prepared
operation. The eventual coordinator must acquire that same target lock before
preparing or cancelling; credential storage does not query delivery tables.

Cancellation uses a caller-owned transaction and a mandatory current-authority
callback, with a savepoint covering the terminal record and redacted audit. The
callback must establish the target fence before operation-row mutation and verify
that candidate work has been safely disposed and the actual authoritative runtime
has been reconciled. Expired validation does not authorize cancellation, but it
also does not prevent a currently authorized cancellation. Failed authorization,
failed audit or outer rollback leaves the original preparation pending. A timeout,
request cancellation or restart does not release the slot automatically.

Before exposing preparation or cancellation, integrate candidate selection,
current authority and mutation serialization, the process-local pause/drain
barrier, atomic publication commit and restart recovery described in sections 4–6.
The publication fence is wired now; live credential switching remains unavailable.

Research: the Flid library search had no applicable transaction example. The
implementation follows PostgreSQL's [transaction lock lifetime](https://www.postgresql.org/docs/18/explicit-locking.html),
a [partial unique index](https://www.postgresql.org/docs/18/indexes-partial.html) for the unfinished slot,
and [wall-clock time](https://www.postgresql.org/docs/18/functions-datetime.html#FUNCTIONS-DATETIME-CURRENT)
for expiry, because transaction-start time does not advance while waiting.

### Pool cleanup completion prerequisite

Provider drain needs positive cleanup evidence before it can support `in_use` or
old-version retirement. The target pool owns one close task: it fences new health
checks, cancels existing checks, and records completion only after those checks
exit and the underlying session close returns. Concurrent and subsequent close
callers observe that same final result. A caller's canceled context or deadline
only ends its wait; it neither proves cleanup nor becomes the final close result.

Pool-generation retirement preserves that distinction. A bounded retirement may
request forced closure when readers exceed the deadline, but the manager remains
fenced and unfinished until the actual close and admitted refresh work finish.
That includes earlier pool generations still held by readers and rejected
replacement pools whose cleanup belongs to an admitted refresh.
If a refresh request times out while its rejected pool is closing, the caller
may return while the manager continues to own and count that cleanup. Retirement
still waits for its actual completion and retains any cleanup failure.
The directory retains its managers during shutdown so another caller can await
unfinished cleanup and receive the retained failure or success. No retry can
reopen a retired manager or silently replace an unfinished cleanup.

This is a prerequisite, not the activation barrier. Source-provider
admission must cover both `SourceRuntime.Prepare` through provider cleanup
and candidate binding pool preparation/health checks. Provider reads stage local
tables before source preparation returns; holding a work permit for the whole
retained candidate runtime would prevent draining. Authorization snapshot reads
through Runtime Host must remain available to administrators. Candidate selection,
atomic credential publication, startup recovery and live readiness remain pending;
live local-credential switching is still unavailable.

Research: the Flid library had no applicable pool-shutdown example. Go documents
that [canceling a context does not wait for work to stop](https://pkg.go.dev/context#CancelFunc).
The implementation therefore joins actual operation and cleanup completion rather
than treating cancellation as a drain acknowledgment.

### Source-work admission and drain prerequisite

One process-local analytics gate now covers module-owned `SourceRuntime.Prepare`
and shared `PoolManager.Refresh` operations, including requested and scheduled refreshes.
The module passes the same gate to its serving project factory, both writer
materialization paths, and shared pool managers. Source preparation holds a lease
before opening its session or resolving credentials, through source reads and
synchronous cleanup. It releases before the caller retains the local staged
tables. Candidate/runtime pool leases likewise remain separate from active-work
accounting, so retained runtimes cannot indefinitely prevent a source drain.

Refresh holds a lease through credential resolution, pool preparation, and health
checks. Rejected replacement cleanup retains a child lease before its asynchronous
close starts. Even when the request times out, that lease remains until the actual
close returns. Retaining is only a continuation of admitted work; it cannot admit
a new operation after the parent releases. It bypasses a later pause solely to
finish cleanup, avoiding a nested-admission deadlock.

Pause immediately stops new admission. New callers wait with their own contexts;
canceling one of those callers does not reopen the gate. The opaque pause handle
can wait for all admitted work to finish, and retry that wait after a timeout.
Timeouts leave admission paused. Resume requires the current handle and zero
active leases; an earlier handle cannot resume a later pause. Module shutdown
permanently fences the gate. Runtime Host snapshot acquisition remains available
for authorization, and the separate exact-version credential probe remains an
isolated administration operation rather than an ordinary pool refresh.

`WaitDrained` proves that counted Go work has ended, including the return of its
cleanup calls. It does **not** prove every idle resource closed successfully,
credential publication committed, the runtime is ready, or the old credential is
safe to revoke. Resource-close errors and ambiguous provider cleanup must be
incorporated into readiness separately. In particular, the coordinator must not
lose transient resolver/factory close failures or treat an ambiguous failed
`ATTACH` as confirmed detachment. Existing environment-fatal reporting alone is
not a resource-retirement acknowledgment.

This slice exposes only the internal pause control. Normal startup still opens
admission; local credential activation remains unavailable. Before exposing it,
the coordinator must start closed while checking durable pending operations,
integrate pause/retry behavior with workers before domain failure finalization,
select and authorize the exact candidate, commit activation atomically, retire
old resources with successful cleanup evidence, reconcile after a crash, and
resume only after runtime readiness. Those steps establish `in_use`; this gate
alone cannot establish it.

Research: the Flid admission/drain lookup had no applicable example. Existing
source staging, pool refresh, retirement, and authorization paths supplied the
integration boundaries; Astra reviewed the design before implementation.

### Durable switching intent and recovery lookup

The internal preparation record now retains a one-time `switching_at` marker.
`BeginActivationSwitchingTx` reads the exact stored intent, requires its original
receipt actor, and invokes a mandatory current-authority callback before changing
the operation row. The callback must acquire the delivery target fence first,
then lock and recheck actor, owner, binding, destination/configuration, target
revision and predecessor authority through the caller's outer commit. It cannot
perform provider I/O or commit the transaction. As with preparation, switching
requires READ COMMITTED and database wall-clock receipt freshness, checked at the
conditional update and again after the audit write. The marker and redacted audit
share a savepoint; authorization failure, expired proof, audit failure or outer
rollback preserves the prior state.

Switching does not change the authoritative publication or release the unfinished
operation slot. Repeating the transition conflicts instead of producing another
audit event or treating the stored marker as fresh authority. The coordinator
must confirm its outer transaction committed before pausing source admission.
The marker records intent to drain; it does not acknowledge that draining happened.

Cancellation may follow either preparation or switching. The expected switching
timestamp is compared during cancellation because its initial read precedes the
target-fence wait. If switching wins during that wait, cancellation conflicts and
must reread and reauthorize. It cannot write an audit event using stale phase
evidence. Audit sequence is preparation 1, switching 2, and cancellation 2 from
preparation or 3 after switching. Cancellation retains the switching marker and
receipt history; neither can be rewritten or reused.

`GetPendingActivation` finds the sole non-aborted operation by deployment without
needing an operation ID from the crashed process. It returns the original receipt
even when expired. Both prepared and switching records are unfinished precommit
work: a future startup coordinator must remain closed and obtain current authority
for retry or cancellation. A missing row is only a database observation, not a
readiness acknowledgment or permission to reopen.

This remains an internal persistence slice. It does not wire a public coordinator,
pause the running process, change startup admission, or enable local credential
pins. The atomic publication boundary below preserves delivery's existing
publication → lease → target lock order. Its exact-operation credential check
and operation/receipt mutation run in the existing admission transaction after
the target lock and before the publication pointer CAS. Do not pre-lock the target and then enter delivery activation in a
separate transaction, or relax the current fence to admit arbitrary pending work.
Runtime installation, successful cleanup evidence, completion and startup recovery
must still be connected before exposing activation or reporting `in_use`.

Research: the Flid lookup had no matching lifecycle transaction example. Existing
preparation/cancellation stores and delivery activation supplied the boundaries;
Astra reviewed the design before implementation. PostgreSQL documents fresh
statement snapshots at [READ COMMITTED](https://www.postgresql.org/docs/18/transaction-iso.html#XACT-READ-COMMITTED),
[lock lifetime](https://www.postgresql.org/docs/18/explicit-locking.html), and the
difference between transaction-start time and [wall-clock time](https://www.postgresql.org/docs/18/functions-datetime.html#FUNCTIONS-DATETIME-CURRENT).

### Atomic delivery attempt prerequisite

Delivery activation now scopes each attempt to a PostgreSQL savepoint inside the
caller-owned transaction. The boundary includes the admission callback, active
pointer/publication update, retention changes, event and audit writes, and final
proof reads. `ActivateTx` and `ActivateTxWithPreCommitHook` release that savepoint
only after the entire attempt succeeds. A failure rolls back the attempt even if
the caller catches the error and commits unrelated work in the outer transaction.
Rollback uses a live cleanup context when the request has been cancelled. If
savepoint cleanup cannot be confirmed, the repository aborts the caller transaction
and returns the cleanup errors; callers must discard it. In particular, pgx
closes a nested transaction handle even when releasing its savepoint fails, so
`ErrTxClosed` is not evidence that activation writes were rolled back.

Releasing the savepoint does not commit the outer transaction or release its
successful activation locks. The caller still chooses commit or rollback; the
publication → lease → target lock order and exact committed replay checks remain
unchanged. Admission and audit adapters must use the supplied transaction and
must not commit, roll back, or perform provider I/O.

This provides the rollback boundary for credential commit writes in publication
admission: a later delivery failure must not leave those earlier writes behind.
It does not relax the unfinished operation fence or enable runtime activation.
Runtime readiness and startup recovery remain subsequent integration work.

Research: Astra traced the existing delivery admission, retention, audit and
lost-acknowledgment paths before implementation. The Flid lookup found no matching
example. PostgreSQL documents the nested rollback boundary in
[SAVEPOINT](https://www.postgresql.org/docs/18/sql-savepoint.html) and
[ROLLBACK TO SAVEPOINT](https://www.postgresql.org/docs/18/sql-rollback-to.html).

### Atomic credential publication commit

`CommitActivationPublicationTx` records `committed_at` for the exact switching
operation inside delivery's activation-admission savepoint. The stored immutable
intent must match the publication's target, expected revision, predecessor,
candidate, generation, publication ID and original receipt actor. A required
transaction-bound authority callback rechecks and locks current actor, customer
owner, binding and configuration authority and verifies the candidate's exact
credential pin. It must retain those locks through the outer commit and perform
no provider I/O. The ordinary production publication fence remains installed;
the internal operation-scoped adapter is not yet wired to a live coordinator.

Receipt freshness has a precise checkpoint: the transition trigger's database
wall-clock sample during the conditional update, at the final admission gate
before delivery's pointer CAS. The receipt must have been validated and not
expired at that sample. The trigger resamples after any operation-row lock wait;
expiry during that wait returns a conflict and preserves the switching operation.
Expiry after this gate does not undo an authorized publication. This is not a
claim of freshness at the physical outer COMMIT instant, which a prior SQL
statement cannot observe. `committed_at` records this gate's time; callers must
confirm outer commit before reporting durable publication success.

The committed marker, sequence-3 redacted credential audit, publication/pointer,
delivery events and audit all share the transaction. Any subsequent delivery
failure rolls back the entire activation attempt even if the caller commits
unrelated work. Exact committed delivery replay does not invoke admission again
or append a second credential audit. A repeated credential transition conflicts;
recovery reads preserve the original receipt even after it expires.

Committed operations cannot be cancelled and still occupy the unfinished
deployment slot. The `aborted_at IS NULL` pending predicate intentionally includes
them until a later completion phase proves runtime installation and old-client
closure. No extra active-secret pointer or duplicate committed revision is added:
the immutable publication identity and its expected revision define that evidence.
This step does not establish `in_use`, old-credential revocation safety, or runtime
readiness. The next integration work must connect admission pause/drain, runtime
installation, completion and startup recovery before exposing activation.

Research: Astra reviewed the existing delivery and credential stores and the
candidate-provenance transaction readers. Flid discovery returned no matching
reference. PostgreSQL's [savepoint semantics](https://www.postgresql.org/docs/18/sql-savepoint.html)
and [wall-clock semantics](https://www.postgresql.org/docs/18/functions-datetime.html#FUNCTIONS-DATETIME-CURRENT)
support the rollback and freshness boundaries above.

### Retired serving-runtime cleanup gate

`WithRetiredRuntimeCleanup` is a runtime-host prerequisite for a future
coordinator-owned completion write. It acquires the existing context-aware
cutover read fence, requires the exact installed project/environment/generation
identity, and waits for every retired serving runtime owned by that manager to
finish synchronous cleanup. Outstanding reader leases and blocked resource
closure keep the callback waiting. The fence remains held through the callback,
so activation or host shutdown cannot change the installed generation between
the check and that write.

The manager retains its first retired-runtime cleanup failure for its entire
lifetime, including after removing the failed runtime from the retired list.
Any such failure prevents the callback on subsequent attempts. This conservative
rule avoids turning missing cleanup evidence into success; it introduces no
per-resource retry protocol. Restarting the process does not itself establish
credential readiness: recovery must rebuild and verify the full operation.

Cancellation observed before callback invocation stops the wait, leaves cleanup
running, and does not resume source work. The same exact generation can be
checked again. Once the callback starts, its result is returned without a later
cancellation check: cancellation racing with a database commit cannot undo it.
Ambiguous write outcomes require a durable reread. The trusted internal callback
must recheck durable identity and current authority and confirm transaction
commit. A successful no-op callback is only evidence of this cleanup gate.

The caller must own source-work pause/drain and lifecycle mutation serialization.
It must not hold an old-generation reader lease needed by this wait. The callback
must not perform provider I/O, activate or close the host, or reacquire the cutover
fence. Private candidate runtimes and shared pools have separate lifetimes;
snapshot lease releases may still be queued after synchronous cleanup returns.
Their completion needs separate evidence before any `in_use` or revocation claim.

No durable completion marker, public activation route, or local credential
runtime enablement is added here. Committed operations remain unfinished and
continue to fence later publications. Remaining integration must connect source
admission, candidate and pool retirement, exact runtime installation, durable
completion, and startup recovery before exposing activation.

Research: Astra traced runtime ownership, cleanup and startup paths before
implementation and reviewed the gate and concurrency tests. Flid discovery found
no matching reference. Go's [memory model](https://go.dev/ref/mem) specifies the
channel and mutex synchronization used by the cleanup workers;
[context cancellation](https://pkg.go.dev/context#Context) signals that work should
stop and does not prove resource cleanup has completed.

### Candidate cleanup remains tracked through resource closure

Registered private candidate generations remain in the retired set until their
synchronous resource cleanup finishes. Releasing the final reader schedules
cleanup once; it does not remove the generation from tracking. The cleanup task
records its error, retains the first candidate cleanup failure for the registry's
lifetime, removes the completed entry, and then signals `cleanupDone` under the
candidate registry mutex. A later shutdown therefore cannot mistake an empty
retired set for successful historical cleanup.

Cleanup runs asynchronously so a blocked runtime or dependency close does not
bypass the shutdown drain timeout. Expiry reaping returns a count of candidates
fenced against new leases, not a cleanup acknowledgment. Explicit retirement
returns the captured cleanup handle described below.
Timeout stops the shutdown caller's wait; it does not prove cleanup finished or
force-release readers. The background shutdown path waits for actual candidate
and serving-generation cleanup before closing their shared snapshot-release
queue. The manager must not independently close that queue when the registry
owns the combined wait, including after a serving-generation timeout.

This repairs cleanup accounting for registered candidates. It does not fence
concurrent preparation or registration for credential activation or establish
shared-pool closure. Prepared
but unregistered candidates and shared pools retain their separate ownership
rules. The future coordinator still needs those boundaries, durable completion
and startup recovery before enabling activation or claiming revocation safety.

Research: Astra reviewed candidate replacement, lease release, shutdown and
pool retirement before implementation. Flid searches found no matching source.
Go's [Once contract](https://pkg.go.dev/sync#Once) and
[channel synchronization](https://go.dev/ref/mem) support one-shot cleanup
scheduling and publication of its result before completion is signaled.

### Exact candidate retirement evidence

`RetireCandidate` requires the candidate ID, owner ID, full serving identity
(project, environment and generation), and canonical compatibility evidence.
Under the candidate registry mutex it captures all matching current and retired
runtime instances and fences the matching current instance against new leases.
Repeated installations of an identical tuple are distinct instances: all that
still exist at capture time must finish cleanup. A missing match returns
`ErrCandidateRuntimeNotFound`; absence never produces a successful empty handle.
There is no compatibility wrapper for the former ID-only, count-returning API.

The opaque process-local handle retains the captured instances after removal
from the registry. Its `Wait` waits for their actual synchronous cleanup, may be
called concurrently or repeatedly, and remains usable after registry shutdown.
Cancellation stops only that wait; it neither cancels cleanup nor restores lease
admission. Keep the handle for retries. Reissuing retirement after completed
instances have disappeared requires reconciliation rather than treating a missing
entry as proof of cleanup.

Any registry cleanup failure already known at capture time is retained on the
handle, but does not prevent fencing matching instances. `Wait` returns that
baseline failure together with captured instance failures. A later unrelated
cleanup failure or replacement does not change this captured result. Cleanup
publishes each instance's error before closing its completion channel.

This is evidence for captured registered candidates only. Before a credential
transition uses it, the coordinator must pause and drain candidate preparation
starting **before connection lease acquisition**, and prevent registration through
the transition. The existing source-work pause does not establish this boundary;
prepared but unregistered candidates retain separate ownership. The handle does
not close shared pools, drain asynchronous snapshot release, establish live
credential readiness, or authorize upstream revocation. Activation remains
disabled pending admission, pool retirement, completion and startup recovery.

Research: Astra reviewed the exact selection and cleanup ownership before
implementation and reviewed the handle and concurrency tests. Flid discovery
found no relevant retirement example. Go's
[channel synchronization](https://go.dev/ref/mem) supports observing cleanup errors
after completion, while [context cancellation](https://pkg.go.dev/context#Context)
does not establish resource closure.

### Candidate preparation admission pause

Application composition now supplies the deployment module with one pauseable
candidate-preparation admission adapter. Its dedicated `sourcework.Gate` is
acquired before workload admission and before connection acquisition. Standalone
preparation still uses the control workload class. A preparation inheriting a
refresh workload context still acquires this lifecycle gate, preserves that
context, and does not release the outer refresh workload lease.

The existing delivery-build HTTP handler and lazy native-preview path defer the
composite lease's release through their admitted calls. Preview admission covers
artifact recovery, connection acquisition, runtime preparation and registration,
including synchronous cleanup on failure. Workload rejection releases lifecycle
admission; cancellation after admission does not. Release is idempotent and
releases workload accounting before declaring lifecycle work finished.

`Pause` stops new admission. Its handle supports `WaitDrained` retries after
cancellation and permits `Resume` only when admitted work has returned. Pausing
does not cancel existing work. The future coordinator must pause and drain
candidate preparation **before** pausing analytics source work: an admitted
preparation can need source work to open a cold pool. These are separate gate
instances; sharing one would allow a nested acquisition to deadlock its drain.

This is an internal composition primitive, not a credential activation command.
No production coordinator invokes the pause yet. It covers callers of this
adapter, not direct low-level runtime-host prepare/register calls or the native
refresh executor's separate build path. The unused private deployment
`prepareCandidate` helper also does not establish this admission boundary and
must not be made reachable without admission. Integration must serialize all
relevant entry points, preserve shutdown behavior while paused, and separately
wait for exact registered-candidate retirement and pool closure. A successful
preparation drain does not prove cleanup succeeded, establish readiness, or
authorize revocation. Activation remains disabled.

Research: Astra traced production construction, preparation callers, refresh
inheritance and nested pool refresh before implementation. The existing
source-work gate supplies pause/drain semantics without a new gate algorithm or
durable state. Astra's final review and race tests cover paused refresh admission,
connection acquisition, cancellation during blocked synchronous cleanup, workload
rejection and concurrent release. Flid discovery found no matching admission-gate
reference.

### Exact shared-pool manager retirement evidence

`PoolDirectory.RetireBinding` captures one existing manager using a validated
`TargetBinding`. It compares binding, target, connection and project/environment
identities, revision, connector, endpoint digest, provider version, health,
enabled state, authentication mode and credential reference. Timestamps and
health diagnostic codes are excluded. Comparison and the retirement fence share
the manager mutex while the directory mutex protects selection; a concurrent
refresh cannot change the
matched version between those actions. Missing entries return not-found, and
mismatched entries return conflict without fencing their manager.

The fenced manager remains in the directory. Same-revision callers cannot lease
it; normal revision replacement must finish its cleanup successfully before
removing it. A timeout or close failure retains that manager and its result for
retry, and directory shutdown continues to see it. This reuses existing manager
ownership rather than adding a retired-manager registry.

The opaque `PoolRetirement` handle retains the selected manager across directory
replacement or shutdown. Its bounded, repeatable and concurrent `Wait` delegates
to `PoolManager.RetireBounded`, which waits for that manager's active and draining
generations and admitted refresh cleanup. Deadline or cancellation requests
forced physical closure and bounds the caller's wait; neither is closure proof.
A later wait observes actual completion and retained cleanup errors. Forced
closure can finish while reader lease objects remain outstanding, so success
does not prove those readers released their leases.

The analytics module forwards this operation only to its existing directory,
checks target/environment scope, and never initializes missing pools to produce
an empty success. The lifecycle caller must retain the handle, hold preparation
and source-work admission pauses, and serialize binding replacement. A handle
does not fence a later manager or establish durable activation authority.

This is whole-manager closure evidence. `TargetBinding` and shared-manager
ownership still identify provider versions, not local `CredentialVersionID`
UUIDs. Local credential pins continue to fail closed; integrating local pool
ownership with the shared manager, coordinator completion and startup recovery
remains necessary.
No activation route, `in_use` status or upstream revocation guarantee is added.

Research: Astra audited the clean-break scope, public operation surface, runtime
admission and local-pin rejection before approving this prerequisite. Existing
manager retirement supplies the close tasks and retry results. Flid discovery
found no relevant pool-retirement reference.

### Concrete-pool credential identity (partial integration)

The internal runtime reader now passes its exact, authority-rechecked
`RuntimeCredentialReference` alongside the decrypted fields. Consumers can retain
the non-secret reference; secret fields remain limited to the callback. The
caller still needs an admission lease through the lifetime of any derived client.

Credential snapshots distinguish a provider version from a canonical nonzero
local credential UUID. Their constructors select one origin; mixed or empty
identities are invalid. Local snapshots have no provider version. The concrete
DuckDB target pool retains a copy of this identity after snapshot destruction
and pool closure, so later lifecycle code can identify the version the pool used.

`PrepareLocal` is a separate internal PostgreSQL preparation entrypoint. The
ordinary provider preparation, binding application and manager refresh paths
reject local snapshots. No local UUID is persisted as a provider validated
version, and `TargetBinding` does not gain a local pin. Its endpoint policy is
used during preparation; its external reference is not resolved by this path.

The UUID is an identity tag, not authorization or destination evidence. Before
wiring this entrypoint, the runtime adapter must compare the exact reference's
resource, owner, purpose, provider and destination with committed serving
authority and the target binding, while holding admission. The synchronous
adapter below connects the reader to a temporary pool check; it does not retain
a serving pool. The active serving resolver still rejects local pins before
external resolution or pool creation.

This prerequisite does not establish local-version retirement across all pools,
coordinator completion, startup reconciliation, readiness, `in_use`, or safety
to revoke an old upstream credential. Those remain integration work.

Research: Astra reviewed the boundary before implementation. Flid discovery for
credential-version pool ownership and PostgreSQL returned no matching reference;
the existing reader, provider manager and concrete DuckDB pool supply the design.

### Synchronous scoped runtime credential check (partial integration)

The app-side adapter compares the reader's authority-rechecked reference with
the exact expected local pin and the scope derived from the persisted customer
owner and target binding. It matches instance, project, environment, connection,
purpose, provider and destination digest. It rechecks the binding and owner
around use; observed drift fails the check. It does not use the draft resolver's
current-project restriction, so an authorized retained generation can be checked.

The analytics module acquires its existing source-work operation lease before
calling the reader. Inside that lease it creates a local snapshot, prepares and
health-checks a temporary PostgreSQL pool, and waits synchronously for actual
native cleanup. Only non-secret identity is returned. Local pool `Resolve` now
rejects requests rather than returning authentication copies that could outlive
this operation. Provider pools retain their existing resolver behavior.

Cancellation does not release admission while native cleanup is still running.
Preparation cleanup errors are preserved and converted to a fixed safe failure
at the module boundary. Failed cleanup quarantines the operation lease: a later
source-work drain cannot report success. There is no in-process override or
retry that turns an unconfirmed native close into success; process recovery is
required before that quarantined gate can be replaced. Draining still does not
prove retirement of other pools or consumers.

This adapter requires an injected runtime reader that enforces committed
generation and current governed workload authority. The foreground authority
adapter below covers a direct connection-use check. The internal foreground
wrapper composes its reader from configured services; an ordinary serving caller
remains absent. A successful
check proves only that this exact temporary pool completed its check and cleanup.
Persistent local pool ownership, derived-client lifetimes, coordinator/recovery
integration, readiness and `in_use` remain pending. No activation route is added.

Research: Astra reviewed scope comparison, admission ordering and native cleanup
before implementation. Flid discovery found no matching reference. Existing
source-work and pool-close contracts remain authoritative for the implementation;
Go's [database close contract](https://pkg.go.dev/database/sql#DB.Close) also
distinguishes blocking new work from waiting for in-flight work to finish.

### Foreground connection-use credential authority (partial integration)

The request-scoped app adapter resolves a local credential reference only for an
authenticated foreground caller with current `connection.use` permission. It
requires the explicit caller to match the request principal and reuses the
existing access-module checks for current credential lifecycle, typed connection
grants, current subject membership and the API credential's permission ceiling.
Workload scheduling admission, an administrator role, or a saved validation
receipt does not substitute for these checks.

Each authority read acquires a runtime lease and matches the full requested
project, environment and generation to both the lease and its bound authorization
snapshot. When composed with the active production provider, this permits only
the current generation. A retained generation cannot fall back to the current generation's
permission snapshot; its eventual caller needs an exact retained-runtime
authorization contract.

The adapter reuses release provenance and the existing committed-generation
proof to select the exact local pin. It matches that evidence against the enabled
PostgreSQL target binding and derives scope from the persisted customer owner
and destination digest. It does not consult the newest draft or interpret a
provider version as a local credential UUID. No separate pin registry or active
credential pointer is introduced.

The runtime lease covers each authority read, not the subsequent credential
consumer by itself. The foreground wrapper below owns an outer lease through
the complete check. The runtime reader repeats the authority decision after storage I/O;
those observations do not establish an activation barrier. The synchronous
check still owns source-work admission through actual temporary-pool cleanup.
Future serving integration must establish admission and serving-resource
ownership through every derived client's lifetime.

This is a foreground connection-use adapter, not authority for semantic queries,
background refresh, startup or system work. It is exercised with the internal
runtime reader but is not wired into production startup or an HTTP route.
Ordinary serving still rejects local pins. Coordinator recovery, readiness,
`in_use` and old-credential retirement remain pending.

Research: Astra reviewed the authorization boundary and existing native-client
ownership before implementation. Focused Flid discovery returned no exact match;
broader discovery found upstream DuckDB secret-lifetime documentation
(`duckdb-docs`, revision `6f6cd1659f0e2ddd1965b1d3f1833e7fc512e7ac`,
`docs/current/configuration/secrets_manager.md`). Temporary DuckDB secrets live
in the instance until explicitly dropped or the instance ends; a returned Go
callback is not a cleanup boundary. The current target pool is a temporary probe;
source refresh uses its project DuckDB session and source-work admission, not
the isolated probe pool. Persistent pool machinery is deferred until a real
serving consumer requires it. Flid also supplied Lightdash at revision
`35906ad9e116df59d2d59da2f58e45e9b39eaff8`: its
`docs/authentication-and-roles.md` and `UserService.loginWithPersonalAccessToken`
support distinguishing the token from its owning actor and resource grants. Its
PAT flow does not supply a per-token permission ceiling or the same immediate
lifecycle recheck; LeapView reuses its own existing access-module contracts for
those requirements.

### Request-bound reader and generation ownership (partial integration)

Credential module services retain the repository and keyring already verified by
setup. `RuntimeReader(authority)` creates a reader for one request authority from
those same dependencies; it does not reload keys, open another PostgreSQL pool,
or expose a repository or keyring accessor. Missing setup or authority fails
closed. Readers are not cached across callers.

The private foreground check accepts a connection locator and obtains its caller
from authenticated request context. It uses a short runtime lease from the
configured provider to derive the full serving identity and check its bound
authorization snapshot, then releases that lease before waiting for source-work
admission. The existing authority adapter selects the
exact committed credential reference. The request cannot choose a generation or
credential version. Fresh authority reads remain in place, so an observed cutover
or loss of permission fails closed instead of borrowing a newer generation's
permission snapshot.

After the analytics module admits source work, a private reader adapter acquires
a generation lease and requires the exact selected identity and bound snapshot
again. It holds that lease through reader execution and actual temporary-pool
cleanup inside the synchronous consumer. The analytics module holds its existing
source-work lease through the same cleanup boundary. Source-work admission comes
first: a check waiting at a paused source-work gate must not hold a generation
lease that could prevent activation from draining that generation.
Cancellation does not release either lease while cleanup is unfinished. Ordinary
completed checks and safe failures release the generation lease; unconfirmed
native cleanup or a panic during reader execution quarantine it, matching
source-work quarantine. The adapter observes callback cleanup failures before
the runtime reader redacts callback errors. There is no in-process override that
marks an unclosed client drained.

This is internal composition for a synchronous foreground check. It adds no route
or ordinary serving caller, installs no persistent pool, and proves neither
`in_use` nor retirement of all consumers. Activation, coordinator recovery and
safe revocation of an old credential still require their remaining integration.

Research: Astra reviewed the factory and consumer lifetime before implementation.
Flid's DuckDB secret-lifetime reference cited above reinforces that native cleanup,
not callback return alone, ends derived secret use. Flid discovery found no
additional applicable Go composition example; the existing verified module setup,
filtered runtime provider and source-work gate supply the implementation contracts.


### Native source-preparation cleanup boundary (partial integration)

The production `SourceRuntime.Prepare` path must finish removing external
attachments and temporary secrets before releasing source-work admission. It
records an attempted attachment before executing `ATTACH`: an error or panic can
arrive after DuckDB has already created the handle. Cleanup uses the bundled
engine's `DETACH DATABASE IF EXISTS` form to confirm the alias is absent, including
when the failed attempt created nothing. It also drops named temporary database
connector secrets; detaching a PostgreSQL or MySQL connection alone does not drop
its password secret.

Failed external cleanup, failed transient-session close or a panic leaves the
source-work lease quarantined. Native panics become a fixed cleanup failure
instead of exposing driver panic payloads. A later successful close does not
erase an earlier cleanup failure. Ordinary preparation failures may release admission only after
cleanup succeeds. Cancellation does not substitute for cleanup, and a paused
gate cannot report drained while a quarantined lease remains outstanding.

Successful preparation still releases source-work admission before handing back
staged local tables. Those tables no longer require external credentials; keeping
the lease until their later materialization or deletion would unnecessarily block
activation. This change adds no local-pin resolution or activation route, does not
persist completion, and is not proof of readiness or old-version retirement.
Startup recovery and the exact local-credential consumer remain pending.

Refresh preparation copies resolved authentication maps into its own connection
values. After native cleanup, it clears those owned maps and removes their
references before releasing source-work admission, including on error or panic.
Partial credential resolution also clears accumulated owned maps when it fails.
Resolver-owned maps and compiled model inputs remain unchanged. The returned
prepared model retains the metadata needed to plan against staged data, with no
resolved Auth maps. Removing these references is not a guarantee that Go string
memory has been zeroed, and it does not replace native cleanup or lift quarantine
when that cleanup is uncertain.

Research: Astra reviewed the native session and source-work ownership before
implementation. Flid's DuckDB documentation (`duckdb-docs`, revision
`6f6cd1659f0e2ddd1965b1d3f1833e7fc512e7ac`,
`docs/current/sql/statements/attach.md`) distinguishes detaching an attached
database from closing a connection: handles belong to the DuckDB instance.
Its secrets-manager documentation describes the same instance lifetime for
temporary secrets. An embedded-engine regression checks cleanup of existing,
missing and already-detached aliases against the project's exact driver; the
shorter `DETACH IF EXISTS` form is not accepted by that version. Flid's Rill
controller (`4f814a86196fac2ba9664b9237826582de2dad03`,
`runtime/controller.go`) likewise waits for cancelled invocations to finish
before declaring shutdown complete; LeapView reuses its own existing gate and
fatal-resource reporting contracts.

### Native refresh mutation authority (partial integration)

The native refresh executor revalidates the exact queued authority immediately
before creating a delivery plan and again before building that plan. It uses the
same platform jobs revalidator as dequeue and the refresh service, preserving
the existing caller-credential and delegated-workload rules. Manual job capture
includes the server-configured instance ID, as scheduled capture already does.
A missing checker,
wrong instance, cancelled context or denied check prevents the next mutation.
Authority lost while reading the base or completing the plan cannot rely on the
earlier execute-boundary check to authorize the next mutation.

These checks grant no connection permissions and do not enable local credential
pins. Source consumption still needs authority for the exact committed
credential after source-work admission, held through native cleanup. Astra
reviewed the existing job, refresh and native delivery paths. Flid's Rill
`runtime/connections.go` (revision `4f814a86196fac2ba9664b9237826582de2dad03`)
separates system and instance connector acquisition, but supplies no equivalent
job-authority contract; LeapView reuses its own ADR-0026 revalidator.

### Refresh authority after source-work admission (partial integration)

Native refresh builds carry a request-scoped revalidation callback bound to the
captured queued authority. Source preparation invokes it after acquiring source-work
admission, before opening a session, again before each connection resolver (which
may prepare and health-check its native pool), and immediately before each
external source access after extension setup and source-scope waits.
Cancellation is checked around each callback. An explicitly installed missing
checker fails closed; inherited checks cannot be replaced with a weaker check.
Errors retain their classification while hiding callback diagnostics.

Denied work uses the existing synchronous cleanup boundary: safely closed work
releases admission, while uncertain native cleanup remains quarantined. The
callback grants no connection permissions and creates no persisted readiness or
retirement evidence. Other governed source callers keep their existing authority
contracts; absence of this refresh callback does not authorize local pins.

Exact local-version consumption is still pending. Native refresh materialization
uses a new candidate generation, while its committed base is the refresh job's
serving identity. A future credential reader must select the exact reference from
that committed base and enclose native use and cleanup in its consumption callback;
returning decrypted fields from the ordinary connection resolver is insufficient.

Research: Astra traced the production build-to-source context and reviewed the
admission and cleanup boundaries. Flid's Rill `runtime/connections.go` (revision
`4f814a86196fac2ba9664b9237826582de2dad03`) explicitly checks cancellation when
acquiring an instance connector because cached paths may not otherwise observe it.
It provides no equivalent queued-authority contract; the implementation reuses
LeapView's existing jobs revalidator.


### Per-source connection ownership (partial integration)

The live connection resolver now uses one synchronous `WithConnection` callback
per external source. The resolver operation owns pool preparation and the
consumer callback through cleanup. The callback encloses extension and scope
admission, authority revalidation after waits, attachment and staging,
schema inspection, and removal of native attachments and secrets. Owned Auth
maps are cleared before returning. Staged local data retains no resolved Auth.
The old auth-bearing `Resolve` contract is removed.

Candidate binding leases stay held through the callback. Active release bindings
keep their exact provider-version temporary pool alive through native consumption
and cleanup, then close it. Source callbacks execute sequentially, without nested
candidate read locks or holding one source's scope while resolving another.
Target pools register callback work under their existing mutex and release that
mutex during consumption; closure fences new work and waits for callbacks to
finish. A cancelled close wait is not completion evidence.

An uncertain native or owner cleanup propagates a fixed cleanup marker to the
source-work admission barrier before redaction. It quarantines that admission;
target pools also reject subsequent consumption and retain the failure in their
close result. Completed callback counters are released even on failure, so
candidate disposal can finish and manager retirement reports the retained error
instead of waiting forever. A candidate registration's successful `Close` alone
is not proof of clean pool retirement.

This is a prerequisite for exact local-version consumption, not that integration.
Local pins remain denied. Native refresh still needs committed-base credential
selection, connection-use authority for the actual job/workload, exact destination
and owner checks, and local candidate acquisition before enabling that path.
No activation route, persisted `in_use` state or revocation guarantee is added.

Research: Astra reviewed callback ownership and lock ordering before implementation.
Flid's DuckDB secrets-manager reference cited above confirms that temporary
secrets have instance lifetime; synchronous native cleanup is therefore required
inside the resolver callback. Existing candidate leases, pool work counters and
source admission provide the ownership machinery.


### Queued refresh connection authority (prerequisite)

Manual refresh admission now derives exact connection IDs from the pipeline's
source closure and captures their `connection.use` pairs alongside the generated
`pipeline.run` pair. Missing or noncanonical source-to-connection evidence denies
admission; connection names are never substituted for resource IDs. Shared
connections are recorded once. This prerequisite applies to all actual source
connections of that pipeline, including provider-backed connections, rather than
only future local credential pins. It does not require unrelated project
connections. Existing manual callers and delegated schedules therefore need
explicit authority for those dependencies.

Before persisting a run, the existing live authority revalidator checks the full
set against the same initiating session or typed token and current principal
permissions. The token ceiling must allow each pair. A delegated run requires
each dependency in its already captured grant; its immutable permission set and
current workload principal are checked against the live grant. Pipeline-run or
source-read permission does not imply connection use.

At prepare, execute and publish, both modes rebuild the executable plan from the
current active artifact and require its exact queued digest. Caller connection
pairs must equal the rebuilt source connection set; delegated grants must contain
it. Later token or role expansion cannot add an uncaptured connection to an
already queued job. The existing per-source live revalidator checks all captured
pairs after admission waits and before native work. Output follows successful
candidate activation and rechecks current credential/grant and permission state,
plus immutable delegated plan evidence, without requiring the retired base
generation to remain active. Output does not consume source credentials.

This uses the existing authority envelope and binding digest, with no new durable
plan schema or compatibility path. Local pins remain denied: committed-base
version selection, exact owner/binding/destination checks and local candidate
acquisition still need runtime integration. No activation route, `in_use` claim,
or old-credential revocation guarantee is enabled by this step.

Research: Astra reviews identified that a live-only connection check would allow
permissions acquired after queueing to expand a job's authority. Flid discovery
found no corresponding durable job authority implementation; Rill's instance
connector acquisition and cancellation checks remain narrow prior art. The
implementation follows LeapView's existing ADR-0026 caller/delegated envelope and
canonical pipeline closure contracts.


### Committed-base refresh credentials (prerequisite)

The private refresh credential reader binds an immutable copy of the queued
caller or delegated authority to the job's committed base generation. It checks
an exact captured `connection.use` pair, that generation's authorization
snapshot, and current credential/grant and execution-principal permissions.
Ambient HTTP principals and worker identities confer no authority. The shared
committed-pin projection then checks the exact version, current binding revision,
connector, destination digest and declared customer owner. The existing runtime
resolver repeats authority checks after storage lookup and before decryption.

The reader uses the same per-consumption serving lease as the foreground check;
its future native consumer must acquire source-work admission first. The lease
covers synchronous consumption and cleanup. Both native cleanup failure markers
are observed before resolver error redaction, so uncertain cleanup retains the
lease. Escaping panics also retain it. This reader is tested with the real runtime
resolver but is not yet connected to a local candidate consumer.

Native refresh now performs a non-secret base-credential preflight after current
authority revalidation, before plan creation and again before candidate build.
It rejects any committed local pin in the base generation, including connections
outside the selected pipeline. This prevents candidate preparation from silently
losing a local pin or substituting provider credentials. Selected pins can receive
metadata authority checks; the preflight never reads encrypted payloads or
decrypts. Provider-only bases retain the existing refresh path.

Local candidate acquisition, sealing and publication must still preserve exact
pins. Before enabling them, the actual native materialization source set must
also align with the queued permission closure; native requests currently include
project-wide model/table inputs. No activation route, `in_use` claim, persisted
lifecycle schema or old-credential revocation guarantee is added here.

Research: Astra reviewed the existing native candidate path and these boundaries.
Flid Rill revision `4f814a86196fac2ba9664b9237826582de2dad03`,
`runtime/connections.go`, provides narrow prior art for separating system and
instance connector handles and checking cancellation after cache acquisition;
it does not supply durable queued-job authority. LeapView's ADR-0026 envelope,
committed release evidence and runtime resolver remain the governing contracts.


### Native refresh candidate scope admission

Before native refresh inspects connection bindings or prepares candidate pools,
it now compares the compiled candidate's actual work with the captured
`PipelinePlan`. Rebuilding the pipeline from that artifact must reproduce the
captured selection and binding digests, including the connection name-to-ID
mapping. The complete physical Model set and every source visited by
native source preparation must match the captured materialization/source
closure. Target-bound, authored and managed connection requirements must belong
to that closure's exact compiled Connection IDs. A shared artifact check applies
to fresh builds, recovered artifacts and successor builds; planning performs the
same check before resolving binding evidence. Read-only compiler inspection and
managed-pin/policy metadata lookup precede this check; managed root resolution
and materialization do not. Ordinary delivery without a pipeline plan retains
its existing admission contract.

This deliberately rejects a partial-project refresh when native execution would
visit additional Models, sources or connections. Current native materialization
uses project-wide inputs and does not support copying unchanged base relations
into a new candidate namespace. Reducing only the table list would still prepare
unselected sources and could produce an incomplete generation. Captured token or
grant authority is never expanded to make the candidate pass. Supporting partial
refresh requires a separate scoped-materialization/base-reuse implementation.

Local credential pins remain rejected by the committed-base preflight. Existing
candidate fingerprints, snapshot seals and release provenance already preserve
local version IDs; the missing governed local acquisition path must supply that
identity before activation can be enabled. No new durable schema, compatibility
path or context-based authority mechanism is introduced here.

Research: Astra independently traced candidate inspection, native materialization,
source preparation and recovery before approving this guard. Flid's Rill
`runtime/reconcilers/model.go` checks dependency references before reconciliation
and includes source/model reference identities in its execution-spec hash. This
is narrow prior art for binding work to dependencies, not a replacement for
LeapView's captured caller/delegated authority or credential lifecycle contract.


### Candidate pool acquisition authority

Native refresh carries its captured authority revalidator into candidate build
through the existing source-work context. Provider-backed pool acquisition now
uses that check after source admission and serialization waits, before credential
resolution, and after provider resolution, pool preparation and health checking.
A caller whose authority was revoked during a wait cannot continue into the next
credential or pool operation. Denial detected before saving validation state
returns without marking the binding degraded, writing validation state or
producing a credential-rotation success event. A prepared but uncommitted pool
is closed using the existing tracked cleanup path.

Shared refresh work does not confer one caller's authority on another caller.
Every waiter rechecks its own authority after shared work completes, and the pool
directory checks each acquisition before directory work and before returning a
cached or newly refreshed lease. A denied acquisition releases its lease. A
denied refresh leader may fail the shared refresh for otherwise authorized
waiters; they can retry normally. These boundary checks do not make external
permission changes atomic with the binding store's save operation. A later
per-caller lease denial does not undo an already completed shared refresh.

This reuses the existing revalidator and pool lifecycle. It adds no schema or
compatibility path and does not enable local credential candidate consumption or
activation. Exact local consumption, candidate evidence and publication still
need to be connected before the committed-base local-pin rejection can be lifted.

Research: Astra reviewed these boundaries before implementation. Flid's Rill
`runtime/connections.go` checks cancellation before cached connection acquisition,
and `runtime/connection_cache.go` returns a handle together with its release
function. This is narrow lifecycle prior art; LeapView's queued authority checks
come from ADR-0026 and the existing source-work revalidator.


### Scoped local connection consumption

The target runtime factory exposes a synchronous local connection operation for
an exact local credential snapshot. It prepares a transient PostgreSQL pool,
checks its credential identity and health, invokes one connection consumer, and
closes the pool before returning. The callback owns a temporary copy of the
connection's authentication fields; that copy is cleared when the callback ends.
The existing local health check uses this same lifecycle with a no-op consumer.
The ordinary pool resolver continues rejecting local credential identities.

The caller supplies authority and admission. This factory operation does not
read credential storage, select a version, acquire source-work admission or
register a serving pool. For the health check, the analytics module owns source
admission and the application reader holds the exact committed-generation lease
through the scoped operation. For future candidate source use, SourceRuntime
already owns admission and must invoke the operation inside the authorized
reader callback; acquiring admission again there could deadlock during a pause.
Source detach and temporary-secret removal must finish inside the connection
callback, before the transient pool closes and the credential reader returns.

Cancellation does not skip synchronous pool cleanup. Callback panics and native
cleanup uncertainty return fixed cleanup markers so the enclosing reader and
source-work owner can retain their leases. Provider, driver and consumer
diagnostics do not escape this boundary. Successful source staging can remain
usable after the credential scope ends because it no longer needs remote access.

This is the consumption primitive, not candidate integration or activation.
Native refresh preflight and active-serving resolution still reject local pins.
Candidate authority, exact-pin evidence and publication must be wired together
before either fence can be lifted. No new persistence or compatibility path is
introduced.

Research: Astra reviewed the nesting of source admission, credential-reader
leases, per-source callbacks and native cleanup before implementation. Flid's
Rill connection acquisition provides narrow prior art for paired handle/release
lifetimes and cancellation checks; no matching scoped local-secret consumer was
found. LeapView's existing SourceRuntime and credential lifecycle contracts
govern this operation.

### Job-bound candidate credential handoff

Native refresh now obtains a separate plan/build mutation port for each captured
job, after live authority and committed-base preflight checks. The port copies
the configured coordinators and installs the same job-bound connection adapter
for planning evidence and build acquisition. It does not change the shared
ordinary-delivery coordinator or carry a credential reader in context values.
Every requested connection must belong to the job's captured `connection.use`
permission set. Existing native artifact-scope checks still enforce the complete
materialization closure.

The adapter records exact committed local pins as non-secret metadata. Planning
and candidate registration preserve `CredentialVersionID` separately from the
provider version, so they produce the same binding fingerprint. Neither step
decrypts. A local connection cannot fall back to provider acquisition, overlap an
authored/provider connection, or disappear from the candidate requirements.
Changed ownership, binding revision, endpoint, version or captured permission
denies use.

Candidate source callbacks invoke the bound runtime reader and scoped local
connection factory inside existing SourceRuntime admission. Callback auth and
transient pool cleanup finish before the reader returns. The adapter preserves
the fixed cleanup-failure marker even when the reader redacts its diagnostic, so
the generation lease and source-work admission remain held when cleanup is
uncertain. Closing a candidate registration removes discovery, rejects retained
resolver calls and waits for callbacks before releasing its provider leases.

Production local refresh remains denied by the committed-base preflight. The
local candidate branch is integrated and tested behind that gate; this does not
enable publication, active-serving resolution or credential replacement. The
transactional publication admission below compares
successor local pins with the exact committed predecessor (or an explicit
credential activation receipt for changed pins). The existing post-commit
generation proof and semantic admission hook alone do not provide that comparison.

Research: PR #744 at `5938aeaa8a02c2b84084036961d7ce7edc29ddd9` keeps
deployment/bootstrap secrets in protected configuration and customer credentials
encrypted in PostgreSQL, and explicitly requires connection lifecycle handling
beyond a database transaction. Astra reviewed the job handoff and identified the
missing publication comparison before implementation. Flid's Rill connection
handle/release pairing and model dependency hashing provide narrow prior art;
LeapView's exact-pin and queued-authority contracts govern this implementation.

### Transactional credential-pin continuity

Ordinary publication must preserve the complete local credential-pin set from
the exact committed predecessor. Admission compares connection identity, binding
identity and revision, connector, credential version and endpoint digest inside
the existing activation transaction, after the target lock and predecessor CAS
check and before changing the active pointer. Adding or removing a local pin,
switching it to a provider credential, or changing any part of its tuple requires
an explicit credential lifecycle operation. The current prepared-activation
path only adds or replaces a local pin; removal and a switch to provider
authentication remain denied. Provider-only changes retain their existing
publication rules.

The comparison reads immutable candidate provenance through the caller's
transaction and binds it to the target, serving generation, candidate revision,
artifact and sealed binding fingerprint. The predecessor also needs durable
committed-publication proof. Missing or ambiguous evidence denies publication;
a qualified candidate or a matching version string alone is insufficient.

An explicit prepared credential activation permits only the connection covered
by its exact validation receipt to differ. Every other local pin must remain
identical. The existing receipt, current-authority and pending-operation checks
still apply. A receipt for one connection cannot authorize changes to another.

Rejection stays inside activation's savepoint, so a caller committing its outer
transaction cannot retain a partial pointer, operation, event or audit change.
Exact committed replays return the existing result without repeating mutation
admission. This is publication integrity, not evidence that running consumers
have switched: native local refresh and active-serving local resolution remain
blocked until their complete runtime handoff is qualified.

Research: PR #744's secrets lifecycle section remains the architectural
reference. Astra reviewed the target-lock placement, transaction-bound evidence
and single-connection receipt exception. Flid discovery found no direct
transactional credential-publication implementation; Rill's dependency hashing
is only narrow prior art for retaining exact dependency identity.

### Sealed activation preparation evidence

Preparing a successor runtime happens before its publication commits. Its
result-cache dependency identity therefore uses a separate, metadata-only
sealed-candidate evidence source. The normal active-runtime evidence source
continues to require committed-publication proof for local credential pins.

The preparation source binds the requested target, project, environment,
generation, candidate, seal and artifact to exactly one durable candidate
generation. It reads provenance for that exact candidate revision, validates
the provenance and compares its credential binding fingerprint with the persisted
seal's qualification evidence. Missing, ambiguous or substituted evidence denies
preparation. Private candidate preparation and sealed activation remain distinct;
sealed activation builds a production runtime without retaining a candidate
credential resolver or a queued caller's authority.

This check supplies only immutable dependency metadata. It does not decrypt,
open customer connections, authorize publication or report a credential as in
use. Publication still performs its transaction-bound continuity checks; normal
startup and active credential resolution still require their existing evidence.
Native local refresh and active-serving local resolution remain blocked pending
qualification of the complete runtime handoff and recovery contract.

Research: PR #744 at `5938aeaa8a02c2b84084036961d7ce7edc29ddd9`
requires connection lifecycle handling beyond a database transaction. Astra
identified the precommit metadata dependency and reviewed this separate evidence
path before implementation. Flid discovery found no direct implementation for
this boundary; the existing candidate-generation, seal and provenance readers
provide the relevant in-tree implementation.

### Runtime readiness during cutover

Readiness compares the full project, environment and generation identity from
the durable active serving state with the identity of an acquired local runtime
lease. A process still serving a previous generation after publication reports
HTTP 503 with the existing `runtime: failed` check. The lease is released on
success and mismatch, and the existing no-active-deployment policy is preserved.
Public readiness responses do not expose generation identities or dependency
diagnostics.

The existing sealed startup and periodic reconciliation paths load the durable
generation. Readiness becomes successful once the observed local and durable
identities match; a failed reconciliation leaves it unsuccessful. This is a
point-in-time check for new readers, not a lock on publication and not proof
that old readers have drained or that an upstream credential can be revoked.
Local credential refresh remains fenced until its materialization-to-serving
handoff is qualified.

Research: PR #744's connection lifecycle requirement motivates distinguishing
durable publication from process-local convergence. Astra identified and
reviewed the missing readiness comparison. Flid discovery had no direct match;
the existing runtime-host reconciliation and lease lifecycle are reused.

### Physical local-credential materialization qualification

The analytics module retains its configured extension admission and forwards it
to materializer and project runtimes. Source preparation therefore uses the
same explicit admission policy as the isolated target connection factory.
Missing admission still denies source extensions; no implicit installation or
fallback is added.

A native candidate can materialize a real PostgreSQL source into a local DuckLake
snapshot through its registered job-bound connection resolver and the analytics
module's scoped local connection factory. Planning and registration use metadata
only. The source callback reads the exact committed credential version through
the runtime reader; callback authentication and source access end before the
materializer reports its snapshot. Closing candidate registration removes its
source capability.

The integration test exercises that complete consumption path with a disposable
TLS PostgreSQL source, the existing deterministic credential repository/keyring
fixture, and a real DuckLake catalog. It checks source-work admission, credential
read counts, authentication cleanup, and reopened snapshot results without
another credential read or source resolution. This qualifies consumption and
snapshot serving; it does not qualify durable credential storage, publication,
activation, or upstream revocation. Existing lower-level failure tests retain
coverage for uncertain cleanup and held generation/source-work leases.

Local refresh preflight and active-serving local resolution remain denied.
The next qualification connects this consumption path to native refresh
finalization and publication before changing the refresh preflight.

Research: PR #744 at `5938aeaa8a02c2b84084036961d7ce7edc29ddd9`
requires scoped version use and connection cleanup beyond a database update.
Astra reviewed the fixture boundaries and acceptance checks before implementation.
Flid's Rill `AcquireHandle` and connection-cache release pairing provide narrow
prior art; LeapView's existing candidate, source-work and snapshot contracts
remain the governing implementation.

### Native refresh publication with retained local pins

After a candidate is admitted, ordinary refresh finalization preserves the
committed predecessor's complete local credential-pin tuple. The refresh
publication unit of work calls the native finalizer and ordinary credential
admission through the same PostgreSQL transaction as the refresh publication
link, data version, run completion and job completion. A changed credential
version, endpoint or switch to provider resolution cannot be published as an
ordinary refresh.

The PostgreSQL qualification joins those existing authorities with persisted
predecessor and successor provenance. An injected error after the actual job
completion rolls back the native pointer, publication, activation event/audit,
generation retention root and refresh completion records. The prepared run and
running job remain available for retry. Retrying with the unchanged pin commits
once; replay after a lost acknowledgement does not advance the target again or
duplicate consequences. A different snapshot is rejected.

This test starts at the prepared-job/admitted-result boundary. Physical build,
canonical artifact verification and physical identity resolution are explicit
fixture seams; pin admission, finalization and transactional persistence are
real. Together with the preceding materialization qualification, it establishes
separate consumption and publication contracts, not a complete native execution
journey. It does not establish runtime convergence, old-reader retirement or
permission to revoke an upstream credential. Local refresh preflight remains
closed pending qualification of the composed execution path.

Research and review: Astra reviewed this boundary before implementation against
PR #744 at `5938aeaa8a02c2b84084036961d7ce7edc29ddd9`. Flid discovery found no
direct refresh-finalizer precedent; the existing native finalizer and pin
continuity tests supply the implementation pattern. No new recovery service,
compatibility path or publication mechanism is introduced.

### Native refresh completion and runtime cutover

The production completion coordinator is shared with a PostgreSQL qualification
that exercises the real runtime host. It resolves the exact admitted generation,
target, plan and candidate; validates publication ownership; prepares that sealed
runtime; and supplies durable refresh completion as the runtime host's activation
callback. New readers switch only after that callback succeeds. Readers already
holding the predecessor can finish; the host closes the predecessor after its
last reader releases it.

The qualification proves that preparation failure never enters completion, and
that a late transactional completion failure leaves both the durable target and
the process runtime on the predecessor while discarding the prepared successor.
On success, PostgreSQL commits before the process pointer changes. Exact replay
does not duplicate publication consequences, and an altered snapshot cannot
replace the running runtime.

A lost commit acknowledgement remains an explicit uncertain outcome: completion
returns an error and the local runtime stays on the predecessor even though the
durable target has advanced. The qualification injects that outcome, then checks
both reconciliation from the durable target and a fresh runtime-host startup.
Both recover the committed generation without another publication. This uses
the existing sealed-host recovery contract; it does not make the database and
process pointer an atomic operation or authorize upstream credential revocation.

The fixture starts with a prepared job and admitted result. Physical preparation,
canonical artifact verification, physical identity resolution and publication
ownership policy remain deterministic seams. Serving-state reads, retained-pin
admission, native finalization, completion persistence, runtime pointer changes
and in-process reader retirement use their production implementations. It does
not exercise the full executor/materializer journey, real DuckLake query leases,
multi-process convergence or source-connection credential rotation. Local refresh
preflight and generic active-serving local source resolution remain closed.

Research and review: Astra reviewed the boundary against PR #744 at
`5938aeaa8a02c2b84084036961d7ce7edc29ddd9`. Flid's Rill connection acquisition and
release pairing supplies narrow lifetime precedent; it has no direct equivalent
of LeapView's native completion coordinator. The next qualification must join
the existing source-consumption path to native execution and this completion
boundary before local-credential refresh can be enabled.

### Native refresh orchestration with local source materialization

A bounded integration qualification drives `Service.ExecuteClaimedJob` with the
native refresh executor and shared completion coordinator. A test build adapter
uses the existing job-scoped candidate connection authority to materialize real
TLS PostgreSQL source rows into DuckLake. The captured job authority remains
present and is revalidated at the service, executor and source-work boundaries.
The executor's result must carry the actual materialized snapshot and the exact
candidate/generation evidence supplied by the build adapter.

On success, the adapter closes candidate registration and the source session
before build-command completion and the publication callback. On a real source
query failure, the service takes its failure path without completing the build
command, preparing the successor runtime or invoking publication. Both outcomes
check source cleanup and that plaintext credential material is cleared.

Production preflight still rejects the local pin before mutations or decryption;
the test exercises that rejection separately. Its successful downstream cases
use an explicit test-only preflight allowance for the captured fixture job.
They do not demonstrate a successful production local-credential refresh.

Credential metadata, record storage and keyring, native plan/build persistence,
immutable result metadata, workflow persistence, runtime preparation and
publication are deterministic test seams here. The test adapter owns
registration-close sequencing. This qualifies
service/executor/completion ordering around real source work, not the production
`NativeBuildCoordinator` lease lifecycle or durable publication of this DuckLake
snapshot. The preceding PostgreSQL/runtime-host qualification remains a separate
test of the prepared-result completion boundary.

### First-delivery native build materialization failure

An integration test drives the actual `NativeBuildCoordinator.BuildPlan` with
PostgreSQL-backed operation and attempt stores, a PostgreSQL DuckLake writer,
and a real local credential read against TLS PostgreSQL. It drops the source
relation before execution and checks that the source query fails, the build
attempt and operation settle as indeterminate, the candidate remains building,
the target lease is released, no seal or generation is recorded, and the local
connection and plaintext credential are cleared.

This is deliberately a first-delivery failure case with no `PipelinePlan`. It
qualifies source consumption and durable failure settlement through the native
build coordinator. It does not qualify generation-bound pipeline admission,
base-pin continuity, successful build qualification or sealing, or publication.
The shared catalog fixture now derives its runtime tuple from admitted extension
artifacts and runs the existing local physical-pool conformance checks. The
focused Docker-backed runtime test and PostgreSQL conformance
application shard 3 both passed in this workspace.

The test calls the coordinator directly, so it bypasses the production refresh
executor preflight. Production continues to reject local pins through
`checkBaseCredentials`; this test asserts that denial separately. It does not
authorize opening local-credential refresh. No compatibility path or new
lifecycle mechanism is added. PR #744 at
`5938aeaa8a02c2b84084036961d7ce7edc29ddd9` remains the design reference; Flid's
Rill connection acquisition/release pairing supplies narrow lifetime precedent,
not a replacement for LeapView's native build and publication contracts.

### Local credential physical build and exact-snapshot qualification

A lower-level integration qualification uses the real candidate connection
registration and `BuildNativePhysical` to materialize TLS PostgreSQL source rows
into a PostgreSQL-backed DuckLake catalog. It checks the exact credential version
and destination, source-session closure, cleared plaintext, and the committed
snapshot's marker and physical closure. The shared catalog fixture derives engine
versions from admitted extension artifacts, runs local pool conformance, and
persists bootstrap compatibility through the existing administrative transaction.
Read-only qualification consults that record through the restricted runtime role.

After closing candidate registration, the test revokes its fixture source token
and drops the upstream table. `QualifyNativeSnapshot` must still pass source-schema
and model row-count checks against the exact committed snapshot without another
credential read. Altered snapshot or compatibility evidence must fail.

This test supplies an attempt value; it does not durably admit or complete a build
attempt, persist source observations, seal a serving generation, or publish it.
Credential metadata, record storage, keyring and artifact/plan identities remain
fixture inputs, and the test owns candidate-registration sequencing. It does not
establish immutable artifact provenance. A successful `NativeBuildCoordinator`
journey with immutable artifact/policy evidence and transactional generation
admission is covered separately below before joining publication and activation.
Production local-credential preflight remains closed and is asserted separately.

Astra reviewed this boundary against PR #744's credential-lifecycle requirements.
Flid's Rill acquisition/release pairing supplies lifetime precedent; the existing
LeapView physical-build and qualification factories provide the implementation.

### Local credential native build and generation admission

An integration qualification joins the existing native source synchronizer,
release artifact phases, physical build, snapshot qualification and generation
admission. Authored source bytes are retained through the PostgreSQL source
repository and an immutable memory object store. The release module compiles the
retained project and produces the serving artifact against a persisted target
authorization-policy revision. The coordinator owns candidate connection lifetime,
source-observation capture and the transaction that completes the operation and
admits its sealed generation.

The test checks the exact local credential version and destination in retained
provenance, the admitted artifact and snapshot identities, and source-connection
cleanup. Repeating the completed request after revoking the fixture source token
and removing the upstream relation must return the same sealed result without
another credential read or materialization. The target remains unpublished:
sealed generation admission is not runtime activation or permission to revoke an
old external credential.

This remains a direct first-delivery coordinator qualification. Credential record
storage, keyring and captured job authority use the existing test fixture, and
the immutable object store is in memory. The direct planner and build coordinator
do not enable production compound authorization; persisted policy identity is
still checked. This does not establish the generation-bound
refresh pipeline, crash recovery during an incomplete build, publication or
consumer activation. Production local-credential preflight remains closed and is
asserted separately. Those boundaries must be joined before enabling local pins.

### Transaction-bound activation access authority

`access/postgres.AuthorizeCredentialActivationTx` checks both exact
`connection.manage` and `connection.use` permissions in a caller-owned
READ COMMITTED transaction. It binds the supplied generation snapshot to its
persisted digest, intersects its captured authority with the current locked
policy, and checks the active principal, effective group memberships and exact
browser-session or API-token evidence. API-token permissions remain a separate
ceiling. Locks fence revocation through the caller's transaction; concurrent
membership additions are conservatively ignored. Credential expiry is checked
with the database clock after potentially blocking authority reads.

PostgreSQL tests cover exact scope, current policy and membership revocation,
API-token attenuation, caller-owned transaction lifetime and revocation locks.
The expiry regression holds a token row lock until the database clock passes
expiry and requires rejection after release.

This is the access decision, not permission to publish or report a credential
as in use. The app-owned callback must still bind the receipt actor, customer
owner, exact binding/configuration and target revision/predecessor in the same
transaction. Publication approvals remain independent. The caller must perform
no provider work while holding the transaction. Production activation remains
closed until that composition, consumer drain, runtime installation and recovery
are qualified together.
