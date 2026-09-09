# FAI-649 deadline activation-readiness layer

Status: **IMPLEMENTED / PARTIAL scope; qualification is commit-scoped**.

This exact-binding layer is stacked on `ganesh/fai-649-activation-readiness` at
`8538f55029efa41d5c43763358b97a5037753a5f`. The deadline scope explicitly replaces
a complete DataPolicy migration with activation safety, approval integrity,
restricted creation and honest evidence. The [bounded profile](semantic-access-acceptance-boundary.md)
is not expanded. No provider, principal or neutral verification context is
fabricated, and no new classifier, approval model, digest authority or migration
is introduced.

## Implemented versus deferred

| Boundary | Readiness-layer guarantee | Remaining implementation |
| --- | --- | --- |
| Existing deployment approval | Retain exact FAI-622/662 publication, baseline, current lifecycle, graph, registry/type and policy evidence in canonical delivery-plan evidence. The publication retains its original lifecycle binding inside immutable policy evidence while the plan separately binds the current ACTIVE base. Canonical approval request/decision events now explicitly retain the existing plan and evidence digests in the append-only delivery event ledger; the approval service rejects changed or incomplete pairs. The final SQLite write transaction re-reads the granted event, current publication/plan/candidate chain, approval status, scope, release, expiry and credential expiry before its target CAS. No approval column, migration, store or digest authority is added. | This positive path is limited to an unchanged protected model in the exact active base, the latest immutable policy-bearing publication, and a current v3 typed-registry snapshot. Historical canonical approvals lacking explicit event references remain readable/revocable but fail closed for approval or activation. |
| Protected candidate admission | Admit only already-ACTIVE protected identities whose candidate model is byte-identical to the retained active artifact. Re-read the latest immutable publication and current ACTIVE lifecycle during planning; at commit, hold the existing PostgreSQL identity/publication/Access locks while rechecking lifecycle, latest publication and current registry evidence around the existing target CAS. | First protected activation, restore, changed protected contracts, asynchronous legacy sealed jobs, missing policy-bearing publications and absent/currently stale authority remain rejected. They require an explicit later protocol rather than inference. |
| DataPolicy transition | Standalone authoring is deprecated. New creation must not be represented as supported by the public API; compatibility readers and existing artifacts remain. | Full authoring/compiler/runtime/API/UI removal and explicit migration of existing source remain deferred. No automatic conversion of arbitrary expressions or masks to semantic grants/filters. |
| Rollback | Historical artifacts and evidence remain immutable. This binary rejects canonical and legacy sealed rollback of a generation carrying protected contract-activation evidence; an earlier reader cannot silently ignore the new field because the existing evidence digest no longer validates. | A separately qualified protected-generation rollback/restart protocol remains absent. Historical decode/replay is not old-binary policy-enforcement compatibility. |

The exact semantic flow `classification → publication evidence → approval →
activation` is implemented for the bounded already-ACTIVE/unchanged-contract
profile only. Publication still requires an ACTIVE identity and an exact
lifecycle sequence; a new protected identity needs an explicitly designed
ordering between publication, approval and identity activation. Do not make an
identity live merely to manufacture pre-approval evidence. The deadline layer
retains rejection instead of introducing that protocol opportunistically.

Identity/Access authority and the delivery target remain an existing durable
saga, not one cross-database transaction. PostgreSQL locks prevent lifecycle,
publication and registry drift while SQLite linearizes approval and target
activation. A failure after the target CAS still uses the existing indeterminate
publication/transition recovery path; this layer does not claim distributed
atomic commit.

## Evidence ownership

- FAI-622/662 remain the classifier and immutable publication authorities.
  `ContractPolicyApprovalInput` explains a validated decision; it is not an
  approval and cannot be supplied as a substitute for one.
- Deployment retains the existing approval service, retained plan digests,
  credentials, expiry and publication scope checks.
- FAI-637 owns registry and durable assignment resolution. Only that bounded
  source profile is a future admission input; claim-source envelope recognition
  does not admit an external provider.
- FAI-641 owns structural verification design and planner barriers. Existing
  neutral protected-plan rejection tests remain valid.
- FAI-642/645 own consumer and cache/lifecycle enforcement. Production principal
  authority and guarded cache configuration are not enabled by this layer.

