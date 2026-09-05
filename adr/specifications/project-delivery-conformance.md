# Project delivery conformance specification

Status: accepted

Last updated: 2026-09-04

Owners: LeapView maintainers

Governing decision: [ADR-0020](../0020-adopt-a-postgresql-centered-target-data-architecture.md)

Historical invariants: [ADR-0007](../0007-adopt-plan-driven-project-delivery.md),
[ADR-0008](../0008-isolate-ducklake-candidate-physical-state.md), and
[ADR-0009](../0009-separate-control-and-physical-transactions.md)

ADR-0020 supersedes the private file-backed catalog and catalog-object
mechanics from ADR-0008, and the SQLite control-store selection from ADR-0009.
The plan/build/publish lifecycle, exact identity, candidate isolation, fencing,
lease, retention, and cross-store reconciliation invariants remain in force
where they are expressed by the native PostgreSQL target. Production evidence
therefore uses the native PostgreSQL delivery authority, a long-lived
PostgreSQL-backed DuckLake catalog, and immutable snapshot seals. Local and
evaluation adapters may still use a process-private DuckLake catalog file;
that fixture support is not production evidence.

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
  resolved execution record, immutable DuckLake snapshot seal (catalog,
  snapshot, relation-closure, and object-root evidence), physical pool, runtime
  identity, and qualification record.
