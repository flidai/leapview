# Credential foundation scope review

Status: activation-access and receipt-publication adapters extracted; remaining allocation and maintainer decisions pending

Review date: 2026-10-02

This is the review package for D02 ([PR #785](https://github.com/flidai/leapview/pull/785)),
not an additional implementation phase. It refines the
[delivery scope](credential-lifecycle-contract.md#delivery-scope-and-shared-decisions)
using the 1 October migration roadmap and the technical acceptance requirements
from the 1 October managed-service assurance assessment. It does not adopt that
assessment's legal conclusions, operational status, dates or programme scope.

## Evidence and recommendation

The inspected implementation is `0b01b57cddff6e71b7c123c81c44ae767cd2305c`,
compared with its integrated mainline `f41e2a1c98e0e1c737812d30fbac808bfaf65261`:
385 changed files. These pins describe the review baseline, not current main or
proof of merge readiness. The deployment reference is
[PR #744's ADR-0028 at b305bcc](https://github.com/flidai/leapview/blob/b305bcc616892538a27bd3d255fe0b4e9fa66dff/adr/0028-share-an-open-deployment-stack-for-self-hosted-and-managed-leapview.md#decision-reconciliation-and-acceptance-boundary).

Recommend a narrow foundation plus the dependencies of its installed denial
checks. Allocate activation writers and consumer integration to D12 after the
D10/D11 interfaces are reviewed. Separate independently useful CI, release and
deployment fixes under their existing owners. The tables identify concrete
files and related test families; they are not a patch or an exhaustive assignment
of every changed hunk. No file is safe to delete solely because of its directory.

### First extraction: activation access authority

The uncomposed `AuthorizeCredentialActivationTx` adapter has been removed from
D02 together with its three test files and seven private SQL queries in
`internal/access/postgres/queries/{authorization_policy,durable_grants}.sql`.
The removed Go files are `internal/access/postgres/activation_authority.go`,
`activation_authority_test.go`, `activation_authority_lock_test.go` and
`activation_authority_scope_test.go`. The adapter had no production callers;
its helpers and added queries were used only by this implementation and tests.

The code and test evidence remain preserved at
`339f364ca5ccc1ce57049ca54619b40595010027` on the branch
`codex/credential-activation-access-preserved`. This is a preservation snapshot,
not a standalone D12 PR or an accepted lifecycle implementation. Future D12 work
can recover this slice after review of the shared contract. This extraction does
not change installed publication or runtime denial, or migrations 051/052.

Activation phase writers, consumer/runtime integration, retirement machinery and
supporting CI/release/deployment changes remain in D02 pending further extraction
and review. The allocation below remains proposed for those changes. This first
slice neither completes the split nor accepts ADR-0027/ADR-0028 reconciliation.

### Second extraction: receipt-backed publication admission

The uncomposed `newCredentialPublicationAdmission` adapter and its candidate-pin
verification have been removed from D02, along with their receipt-only tests and
helpers. The single-connection replacement exception has also been removed from
`verifyPublicationCredentialContinuity`: ordinary publication still compares the
complete local-pin set exactly, as it did before this extraction.

The shared provenance-reader interface, ordinary publication fixture, pin
continuity/evidence tests and refresh finalization/runtime tests remain. The
fixture still installs credential schema because the pending-operation fence
reads it. Migrations, pending-state checks and activation phase writers are
unchanged. The removed adapter and tests are preserved at the same
`339f364ca5ccc1ce57049ca54619b40595010027` snapshot above; their files were unchanged
between that snapshot and the pre-extraction revision `a00b5c61a`.

The candidate refresh factory is installed in `postgres_build.go` and also serves
provider-only jobs with captured-authority checks. It is not unused merely
because local credentials remain denied by preflight; its extraction requires a
separate dependency review. This second slice does not complete the wider split
or resolve the shared lifecycle decisions.

### Retain in D02

Paths below are repository-relative; matching tests travel with their behavior.

| Behavior | Files and boundaries |
| --- | --- |
| Encrypted immutable drafts, scoped metadata, audit and validation | `internal/credential/credential.go`, `encryption/`, `service.go`, `validation.go`; draft/receipt portions of `internal/credential/postgres/repository.go`, `validation.go`, `schema.sql` and `queries/credential.sql`. Retain migrations `048_credential_draft_storage.sql` through `050_credential_validation_receipts.sql` under `internal/platform/postgres/migrations/`. |
| Server-owned customer identity and operator-provisioned keyring | `internal/credential/module/scope.go`, `setup.go`; `internal/platform/bootstrap/postgres/customer_owner.go` and its schema/query changes; `internal/app/adminpostgres/credential_setup.go`; `internal/admin/cli/credential_setup.go` and command registration. Retain keyring configuration in `internal/app/config/spec/spec.go`, its generated schema and setup documentation. Platform-funded keys stay in deployment configuration. |
| Save/metadata/validation composition | `internal/credential/module/apigen.go`, `apigen_validation.go`, `contracts.go` and foundation portions of `composition.go`; `internal/app/credential_composition.go`; `internal/analytics/module/credential_probe.go`; foundation authorization in `internal/access/module/credential_authority.go`; app route/startup wiring and `api/typespec/credentials.tsp` with generated contracts. Offline owner setup is not customer-secret bootstrap or first publication. |
| Non-replayable secret transport | `internal/app/api/protocol/nonreplayable.go`, protocol/APIGen handler wiring; command-idempotency source and generated artifacts under `pkg/apigen/`; `internal/platform/web/actions/` and `web/components/shared/command.ts`. Keep the end-to-end contract together, even if generic APIGen support lands as a prerequisite PR. Never restore durable secret-body replay to simplify a split. |
| Existing unavailable-operation behavior | Changes to connection-binding API/UI contracts and commands that remove unsupported rotation behavior. Extract by hunk; no split may reintroduce a route or UI promise that a stored draft is active. |

### Preserve installed safeguards during any split

| Safeguard | Dependency that must remain covered |
| --- | --- |
| Ordinary publication rejects every non-aborted preparation | `internal/app/deploymentpostgres/composition.go` installs `newOrdinaryCredentialPublicationAdmission` from `credential_admission.go`; it calls `internal/credential/postgres/pending.go`. `CheckNoPendingActivation` in `queries/credential.sql` filters on `activation_preparation.aborted_at IS NULL`, including preparations whose commit was recorded. Keep the reader, transaction/fence checks and supporting **051/052** schema while this composition remains. The query alone does not require 053/054. |
| Ordinary publication cannot introduce or silently change a local credential pin | `internal/app/deploymentpostgres/credential_pin_continuity.go`, its ordinary-admission caller, committed-generation readers in app/deployment PostgreSQL adapters, and exact release-provenance evidence. Preserve the ordinary path independently of the receipt-backed activation adapter proposed for D12. |
| Active serving and refresh reject unsupported local credentials | Denial branches in `internal/analytics/module/active_runtime_bindings.go` and `internal/app/credential_runtime_refresh.go`, with their production composition and tests. These are mixed with proposed consumer machinery; preserve the denial without treating the whole file as automatically accepted D02 scope. |

Recommendation for the pending-operation fence: retain its minimum schema and
reader as an explicit D02 prerequisite. An alternative extraction must first
demonstrate equivalent denial and account for all retained producers/readers.
Do not remove the check, swallow missing-table errors, or assume setup being
disabled makes existing operation records irrelevant.
The installed reader does not distinguish committed from still-switching work;
D12 must define completion/reopening before changing this conservative denial.

### Allocate to follow-up implementation or its existing owner

| Work | Concrete extraction candidates and exceptions |
| --- | --- |
| Credential phase writers and activation authority | `internal/credential/{preparation,switching,abort,commit}.go`, matching PostgreSQL writers/tests. The uncomposed access-authority adapter and its private queries were extracted in the first slice above. D12 owns their future composition. Review 053/054 switching/commit migrations with these callers; preserve the 051/052 reader dependency above. |
| Credential consumption and candidate publication | `internal/credential/runtime.go`, `internal/credential/module/runtime_contracts.go`; consumer portions of `internal/app/credential_runtime*.go`, `candidate_local_connections.go`; native candidate/refresh integration tests. The receipt-backed publication adapter is preserved for D12 follow-up as described above; the app activation authority callback remains missing. Keep installed ordinary publication denial and provider-only refresh behavior. |
| Pool/source/worker lifetime and restart | Retirement and cleanup changes in `internal/analytics/connectionbinding/`, `internal/analytics/duckdb/`, `internal/analytics/sourcework/`, `internal/runtimehost/`, and refresh execution/completion under `internal/refresh/` and `internal/app/refreshpostgres/`. D11/D12 must agree cross-process ownership and drain first. Existing ordinary-runtime correctness fixes need separate review before extraction. |
| CI, browser stability and advisory evidence | Shard changes in `Taskfile.yml`, `.github/workflows/{merge-validation,nightly}.yml`, `internal/platform/ci/`, `scripts/frontend_ci_contract.test.ts`, `package.json`, related CI docs and browser tests; `.security/javascript-vulnerability-evidence.json`. Prefer separate CI/security review; retain foundation-specific generation and transport coverage. Each `.gitleaks.toml` exception follows its exact non-secret fixture. |
| Generic release compatibility | Historical version-five support in `internal/release/provenance.go`, `provenance_test.go`, `testdata/provenance_v5.json` and the compose historical-transition test. Review under the release owner; separate these hunks from current credential-pin evidence. This is not a legacy credential-format reader. |
| Demo readiness and startup cleanup | `scripts/deploy_demo.sh`, `scripts/tests/test_demo_publish_adapter.py`, `internal/app/runtime_readiness.go`, `runtime_router.go`, `postgres_lifecycle.go` and relevant tests. Deployment/runtime owners review the connected fixes. Readiness does not authorize credential mutation or retirement. |

### Mixed files and extraction checks

- `internal/app/postgres_build.go` and `postgres_build_helpers.go` combine
  foundation wiring, moved helpers and runtime changes. Retain working startup,
  route authorization, redacted failures and denial composition together.
- `internal/credential/module/composition.go` initializes both draft/validation
  services and runtime-reader dependencies. Split fields and constructors with
  their callers; do not replace missing authority with permissive defaults.
- Credential SQL/schema, `sqlc.yaml`, migration registry/tests and generated
  bindings must describe the same retained schema. Preserve published migrations
  045–047; reconcile draft numbering with main before extraction. This proposal
  neither renumbers migrations nor authorizes rewriting an applied migration.
- `Taskfile.yml`, API catalogs/OpenAPI, audit inventories and architecture rules
  contain both foundation and follow-up changes. Regenerate from the retained
  source contracts; do not independently copy generated outputs.
- Credential identity changes in `internal/analytics/connectionbinding/binding.go`
  and `internal/release/provenance.go` cross several groups. Preserve the exact
  pin and provider/local distinction required by retained validation and denial.
- For each actual extraction, verify imports, SQL callers, routes and denial
  tests on both resulting revisions. Run focused tests and the repository CI
  contract before publishing implementation changes. A scope document is not
  evidence that a proposed extracted branch builds or behaves correctly.

## Assurance acceptance handoffs

These are technical acceptance inputs, not additional D02 product features.
Accountable roles below still need named owners/reviewers; none is assigned here.

| Assessment reference | Required evidence | Delivery boundary |
| --- | --- | --- |
| FAI-968 / FAI-973: key and credential custody | Rotation, revocation, expiry, missing/wrong historical keys and provider outage retain expected denial; supported new-format backups decrypt with independently recovered matching keys; plaintext is absent from logs, replay and evidence. | D02 supplies scoped storage/audit evidence. D12 implements lifecycle and key recovery; D13/platform owners qualify the selected deployment and operator procedure. |
| FAI-969 / FAI-981: access lifecycle after disaster | Actual supported mutation paths record facts transactionally; backup capture binds their sequence and authority to the recovery point; independently retained authoritative post-backup facts survive loss of the original database/host; missing or incomplete evidence blocks restored-state exposure. | Existing access/privacy and recovery owners supply recording, capture, evidence survival and replay. D13 consumes their qualified result. Do not build a second backup engine or generic journal in the credential PR. |
| FAI-976: audit operation | Required events commit with their mutation; redacted evidence has protected export, integrity, retention, clocking and loss-detection acceptance. | D02 owns credential transaction/audit correctness. Broader export, retention and monitoring remain with audit/operations/privacy owners. No fixed retention period is invented here. |
| Customer isolation and release recovery | Test the selected artifact/configuration/profile for cross-customer secret, log and backup denial; qualify compatible rollback after writes and incompatible-format maintenance separately. | D12 supplies credential boundaries; D11/D13 and deployment/security owners qualify the combined profile. A single-owner instance does not remove the two-customer isolation exercise. |

An independently recoverable **keyring** preserves decryption capability;
independently surviving **access lifecycle evidence** preserves later revocations.
Neither substitutes for the other. `ReconcileLifecycleActions`,
`CaptureLifecycleFrontier` and `DisablePrincipalWithLifecycle` in
[`internal/access/postgres/lifecycle.go`](../../internal/access/postgres/lifecycle.go)
have no non-test callers at this review baseline. Their principal-disable support
does not implement customer-secret retirement or upstream provider revocation.
Restored credentials must still pass the
[credential restore contract](credential-lifecycle-contract.md#7-key-rotation-and-restore-of-the-new-format).

## Decisions required before the next implementation boundary

| Decision | Required review result |
| --- | --- |
| D02 keep/split | Accept or amend the allocation and the minimum pending-operation schema exception. Identify the dependencies and owner of each extraction; no blanket directory deletion. |
| D01/D11 process model | Resolve ADR-0027's component assumptions against ADR-0028's candidate-first Kamal overlap. Name who controls shared storage, workers, mutations and draining across old/new processes. A process-local pause is insufficient evidence of global drain. |
| D10 bootstrap | Name the bounded setup authority and reuse the same authorized, audited customer-secret service for UI/API/bootstrap. No parallel raw-SQL secret path. |
| D11/D12 activation success | Define the exact validated version, destination/configuration and expected predecessor; serialization/current authority; every affected source, pool, worker and agent consumer; and the durable evidence that distinguishes saved, validated, committed and in use. Define the action when installation succeeds but recording completion fails, or commit succeeds before installation. |
| D11/D12 restart and retirement | For every interrupted step, record PostgreSQL state, runtime use, administrator-visible status, restart action and whether old-password revocation is safe. Require confirmed drain/stop of all affected old consumers before claiming local retirement; external revocation remains separate. |
| D12/D13 recovery | Name keyring and lifecycle-evidence custodians, supported recovery points and activation cutoff. Define handling of incomplete evidence, compatible rollback retention and incompatible-format maintenance before mutation; no legacy credential reader or automatic destructive reset. |

For the activation review, walk one A-to-B replacement through concurrent edits,
authority revocation while waiting, commit-before-install failure,
install-before-completion failure, a remaining old consumer, process overlap,
and restoration of an older backup after revocation. Record answers in the
existing lifecycle contract rather than introduce a new coordination framework.

Next: review these two extractions and the remaining allocation and shared
decisions, then complete the remaining dependency-complete extractions,
foundation validation and D02 closeout. D12 then
implements the agreed lifecycle in separate reviewable work. No approval,
production gate opening, managed qualification or programme restart is recorded
by this document.
