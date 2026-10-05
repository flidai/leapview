# Credential foundation scope review

Status: D02 scope cleanup; final validation and maintainer acceptance recorded separately

Review date: 2026-10-02

This is the scope and handoff for [PR #785](https://github.com/flidai/leapview/pull/785),
owned by Anand under [D02 / FAI-1019](https://linear.app/flid/issue/FAI-1019).
[Ganesh's D12 / FAI-1029](https://linear.app/flid/issue/FAI-1029) completes the
customer credential lifecycle after the reviewed foundation and shared D01/D11
contract. The approved Linear roadmap assigns those responsibilities separately.
No new D12 feature belongs in this foundation PR.

## Retained foundation

| Behavior | Implementation and evidence |
| --- | --- |
| One server-owned customer and independently provisioned keyring | `internal/credential/module/{scope,setup}.go`, offline `admin credentials setup`, platform owner storage and same-transaction audit; keyring configuration and setup tests. Request bodies cannot select an owner. Platform-funded keys remain deployment configuration. |
| Encrypted immutable drafts and scoped metadata | `internal/credential/{credential,service}.go`, `encryption/`, PostgreSQL draft repository and foundation module/API; exact context, key custody, encryption-budget and audit tests. |
| Isolated validation | `internal/credential/validation.go`, receipt repository/module API and analytics credential probe. Exact version, current binding/configuration and actor authority; PostgreSQL password only. Validation neither promotes a pool nor changes active credentials. |
| Non-replayable secret commands | Product protocol, generated APIGen policy and browser command transport. Reject replay keys before body persistence; strict bounded input and redacted errors. Keep the generator and end-to-end transport coverage together. |
| Honest unavailable-operation behavior | Remove the old pool-promoting Test route/action without compatibility aliases. Explicit Refresh retains its existing meaning. No draft selector, activation endpoint or UI status claims in-use credentials. |
| Foundation composition | Startup owner/keyring checks, authorized API routes and cleanup on failed credential initialization. The extracted build helpers/lifecycle file retain preexisting startup behavior; they are not a new deployment lifecycle. |

## Retained denial dependencies and schema disposition

Ordinary publication keeps its mandatory composition-owned admission port,
exact local-pin continuity and committed-generation/provenance evidence. It
rejects introducing or silently changing a local credential pin. The pending
reader runs at READ COMMITTED after the delivery target-lock wait; its unchanged
`aborted_at IS NULL` predicate includes committed records. Missing schema and
stale transaction isolation remain errors, not permission to publish.

Active serving and refresh preflight continue to reject local credential pins.
The retained provider-only paths must behave as on main; removing a local
credential factory is not permission to omit a preflight denial.

Schema allocation for this implementation is explicit:

- **048–050:** draft/envelope storage, immutable customer owner and validation
  receipts are foundation storage.
- **051/052:** operation identity and abort state are the minimum schema read by
  installed pending-operation denial.
- **053/054:** retain the existing switching/commit columns and transition guards
  unchanged with their migration tests. This preserves the recorded migration
  chain and denial coverage for committed states. No phase mutation service or
  production activation API is retained. Their inclusion is a bounded schema
  exception for maintainer review, not a claim that the pending SQL requires them.

Do not rewrite the schema chain as part of scope cleanup. Do not silently reopen
publication because activation writers are absent. D12 must define completion
before changing the conservative non-aborted-record denial.

## Completed extraction boundaries and preserved handoff

| Group | Disposition |
| --- | --- |
| Activation access authority | Uncomposed adapter, three private test files and seven access queries removed; shared authorization remains. |
| Receipt-backed publication | Uncomposed adapter, candidate-pin replacement exception and private tests removed; ordinary all-pin continuity and pending denial remain. |
| Preparation/switching/commit/abort | All phase writers, domain types, recovery readers and seven private queries removed. Current audit inventory no longer advertises their producer. Existing admission tests seed legal persisted states under real schema guards and target locks. |
| Runtime consumers and lifetime machinery | Remove customer-credential runtime readers, foreground/candidate adapters and their consumer-specific tests. Restore provider runtime/pool/source/refresh behavior from main, retaining the local-pin denial dependencies above. D11/D12 own admission/drain/retirement and restart completion. |
| CI/browser/security extras | Restore unrelated sharding/planner/workflow, browser-stability and advisory changes to main. Keep only credential generation and non-replayable transport coverage. Existing baseline security evidence remains tied to the unchanged package manifest. |
| Generic release/deployment extras | Remove historical-qualification changes, demo adapter changes and added readiness switching behavior. Retain current exact-pin evidence, the preexisting v4 release reader, and strict reading of v5 provider-only provenance: existing serving records must remain readable after D02 introduces v6 local-pin evidence. A frozen v5 fixture verifies unchanged digests; v5 cannot carry local credential pins. No legacy credential reader or v5 writer is introduced. |

The first three groups and their evidence are preserved at
[339f364ca](https://github.com/flidai/leapview/commit/339f364ca5ccc1ce57049ca54619b40595010027)
on `codex/credential-activation-access-preserved`.
The full pre-cleanup revision, including the other groups, is preserved at
[059f61ff2](https://github.com/flidai/leapview/commit/059f61ff22601d04edbb0a11cf74c800287973b7)
on `codex/credential-d02-scope-preserved-059f61ff2`.
These are recovery snapshots, not separate approved PRs. Ganesh can inspect the
D12 work without requiring it to land in D02; CI/release/deployment owners can
review their own changes independently. No handoff implies accepted interfaces.

## Validation and remaining acceptance

The PR description records the exact tested revision, command results and hosted
checks. Required evidence includes foundation storage/validation/audit and secret
transport tests, provider-only refresh behavior, installed local-pin and pending
operation denial, schema/migration tests, generated contracts and `task ci`.
Astra reviews the final dependency boundary as well as the initial extraction
plan. Flid discovery supplied no directly applicable example; in-tree callers,
mainline behavior and the Linear scope determine this extraction.

Maintainers still need to:

1. Review ADR-0027 and this D02 boundary, including the retained schema exception.
2. Reconcile D01/D11 candidate-first Kamal overlap and incompatible credential
   transitions before accepting the affected D12 contract. Define exact-version
   validation, serialization/current authority, all affected consumers, durable
   completion/restart and confirmed old-consumer closure before external revocation.
3. Review and merge the exact validated D02 candidate through the normal queue.

Broader key recovery, retained-backup decryption, audit export/retention and
managed/customer isolation qualification remain with D12/D13 and their existing
security/operations owners. No extra backup engine, consumer registry or
compliance subsystem is added here. Passing foundation tests does not establish
production activation, profile acceptance or safe upstream credential revocation.