## DataPolicy migration and rollback guidance

Prefer typed `SemanticModel.accessGrants`, `requiredAccessGrants` and
`accessFilters` for the future contract. That authoring direction does not mean
the protected contract is currently deployable. Existing DataPolicy-bearing
artifacts retain their legacy enforcement; do not remove their policies before
an equivalent, qualified replacement is available. Compatibility source loading
is not a supported way to introduce new policy features.

Inventory retained policies and classify representability explicitly. Policies
that rely on arbitrary expressions, masking or other semantics outside the
bounded contract require manual redesign and review. Do not silently drop,
translate, weaken or reclassify them. No historical artifact, publication row,
checksum, digest or migration is rewritten here.

Follow the [policy-evidence reader-before-writer rules](semantic-access-policy-evidence.md).
An old binary that decodes a newer record but ignores its policy evidence is
not a compatible rollback target. Existing historical replay remains evidence,
not permission to activate or reuse stale cache state. Production rollback
qualification is deferred along with production semantic activation.

## ADR review result

The normative matrix remains **55 PASS / 45 PARTIAL / 2 FAIL**. STR-08 and
ENF-06 still fail because standalone DataPolicy and its authored language remain.
Deprecation or a creation restriction is not complete removal. FAI-649 remains
partial; FAI-648 and FAI-632 are not completed by this safety layer.

The ADRs can be reviewed with this explicit evidence boundary, but they cannot
truthfully claim complete production semantic activation or the final ADR-0016
conformance closure. Hosted validation must cite this layer's exact commit; the
green parent run is not substituted for current-layer qualification.

## Verification

Implementation and regression evidence are deliberately narrower than production
activation acceptance:

| Guarantee | Implementation | Executable evidence |
| --- | --- | --- |
| Exact publication and plan binding | `internal/project/identityledger/activation_reference.go`, `internal/deployment/plan_delivery_evidence.go`, `internal/app/contract_activation.go` | `TestPolicyActivationReferenceBindsExactPublicationGraphAndPolicy`, `TestContractActivationReferenceChangesExistingPlanAndApprovalDigestChain`, `TestResolveContractActivationReferencesRejectsChangedOrFirstProtectedModel`. |
| Approval evidence, freshness and revocation at commit | Existing Deployment approval plus the existing delivery event ledger in `internal/deployment/sqlite/approval_repository.go` and `internal/deployment/sqlite/plan_delivery_publication.go` | `TestApprovalBindsCanonicalPlanAndEvidenceDigests`, `TestDurableApprovalVerifierBindsCandidatePlanAndRelease`, and `TestDeliveryRepositoryPlanBuildSealCandidatePublication` cover incomplete/changed evidence, artifact substitution, publication mutation, policy-evidence drift, stale credentials, committed revocation, replacement approval and a revocation/activation write race. |
| Current lifecycle/publication/registry fence | Existing PostgreSQL identity publication and Access registry authorities through `internal/project/identityledger/postgres/activation_fence.go` | PostgreSQL 18 tests `TestContractActivationFenceUsesExactActivePublicationEvidence`, `TestContractActivationFenceSerializesConcurrentPublication`, and `TestContractActivationFenceRejectsCurrentRegistryDrift`. |
| Protected rollback and unsupported readers fail closed | canonical plan evidence and application rollback admission | `TestProtectedContractRollbackRemainsFailClosed`, evidence-version validation, and canonical plan digest validation. No historical row or old digest is rewritten. |
| Public standalone creation rejected without opening storage | Access HTTP handler, APIGen adapter and module dispatcher | `TestCreateDataPolicyRejectsNewCreationWithoutStorageMutation`, `TestDispatchAPIGenCreateDataPolicyRejectsNewCreation`. Source compatibility loading remains and is not represented as globally blocked. |
| Existing authority ownership and ordering retained | Production composition and candidate boundary | `TestSemanticActivationReadinessPreservesAuthorities`, full architecture suite. |

Local validation evidence and the canonical hosted CI result are recorded on
FAI-649 against this layer's exact commit. Local Docker access is not cited as
live PostgreSQL evidence; the hosted PostgreSQL gate remains required. None of
these checks upgrades the normative matrix or qualifies production semantic
activation.
