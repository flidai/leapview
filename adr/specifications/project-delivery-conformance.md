# Project delivery conformance specification

Status: accepted

Last updated: 2026-08-18

Owners: LeapView maintainers

Governing decisions: [ADR-0007](../0007-adopt-plan-driven-project-delivery.md),
[ADR-0008](../0008-isolate-ducklake-candidate-physical-state.md), and
[ADR-0009](../0009-separate-control-and-physical-transactions.md)

## Purpose

This mutable specification defines the evidence required to implement the
plan-driven delivery decisions. The ADRs own architectural intent. This file
may evolve with schemas, APIs, test organization, and operational tooling as
long as it preserves those decisions.

`MUST`, `MUST NOT`, `SHOULD`, and `MAY` have their ordinary normative meaning.
Tests may be organized differently from the identifiers below, but maintained
evidence must cover every requirement applicable to an implemented surface.

## Lifecycle and identity

- **LC-01:** CLI, API, browser, agent, CI, local evaluation, and hosted paths
  expose the same `Source snapshot -> Plan -> Candidate -> Generation`
  transition model.
- **LC-02:** Routine human commands are `plan -> build -> publish`; local
  validation is never represented as a target deployment plan.
- **LC-03:** Every plan binds target, environment, project, operation kind,
  exact portable source digest, active base generation, and base target
  revision.
- **LC-04:** Every ready candidate binds exactly one plan, source snapshot,
  resolved execution record, immutable DuckLake catalog digest and object key,
  physical pool, runtime identity, and qualification record.
- **LC-05:** Every generation binds exactly one published candidate and remains
  a complete immutable project graph.
- **LC-06:** Reusing an idempotency identity with different canonical inputs is
  a conflict. Retrying identical input returns the same durable result.
- **LC-07:** Secret values are absent from source snapshots, plans, manifests,
  artifacts, checkpoints, events, structured output, and logs covered by
  non-secret evidence contracts.

## Evidence matrix and implementation gates

The release gate maps each surface to a maintained producer and a failure
mode. A row is **unsupported**, not passed, when its lane or external system
was not run.

Production delivery, event, revision, retention, and recovery evidence is
exercised against the native PostgreSQL repositories and target database.

| Surface | Required evidence | Local gate | Remote/MinIO gate | Fail-closed condition |
| --- | --- | --- | --- | --- |
| **SP — shared physical pool** | Versioned compatibility tuple, all nine named checks, per-check observation digests, canonical evidence digest, explicit create/admit record, stable namespace owner marker and per-run deletion lease | Native physical-pool tests plus `deployment/gcstore` marker/lease tests | `task test:go:minio-conformance`; the release workflow requires the pinned runtime/extension tuple and uploads the validated evidence JSON, checksum, exact image/runtime versions, and test log | Missing/unknown check, tuple mismatch, unavailable extension, stale digest, missing evidence artifact, unadmitted pool, truncated marker, lease conflict, or cross-instance namespace without one owner |
| **SE — sealed serving** | Candidate/seal/generation share catalog, artifact, pool, compatibility, and serving-state identities; read-only attach and lease evidence | `internal/app/runtimefactory/sealed_test.go`; `internal/platform/architecture/delivery_conformance_test.go:TestLEA414ProductionUsesSealedCanonicalPath`; serving identity migration tests | Remote object read and credential bootstrap in the MinIO qualification lane | Preparing/unverified/legacy-identity row, mutable artifact, failed lease/auth, or mixed legacy serving path |
| **AO — append-only operations** | Plan/build/qualification/approval/publish/activate/retire/rollback/lease/GC events with actor, object, request/result digests, outcome and UTC time | Native PostgreSQL event/repository and deployment lifecycle tests | Run against the target's durable PostgreSQL database during release qualification | Event update/delete, conflicting replay, missing event after a committed transition, secret-bearing details, or indeterminate publication without reconciliation |

The CI architecture gate scans authored production source and migrations for
native cleanup/checkpoint calls outside guarded DuckLake adapters, SQLite file
membership/reference-count manifests, mutable sealed artifact fields, and
legacy serving construction. The migration gate applies every migration to a
fresh database and exercises rollback-trigger protections. The operator gate
requires the delivery physical-pool bootstrap dry-run before `--apply`.
Reachability and recovery are observed through the target API and provider-native
PostgreSQL/DuckLake/object-store tooling; the former offline delivery audit and
repair commands are intentionally not part of the operator contract.

### Maintained requirement coverage

This index is the release-review checklist for every normative requirement
below. Adding an item requires adding its producer/test evidence in the same
change; a lane that was not run is recorded as unsupported.