- **LC-05:** Every generation binds exactly one published candidate and its
  immutable snapshot seal and serving artifact, and remains a complete
  immutable project graph.
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
| **SE — sealed serving** | Candidate/seal/generation share snapshot, artifact, pool, compatibility, and serving-state identities; exact read-only attach and lease evidence | `internal/app/runtimefactory/postgres_sealed_delivery_test.go`; `internal/app/runtimefactory/postgres_test.go`; `internal/platform/architecture/delivery_conformance_test.go:TestLEA414ProductionUsesSealedCanonicalPath` | Remote snapshot read and credential bootstrap in the MinIO qualification lane | Preparing/unverified row, mutable artifact, failed lease/auth, or mixed snapshot identity |
| **AO — append-only operations** | Plan/build/qualification/approval/publish/activate/retire/rollback/lease/retention events with actor, object, request/result digests, outcome and UTC time | Native PostgreSQL event/repository and delivery lifecycle tests | Run against the target's durable PostgreSQL database during release qualification | Event update/delete, conflicting replay, missing event after a committed transition, secret-bearing details, or indeterminate publication without reconciliation |

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
| LC-04 | Ready candidate requires exact plan, resolved inputs, snapshot seal/pool, runtime and qualification identities. | `internal/deployment/postgres/complete_build_test.go:TestPostgresCompleteBuildTxSuccessAndExactReplay`; `internal/app/deploymentpostgres/native_seal_assembler_test.go:TestAssembleNativeGenerationAdmissionInputAcceptsExactEvidence` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| LC-05 | Generation is one immutable published candidate, snapshot seal, and serving artifact. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`; `internal/servingstate/postgres/repository_test.go:TestRecordDuckLakeSnapshotVerifiesImmutableSealedEvidence` | [upgrades](../../docs/articles/operate/upgrades.md) |
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
| TP-09 | Expired, cross-target and source-mismatched plans are rejected. | `internal/deployment/postgres/plan_document_test.go:TestCreatePlanRejectsRichDocumentOutsideTargetScope`; `internal/deployment/postgres/repository_test.go:TestPostgresLeaseCASRaceAndStaleFence` | [upgrades](../../docs/articles/operate/upgrades.md) |
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
| CQ-02 | Blocking native qualification/build failure settles the attempt without admitting a ready candidate or changing active state. | `internal/app/deploymentpostgres/native_build_test.go:TestNativeBuildDeterministicFailureSettlesEveryLedgerAndRejectsCandidate`; `internal/app/deploymentpostgres/native_qualification_test.go:TestQualifyNativeSnapshotClosesPartiallyOpenedEnvironment` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| CQ-03 | Qualification policy and data-input modes are explicit in native evidence. | `internal/app/deploymentpostgres/native_qualification_test.go:TestQualifyNativeSnapshotRunsGatesAgainstExactNamespace`; `internal/app/deploymentpostgres/native_qualification_inputs_test.go:TestNativeQualificationInputsRejectsUnknownDataMode` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| CQ-04 | Reject-stale policy prevents consequences after target-revision drift. | `internal/app/deploymentpostgres/native_create_plan_postgres_test.go:TestNativeCreatePlanPostgresRejectsTargetRevisionDriftBeforeConsequences`; `internal/deployment/postgres/repository_test.go:TestPostgresLeaseCASRaceAndStaleFence` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| CQ-05 | A permitted retained-base fallback requires exact native qualification evidence. | `internal/app/deploymentpostgres/native_qualification_inputs_test.go:TestNativeQualificationInputsRequiresReuseModeForBaseFallback` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| CQ-06 | Stale candidates are permanently ineligible without mutation. | `internal/deployment/postgres/complete_build_test.go:TestPostgresCompleteBuildTxConflictingReplayAndStaleLease`; `internal/app/deploymentpostgres/attempt_termination_test.go:TestAttemptReconciliationExpiredLeaseExactCommitAndReplay` | [upgrades](../../docs/articles/operate/upgrades.md) |
| CQ-07 | Approval binds one exact candidate/plan and never carries forward. | `internal/deployment/approval_test.go:TestApprovalBindsDecisionToExactDeploymentPlan` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| CQ-08 | Candidate preview applies live grants and policies. | `internal/dashboard/queryauthz/canonical_test.go:TestCanonicalRLSMasksAndPolicyFingerprint`; `internal/dashboard/queryauthz/canonical_test.go:TestCanonicalPublicPublicationAndCandidateClosures`; active delivery object authorization is asserted by `internal/app/canonical_authorization_test.go:TestDeliveryAuthorizationRequiresEveryAffectedResource`. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-01 | Each build uses the admitted long-lived PostgreSQL-backed DuckLake catalog with an attempt-, candidate-, and writer-fence-qualified relation namespace; private catalog files are fixture-only. | `internal/app/deploymentpostgres/native_physical_build_test.go:TestBuildNativePhysicalRejectsRelationNamespaceDrift`; `internal/analytics/ducklake/postgres_catalog_test.go:TestPostgresCatalogPoolNamespaceRejectsCrossDomainSchema` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-02 | Concurrent candidates and successor attempts remain isolated by their native relation namespace and fencing identity. | `internal/deployment/postgres/successor_attempt_test.go:TestSuccessorNamespaceDerivationIsAttemptAndFenceQualified`; `internal/app/deploymentpostgres/native_physical_build_test.go:TestBuildNativePhysicalRejectsConflictingPrepopulatedRelationNamespace` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-03 | Shared data reuse requires one admitted physical-pool compatibility tuple. | `internal/analytics/ducklake/shared_pool_safety_test.go:TestSharedPoolConformanceLocalClosedCloneFixture`; `internal/analytics/ducklake/conformance_artifact_test.go:TestSharedPoolEvidenceArtifactIsCompleteAndPortable` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-04 | Exact reuse identity preserves the existing physical relation; every execution mismatch rebuilds the affected relation namespace. | `internal/app/runtimefactory/delivery_plan_test.go:TestCandidatePlanReuseDecisionUsesExactActiveIdentity`; `internal/app/runtimefactory/delivery_plan_test.go:TestCandidatePlanEmitsRelationScopedReuseDecisions` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-05 | A successful native build seals one exact committed DuckLake snapshot; retention/expiry is separate maintenance and does not perform physical cleanup during sealing. | `internal/analytics/ducklake/postgres_runtime_test.go:TestPostgresRuntimeCommitMarkerReconcilesExactSnapshot`; `internal/deployment/postgres/complete_build_test.go:TestPostgresCompleteBuildTxSuccessAndExactReplay` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-06 | Preview, qualification, comparison, and serving attach the exact snapshot seal read-only. | `internal/analytics/ducklake/postgres_catalog_test.go:TestPostgresCatalogServingRequiresExactReadOnlySnapshot`; `internal/app/runtimefactory/postgres_test.go:TestPostgresSealedFactoryRejectsIncompleteOrMixedSealIdentityBeforeLease` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-07 | Native attach and qualification enforce the configured DuckLake inlining policy; analytical rows and physical manifests are never duplicated into PostgreSQL control tables. | `internal/analytics/ducklake/postgres_catalog_test.go:TestPostgresCatalogWriterDisablesDataInliningAtAttachScope`; `internal/platform/architecture/delivery_conformance_test.go:TestPlanDeliveryPhysicalAuthorityGuards` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-08 | Production writes only through an attempt-qualified namespace in the long-lived catalog; no mutable catalog is byte-copied. | `internal/platform/architecture/delivery_conformance_test.go:TestPlanDeliveryPhysicalAuthorityGuards`; `internal/app/deploymentpostgres/native_physical_build_test.go:TestBuildNativePhysicalRejectsRelationNamespaceDrift` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PI-09 | All declared result-affecting semantics alter execution identity. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryExecutionDigestChangesForEveryResultAffectingInput`; `internal/app/runtimefactory/delivery_plan_test.go:TestCandidatePlanExecutionIdentityIncludesDataModeAndEffectiveBindings` | [validate/deploy](../../docs/guides/cli/validate-deploy.md) |
| PI-10 | Provenance/approval/owner/secret rotation can reuse equivalent execution. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryGovernanceAndCredentialRotationPreserveExecutionReuseIdentity`; `internal/app/runtimefactory/delivery_plan_test.go:TestCandidatePlanReuseDecisionUsesExactActiveIdentity` | [validate/deploy](../../docs/guides/cli/validate-deploy.md) |
| PI-11 | Undeclared nondeterminism disables reuse. | `internal/deployment/plan_delivery_contracts_test.go:TestDeliveryReusePolicyDisablesUndeclaredNondeterminism`; `internal/app/runtimefactory/delivery_plan_test.go:TestCandidatePlanReuseDecisionUsesExactActiveIdentity` | [validate/deploy](../../docs/guides/cli/validate-deploy.md) |
| PI-12 | No SQLite file-membership manifest; the PostgreSQL-backed DuckLake catalog is the physical authority. | `internal/platform/architecture/delivery_conformance_test.go:TestPlanDeliveryPhysicalAuthorityGuards` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PI-13 | Physical-pool objects and credentials stay target-authorized. | `internal/analytics/ducklake/shared_pool_safety_test.go:TestCredentialBootstrapRunsForEveryPooledConnector`; `internal/app/gcadapter/credentials_test.go:TestNewPoolCredentialBootstrapS3RequiresTargetKeys`; `internal/app/gcadapter/credentials_test.go:TestNewPoolStoreS3RequiresTargetKeysBeforeAWSConfig`; `internal/app/runtimefactory/postgres_test.go:TestPostgresSealedFactoryRequiresTargetCapabilities`; `internal/app/runtimefactory/postgres_test.go:TestPostgresSealedFactoryAcquiresAuthorizesAndReleasesOnAttachFailure` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PI-14 | Runtime upgrades require the complete shared-pool conformance lane. | `task test:go:minio-conformance`; `.github/workflows/release.yml:minio-conformance` fails on an unavailable extension or tuple drift and retains the complete evidence artifact, checksum, exact DuckDB/DuckLake versions, pinned MinIO digest, and logs. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-01 | Build attempt binds plan/input/base/pool/writer lease before work. | `internal/deployment/postgres/repository_test.go:TestPostgresCallerOwnedLeaseAndBuildAttemptAdmission`; `internal/app/deploymentpostgres/candidate_build_attempt_admission_test.go:TestCandidateBuildAttemptAdmissionPostgresAtomicSuccessReplayAndRollback` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-02 | An indeterminate native build is reconciled from its persistent commit marker; no candidate is ready until the exact snapshot is proven. | `internal/analytics/ducklake/postgres_runtime_test.go:TestCommitMarkerAckFailureReconcilesOnFreshSession`; `internal/app/deploymentpostgres/native_physical_recovery_test.go:TestRecoverNativePhysicalBuildPersistsMarkerAnomalyBeforeFailingClosed` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-03 | Snapshot commit evidence records one exact committed version and marker; physical expiration is deferred to retention maintenance. | `internal/analytics/ducklake/postgres_catalog_test.go:TestResolveCommittedSnapshotMatchesSemanticMarkerJSONAfterRestart`; `internal/analytics/ducklake/postgres_runtime_test.go:TestSnapshotSealEvidenceAllowsBoundedCanonicalMarkerDocument` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-04 | Native qualification verifies the exact namespace, runtime contract, relation closure, and snapshot-seal evidence. | `internal/app/deploymentpostgres/native_qualification_test.go:TestQualifyNativeSnapshotRunsGatesAgainstExactNamespace`; `internal/analytics/ducklake/native_snapshot_closure_test.go:TestNativeSnapshotClosureEvidenceCanonicalDigestsAreStable` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-05 | The PostgreSQL-backed DuckLake metadata session closes through the bounded maintenance boundary; runtime attachments cannot invoke catalog cleanup/checkpoint paths. | `internal/analytics/ducklake/postgres_physical_maintenance_test.go:TestPostgresCatalogMaintenanceFailsClosedForSharedRuntimeAndAmbiguousCatalog`; `internal/analytics/ducklake/postgres_catalog_test.go:TestPostgresCatalogMigrationModeIsExplicitAndRuntimeCannotEnableIt` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PR-06 | PostgreSQL snapshot-seal admission records the exact catalog/snapshot, pool, object-root, and qualification identity before publication. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`; `internal/app/deploymentpostgres/native_seal_assembler_test.go:TestAssembleNativeGenerationAdmissionInputAcceptsExactEvidence` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-07 | Native seal admission and replay preserve exact catalog/object-root identity and qualification evidence; PostgreSQL is the delivery authority rather than a generic upload/acknowledgement seam. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`; `internal/app/deploymentpostgres/native_seal_assembler_test.go:TestAssembleNativeGenerationAdmissionInputRejectsIdentityDrift` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-08 | Mismatching existing object is corruption and never overwritten. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`; `internal/deployment/postgres/complete_build_test.go:TestPostgresCompleteBuildTxConflictingReplayAndStaleLease` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PR-09 | Ready candidate follows read-only snapshot-seal and relation/object-closure verification. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`; `internal/app/deploymentpostgres/native_qualification_test.go:TestQualifyNativeSnapshotRejectsTamperedPhysicalEvidenceBeforeOpen` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-10 | Sealing identity drift conflicts; identical retry converges. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`; `internal/deployment/postgres/complete_build_test.go:TestPostgresCompleteBuildTxConflictingReplayAndStaleLease` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PR-11 | Lost pre-seal native build evidence is reconciled from the persistent marker; orphan snapshots/objects remain fenced and grace-delayed before native retention maintenance. | `internal/app/deploymentpostgres/native_build_recovery_preparation_test.go:TestPrepareNativeBuildRecoveryNormalizesAndReplays`; `internal/app/deploymentpostgres/native_build_recovery_finalization_test.go:TestCompleteRecoveredNativeBuildPostgresSuccessAndExactReplay`; `internal/analytics/ducklake/postgres/snapshot_orphan_coordinator_test.go:TestPostgres18SnapshotOrphanCoordinatorGraceAndReplayAfterNativeFailure` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PR-12 | Refresh/restatement creates a new attempt- and namespace-qualified snapshot and retains effective-input, seal, and qualification evidence. | `internal/deployment/plan_delivery_contracts_test.go:TestRestatementPlanRetainsBoundedIntervalsScopeStrategyAndIdempotency`; `internal/app/deploymentpostgres/native_build_test.go:TestNativeBuildPlanProjectsRestatementReuseToRefreshSourcesBeforeMaterialization` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PU-01 | Publish accepts only exact ready eligible candidate and approved plan. | `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresPublishCandidatePersistsEvidenceAndReplays` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PU-02 | Publish performs no source capture/compile/qualification/candidate mutation. | `internal/deployment/module/native_delivery_test.go:TestNativeDeliveryPublicationHandlersPreferNativePort` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PU-03 | Active generation/revision CAS is atomic and stale-safe. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`; `internal/deployment/postgres/revision_allocation_test.go:TestPostgresGenerationRevisionAllocationReplayRollbackAndConcurrency` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PU-04 | Generation snapshot-retention root and active pointer commit in one transaction. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PU-05 | Activation timeout/crash persists indeterminate and reconciles. | `internal/deployment/postgres/activate_lost_ack_test.go:TestPostgresActivateReplaysAfterCommitLostAcknowledgement`; `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresActivationPreCommitHookRollsBack` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| PU-06 | Reconciliation never activates or cleans unknown candidate. | `internal/deployment/module/jobs_test.go:TestActivationJobRequiresPostCommitReconciliation`; `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresMutationFailuresRollbackSourceAndOperation` | [upgrades](../../docs/articles/operate/upgrades.md) |
| PU-07 | Publication mutates no DuckLake physical object. | `internal/platform/architecture/delivery_conformance_test.go:TestPlanDeliveryPhysicalAuthorityGuards` | [upgrades](../../docs/articles/operate/upgrades.md) |
| RG-01 | Retention roots protect exact DuckLake snapshots through candidate TTL and quota cleanup; DuckLake owns their object reachability. | `internal/servingstate/postgres/repository_test.go:TestRetentionInventoryScopesRootsLeasesAndSnapshotEvidence`; `internal/analytics/ducklake/postgres/retention_coordinator_test.go:TestPostgres18RetentionCoordinatorReplayAndExactSnapshotVerification` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-02 | Native PostgreSQL retention roots plus the exact DuckLake snapshot/file closure are authoritative. | `internal/servingstate/postgres/repository_test.go:TestRetentionInventoryScopesRootsLeasesAndSnapshotEvidence`; `internal/deployment/postgres/repository_test.go:TestRetentionRootLifecycleTxReplayGraceAndExpiry`; `internal/analytics/ducklake/native_snapshot_closure_test.go:TestCanonicalNativeObjectsCanonicalizesRootsAndRejectsConflicts` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-03 | Root, query lease and retirement share one durable fence. | `internal/deployment/postgres/repository_test.go:TestPostgresLeaseCASRaceAndStaleFence`; `internal/platform/events/postgres/repository_test.go:TestPostgreSQL18RetirementFenceClosesProducerFanoutRace` | [upgrades](../../docs/articles/operate/upgrades.md) |
| RG-04 | All sealing/candidate/generation/publication/rollback/lease/recovery roots are enumerated. | `internal/deployment/postgres/repository_test.go:TestPostgresDeliveryAuthorityLifecycleAndReplay`; `internal/deployment/postgres/recovery_retention_test.go:TestRecoveryRetentionRootMaintenanceRetiresAndExpires` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-05 | Shared-pool snapshots are scanned by one fenced PostgreSQL retention/orphan coordinator. | `internal/analytics/ducklake/postgres/snapshot_orphan_scanner_test.go:TestPostgres18SnapshotOrphanScanBoundedReplayAndFencedRole`; `internal/analytics/ducklake/postgres/snapshot_orphan_coordinator_test.go:TestPostgres18RetentionCoordinatorInvokesOrphansBeforeFilePhases` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-06 | A fenced control transaction freezes the exact snapshot-expiration set before native maintenance; replay cannot absorb newly eligible snapshots. | `internal/analytics/ducklake/postgres/retention_coordinator_test.go:TestPostgres18RetentionReplayReconcilesAdvancedStateBeforeChildEvidence`; `internal/analytics/ducklake/postgres/snapshot_catalog_page_scanner_test.go:TestPostgresSnapshotCatalogPageScannerKeysetAndBounds` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-07 | Destructive maintenance fences writers/readers and applies configured grace periods. | `internal/analytics/ducklake/postgres/retention_coordinator_test.go:TestPostgres18RetentionFenceInterlocksAndFences`; `internal/app/deploymentpostgres/retention_lifecycle_test.go:TestDeliveryRetentionRootRetirementDrainsExactReaderLeases`; `internal/deployment/postgres/repository_test.go:TestPostgresLeaseCASRaceAndStaleFence` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-08 | Retention maintenance revalidates pool epoch, roots, leases, and state before each bounded native phase. | `internal/analytics/ducklake/postgres/retention_coordinator_test.go:TestPostgres18RetentionMaintenanceRoleCapabilities`; `internal/analytics/ducklake/postgres_physical_maintenance_test.go:TestPostgresCatalogMaintenanceRevalidatesFenceAfterPhase` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-09 | Every native expiry/delete phase uses bounded intent and reconciles already-absent or ambiguous outcomes. | `internal/analytics/ducklake/postgres/snapshot_orphan_coordinator_test.go:TestPostgres18SnapshotOrphanCoordinatorReplaysAlreadyAbsentClaim`; `internal/analytics/ducklake/postgres/retention_coordinator_test.go:TestPostgres18RetentionReplayReconcilesAdvancedStateBeforeChildEvidence` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-10 | Snapshot expiration and old-file/orphan cleanup are reachable only through the dedicated fenced maintenance session, never serving attachments. | `internal/analytics/ducklake/shared_pool_safety_test.go:TestSharedPoolRejectsNativeCleanupCheckpointAndMaintenance`; `internal/analytics/ducklake/postgres_physical_maintenance_test.go:TestPostgresCatalogMaintenanceRunsExplicitBoundedSequence` | [upgrades](../../docs/articles/operate/upgrades.md) |
| RG-11 | Snapshot expiry mutates only the PostgreSQL-backed DuckLake metadata through the fenced maintenance role; physical cleanup remains a later native phase. | `internal/analytics/ducklake/postgres/retention_coordinator_test.go:TestPostgres18RetentionCoordinatorReplayAndExactSnapshotVerification`; `internal/analytics/ducklake/postgres_physical_maintenance_test.go:TestPostgresCatalogMaintenanceRunsExplicitBoundedSequence` | [plan/build-publish](../../docs/articles/operate/plan-build-publish.md) |
| RG-12 | Multi-process writer/GC leases use durable epochs, not mutexes. | `internal/deployment/postgres/repository_test.go:TestPostgresLeaseCASRaceAndStaleFence`; `internal/deployment/postgres/successor_attempt_test.go:TestPostgresAdmitSuccessorBuildAttemptRequiresMarkerResolutionAndFencesPredecessor`; `internal/deployment/gcstore/local_test.go:TestLocalDeletionLeaseFencesClonedMetadataDatabases` | [upgrades](../../docs/articles/operate/upgrades.md) |
| RG-13 | Pre-seal orphan snapshots/objects are fenced, non-queryable, and grace-delayed before orphan/file maintenance. | `internal/analytics/ducklake/postgres/snapshot_orphan_coordinator_test.go:TestPostgres18SnapshotOrphanCoordinatorGraceAndReplayAfterNativeFailure`; `internal/analytics/ducklake/postgres/snapshot_orphan_scanner_test.go:TestPostgres18SnapshotOrphanScanBoundedReplayAndFencedRole` | [plan/build-publish](../../docs/articles/operate/plan-build-publish.md) |
| DP-01 | Generation rollback class/window/effects are validated. | `internal/app/deploymentpostgres/generation_admission_test.go:TestNormalizeGenerationAdmissionAcceptsExactEvidence`; `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresRollbackRequiresRetainedGeneration` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DP-02 | Rollback selects retained generation and appends rollback events. | `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresRollbackRequiresRetainedGeneration` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DP-03 | Watch mode changes only private candidate/session state. | `internal/project/devloop/service_test.go:TestReconcilePreservesLastValidCandidateWhenNextBuildFails`; `internal/project/devloop/watcher_test.go:TestWatcherDebouncesReachableChangesAndIgnoresUnrelatedFiles` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DP-04 | Build/publish inherit plan target and reject destination assertion drift. | `internal/project/cli/publish_command_test.go:TestPublishCommandUsesExactCheckpointWithoutReadingProjectSource`; `internal/project/cli/publish_command_test.go:TestCandidateCheckpointStoreRoundTripsExactNonSecretIdentity`; `internal/deployment/postgres/repository_test.go:TestPostgresLeaseCASRaceAndStaleFence`. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DP-05 | Promotion replans portable bytes for destination target. | `internal/app/runtimefactory/dp_conformance_test.go:TestPromotionReplansPortableArtifactForEachDestinationTarget` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DP-06 | Development qualification remains provenance and cannot replace destination qualification. | `internal/app/runtimefactory/dp_conformance_test.go:TestPromotionReplansPortableArtifactForEachDestinationTarget`; `internal/app/deploymentpostgres/native_qualification_test.go:TestQualifyNativeSnapshotRunsGatesAgainstExactNamespace` | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| DP-07 | Convenience commands emit durable identities and cannot bypass gates. | `internal/app/cli/project_deploy_test.go:TestDeployComposesCanonicalPlanBuildAndPublication`; `internal/project/cli/publish_command_test.go:TestPublishCommandUsesExactCheckpointWithoutReadingProjectSource`; `internal/project/cli/publish_command_test.go:TestCandidateCheckpointStoreRoundTripsExactNonSecretIdentity`; bootstrap safety remains covered by `internal/app/cli/command_surface_test.go:TestDeliveryPoolBootstrapDocumentsWriteEffectAndExplicitConfirmation`. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| AO-01 | All listed transitions append immutable actor/object/digest/result events, including approvals, leases, GC and restatements. | Native PostgreSQL event/repository suites (`internal/platform/events/postgres/repository_test.go`, `internal/deployment/postgres/repository_test.go`) and deployment lifecycle tests. | [upgrades](../../docs/articles/operate/upgrades.md) |
| AO-02 | Runtime lineage is lossless where supported; compiler and delivery projections remain authoritative. | Reviewed operational check: no OpenLineage adapter or outbound lineage integration is present; immutable PostgreSQL lineage projections plus canonical event and audit records retain admitted evidence. | [upgrades](../../docs/articles/operate/upgrades.md) |
| AO-03 | UX exposes impact, qualification, eligibility and decisions. | `internal/app/cli/delivery_cli_test.go:TestDeliveryPlanResultPreservesReviewEvidence`; `internal/project/cli/command_test.go:TestDeliveryPlanTextOutputIncludesReviewEvidence`; `internal/deployment/module/delivery_api_test.go:TestDeliveryPlanPreviewExposesImmutableReviewEvidence`; browser/agent have no separate delivery mutation surface. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| AO-04 | Inspection exposes immutable source/execution/provenance/governance/target/candidate/seal/publication evidence. | `internal/deployment/module/delivery_api_test.go:TestDeliveryPlanPreviewExposesImmutableReviewEvidence`; `internal/deployment/module/native_delivery_read_test.go:TestNativeDeliveryCandidateReadProjectsResolvedServingState`; `internal/app/cli/publish_test.go:TestProjectPublishOperationsEmitVersionedAcceptedJSON`. | [upgrades](../../docs/articles/operate/upgrades.md) |
| AO-05 | Delivery evidence is inspected through the target API and provider-native PostgreSQL/DuckLake/object-store tooling; no offline audit or repair mutation is exposed. | `internal/deployment/module/delivery_api_test.go:TestDeliveryPlanPreviewExposesImmutableReviewEvidence`; `internal/deployment/module/native_delivery_read_test.go:TestNativeDeliveryCandidateReadProjectsResolvedServingState`; release delivery qualification evidence | [delivery recovery](../../docs/articles/operate/delivery-recovery.md) |
| E2E-01 | Local evaluation, private/PR, and protected-production policies use one plan/build/publish lifecycle; only policy, input, and approval configuration differ. | `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresLifecycleReplayAndScope`; protected provenance is enforced by `internal/project/module/candidate_sources_test.go:TestCandidateSourceSynchronizerAuthorizesOnlyPlannedOwnerUploads`; release gate `task test:go:plan-gc-conformance`. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| E2E-02 | Composition and focused failure suites cover concurrent namespace-qualified candidates, marker/seal crash recovery, orphan reconciliation, and lost publication acknowledgement. | `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresLifecycleReplayAndScope`; `internal/analytics/ducklake/postgres_runtime_test.go:TestCommitMarkerAckFailureReconcilesOnFreshSession`; `internal/app/deploymentpostgres/native_build_recovery_finalization_test.go:TestCompleteRecoveredNativeBuildPostgresSuccessAndExactReplay`; `internal/deployment/postgres/activate_lost_ack_test.go:TestPostgresActivateReplaysAfterCommitLostAcknowledgement`. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| E2E-03 | Long readers, root/retention races, lease/retirement fencing, exact rollback, and outside-window rejection are maintained together. | `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresLifecycleReplayAndScope`; `internal/deployment/postgres/repository_test.go:TestPostgresLeaseCASRaceAndStaleFence`; `internal/analytics/ducklake/postgres/retention_coordinator_test.go:TestPostgres18RetentionFenceInterlocksAndFences`. | [delivery recovery](../../docs/articles/operate/delivery-recovery.md) |
| E2E-04 | Pinned, bounded, and observed modes run through the same lifecycle; destination promotion requalifies instead of copying candidate identity. | `internal/deployment/module/native_coordinator_pg_test.go:TestNativeCoordinatorPostgresLifecycleReplayAndScope`; `internal/app/runtimefactory/dp_conformance_test.go:TestPromotionReplansPortableArtifactForEachDestinationTarget`; `internal/app/deploymentpostgres/native_qualification_inputs_test.go:TestNativeQualificationInputsUsesCompleteValidBaseEvidence`. | [plan/build/publish](../../docs/articles/operate/plan-build-publish.md) |
| E2E-05 | CLI/API vocabulary, release gates, and operator runbooks expose the same immutable plan/candidate/generation/snapshot-seal contract. | `internal/deployment/module/delivery_api_test.go:TestDeliveryPlanPreviewExposesImmutableReviewEvidence`; `internal/platform/architecture/delivery_conformance_test.go:TestPlanDeliveryPhysicalAuthorityGuards`; release jobs `minio-conformance` and `plan-gc-conformance`. | [delivery recovery](../../docs/articles/operate/delivery-recovery.md) |

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

## DuckLake snapshot seals, namespace isolation, and reuse

- **PI-01:** Each build uses the admitted long-lived PostgreSQL-backed DuckLake
  catalog and an immutable relation namespace qualified by candidate, build
  attempt, and writer-fencing epoch. A private file-backed catalog is allowed
  only for local/evaluation fixtures.
- **PI-02:** Two candidates built concurrently from the same base remain
  isolated when changing either the same or different logical tables because
  their native relation namespaces and writer fences differ.
- **PI-03:** A base and child that reuse physical data bind the same
  `PhysicalPool`; its one long-lived catalog is governed by one retention
  authority and uses one admitted DuckDB runtime, DuckLake extension,
  catalog format, storage implementation, and object-naming compatibility
  tuple. This shared-writer topology is a version-gated LeapView contract, not
  an upstream DuckLake guarantee.
- **PI-04:** A complete reuse-key match retains the exact base data and delete
  file references without creating replacement data, while every relevant
  execution mismatch rebuilds the affected state into new immutable objects.
- **PI-05:** A successful build commits one DuckLake transaction and records
  the exact committed snapshot in an immutable snapshot seal. Snapshot expiry
  and physical cleanup are separate fenced maintenance phases.
- **PI-06:** Preview, qualification, comparison, and serving attach the exact
  snapshot seal read-only. Historical state is selected through another
  retained generation's exact snapshot, not catalog recency or an internal
  catalog-file time-travel shortcut.
- **PI-07:** Native attach and qualification enforce the configured DuckLake
  inlining policy. Analytical rows, file manifests, and cache payloads are
  never copied into PostgreSQL control tables as a second authority.
- **PI-08:** Candidate construction never mutates a sealed snapshot or
  generation. Open/mutating metadata is not byte-copied; the build writes its
  qualified namespace through the long-lived catalog and records a persistent
  commit marker before the snapshot commit. Local fixture catalog copies are
  outside production qualification evidence.
- **PI-09:** Changed logic, upstream identity, pinned version, bounded interval,
  observed input, result-affecting policy or binding, materialization semantic,
  declared nondeterministic input, connector, adapter, executable contract, or
  runtime compatibility changes execution identity.
- **PI-10:** Provenance-only, approval-only, owner, and secret-rotation changes
  do not prevent reuse when execution identity is unchanged.
- **PI-11:** Undeclared nondeterminism disables reuse, and an observed source is
  reusable only with a stable connector-provided equivalence token accepted by
  target policy.
- **PI-12:** Candidate schemas are not a second physical authority and there is
  no SQLite physical-output ownership graph; DuckLake remains authoritative for
  schema, table, file, delete-file, statistics, and snapshot membership.
- **PI-13:** Physical-pool objects and storage credentials
  remain inaccessible outside target-controlled authorization.
- **PI-14:** Every DuckDB runtime or DuckLake extension upgrade reruns
  concurrent same-table and different-table writes, new-object disjointness and
  collision checks, abort cleanup isolation, remote snapshot reads, persistent
  orphan-marker reconciliation, and frozen expiration-set completeness before
  the new tuple may join a pool.

## Build, seal, and reconciliation

- **PR-01:** A durable native PostgreSQL build attempt binds canonical plan and
  execution inputs, exact base snapshot seal, physical pool, and writer lease
  before physical work begins.
- **PR-02:** Build-local DuckLake transactions and intermediate snapshots are
  isolated by the candidate/attempt/fence-qualified namespace. An indeterminate
  commit is reconciled from its persistent marker before a candidate can become
  ready; an unprovable attempt requires a disjoint successor.
- **PR-03:** Snapshot commit evidence supplies one exact committed version and
  canonical marker, performs no physical cleanup, and verifies the snapshot-seal
  postcondition.
- **PR-04:** Qualification and seal preparation verify contracts, tests, audits,
  admitted runtime compatibility, exact namespace, and the complete current
  data/delete-file closure.
- **PR-05:** The metadata session closes through the bounded DuckLake adapter;
  runtime attachments cannot invoke catalog-level cleanup/checkpoint paths.
- **PR-06:** Before candidate readiness, PostgreSQL durably records the exact
  snapshot seal identity (catalog, snapshot, namespace, closure, pool), canonical
  inputs, and verification evidence under a unique sealing identity.
- **PR-07:** Snapshot-seal admission is immutable and exact. A lost response is
  reconciled by reading the recorded PostgreSQL attempt/seal rows and the native
  DuckLake marker; no catalog object upload acknowledgement is authoritative.
- **PR-08:** Any mismatching snapshot marker, closure, or seal evidence is
  corruption and is never overwritten or accepted for readiness.
- **PR-09:** Native PostgreSQL marks a candidate ready only after read-only attach
  verifies the exact snapshot seal and physical closure.
- **PR-10:** Reusing a sealing identity with different canonical inputs, snapshot,
  pool, namespace, or evidence is a conflict; retrying the same identity
  converges on the same candidate.
- **PR-11:** A pre-seal attempt whose marker or evidence is unresolved may fail
  and be recomputed under a disjoint successor attempt. Its unreferenced
  snapshots/objects remain non-queryable until fenced retention maintenance.
- **PR-12:** Refresh and restatement create a new namespace-qualified snapshot
  and retain run, effective-input, strategy, seal, and qualification evidence
  through every crash boundary.

## Exact publication

- **PU-01:** Publication accepts only the exact ready,
  publication-eligible candidate and plan approved for the target.
- **PU-02:** Publication performs no source capture, source-ref resolution,
  compilation, materialization, qualification rerun, or candidate mutation.
- **PU-03:** Active generation and target revision compare-and-swap atomically;
  stale publication affects neither active state nor newer evidence.
- **PU-04:** Successful publication establishes the generation's exact snapshot
  retention root in the same native PostgreSQL transaction as the active pointer.
- **PU-05:** A timeout or crash around activation leaves a durable indeterminate
  publication that resolves to the committed result or a proven non-commit
  before retry.
- **PU-06:** Publication reconciliation never activates a newly built candidate
  or cleans up the intended candidate while outcome is unknown.
- **PU-07:** Publication mutates no DuckLake table, snapshot seal, or
  physical-pool object; it changes only PostgreSQL control pointers and events.

## Retention and garbage collection

- **RG-01:** Candidate TTL, quota, retirement, orphan cleanup, and retention
  exceptions preserve every rooted snapshot seal. DuckLake remains authoritative
  for the objects reachable from each retained snapshot.
- **RG-02:** Native PostgreSQL is authoritative for retention roots; the
  PostgreSQL-backed DuckLake catalog is authoritative for snapshot,
  data-file, and delete-file membership.
- **RG-03:** Root creation, query-lease acquisition, and snapshot retirement
  serialize through one durable fence: a winning root or lease prevents
  retirement, while winning retirement rejects new roots and leases.
- **RG-04:** Roots cover sealing or indeterminate seal records, ready
  candidates, prepared and active generations, pending or indeterminate
  publications, rollback windows, retention exceptions, and active candidate
  or generation query leases. Unsealed work is protected by writer fencing.
- **RG-05:** One admitted physical pool owns exactly one long-lived DuckLake
  catalog and object namespace. Different retention or security authorities do
  not share that pool merely to improve reuse.
- **RG-06:** A fenced control transaction freezes the exact explicit snapshot
  expiration set and persists its digest and per-snapshot children before any
  DuckLake maintenance call. Replay uses only that set and cannot absorb newly
  eligible snapshots.
- **RG-07:** The destructive phase excludes or epochs physical writers and
  protects in-flight namespaces and objects newer than the configured build,
  orphan, and reader grace periods.
- **RG-08:** Immediately before expiration, maintenance revalidates its pool
  lease/fence and the frozen set; a relevant authority change aborts or
  reconciles the exact operation.
- **RG-09:** Every expiration set has bounded PostgreSQL intent and reconciles an
  ambiguous DuckLake response by verifying each intended snapshot postcondition.
- **RG-10:** Only the dedicated maintenance session may run explicit snapshot
  expiration and catalog-wide old-file/orphan cleanup. Cleanup follows the
  configured grace after expiration and never runs from serving attachments.
- **RG-11:** An unbound snapshot remains quarantined through the attempt/orphan
  grace while its persistent commit marker is reconciled. Absence of a control
  row never permits immediate expiration.
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
- **AO-02:** Runtime lineage is projected immutably from compiler and delivery
  evidence. OpenLineage compatibility may be added only where it is lossless;
  no outbound adapter is part of this target.
- **AO-03:** Routine UX exposes plan impact, candidate qualification,
  publication eligibility, and required decisions without requiring users to
  restate target or provenance internals.
- **AO-04:** Inspection and audit surfaces expose immutable source, execution,
  provenance, governance, target revision, plan, candidate, snapshot seal,
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
  every build-and-seal crash boundary, lost snapshot-commit acknowledgements,
  and lost publication acknowledgements.
- **E2E-03:** Maintained suites cover root-versus-retirement and
  lease-versus-retirement races, long queries through publication and snapshot
  expiration, and rollback within and outside retention.
- **E2E-04:** Maintained suites cover cross-target requalification and pinned,
  bounded, and observed data inputs through the same lifecycle contracts.
- **E2E-05:** Generated CLI and API contracts, public workflow documentation,
  maintained CI examples, release evidence, and operational runbooks agree
  with the implemented lifecycle.
