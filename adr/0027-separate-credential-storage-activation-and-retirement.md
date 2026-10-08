# ADR-0027: Separate credential storage, activation and retirement

Status: proposed

Decision date: 2026-09-27

Proposal date: 2026-09-27

Review: pending; the date records this proposal, not maintainer acceptance.

Implementation: D02 encrypted draft setup/API, isolated validation receipts,
transactional audit and non-replayable secret transport, with installed denial
checks. Activation writers and runtime/lifetime consumers are preserved for D12,
not exposed in production. The companion scope review records the retained
operation-schema exception and the separate maintainer acceptance gate.

Deciders: LeapView maintainers

Supersedes: none

Amends: [ADR-0003](0003-retain-narrow-infisical-resolver.md), proposing PostgreSQL
as the default customer credential store and the narrow Infisical resolver as an
optional integration. Its security invariants remain; accepted ADRs are unchanged.

Related: [deployment proposal #744](https://github.com/flidai/leapview/pull/744),
[publication authority](0007-adopt-plan-driven-project-delivery.md),
[audit](0015-adopt-durable-audit-and-compliance-controls.md),
[resource permissions](0025-adopt-typed-resource-permissions-and-scoped-api-credentials.md),
[workload authority](0026-preserve-authority-across-governed-operations.md),
[research inventory](specifications/credential-lifecycle-inventory.md),
[implementation contract](specifications/credential-lifecycle-contract.md).

## Context and constraints

PR #744 separates deployment/bootstrap secrets from customer credentials and
proposes application-encrypted PostgreSQL storage with an independent keyring.
Jacob's subsequent scope clarification explicitly excludes backward compatibility
and asks for a small design. This proposal therefore specifies a clean break:
no legacy ciphertext reader, dual-write/read rollout, automatic credential import,
old API compatibility or downgrade across an incompatible credential-format
transition. The inventory describes existing behavior; it is not a requirement
to preserve every old path.

Saving a credential still does not prove the application uses it. Existing source
publications pin exact provider versions, and agent runs pin configuration
revisions. These are current product correctness requirements, including for
records created after the change; removing backward compatibility does not make
those references safe to overwrite.

## Delivery boundary and decision reconciliation

The 1 October 2026 migration roadmap assigns [PR #785](https://github.com/flidai/leapview/pull/785)
to D02, the credential ADR and foundation. D12 owns completion of the customer
credential lifecycle, coordinated with D10 bootstrap and D11 application lifecycle.
Keep activation/runtime feature work out of D02. The
[scope review](specifications/credential-foundation-review.md) records the
implementation split and retained denial dependencies. Extraction and passing
tests do not accept this proposed decision.

The [deployment proposal in PR #744](https://github.com/flidai/leapview/pull/744)
has been removed from main pending renewed review. Its historical revisions do
not establish an accepted managed lifecycle. The single-process assumptions below
limit the foundation's component evidence; they do not qualify a managed profile.
D01/D02 must reconcile the process contract before accepting affected lifecycle
implementation, and D11/D12 must agree ownership, admission/draining, publication
and restart interfaces before implementing their combined path. Readiness alone
must not grant worker, mutation, activation or retirement authority. A stop-first
maintenance profile requires a separate reviewed amendment to the deployment ADR
and roadmap, named interruption bounds and its own qualification.

Classify an incompatible credential-format transition before durable mutation and
use its reviewed maintenance/recovery procedure, not ordinary image rollback.
Within a declared compatible release window, retain usable credential versions,
keys and acknowledged writes for the live and rollback releases. This does not
introduce a legacy credential reader. Decision acceptance, implementation review,
profile qualification and deployment adoption remain separate gates.

## Decision

### Store each secret with its owner

- Deployment/operations secrets remain scoped to their operator jobs and hosts.
- Bootstrap secrets, including LeapView's PostgreSQL access and versioned
  encryption keyring, remain in protected operator configuration.
- Customer connection and agent credentials use one authorized, audited
  application service shared by UI, API and bootstrap/CLI, backed by encrypted
  PostgreSQL records. Verification-only passwords/tokens retain their hashing.

Bind a credential to one exact connection binding or instance-agent configuration
family. V1 declares one customer owner during instance setup; domain credentials
bind to that server-owned declaration, never to an owner submitted with a secret.
Instance administration alone does not establish customer ownership.
Platform-funded provider accounts remain bootstrap-owned. Do not add per-account
ownership administration, cross-resource sharing or a provider framework.
Infisical may remain explicitly selected through the existing narrow resolver;
it is not a required dependency or automatic fallback for PostgreSQL credentials.

Use standard authenticated encryption, random nonces, key IDs and authenticated
owner/resource/purpose/version context. Keep logical secret versions immutable;
rewrapping their ciphertext does not change the version a publication/run pins.
Keep the keyring outside PostgreSQL with an independent encrypted recovery copy.
Missing keys fail closed rather than being regenerated over encrypted records.

### Single-process foundation assumptions

The current foundation assumes one supervised process with colocated workers,
stop/start deployment and no process overlap. Reuse the instance home lock and
existing pool, runtime-host and publication transaction mechanisms. Deployment
must give that lock a shared host path; it does not establish exclusion across
hosts or isolated container filesystems. These component assumptions establish
neither a supported managed deployment profile nor cross-process drain evidence.
The proposed lifecycle below remains subject to the D01/D11/D12 reconciliation.

Within that single-process model, a lifecycle service would close an in-process
admission barrier, drain current credential consumers, commit existing
publication/configuration authority, then install and verify the committed state
before reopening. Persist the exact operation and phase with transactional audit;
do not introduce a durable
per-consumer registry, membership service or distributed acknowledgment protocol.
An unresponsive consumer leaves the change blocked until it stops or the
supervisor confirms the entire process has exited.

Every restart starts with admission closed, reads durable authority and rebuilds
runtime readiness. An incomplete pre-commit operation needs a currently authorized
retry or abort. After commit, startup may load only the committed state under
current resource-owned runtime authority; it cannot publish another version.
Do not introduce a new delegated background activation workflow.

### Report completion precisely

Expose `saved`, `validated`, `switching`, `in_use` and `blocked` separately.
Validation identifies the exact version, destination/configuration, expected
revision and actor. Concurrent edits or expired validation cannot authorize a
commit. Source credentials still require a candidate/publication; agent saves
commit a configuration revision referencing the credential version.

`in_use` means the committed version has been installed and checked in the live
process. Persisted past success alone is not current readiness after restart.
Keep old versions while current generations, rollback references or resumable
runs still need them. Retire a version only after explicitly resolving those
references, denying further use and draining its local consumers. Upstream
revocation and termination of source-side sessions remain separate actions.

## Alternatives and consequences

One mutable secret plus an asynchronous refresh cannot explain partial failure
or preserve exact version pins. A distributed secret-management platform would
solve a larger problem than this release needs. The selected approach adds a
small durable lifecycle around existing runtime and publication boundaries.

This single-process model requires an instance-wide pause for credential-backed
work and does not solve managed release overlap. Blocking failures can require
operator restart. Legacy-format credential data and backups requiring that format
are not promised compatibility; an unsupported format must be rejected with setup
guidance, never silently erased
or guessed. Any destructive reset is a separate explicit operation.

Key rotation and restore of the new format remain required. Keep old keys for
supported database backups. This does not add backup software, a general key
custody platform, hosting migration or automatic external credential revocation.

## Confirmation

The [companion contract](specifications/credential-lifecycle-contract.md) specifies
transaction boundaries and crash outcomes. Qualify stale/concurrent validation,
secret redaction, scoped encryption, publication pins, process exclusion,
consumer draining, restart after commit, retirement, interrupted rewrapping and
restore with missing keys. These are implementation acceptance criteria, not
claims that the behavior already exists. Maintainer acceptance is still pending.