| ID | Maintained producer and fail-closed assertion | Exact test/evidence gate | Operator/runbook evidence |
| --- | --- | --- | --- |
| LC-01 | Plan/candidate/generation contracts are shared by CLI, API, and CI adapters; browser/agent surfaces consume the target-hosted page and do not expose a separate delivery mutation API. | `internal/app/cli/delivery_cli_test.go:TestDeliveryLifecycleAdaptersUseGeneratedContracts`; `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresLifecycleReplayAndScope`; browser/agent delivery mutation surfaces are intentionally absent. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| LC-02 | Human command surface emits plan → build → publish; local checks do not create target plans. | `internal/app/cli/project_deploy_test.go:TestDeployComposesCanonicalPlanBuildAndPublication`; deprecated deploy surface is asserted in `internal/app/cli/command_surface_test.go:TestDeployCommandUsesTargetOwnedAtomicCandidatePreparation`. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| LC-03 | `DeliveryPlan.Validate` binds target, scope, operation, source, active base and target revision. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryPlanRequiresExplicitEvidenceAndCanonicalizesOrder` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| LC-04 | Ready candidate requires exact plan, resolved inputs, sealed catalog/pool, runtime and qualification identities. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryBuildSealAndCandidateTransitionsAreChecked`; `internal/deployment/postgres/complete_build_test.go:TestPostgresCompleteBuildTxSuccessAndExactReplay` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| LC-05 | Generation is one immutable published candidate and catalog root. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay` | [upgrades](../../docs/articles/operate/upgrades.md) |
| LC-06 | Idempotent identities compare canonical inputs and reject drift. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`; `internal/platform/events/postgres/repository_test.go:TestPostgreSQL18ConcurrentSameEventIdentityIsIdempotent` | [upgrades](../../docs/articles/operate/upgrades.md) |
| LC-07 | Evidence/event details reject secret-like values and non-canonical text. | `internal/deployment/delivery_events_test.go:TestDeliveryEventDetailsRejectSecretsAndUnknownFields` | [upgrades](../../docs/articles/operate/upgrades.md) |
| TP-01 | One target revision row is authoritative for each target/project/environment. | `internal/deployment/postgres/revision_allocation_test.go:TestPostgresTargetRevisionAllocationReplayRollbackAndConcurrency` | [upgrades](../../docs/articles/operate/upgrades.md) |
| TP-02 | Invalidating mutations bump revision in the same native PostgreSQL transaction. | `internal/deployment/postgres/revision_allocation_test.go:TestPostgresTargetRevisionAllocationReplayRollbackAndConcurrency` | [upgrades](../../docs/articles/operate/upgrades.md) |
| TP-03 | Sessions and non-invalidating leases do not bump target revision. | `internal/deployment/postgres/repository_test.go:TestPostgresLeaseCASRaceAndStaleFence`; lease transitions are kept separate from target revision allocation. | [upgrades](../../docs/articles/operate/upgrades.md) |
| TP-04 | Publication compares base generation and revision atomically. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| TP-05 | Concurrent stale writers fail closed without rebasing. | `internal/deployment/postgres/repository_test.go:TestPostgresLeaseCASRaceAndStaleFence`; `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresPublishCandidatePersistsEvidenceAndReplays` | [upgrades](../../docs/articles/operate/upgrades.md) |
| TP-06 | Target component evidence identifies revision causes without a second CAS authority. | `internal/deployment/postgres/revision_allocation_test.go:TestPostgresTargetRevisionAllocationReplayRollbackAndConcurrency` | [upgrades](../../docs/articles/operate/upgrades.md) |
| TP-07 | Plan output contains graph impact, policy, compatibility and physical-work evidence. | `internal/app/cli/delivery_cli_test.go:TestDeliveryPlanResultPreservesReviewEvidence`; `internal/project/cli/command_test.go:TestDeliveryPlanTextOutputIncludesReviewEvidence`; redacted API projection is asserted by `internal/deployment/module/delivery_api_test.go:TestDeliveryPlanPreviewExposesImmutableReviewEvidence`. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| TP-08 | Semantic relationship paths and qualification scope are governed by the plan. | `internal/app/runtimefactory/delivery_plan_test.go:TestCandidatePlanPreservesSemanticRelationshipPathsAndQualificationScope`; `internal/analytics/query/planner_test.go:TestPlannerRelationshipDependenciesRemainFactQualified` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| TP-09 | Expired, cross-target and source-mismatched plans are rejected. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryPublicationLeaseAndGCFencesAreIdempotent`; `internal/deployment/postgres/plan_document_test.go:TestCreatePlanRejectsRichDocumentOutsideTargetScope` | [upgrades](../../docs/articles/operate/upgrades.md) |
| EP-01 | Execution, provenance and governance are separate canonical fields. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryPlanSeparatesExecutionFromProvenance` | [validate/deploy](../../docs/guides/cli/validate-deploy.md) |
| EP-02 | Result-affecting compiler/runtime/input changes alter execution digest. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryExecutionDigestChangesForEveryResultAffectingInput`; `internal/app/runtimefactory/delivery_plan_test.go:TestCandidatePlanExecutionIdentityIncludesDataModeAndEffectiveBindings` | [validate/deploy](../../docs/guides/cli/validate-deploy.md) |
| EP-03 | Provenance-only changes do not alter execution equivalence. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryPlanSeparatesExecutionFromProvenance` | [validate/deploy](../../docs/guides/cli/validate-deploy.md) |
| EP-04 | Governance-only changes require policy transition without physical rebuild when equivalent. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryGovernanceAndCredentialRotationPreserveExecutionReuseIdentity`; `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryReusePolicyRequiresExactPhysicalIdentity` | [validate/deploy](../../docs/guides/cli/validate-deploy.md) |
| EP-05 | Secret rotation with unchanged binding semantics preserves execution identity. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryGovernanceAndCredentialRotationPreserveExecutionReuseIdentity` (credential provider rotation is provenance-only; secret material is excluded from execution inputs) | [upgrades](../../docs/articles/operate/upgrades.md) |
| EP-06 | Effective binding or policy changes alter execution identity. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryExecutionDigestChangesForEveryResultAffectingInput`; `internal/app/runtimefactory/delivery_plan_test.go:TestCandidatePlanExecutionIdentityIncludesDataModeAndEffectiveBindings` | [validate/deploy](../../docs/guides/cli/validate-deploy.md) |
| EP-07 | Trusted provenance validates supported attestations. | `internal/project/module/candidate_sources_test.go:TestCandidateSourceSynchronizerAuthorizesOnlyPlannedOwnerUploads`; `internal/project/module/candidate_sources_test.go:TestCandidateSourceSynchronizerRetainsActivePlanAcrossRestart` | [validate/deploy](../../docs/guides/cli/validate-deploy.md) |
| DI-01 | Every planned input is classified pinned, bounded or observed. | `internal/deployment/plan_delivery_contracts_test.go:TestResolvedBuildInputsRequireObservedEvidenceDigest` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DI-02 | Pinned inputs read exactly the immutable revision. | `internal/deployment/plan_delivery_contracts_test.go:TestResolvedBuildInputsBindExactlyToPlanDeclarations` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DI-03 | Bounded inputs enforce their declared interval/watermark. | `internal/deployment/plan_delivery_contracts_test.go:TestResolvedBoundedInputRejectsChangedWatermark` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DI-04 | Observed inputs record their observation and obey target policy. | `internal/deployment/plan_delivery_contracts_test.go:TestResolvedBuildInputsRequireObservedEvidenceDigest` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DI-05 | Planning observations never masquerade as pinned versions. | `internal/deployment/plan_delivery_contracts_test.go:TestResolvedBuildInputsRequireObservedEvidenceDigest` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DI-06 | Candidate evidence records actual input versions/bounds/observations. | `internal/deployment/plan_delivery_contracts_test.go:TestResolvedBuildInputsBindExactlyToPlanDeclarations` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DI-07 | Restatement evidence records interval, mode, versions, scope and idempotency. | `internal/deployment/plan_delivery_contracts_test.go:TestRestatementPlanRetainsBoundedIntervalsScopeStrategyAndIdempotency` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| CQ-01 | One successful attempt seals at most one candidate; failed attempts never ready. | `internal/deployment/postgres/complete_build_test.go:TestPostgresCompleteBuildTxRollbackLeavesBuildOpen`; `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| CQ-02 | Blocking qualification failure preserves active and last-valid pointers. | `internal/analytics/candidatecatalog/qualification_test.go:TestQualificationFailureRemovesWorkingStaging` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| CQ-03 | Non-blocking and bounded/sampled qualification is explicitly labelled. | `internal/analytics/candidatecatalog/qualification_test.go:TestQualificationPolicyAndExpectedSetValidation` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| CQ-04 | Reject-stale policy prevents physical work after stale detection. | `internal/deployment/lifecycle_test.go:TestDeliveryLifecycleRejectsStaleBeforePhysicalWork` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| CQ-05 | Permitted stale qualification uses retained exact base and inputs. | `internal/deployment/lifecycle_test.go:TestDeliveryLifecycleAllowRetainedBaseRequiresExactSealedIdentity` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| CQ-06 | Stale candidates are permanently ineligible without mutation. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryBuildSealAndCandidateTransitionsAreChecked` | [upgrades](../../docs/articles/operate/upgrades.md) |
| CQ-07 | Approval binds one exact candidate/plan and never carries forward. | `internal/deployment/approval_test.go:TestApprovalBindsDecisionToExactDeploymentPlan` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| CQ-08 | Candidate preview applies live grants and policies. | `internal/dashboard/queryauthz/canonical_test.go:TestCanonicalRLSMasksAndPolicyFingerprint`; `internal/dashboard/queryauthz/canonical_test.go:TestCanonicalPublicPublicationAndCandidateClosures`; active delivery object authorization is asserted by `internal/app/canonical_authorization_test.go:TestDeliveryAuthorizationRequiresEveryAffectedResource`. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-01 | Each build uses a private writable DuckLake catalog. | `internal/analytics/candidatecatalog/catalog_test.go:TestConcurrentBuildsFromOneBaseAreDistinct` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-02 | Concurrent same/different-table candidates remain isolated. | `internal/analytics/candidatecatalog/catalog_test.go:TestConcurrentBuildsFromOneBaseAreDistinct` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-03 | Shared data reuse requires one admitted physical-pool compatibility tuple. | `internal/analytics/ducklake/shared_pool_safety_test.go:TestSharedPoolConformanceLocalClosedCloneFixture`; `internal/analytics/ducklake/conformance_artifact_test.go:TestSharedPoolEvidenceArtifactIsCompleteAndPortable` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-04 | Reuse-key matches preserve exact references; mismatches rebuild. | `internal/app/runtimefactory/delivery_plan_test.go:TestCandidatePlanReuseDecisionUsesExactActiveIdentity`; `internal/app/runtimefactory/delivery_plan_test.go:TestCandidatePlanEmitsRelationScopedReuseDecisions`; `internal/analytics/candidatecatalog/catalog_test.go:TestConcurrentBuildsFromOneBaseAreDistinct` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-05 | Seal normalization retains exactly one snapshot without physical cleanup. | `internal/analytics/candidatecatalog/qualification_test.go:TestNormalizeAndQualifyRetainsOneSnapshotAndProbesClosure` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-06 | Preview/qualification/serving attach exact digest read-only. | `internal/analytics/candidatecatalog/catalog_test.go:TestOpenRejectsDigestMismatchWithoutPrivateStaging` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-07 | Inlining is zero at every scope and no current catalog contains live inlined rows or deletes. | `internal/analytics/ducklake/shared_pool_safety_test.go:TestEnvironmentReportsZeroInliningAcrossRuntimeScopes`; `internal/analytics/candidatecatalog/qualification_test.go:TestNormalizeRejectsLiveInlineDataWithoutRepair` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-08 | Open/mutating catalogs are never byte-copied; closed artifacts verify digest. | `internal/platform/architecture/delivery_conformance_test.go:TestPlanDeliveryPhysicalAuthorityGuards` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-09 | All declared result-affecting semantics alter execution identity. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryExecutionDigestChangesForEveryResultAffectingInput`; `internal/app/runtimefactory/delivery_plan_test.go:TestCandidatePlanExecutionIdentityIncludesDataModeAndEffectiveBindings` | [validate/deploy](../../docs/guides/cli/validate-deploy.md) |
| PI-10 | Provenance/approval/owner/secret rotation can reuse equivalent execution. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryGovernanceAndCredentialRotationPreserveExecutionReuseIdentity`; `internal/app/runtimefactory/delivery_plan_test.go:TestCandidatePlanReuseDecisionUsesExactActiveIdentity` | [validate/deploy](../../docs/guides/cli/validate-deploy.md) |
| PI-11 | Undeclared nondeterminism disables reuse. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryReusePolicyDisablesUndeclaredNondeterminism`; `internal/app/runtimefactory/delivery_plan_test.go:TestCandidatePlanReuseDecisionUsesExactActiveIdentity` | [validate/deploy](../../docs/guides/cli/validate-deploy.md) |
| PI-12 | No SQLite file-membership manifest; the PostgreSQL-backed DuckLake catalog is the physical authority. | `internal/platform/architecture/delivery_conformance_test.go:TestPlanDeliveryPhysicalAuthorityGuards` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PI-13 | Catalog/pool objects and credentials stay target-authorized. | `internal/analytics/ducklake/shared_pool_safety_test.go:TestCredentialBootstrapRunsForEveryPooledConnector`; `internal/app/gcadapter/credentials_test.go:TestNewPoolStoreS3RequiresTargetKeys`; `internal/app/gcadapter/credentials_test.go:TestNewPoolStoreS3RequiresTargetKeysBeforeAWSConfig`; `internal/app/runtimefactory/postgres_test.go:TestPostgresSealedFactoryRequiresTargetCapabilities`; `internal/app/runtimefactory/postgres_test.go:TestPostgresSealedFactoryAcquiresAuthorizesAndReleasesOnAttachFailure` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PI-14 | Runtime upgrades require the complete shared-pool conformance lane. | `task test:go:minio-conformance`; `.github/workflows/release.yml:minio-conformance` fails on an unavailable extension or tuple drift and retains the complete evidence artifact, checksum, exact DuckDB/DuckLake versions, pinned MinIO digest, and logs. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-01 | Build attempt binds plan/input/base/pool/writer lease before work. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryBuildSealAndCandidateTransitionsAreChecked`; `internal/deployment/postgres/repository_test.go:TestPostgresCallerOwnedLeaseAndBuildAttemptAdmission` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-02 | Pre-seal crash leaves no candidate and retries exact base. | `internal/deployment/lifecycle_test.go:TestDeliveryLifecycleClosesPhasedCatalogOnEveryPreSealFailure` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-03 | Snapshot normalization rejects non-zero inlining policy or live inlined rows/deletes. | `internal/analytics/candidatecatalog/qualification_test.go:TestNormalizeRejectsLiveInlineDataWithoutRepair` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-04 | Qualification verifies contracts, runtime, inlining and closure. | `internal/analytics/candidatecatalog/qualification_test.go:TestQualificationPolicyAndExpectedSetValidation`; `internal/analytics/candidatecatalog/qualification_test.go:TestProbeClosureCanonicalizesAndDeduplicatesReferences` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-05 | Metadata DB closes without catalog checkpoint/cleanup. | `internal/analytics/ducklake/shared_pool_safety_test.go:TestSharedPoolRejectsNativeCleanupCheckpointAndMaintenance` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PR-06 | Seal record precedes upload with digest/size/pool/create-only key. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`; `internal/analytics/catalogseal/catalogseal_test.go:TestSealRecomputeAfterLostLocalLeavesUnreferencedObjectForGC` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-07 | Upload is conditional and lost acknowledgement reconciles exact bytes. | `internal/analytics/catalogseal/catalogseal_test.go:TestSealRecomputeAfterLostLocalLeavesUnreferencedObjectForGC`; PostgreSQL seal identity is persisted by `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-08 | Mismatching existing object is corruption and never overwritten. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`; `internal/deployment/postgres/complete_build_test.go:TestPostgresCompleteBuildTxConflictingReplayAndStaleLease` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PR-09 | Ready candidate follows read-only artifact/closure verification. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`; `internal/app/gcadapter/inspector_test.go:TestVerifyReferencedObjectRequiresImmutableExistingDigest` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-10 | Sealing identity drift conflicts; identical retry converges. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`; `internal/deployment/postgres/complete_build_test.go:TestPostgresCompleteBuildTxConflictingReplayAndStaleLease` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PR-11 | Lost pre-seal artifacts recompute and orphan objects await fenced GC. | `internal/analytics/catalogseal/catalogseal_test.go:TestSealRecomputeAfterLostLocalLeavesUnreferencedObjectForGC`; `internal/deployment/gc/collector_test.go:TestCollectorMarksCrossCatalogDataAndDeleteFiles` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-12 | Refresh/restatement creates private catalog and retains evidence. | `internal/deployment/plan_delivery_contracts_test.go:TestRestatementPlanRetainsBoundedIntervalsScopeStrategyAndIdempotency`; `internal/analytics/candidatecatalog/catalog_test.go:TestConcurrentBuildsFromOneBaseAreDistinct` (private catalog isolation) | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PU-01 | Publish accepts only exact ready eligible candidate and approved plan. | `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresPublishCandidatePersistsEvidenceAndReplays` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PU-02 | Publish performs no source capture/compile/qualification/candidate mutation. | `internal/deployment/sealedcontrol/coordinator_test.go:TestPublishRequiresAuthorizationAndBindsExactSeal` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PU-03 | Active generation/revision CAS is atomic and stale-safe. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`; `internal/deployment/postgres/revision_allocation_test.go:TestPostgresGenerationRevisionAllocationReplayRollbackAndConcurrency` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PU-04 | Generation root and active pointer commit in one transaction. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PU-05 | Activation timeout/crash persists indeterminate and reconciles. | `internal/deployment/postgres/activate_lost_ack_test.go:TestPostgresActivateReplaysAfterCommitLostAcknowledgement`; `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresActivationPreCommitHookRollsBack` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PU-06 | Reconciliation never activates or cleans unknown candidate. | `internal/deployment/module/jobs_test.go:TestActivationJobRequiresPostCommitReconciliation`; `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresMutationFailuresRollbackSourceAndOperation` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PU-07 | Publication mutates no DuckLake physical object. | `internal/platform/architecture/delivery_conformance_test.go:TestPlanDeliveryPhysicalAuthorityGuards` | [upgrades](../../docs/articles/operate/upgrades.md) |
| RG-01 | Rooted catalogs and reachable objects survive TTL/quota cleanup. | `internal/deployment/gc/collector_test.go:TestCollectorMarksCrossCatalogDataAndDeleteFiles` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-02 | Native PostgreSQL root state plus the DuckLake closure are authoritative. | `internal/servingstate/postgres/repository_test.go:TestRetentionInventoryScopesRootsLeasesAndSnapshotEvidence`; `internal/deployment/postgres/repository_test.go:TestRetentionRootLifecycleTxReplayGraceAndExpiry`; `internal/app/gcadapter/inspector_test.go:TestVerifyReferencedObjectRequiresImmutableExistingDigest` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-03 | Root, query lease and retirement share one durable fence. | `internal/deployment/postgres/repository_test.go:TestPostgresLeaseCASRaceAndStaleFence`; `internal/platform/events/postgres/repository_test.go:TestPostgreSQL18RetirementFenceClosesProducerFanoutRace` | [upgrades](../../docs/articles/operate/upgrades.md) |
| RG-04 | All sealing/candidate/generation/publication/rollback/lease roots are enumerated. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`; `internal/deployment/postgres/recovery_retention_test.go:TestRecoveryRetentionRootMaintenanceRetiresAndExpires` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-05 | Shared-pool catalogs use one global collector. | `internal/analytics/ducklake/shared_pool_safety_test.go:TestCrossCatalogUnionAndOrphanClassificationIncludesDataAndDeleteFiles` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-06 | GC verifies every rooted catalog and current data/delete closure. | `internal/app/gcadapter/inspector_test.go:TestVerifyReferencedObjectRequiresImmutableExistingDigest`; `internal/analytics/candidatecatalog/qualification_test.go:TestNormalizeAndQualifyObjectProbeReceivesCanonicalReferences`; `internal/analytics/ducklake/gc_visibility_test.go:TestCurrentFileClosureIncludesEveryRetainedDeleteFile` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-07 | Destructive phase epochs writers and applies grace periods. | `internal/deployment/gc/collector_test.go:TestCollectorProtectsWriterCrashGraceAndReaderRoot`; `internal/deployment/postgres/repository_test.go:TestPostgresLeaseCASRaceAndStaleFence`; `internal/deployment/postgres/recovery_retention_test.go:TestRecoveryRetentionRootMaintenanceRetiresAndExpires` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-08 | GC revalidates epoch/root/leases/state before every bounded delete batch. | `internal/deployment/gc/collector_test.go:TestCollectorRevalidatesBeforeBatch`; physical namespace lease checks are covered by `gcstore` lease tests. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-09 | Every delete has bounded intent and ambiguous response reconciliation. | `internal/deployment/gc/collector_test.go:TestCollectorLostDeleteAckReconcilesNotFound`; `internal/deployment/gc/collector_test.go:TestCollectorSameVersionLostAckStaysPending` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-10 | Native DuckLake cleanup/checkpoint paths are unreachable for shared pools. | `internal/analytics/ducklake/shared_pool_safety_test.go:TestSharedPoolRejectsNativeCleanupCheckpointAndMaintenance` | [upgrades](../../docs/articles/operate/upgrades.md) |
| RG-11 | Pre-seal expiration mutates private metadata only. | `internal/analytics/ducklake/environment_test.go:TestRetentionCandidatesPreserveProtectedSnapshots` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-12 | Multi-process writer/GC leases use durable epochs, not mutexes. | `internal/deployment/postgres/repository_test.go:TestPostgresLeaseCASRaceAndStaleFence`; `internal/deployment/postgres/successor_attempt_test.go:TestPostgresAdmitSuccessorBuildAttemptRequiresMarkerResolutionAndFencesPredecessor`; `internal/deployment/gcstore/local_test.go:TestLocalDeletionLeaseFencesClonedMetadataDatabases` | [upgrades](../../docs/articles/operate/upgrades.md) |
| RG-13 | Pre-seal orphan objects are fenced, non-queryable, and grace-delayed. | `internal/deployment/gc/collector_test.go:TestCollectorProtectsWriterCrashGraceAndReaderRoot` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DP-01 | Generation rollback class/window/effects are validated. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryBuildSealAndCandidateTransitionsAreChecked` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DP-02 | Rollback selects retained generation and appends rollback events. | `internal/deployment/sealedcontrol/coordinator_test.go:TestRollbackRequiresAuthorizationAndUsesControlStore`; `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresRollbackRequiresRetainedGeneration` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DP-03 | Watch mode changes only private candidate/session state. | `internal/project/devloop/service_test.go:TestReconcilePreservesLastValidCandidateWhenNextBuildFails`; `internal/project/devloop/watcher_test.go:TestWatcherDebouncesReachableChangesAndIgnoresUnrelatedFiles` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DP-04 | Build/publish inherit plan target and reject destination assertion drift. | `internal/project/cli/publish_command_test.go:TestPublishCommandUsesExactCheckpointWithoutReadingProjectSource`; `internal/project/cli/publish_command_test.go:TestCandidateCheckpointStoreRoundTripsExactNonSecretIdentity`; `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryPublicationLeaseAndGCFencesAreIdempotent`. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DP-05 | Promotion replans portable bytes for destination target. | `internal/app/runtimefactory/dp_conformance_test.go:TestPromotionReplansPortableArtifactForEachDestinationTarget` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DP-06 | Development qualification remains provenance and cannot replace destination qualification. | `internal/deployment/dp_conformance_test.go:TestDestinationBuildRunsQualificationAfterDevelopmentEvidence` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DP-07 | Convenience commands emit durable identities and cannot bypass gates. | `internal/app/cli/project_deploy_test.go:TestDeployComposesCanonicalPlanBuildAndPublication`; `internal/project/cli/publish_command_test.go:TestPublishCommandUsesExactCheckpointWithoutReadingProjectSource`; `internal/project/cli/publish_command_test.go:TestCandidateCheckpointStoreRoundTripsExactNonSecretIdentity`; bootstrap safety remains covered by `internal/app/cli/command_surface_test.go:TestDeliveryPoolBootstrapDocumentsWriteEffectAndExplicitConfirmation`. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| AO-01 | All listed transitions append immutable actor/object/digest/result events, including approvals, leases, GC and restatements. | Native PostgreSQL event/repository suites (`internal/platform/events/postgres/repository_test.go`, `internal/deployment/postgres/repository_test.go`) and deployment lifecycle tests. | [upgrades](../../docs/articles/operate/upgrades.md) |
| AO-02 | Runtime lineage is lossless where supported; LeapView ledger remains authority. | Reviewed operational check (2026-08-17): no OpenLineage adapter or outbound lineage integration is present in this checkout; the append-only LeapView delivery event ledger remains the authoritative runtime lineage surface. | [upgrades](../../docs/articles/operate/upgrades.md) |
| AO-03 | UX exposes impact, qualification, eligibility and decisions. | `internal/app/cli/delivery_cli_test.go:TestDeliveryPlanResultPreservesReviewEvidence`; `internal/project/cli/command_test.go:TestDeliveryPlanTextOutputIncludesReviewEvidence`; `internal/deployment/module/delivery_api_test.go:TestDeliveryPlanPreviewExposesImmutableReviewEvidence`; browser/agent have no separate delivery mutation surface. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| AO-04 | Inspection exposes immutable source/execution/provenance/governance/target/candidate/seal/publication evidence. | `internal/deployment/module/delivery_api_test.go:TestDeliveryPlanPreviewExposesImmutableReviewEvidence`; `internal/deployment/module/delivery_api_test.go:TestDeliveryCandidateStatusRedactsObjectAuthorityAndInputs`; `internal/app/cli/publish_test.go:TestProjectPublishOperationsEmitVersionedAcceptedJSON`. | [upgrades](../../docs/articles/operate/upgrades.md) |
| AO-05 | Delivery evidence is inspected through the target API and provider-native PostgreSQL/DuckLake/object-store tooling; no offline audit or repair mutation is exposed. | `internal/deployment/module/delivery_api_test.go:TestDeliveryPlanPreviewExposesImmutableReviewEvidence`; `internal/deployment/module/delivery_api_test.go:TestDeliveryCandidateStatusRedactsObjectAuthorityAndInputs`; release delivery qualification evidence | [delivery recovery](../../docs/articles/operate/delivery-recovery.md) |
| E2E-01 | Local evaluation, private/PR, and protected-production policies use one plan-to-GC lifecycle; only policy, input, and approval configuration differ. | `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresLifecycleReplayAndScope`; protected provenance is enforced by `internal/project/module/candidate_sources_test.go:TestCandidateSourceSynchronizerAuthorizesOnlyPlannedOwnerUploads`; release gate `task test:go:plan-gc-conformance`. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| E2E-02 | Composition and focused failure suites cover concurrent same-base candidates, build/seal crash recovery, ambiguous upload, and lost publication acknowledgement. | `internal/analytics/candidatecatalog/catalog_test.go:TestConcurrentBuildsFromOneBaseAreDistinct`; `internal/deployment/lifecycle_test.go:TestDeliveryLifecycleClosesPhasedCatalogOnEveryPreSealFailure`; `internal/deployment/lifecycle_test.go:TestDeliveryLifecycleReconcilesDurablePhasesAfterRestart`; `internal/deployment/postgres/activate_lost_ack_test.go:TestPostgresActivateReplaysAfterCommitLostAcknowledgement`; MinIO lane candidate/shared-pool tests. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| E2E-03 | Long readers, root/GC revision races, lease/retirement fencing, exact rollback, and outside-window rejection are maintained together. | `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresLifecycleReplayAndScope`; `internal/deployment/postgres/repository_test.go:TestPostgresLeaseCASRaceAndStaleFence`; `internal/deployment/gc/collector_test.go:TestCollectorRevalidatesBeforeBatch`. | [delivery recovery](../../docs/articles/operate/delivery-recovery.md) |
| E2E-04 | Pinned, bounded, and observed modes run through the same lifecycle; destination promotion requalifies instead of copying candidate identity. | `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresLifecycleReplayAndScope`; `internal/deployment/dp_conformance_test.go:TestDestinationBuildRunsQualificationAfterDevelopmentEvidence`; `internal/app/runtimefactory/dp_conformance_test.go:TestPromotionReplansPortableArtifactForEachDestinationTarget`. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| E2E-05 | CLI/API vocabulary, release gates, and operator runbooks expose the same immutable plan/candidate/generation/catalog contract. | `internal/deployment/module/delivery_api_test.go:TestDeliveryPlanPreviewExposesImmutableReviewEvidence`; `internal/platform/architecture/delivery_conformance_test.go:TestPlanDeliveryPhysicalAuthorityGuards`; release jobs `minio-conformance` and `plan-gc-conformance`. | [delivery recovery](../../docs/articles/operate/delivery-recovery.md) |

## Target revision and planning

- **TP-01:** Each target/project/environment scope has one authoritative
  monotonic target revision.
- **TP-02:** Active generation and every plan-invalidating target mutation
  update the revision in the same native PostgreSQL transaction.
- **TP-03:** Sessions, query leases, audit appends, secret rotations with
  unchanged declared execution semantics, and other non-invalidating changes do
  not increment the revision.
- **TP-04:** A publication CAS compares both base generation and base target
  revision and increments revision on success.
- **TP-05:** Concurrent publication, binding, managed-data, capability, or
  policy changes cannot be lost, overwritten, silently rebased, or reverted by
  stale publication.
- **TP-06:** Detailed target evidence identifies which component caused a
  revision change without becoming a second CAS authority.
- **TP-07:** Plan output deterministically reports direct and indirect graph
  impact, compatibility and policy impact, physical work, qualification, reuse,
  and defensible estimates.
- **TP-08:** Semantic impact and qualification cover the relationship paths,
  metric dependencies, filters, grain, and multi-root behavior governed by
  ADR-0006.
- **TP-09:** Expired, cross-target, cross-project, and source-mismatched plans
  fail closed.

## Execution, provenance, and governance

- **EP-01:** Plan persistence and APIs distinguish execution identity,
  provenance, and governance even when one record contains all three.
- **EP-02:** The execution digest changes for every result-affecting compiler,
  executable contract, dependency, runtime, capability, non-secret variable,
  semantic binding, managed-data revision, or declared data-input change.
- **EP-03:** Repository, source revision, builder, and attestation metadata do
  not change execution equivalence when portable bytes and all execution inputs
  remain identical.
- **EP-04:** Governance-only changes may require replanning, new candidate
  qualification, approval, or publication rejection without forcing physical
  rebuilding when execution equivalence still holds.
- **EP-05:** Credential secret rotation does not change execution identity when
  endpoint, database, catalog, schema, role, privileges, and other declared
  binding semantics are unchanged.
- **EP-06:** A change to effective binding or result-affecting policy semantics
  changes execution identity even when the secret reference name is unchanged.
- **EP-07:** Trusted-provenance policy verifies supported attestations rather
  than trusting client-supplied repository and revision strings.

## Data-input modes

- **DI-01:** Every planned data input is classified as `pinned`, `bounded`, or
  `observed` and plan output explains the mode.
- **DI-02:** A pinned input reads exactly its immutable dataset snapshot or
  managed-data revision; mismatch fails qualification.
- **DI-03:** A bounded input enforces the declared interval, as-of point, or
  upper watermark. Newer data outside the bound neither changes the result nor
  makes the plan stale.
- **DI-04:** An observed input records the exact build-time observation and is
  labeled as weaker reproducibility. It is rejected when target policy forbids
  observed inputs.
- **DI-05:** A planning-time observation or estimate is never represented as a
  pinned version.
- **DI-06:** Candidate execution evidence records the actual pinned version,
  enforced bound, or observed value used by every input.
- **DI-07:** Restatement records requested and effective intervals, input modes
  and versions, downstream scope, strategy, estimates, and idempotency evidence.

## Candidate qualification and stale-build policy

- **CQ-01:** One canonical successful build attempt seals at most one immutable
  candidate. A failed attempt never produces a ready candidate.
- **CQ-02:** Blocking validation or audit failure leaves active generation and
  the development session's last valid candidate unchanged.
- **CQ-03:** Non-blocking qualification and complete, bounded, or sampled data
  diffs are labeled explicitly.
- **CQ-04:** When target policy rejects stale builds, expensive physical work
  does not begin after the stale condition is observed.
- **CQ-05:** When target policy permits stale qualification, build proceeds only
  against a retained exact base and available declared inputs.
- **CQ-06:** A candidate built or becoming stale is permanently ineligible for
  publication under that plan. Staleness does not mutate its contents or create
  a candidate revision.
- **CQ-07:** Approval binds one exact candidate and plan digest and never carries
  forward to a replacement candidate or replan.
- **CQ-08:** Candidate preview applies live grants and data policies and cannot
  expose ungoverned physical storage.

## DuckLake catalog isolation and reuse

- **PI-01:** Each build uses one private writable DuckLake catalog created from
  an exact immutable sealed base or a recorded database-native consistent copy.
- **PI-02:** Two candidates built concurrently from the same base remain
  isolated when changing either the same or different logical tables.
- **PI-03:** A base and child that reuse physical data bind the same
  `PhysicalPool`; all catalogs sharing a pool are governed by one global
  retention authority and use one admitted DuckDB runtime, DuckLake extension,
  catalog format, storage implementation, and object-naming compatibility
  tuple. This shared-writer topology is a version-gated LeapView contract, not
  an upstream DuckLake guarantee.
- **PI-04:** A complete reuse-key match retains the exact base data and delete
  file references without creating replacement data, while every relevant
  execution mismatch rebuilds the affected state into new immutable objects.
- **PI-05:** Before seal, every inherited and intermediate snapshot is expired
  without physical cleanup; the sealed catalog contains exactly one retained
  current snapshot.
- **PI-06:** Preview, qualification, comparison, and serving attach the exact
  catalog digest read-only. Historical state is selected through another
  retained generation catalog, not internal catalog time travel.
- **PI-07:** Data inlining is disabled for every LeapView-managed
  materialization. Attach and process defaults are zero, every persisted global,
  schema, and table override is verified as effectively zero, existing inlined
  inserts and deletes are explicitly flushed table by table. Since the pinned
  DuckLake flush implementation still consults `auto_compact`, normalization
  enables it only for the exact target and persists it back to `false`; no
  live inlined data remains. Every current `data_file` and `delete_file` is
  then enumerable.
- **PI-08:** Candidate construction never byte-copies an open or mutating
  metadata database and never mutates a sealed artifact. Producing a fresh copy
  from a live or native database uses DuckLake's documented logical
  `COPY FROM DATABASE` path. Raw byte copying is limited to a closed immutable
  artifact under an admitted compatibility tuple and verifies its digest and
  read-only open.
- **PI-09:** Changed logic, upstream identity, pinned version, bounded interval,
  observed input, result-affecting policy or binding, materialization semantic,
  declared nondeterministic input, connector, adapter, executable contract, or
  runtime compatibility changes execution identity.
- **PI-10:** Provenance-only, approval-only, owner, and secret-rotation changes
  do not prevent reuse when execution identity is unchanged.
- **PI-11:** Undeclared nondeterminism disables reuse, and an observed source is
  reusable only with a stable connector-provided equivalence token accepted by
  target policy.
- **PI-12:** Candidate schemas, globally versioned relation names, and an
  authoritative SQLite physical-output ownership graph are absent; the sealed
  DuckLake catalog is the physical manifest.
- **PI-13:** Catalog artifacts, physical pool objects, and storage credentials
  remain inaccessible outside target-controlled authorization.
- **PI-14:** Every DuckDB runtime or DuckLake extension upgrade reruns
  concurrent same-table and different-table writes, new-object disjointness and
  collision checks, abort cleanup isolation, remote sealed reads, cross-catalog
  orphan classification, and global mark completeness before the new tuple may
  join a pool.

## Build, seal, and reconciliation

- **PR-01:** A durable native PostgreSQL build attempt binds canonical plan and
  execution inputs, exact base catalog, physical pool, and writer lease before
  physical work begins.
- **PR-02:** Build-local DuckLake transactions and intermediate snapshots are
  private and disposable. A pre-seal crash produces no candidate and may retry
  computation from the exact base without transaction-receipt reconciliation.
- **PR-03:** Snapshot normalization supplies an exact version set, preserves the
  latest snapshot, performs no physical deletion, and verifies the single-state
  postcondition. It also verifies effective inlining options at every persisted
  scope and that legacy inlined inserts and deletes have been flushed.
- **PR-04:** Qualification and seal preparation verify contracts, tests, audits,
  admitted runtime compatibility, absence of live inlined data, and the complete
  current data/delete-file closure.
- **PR-05:** The metadata database is detached and safely closed without using a
  DuckLake catalog-level checkpoint or another path that can invoke physical
  cleanup.
- **PR-06:** Before artifact upload, PostgreSQL durably records the catalog
  digest, size, physical pool, content-addressed create-only key, canonical
  inputs, and verification evidence under a unique sealing identity.
- **PR-07:** Artifact creation is conditional and immutable. A lost upload
  acknowledgement is reconciled by reading the exact recorded key and verifying
  bytes, digest, size, and required metadata.
- **PR-08:** An existing object with mismatching bytes or evidence is corruption
  and is never overwritten or accepted for the seal.
- **PR-09:** Native PostgreSQL marks a candidate ready only after remote
  read-only catalog attachment verifies the exact artifact and physical
  closure.
- **PR-10:** Reusing a sealing identity with different canonical inputs, digest,
  pool, or key is a conflict; retrying the same sealed identity converges on the
  same candidate.
- **PR-11:** A pre-seal attempt whose local artifact is lost may fail and be
  recomputed under a new attempt. Its unreferenced objects are eventually GC'd.
- **PR-12:** Refresh and restatement create a new private catalog and retain run,
  effective-input, strategy, seal, and qualification evidence through every
  crash boundary.

## Exact publication

- **PU-01:** Publication accepts only the exact ready,
  publication-eligible candidate and plan approved for the target.
- **PU-02:** Publication performs no source capture, source-ref resolution,
  compilation, materialization, qualification rerun, or candidate mutation.
- **PU-03:** Active generation and target revision compare-and-swap atomically;
  stale publication affects neither active state nor newer evidence.
- **PU-04:** Successful publication establishes the generation's exact catalog
  root in the same native PostgreSQL transaction as the active pointer.
- **PU-05:** A timeout or crash around activation leaves a durable indeterminate
  publication that resolves to the committed result or a proven non-commit
  before retry.
- **PU-06:** Publication reconciliation never activates a newly built candidate
  or cleans up the intended candidate while outcome is unknown.
- **PU-07:** Publication mutates no DuckLake table, snapshot, catalog artifact,
  or physical-pool object.

## Retention and garbage collection

- **RG-01:** Candidate TTL, quota, retirement, pull-request cleanup, orphan
  cleanup, and retention exceptions preserve every rooted catalog artifact and
  every object reachable from it.
- **RG-02:** Native PostgreSQL is authoritative for the catalog root set; each
  verified DuckLake catalog is authoritative for its current data/delete-file
  membership.
- **RG-03:** Root creation, query-lease acquisition, and catalog retirement
  serialize through one durable fence: a winning root or lease prevents
  retirement, while winning retirement rejects new roots and leases.
- **RG-04:** Roots cover sealing or indeterminate seal records, ready
  candidates, prepared and active generations, pending or indeterminate
  publications, rollback windows, retention exceptions, and active candidate
  or generation query leases. Unsealed work is protected by writer fencing.
- **RG-05:** Every catalog sharing a physical pool is visible to the same global
  collector; independently governed catalogs never share a deletable namespace.
- **RG-06:** GC verifies each rooted artifact and marks every non-null
  `data_file` and `delete_file` for every current base table, plus the rooted
  catalog artifacts and declared pool metadata. Current-state enumeration is
  complete only for an admitted compatibility tuple after one-snapshot
  normalization and verification that no live inlined data remains.
- **RG-07:** The destructive phase excludes or epochs physical writers and
  protects in-flight namespaces and objects newer than the configured build,
  orphan, and reader grace periods.
- **RG-08:** Immediately before deletion, GC revalidates its pool epoch, root
  revision, query leases, writer fence, and candidate/publication state; a
  relevant change aborts or restarts deletion.
- **RG-09:** Every delete cycle has an exact bounded PostgreSQL intent and reconciles
  ambiguous storage responses by verifying the intended keys and postcondition.
- **RG-10:** `ducklake_cleanup_old_files`,
  `ducklake_delete_orphaned_files`, explicit or externally scheduled cleanup,
  persisted maintenance defaults when invoked, and checkpoint-triggered cleanup
  are unreachable for catalogs in a shared pool.
- **RG-11:** Explicit pre-seal snapshot expiration may mutate only the private
  metadata catalog; all physical deletion remains global.
- **RG-12:** A multi-process deployment uses durable writer and GC leases,
  epochs, or equivalent control-store fencing rather than relying on a
  process-local mutex.
- **RG-13:** Crashed pre-seal builds may leave orphan objects, but those objects
  cannot become queryable and become collectible only after writer fencing and
  the grace period.

## Rollback, development, and promotion

- **DP-01:** Every generation declares and enforces `rollback_safe`,
  `serving_safe`, or `non_reversible` with a truthful retention window and
  external-effect description.
- **DP-02:** Rollback directly selects a retained qualified generation and
  appends a new audit event; it does not rebuild or imply reversal of undeclared
  external effects.
- **DP-03:** Development watch mode can update only private candidate and
  session-pointer state.
- **DP-04:** `plan` selects the target. Build and publish inherit it and reject a
  different destination assertion.
- **DP-05:** Destination promotion replans the same portable source bytes and
  creates a target-specific candidate rather than copying the source target's
  plan or candidate.
- **DP-06:** Successful development qualification is retained as provenance but
  cannot replace destination qualification.
- **DP-07:** Convenience commands emit durable plan and candidate identities and
  cannot bypass inspection, qualification, approval, concurrency, or evidence.

## Audit and operational evidence

- **AO-01:** Plan creation, build start and completion, qualification,
  publication request, approval or rejection, activation, rollback,
  restatement, retirement, and cleanup append immutable actor-, object-,
  digest-, version-, timestamp-, and result-bound events.
- **AO-02:** Runtime lineage and quality events are OpenLineage-compatible where
  lossless, while LeapView evidence remains authoritative.
- **AO-03:** Routine UX exposes plan impact, candidate qualification,
  publication eligibility, and required decisions without requiring users to
  restate target or provenance internals.
- **AO-04:** Inspection and audit surfaces expose immutable source, execution,
  provenance, governance, target revision, plan, candidate, catalog seal,
  publication, and generation evidence.
- **AO-05:** Delivery evidence is inspected through the target API and
  provider-native PostgreSQL/DuckLake/object-store tooling. No offline audit or
  repair mutation is part of the supported operator contract.

## End-to-end suites

- **E2E-01:** Maintained composition and focused provenance suites exercise the
  same transitions for a local evaluation target, an automated
  private-development or pull-request target, and a protected target requiring
  approval and trusted provenance. Policy differences are configuration rather
  than alternate lifecycle code.
- **E2E-02:** The composition suite and its maintained focused failure suites
  cover concurrent plan and publication changes, same-base candidate builds,
  every build-and-seal crash boundary, lost upload acknowledgements, and lost
  publication acknowledgements.
- **E2E-03:** Maintained suites cover root-versus-GC and
  lease-versus-retirement races, long queries through publication and GC, and
  rollback within and outside retention.
- **E2E-04:** Maintained suites cover cross-target requalification and pinned,
  bounded, and observed data inputs through the same lifecycle contracts.
- **E2E-05:** Generated CLI and API contracts, public workflow documentation,
  maintained CI examples, release evidence, and operational runbooks agree
  with the implemented lifecycle.
