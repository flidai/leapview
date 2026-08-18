using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Deployment;

enum DeploymentStatus {
  queued;
  running;
  active;
  failed;
  cancelled;
  superseded;
}

enum CandidateStatus {
  preparing;
  ready;
  failed;
  cancelled;
  expired;
}

enum DeliveryPlanStatus {
  planned;
  expired;
}

enum DeliveryBuildStatus {
  building;
  normalizing;
  validating;
  sealing;
  sealed;
  failed;
  abandoned;
}

enum DeliveryPublicationStatus {
  pending;
  committed;
  rejected;
  indeterminate;
}

enum DeliverySealStatus {
  preparing;
  uploaded;
  verified;
  failed;
}

enum DeliveryCandidateStatus {
  preparing;
  ready;
  failed;
  retired;
}

enum DeliveryGenerationStatus {
  prepared;
  active;
  retired;
}

enum DeliveryRollbackClass {
  rollback_safe;
  serving_safe;
  non_reversible;
}

enum DeliveryOperationKind {
  code_change;
  restatement;
  binding_change;
  policy_change;
}

enum DeliveryRootKind {
  candidate;
  build;
  published;
  rollback;
  retained;
  quarantined;
  lease;
}

enum DeliveryRootStatus {
  active;
  retired;
}

enum DeliveryLeaseStatus {
  active;
  released;
  expired;
}

enum DeliveryGCStatus {
  running;
  marked;
  deleting;
  complete;
  aborted;
}

enum DeliveryGCDeleteStatus {
  pending;
  deleted;
  ambiguous;
}

enum DeliveryAuditStatus {
  accepted;
  rejected;
  indeterminate;
}

enum DeliveryDataInputMode {
  pinned;
  bounded;
  observed;
}

enum DeliveryStalePolicyMode {
  reject;
  allow_retained_base;
}

enum DeploymentApprovalStatus {
  pending;
  approved;
  denied;
  revoked;
  expired;
}

model CandidateStartRequest {
  artifactDigest: string;
  candidateKey?: string;
}

model CandidateArtifactRequest {
  expectedArtifactDigest: string;
  artifactDigest: string;
}

model CandidatePublishRequest {
  expectedRevision: int64;
  provenanceDigest: string;
  targetId: string;
  // Explicit one-shot first protected activation. Omission and false always
  // use the normal protected publication/approval path.
  bootstrap?: boolean;
}

model CandidateSourceArtifact {
  path: string;
  digest: string;
}

model CandidateSourceRevision {
  revision: string;
  repository?: string;
  ref?: string;
  changeId?: string;
}

model CandidateSynchronizationRequest {
  projectFile: string;
  artifactDigest: string;
  @doc("Retain the exact source snapshot without preparing a candidate or writing physical state. Delivery planning sets this true; Build owns the first writer operation.")
  sourceOnly?: boolean;
  candidateKey?: string;
  sourceRevision?: CandidateSourceRevision;
  expectedCandidateId?: string;
  expectedArtifactDigest?: string;
  artifacts: CandidateSourceArtifact[];
}

model CandidateSynchronizationPlanResponse {
  artifactDigest: string;
  missingDigests: string[];
}

model CandidateSourceSnapshotResponse {
  projectId: ResourceId;
  sourceDigest: string;
  // Opaque immutable attestation identity. This distinguishes identical
  // bytes retained from different source revisions.
  sourceAttestationDigest: string;
  projectDigest: string;
  targetId: string;
  environment: string;
}

model CandidateSourceBlobResponse {
  digest: string;
  sizeBytes: int64;
}

model CandidateResponse {
  id: string;
  projectId: ResourceId;
  candidateKey: string;
  targetId: string;
  environment: string;
  ownerId: string;
  baseGeneration: string;
  artifactDigest: string;
  provenanceDigest?: string;
  status: CandidateStatus;
  failureReason?: string;
  previewUrl: url;
  expiresAt: utcDateTime;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
  revision: int64;
  resumed?: boolean;
}

model DeploymentCreateRequest {
  releaseId: string;
}

model DeploymentApprovalDecisionRequest {
  expectedRevision: int64;
}

model DeploymentApprovalResponse {
  id: string;
  projectId: ResourceId;
  deploymentId: string;
  environment: string;
  requestDigest: string;
  releaseId: string;
  status: DeploymentApprovalStatus;
  requestedBy: string;
  requestedAt: utcDateTime;
  approvedBy?: string;
  approvedAt?: utcDateTime;
  deniedBy?: string;
  deniedAt?: utcDateTime;
  revokedBy?: string;
  revokedAt?: utcDateTime;
  expiresAt: utcDateTime;
  revision: int64;
}

model DeploymentPublishEvidence {
  releaseDigest: string;
  artifactContentDigest: string;
  artifactProvenanceDigest: string;
  planDigest: string;
  candidateId: string;
  candidateRevision: int64;
  targetId: string;
  environment: string;
  generationId: string;
  baseGenerationId?: string;
  runtimeVersion: string;
  policyDigest: string;
  sourceRevision?: CandidateSourceRevision;
}

model DeploymentVerification {
  digest: string;
  verifiedAt: utcDateTime;
}

model DeploymentResponse {
  id: string;
  projectId: ResourceId;
  releaseId: string;
  environment: string;
  generationId: string;
  artifactDigest: string;
  priorGenerationId?: string;
  requestDigest: string;
  evidence: DeploymentPublishEvidence;
  status: DeploymentStatus;
  createdBy: string;
  activationPrincipal?: string;
  verification?: DeploymentVerification;
  createdAt: utcDateTime;
  startedAt?: utcDateTime;
  finishedAt?: utcDateTime;
  error?: string;
  approval?: DeploymentApprovalResponse;
}

model DeploymentListResponse {
  items: DeploymentResponse[];
  page: PageInfo;
}

// Plan-driven delivery read models intentionally expose immutable identities
// and bounded evidence only. They do not expose connector observations,
// object-store keys/locations, credentials, or raw evidence documents.
model DeliveryPlanEvidenceView {
  digest: string;
  impactStatement?: string;
  physicalWorkStatement?: string;
  reuseStatement?: string;
  compatibilityBreaking: boolean;
  addedCount: int32;
  removedCount: int32;
  directlyModifiedCount: int32;
  indirectlyAffectedCount: int32;
  reuseCount: int32;
  qualificationStepCount: int32;
  rollbackClass?: DeliveryRollbackClass;
  plannedInputs: DeliveryPlannedInputView[];
  qualificationPolicy: string;
  qualificationSteps: DeliveryQualificationStepView[];
  stalePolicy: DeliveryStalePolicyView;
  reuseDecisions: DeliveryReuseDecisionView[];
}

model DeliveryPlannedInputView {
  id: string;
  mode: DeliveryDataInputMode;
  revision?: string;
  bound?: string;
}

model DeliveryResolvedInputView {
  id: string;
  mode: DeliveryDataInputMode;
  plannedRevision?: string;
  plannedBound?: string;
  actualRevision?: string;
  actualBound?: string;
  observationDigest?: string;
  explanation: string;
}

model DeliveryQualificationStepView {
  id: string;
  kind: string;
  description: string;
  required: boolean;
  blocking: boolean;
}

model DeliveryStalePolicyView {
  mode: DeliveryStalePolicyMode;
  allowRetainedBase: boolean;
  description?: string;
}

model DeliveryReuseDecisionView {
  resourceId: string;
  reusable: boolean;
  retainBase: boolean;
  reason: string;
  reuseKeyDigest?: string;
}

model DeliveryPlanPreviewResponse {
  id: string;
  projectId: ResourceId;
  targetId: string;
  environment: string;
  operation: DeliveryOperationKind;
  sourceDigest: string;
  sourceAttestationDigest: string;
  baseGenerationId?: string;
  baseTargetRevision: int64;
  executionDigest: string;
  provenanceDigest: string;
  governanceDigest: string;
  evidenceDigest: string;
  planDigest: string;
  status: DeliveryPlanStatus;
  expiresAt: utcDateTime;
  createdAt: utcDateTime;
  evidence: DeliveryPlanEvidenceView;
}

model DeliveryBuildStatusResponse {
  id: string;
  planId: string;
  planDigest: string;
  sourceDigest: string;
  executionDigest: string;
  baseGenerationId?: string;
  baseCatalogDigest?: string;
  basePhysicalPoolId?: string;
  physicalPoolId: string;
  writerLeaseId: string;
  status: DeliveryBuildStatus;
  sealId?: string;
  candidateId?: string;
  failureCode?: string;
  revision: int64;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
  terminalAt?: utcDateTime;
}

model DeliverySealStatusResponse {
  id: string;
  attemptId: string;
  planId: string;
  planDigest: string;
  executionDigest: string;
  physicalPoolId: string;
  catalogDigest: string;
  baseCatalogDigest?: string;
  basePhysicalPoolId?: string;
  compatibilityDigest: string;
  servingArtifactId: string;
  servingArtifactDigest: string;
  servingStateId: string;
  objectSize: int64;
  closureDigest?: string;
  qualificationDigest?: string;
  status: DeliverySealStatus;
  createdAt: utcDateTime;
  verifiedAt?: utcDateTime;
  failureCode?: string;
}

model DeliveryCandidateStatusResponse {
  id: string;
  planId: string;
  planDigest: string;
  targetId: string;
  projectId: ResourceId;
  environment: string;
  sourceDigest: string;
  executionDigest: string;
  baseGenerationId?: string;
  baseTargetRevision: int64;
  sealId: string;
  catalogDigest: string;
  baseCatalogDigest?: string;
  basePhysicalPoolId?: string;
  compatibilityDigest: string;
  physicalPoolId: string;
  servingArtifactId: string;
  servingArtifactDigest: string;
  servingStateId: string;
  qualificationDigest?: string;
  resolvedInputsDigest?: string;
  resolvedInputs: DeliveryResolvedInputView[];
  status: DeliveryCandidateStatus;
  failureCode?: string;
  createdAt: utcDateTime;
  readyAt?: utcDateTime;
  retiredAt?: utcDateTime;
}

model DeliveryGenerationStatusResponse {
  id: string;
  candidateId: string;
  planId: string;
  planDigest: string;
  targetId: string;
  projectId: ResourceId;
  environment: string;
  catalogDigest: string;
  physicalPoolId: string;
  servingArtifactId: string;
  servingArtifactDigest: string;
  servingStateId: string;
  compatibilityDigest: string;
  rollbackClass: DeliveryRollbackClass;
  status: DeliveryGenerationStatus;
  createdAt: utcDateTime;
  activatedAt?: utcDateTime;
  retiredAt?: utcDateTime;
  rollbackUntil?: utcDateTime;
}

model DeliveryPublicationEvidenceResponse {
  id: string;
  requestDigest: string;
  targetId: string;
  projectId: ResourceId;
  environment: string;
  planId: string;
  planDigest: string;
  candidateId: string;
  generationId: string;
  expectedBaseGenerationId?: string;
  expectedTargetRevision: int64;
  resultTargetRevision: int64;
  status: DeliveryPublicationStatus;
  reason?: string;
  createdAt: utcDateTime;
  completedAt?: utcDateTime;
}

model DeliveryPhysicalPoolAdmissionView {
  poolId: string;
  identityDigest: string;
  compatibilityDigest: string;
  evidenceDigest: string;
  conformanceVersion: string;
  duckdbRuntime: string;
  ducklakeExtension: string;
  catalogFormat: string;
  storageImplementation: string;
  objectNamingContract: string;
  admittedAt: utcDateTime;
}

model DeliveryRootView {
  poolId: string;
  kind: DeliveryRootKind;
  sourceId: string;
  candidateId?: string;
  generationId?: string;
  leaseId?: string;
  catalogDigest: string;
  status: DeliveryRootStatus;
  createdAt: utcDateTime;
  expiresAt?: utcDateTime;
}

model DeliveryQueryLeaseView {
  id: string;
  holderId: string;
  candidateId?: string;
  generationId?: string;
  poolId: string;
  catalogDigest: string;
  status: DeliveryLeaseStatus;
  createdAt: utcDateTime;
  expiresAt: utcDateTime;
}

model DeliveryWriterLeaseView {
  id: string;
  attemptId: string;
  poolId: string;
  ownerId: string;
  epoch: int64;
  status: DeliveryLeaseStatus;
  createdAt: utcDateTime;
  expiresAt: utcDateTime;
  releasedAt?: utcDateTime;
}

model DeliveryGCCycleView {
  id: string;
  poolId: string;
  epoch: int64;
  rootRevision: int64;
  markDigest?: string;
  status: DeliveryGCStatus;
  createdAt: utcDateTime;
  completedAt?: utcDateTime;
  abortReason?: string;
}

model DeliveryGCDeleteIntentView {
  id: string;
  cycleId: string;
  poolId: string;
  objectDigest: string;
  objectVersion?: string;
  status: DeliveryGCDeleteStatus;
  createdAt: utcDateTime;
  completedAt?: utcDateTime;
}

model DeliveryOperatorSnapshotResponse {
  projectId: ResourceId;
  environment: string;
  targetId: string;
  targetRevision: int64;
  activeGeneration?: string;
  degraded: boolean;
  degradedReasons: string[];
  physicalPools: DeliveryPhysicalPoolAdmissionView[];
  roots: DeliveryRootView[];
  queryLeases: DeliveryQueryLeaseView[];
  writerLeases: DeliveryWriterLeaseView[];
  gcCycles: DeliveryGCCycleView[];
  gcDeleteIntents: DeliveryGCDeleteIntentView[];
}

model DeliveryPlanRequest {
  targetId: string;
  operation: DeliveryOperationKind;
  sourceDigest: string;
  // Opaque target-issued source attestation selected during retention.
  sourceAttestationDigest: string;
  // Target-owned policy, bindings, qualification, and current CAS fence are
  // resolved by the server. Callers submit intent, never internal evidence.
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model DeliveryCommandAuditPayload {
  @apigen.auditInternal
  operationId: string;
  @apigen.auditInternal
  projectId: ResourceId;
  @apigen.auditInternal
  resourceId: string;
  @apigen.auditPublic
  status: DeliveryAuditStatus;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model DeploymentQueuedAuditPayload {
  @apigen.auditInternal
  deploymentId: string;
  @apigen.auditInternal
  projectId: ResourceId;
  @apigen.auditInternal
  releaseId: string;
  @apigen.auditPublic
  status: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model DeploymentCancelledAuditPayload {
  @apigen.auditInternal
  deploymentId: string;
  @apigen.auditPublic
  status: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model DeploymentApprovalRequestedAuditPayload {
  @apigen.auditInternal
  deploymentId: string;
  @apigen.auditInternal
  approvalId: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model DeploymentApprovalDecisionAuditPayload {
  @apigen.auditInternal
  deploymentId: string;
  @apigen.auditInternal
  approvalId: string;
  @apigen.auditInternal
  approvalRevision: int64;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model CandidateAuditPayload {
  @apigen.auditInternal
  operationId: string;
  @apigen.auditInternal
  candidateId: string;
  @apigen.auditInternal
  projectId: ResourceId;
  @apigen.auditInternal
  targetId: string;
  @apigen.auditInternal
  environment: string;
  @apigen.auditInternal
  baseGeneration: string;
  @apigen.auditInternal
  candidateKey: string;
  @apigen.auditPublic
  status: string;
  @apigen.auditPublic
  resumed: boolean;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model CandidateSourceBlobAuditPayload {
  @apigen.auditInternal
  operationId: string;
  @apigen.auditPublic
  surface: string;
  @apigen.auditInternal
  digest: string;
  @apigen.auditInternal
  sizeBytes: int64;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model CandidateSourceRetainedAuditPayload {
  @apigen.auditInternal
  operationId: string;
  @apigen.auditPublic
  surface: string;
  @apigen.auditInternal
  projectId: ResourceId;
  @apigen.auditInternal
  sourceDigest: string;
  @apigen.auditInternal
  sourceAttestationDigest: string;
}

@apigen.failureDefinition(#{
  kind: "approval_credential_required",
  statusCode: 401,
  code: "ACTIVATION_CREDENTIAL_REQUIRED",
  publicDetail: "A bounded activation credential is required.",
})
model ActivationCredentialRequiredFailure {}

@apigen.failureDefinition(#{
  kind: "activation_forbidden",
  statusCode: 403,
  code: "ACTIVATION_FORBIDDEN",
  publicDetail: "Deployment activation is forbidden.",
})
model ActivationForbiddenFailure {}

@apigen.failureDefinition(#{
  kind: "approval_credential_expired",
  statusCode: 401,
  code: "APPROVAL_CREDENTIAL_EXPIRED",
  publicDetail: "The deployment approval credential has expired.",
})
model ApprovalCredentialExpiredFailure {}

@apigen.failureDefinition(#{
  kind: "approval_forbidden",
  statusCode: 403,
  code: "APPROVAL_FORBIDDEN",
  publicDetail: "Deployment approval is forbidden.",
})
model ApprovalForbiddenFailure {}

@apigen.failureDefinition(#{
  kind: "audit_unavailable",
  statusCode: 503,
  code: "AUDIT_UNAVAILABLE",
  publicDetail: "Candidate source blob audit is temporarily unavailable.",
})
model AuditUnavailableFailure {}

@apigen.failureDefinition(#{
  kind: "candidate_conflict",
  statusCode: 409,
  code: "CANDIDATE_CONFLICT",
  publicDetail: "The candidate conflicts with its current state.",
})
model CandidateConflictFailure {}

@apigen.failureDefinition(#{
  kind: "candidate_not_found",
  statusCode: 404,
  code: "CANDIDATE_NOT_FOUND",
  publicDetail: "Candidate not found.",
})
model CandidateNotFoundFailure {}

@apigen.failureDefinition(#{
  kind: "candidate_quota",
  statusCode: 429,
  code: "CANDIDATE_QUOTA_EXCEEDED",
  publicDetail: "Candidate quota exceeded.",
})
model CandidateQuotaExceededFailure {}

@apigen.failureDefinition(#{
  kind: "candidate_unavailable",
  statusCode: 503,
  code: "CANDIDATE_SERVICE_UNAVAILABLE",
  publicDetail: "Candidate service is unavailable.",
})
model CandidateServiceUnavailableFailure {}

@apigen.failureDefinition(#{
  kind: "approval_conflict",
  statusCode: 409,
  code: "DEPLOYMENT_APPROVAL_CONFLICT",
  publicDetail: "The deployment approval conflicts with its current state.",
})
model DeploymentApprovalConflictFailure {}

@apigen.failureDefinition(#{
  kind: "approval_not_found",
  statusCode: 404,
  code: "DEPLOYMENT_APPROVAL_NOT_FOUND",
  publicDetail: "Deployment approval not found.",
})
model DeploymentApprovalNotFoundFailure {}

@apigen.failureDefinition(#{
  kind: "approval_unavailable",
  statusCode: 503,
  code: "DEPLOYMENT_APPROVAL_UNAVAILABLE",
  publicDetail: "Deployment approvals are unavailable.",
})
model DeploymentApprovalUnavailableFailure {}

@apigen.failureDefinition(#{
  kind: "conflict",
  statusCode: 409,
  code: "DEPLOYMENT_CONFLICT",
  publicDetail: "The deployment conflicts with its current state.",
})
model DeploymentConflictFailure {}

@apigen.failureDefinition(#{
  kind: "not_found",
  statusCode: 404,
  code: "DEPLOYMENT_NOT_FOUND",
  publicDetail: "Deployment not found.",
})
model DeploymentNotFoundFailure {}

@apigen.failureDefinition(#{
  kind: "service_unavailable",
  statusCode: 503,
  code: "DEPLOYMENT_SERVICE_UNAVAILABLE",
  publicDetail: "Deployment service is unavailable.",
})
model DeploymentServiceUnavailableFailure {}

@apigen.failureDefinition(#{
  kind: "candidate_invalid",
  statusCode: 422,
  code: "INVALID_CANDIDATE",
  publicDetail: "The candidate request is invalid.",
})
model InvalidCandidateFailure {}

@apigen.failureDefinition(#{
  kind: "source_blob_invalid",
  statusCode: 422,
  code: "INVALID_CANDIDATE_SOURCE_BLOB",
  publicDetail: "Candidate source blob headers do not match the canonical content identity.",
})
model InvalidCandidateSourceBlobFailure {}

@apigen.failureDefinition(#{
  kind: "invalid",
  statusCode: 422,
  code: "INVALID_DEPLOYMENT",
  publicDetail: "The deployment request is invalid.",
})
model InvalidDeploymentFailure {}

@apigen.failureDefinition(#{
  kind: "publication_forbidden",
  statusCode: 403,
  code: "PUBLICATION_MANAGEMENT_REQUIRED",
  publicDetail: "PROJECT_ADMIN is required to activate this publication.",
})
model PublicationManagementRequiredFailure {}

@apigen.failureDefinition(#{
  kind: "release_incomplete",
  statusCode: 409,
  code: "RELEASE_INCOMPLETE",
  publicDetail: "The release is missing its project-generation artifact.",
})
model ReleaseIncompleteFailure {}

@apigen.failureDefinition(#{
  kind: "release_not_ready",
  statusCode: 409,
  code: "RELEASE_NOT_READY",
  publicDetail: "Only ready releases can be deployed.",
})
model ReleaseNotReadyFailure {}

@apigen.failureDefinition(#{
  kind: "separation_of_duty",
  statusCode: 409,
  code: "SEPARATION_OF_DUTY_REQUIRED",
  publicDetail: "A separate principal must approve this deployment.",
})
model SeparationOfDutyRequiredFailure {}

@route("/projects/{project}/deployments")
@tag("Deployments")
@apigen.commandDefaults(#{ guarantee: "best-effort" })
interface Deployments {
  @summary("List project deployments")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project")
  listDeployments(@path project: ResourceId, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<DeploymentListResponse> | CommonErrors;

  @summary("Deploy a ready project release")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_PUBLISH" })
  @extension("x-leapview-object-scope", "project")
  @apigen.asyncExecution(Deployments.getDeployment, Deployments.listDeploymentEvents, #{
    guarantee: "transactional",
    jobKind: "deployment.activate",
    resourceKind: "deployment",
    initialEvent: "deployment.queued",
    initialState: "queued",
    cancellation: "supported",
  })
  @apigen.failsWith(DeploymentNotFoundFailure)
  @apigen.failsWith(DeploymentConflictFailure)
  @apigen.failsWith(ReleaseNotReadyFailure)
  @apigen.failsWith(ReleaseIncompleteFailure)
  @apigen.failsWith(PublicationManagementRequiredFailure)
  @apigen.failsWith(DeploymentServiceUnavailableFailure)
  @apigen.failsWith(DeploymentApprovalUnavailableFailure)
  @apigen.failsWith(ApprovalCredentialExpiredFailure)
  @apigen.failsWith(DeploymentApprovalConflictFailure)
  @apigen.failsWith(InvalidDeploymentFailure)
  @apigen.command(#{
    auditAction: "deployment.queued",
    failures: #[
      #{ kind: "authorization_unavailable", statusCode: 503, code: "AUTHORIZATION_UNAVAILABLE", publicDetail: "Publication activation authorization could not be evaluated." },
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded publication credential is required." },
      #{ kind: "approval_invalid", statusCode: 422, code: "INVALID_DEPLOYMENT_APPROVAL", publicDetail: "The deployment approval request is invalid." },
    ],
  })
  @apigen.auditPayload(DeploymentQueuedAuditPayload)
  createDeployment(@path project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DeploymentCreateRequest): AcceptedJson<DeploymentResponse> | CommonErrors;

  @summary("Get a project deployment")
  @route("/{deployment}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project")
  getDeployment(@path project: ResourceId, @path deployment: string): OkJson<DeploymentResponse> | CommonErrors;

  @summary("List or stream deployment events")
  @route("/{deployment}/events")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project")
  listDeploymentEvents(@path project: ResourceId, @path deployment: string, @header accept?: string, @header("Last-Event-ID") lastEventId?: string, @query limit?: ListLimit, @query pageToken?: PageToken): EventHistoryResponse<AsyncEventListResponse> | CommonErrors;

  @summary("Cancel a deployment before cutover")
  @route("/{deployment}/cancel")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_PUBLISH" })
  @extension("x-leapview-object-scope", "project")
  @apigen.failsWith(DeploymentNotFoundFailure)
  @apigen.failsWith(DeploymentConflictFailure)
  @apigen.failsWith(DeploymentServiceUnavailableFailure)
  @apigen.failsWith(InvalidDeploymentFailure)
  @apigen.command(#{
    auditAction: "deployment.cancelled",
    failures: #[
      #{ kind: "queue_unavailable", statusCode: 503, code: "ASYNC_QUEUE_UNAVAILABLE", publicDetail: "Deployment cancellation could not be persisted." },
    ],
  })
  @apigen.auditPayload(DeploymentCancelledAuditPayload)
  cancelDeployment(@path @apigen.target project: ResourceId, @path deployment: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<DeploymentResponse> | CommonErrors;

  @summary("Retry a terminal publication request without changing its immutable release")
  @route("/{deployment}/retry")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_PUBLISH" })
  @extension("x-leapview-object-scope", "project")
  @apigen.asyncExecution(Deployments.getDeployment, Deployments.listDeploymentEvents, #{
    guarantee: "transactional",
    jobKind: "deployment.activate",
    resourceKind: "deployment",
    initialEvent: "deployment.queued",
    initialState: "queued",
    cancellation: "supported",
  })
  @apigen.failsWith(DeploymentNotFoundFailure)
  @apigen.failsWith(DeploymentConflictFailure)
  @apigen.failsWith(ReleaseNotReadyFailure)
  @apigen.failsWith(ReleaseIncompleteFailure)
  @apigen.failsWith(PublicationManagementRequiredFailure)
  @apigen.failsWith(DeploymentServiceUnavailableFailure)
  @apigen.failsWith(InvalidDeploymentFailure)
  @apigen.command(#{
    auditAction: "deployment.queued",
    failures: #[
      #{ kind: "authorization_unavailable", statusCode: 503, code: "AUTHORIZATION_UNAVAILABLE", publicDetail: "Publication activation authorization could not be evaluated." },
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded publication credential is required." },
    ],
  })
  @apigen.auditPayload(DeploymentQueuedAuditPayload)
  retryDeployment(@path @apigen.target project: ResourceId, @path deployment: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<DeploymentResponse> | CommonErrors;

  @summary("Roll back by creating a deployment of the prior release")
  @route("/{deployment}/rollback")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.asyncExecution(Deployments.getDeployment, Deployments.listDeploymentEvents, #{
    guarantee: "transactional",
    jobKind: "deployment.activate",
    resourceKind: "deployment",
    initialEvent: "deployment.queued",
    initialState: "queued",
    cancellation: "supported",
  })
  @apigen.failsWith(DeploymentNotFoundFailure)
  @apigen.failsWith(DeploymentConflictFailure)
  @apigen.failsWith(ReleaseNotReadyFailure)
  @apigen.failsWith(ReleaseIncompleteFailure)
  @apigen.failsWith(PublicationManagementRequiredFailure)
  @apigen.failsWith(DeploymentServiceUnavailableFailure)
  @apigen.failsWith(InvalidDeploymentFailure)
  @apigen.command(#{
    auditAction: "deployment.queued",
    failures: #[
      #{ kind: "authorization_unavailable", statusCode: 503, code: "AUTHORIZATION_UNAVAILABLE", publicDetail: "Publication activation authorization could not be evaluated." },
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded publication credential is required." },
    ],
  })
  @apigen.auditPayload(DeploymentQueuedAuditPayload)
  rollbackDeployment(@path @apigen.target project: ResourceId, @path deployment: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<DeploymentResponse> | CommonErrors;

  @summary("Request approval for an immutable deployment plan")
  @route("/{deployment}/approval-requests")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_PUBLISH" })
  @extension("x-leapview-object-scope", "project")
  @apigen.failsWith(DeploymentApprovalUnavailableFailure)
  @apigen.failsWith(DeploymentApprovalNotFoundFailure)
  @apigen.failsWith(ApprovalCredentialExpiredFailure)
  @apigen.failsWith(DeploymentConflictFailure)
  @apigen.failsWith(DeploymentNotFoundFailure)
  @apigen.failsWith(InvalidDeploymentFailure)
  @apigen.command(#{
    auditAction: "deployment.approval_requested",
    failures: #[
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded publication credential is required." },
      #{ kind: "approval_conflict", statusCode: 409, code: "DEPLOYMENT_APPROVAL_REQUIRED", publicDetail: "The deployment approval is not valid for this action." },
    ],
  })
  @apigen.auditPayload(DeploymentApprovalRequestedAuditPayload)
  requestDeploymentApproval(@path @apigen.target project: ResourceId, @path deployment: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): CreatedJson<DeploymentApprovalResponse> | CommonErrors;

  @summary("Approve an immutable deployment plan")
  @route("/{deployment}/approval-requests/{approval}/approve")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.failsWith(DeploymentApprovalUnavailableFailure)
  @apigen.failsWith(DeploymentApprovalNotFoundFailure)
  @apigen.failsWith(ApprovalCredentialExpiredFailure)
  @apigen.failsWith(SeparationOfDutyRequiredFailure)
  @apigen.failsWith(DeploymentNotFoundFailure)
  @apigen.command(#{
    auditAction: "deployment.approved",
    failures: #[
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded approval credential is required." },
      #{ kind: "approval_conflict", statusCode: 409, code: "DEPLOYMENT_APPROVAL_REQUIRED", publicDetail: "The deployment approval is not valid for this action." },
      #{ kind: "approval_invalid", statusCode: 422, code: "INVALID_DEPLOYMENT_APPROVAL", publicDetail: "The deployment approval request is invalid." },
    ],
  })
  @apigen.auditPayload(DeploymentApprovalDecisionAuditPayload)
  approveDeployment(@path @apigen.target project: ResourceId, @path deployment: string, @path approval: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DeploymentApprovalDecisionRequest): OkJson<DeploymentApprovalResponse> | CommonErrors;

  @summary("Deny an immutable deployment plan")
  @route("/{deployment}/approval-requests/{approval}/deny")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.failsWith(DeploymentApprovalUnavailableFailure)
  @apigen.failsWith(DeploymentApprovalNotFoundFailure)
  @apigen.failsWith(ApprovalCredentialExpiredFailure)
  @apigen.failsWith(SeparationOfDutyRequiredFailure)
  @apigen.failsWith(DeploymentNotFoundFailure)
  @apigen.command(#{
    auditAction: "deployment.denied",
    failures: #[
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded approval credential is required." },
      #{ kind: "approval_conflict", statusCode: 409, code: "DEPLOYMENT_APPROVAL_REQUIRED", publicDetail: "The deployment approval is not valid for this action." },
      #{ kind: "approval_invalid", statusCode: 422, code: "INVALID_DEPLOYMENT_APPROVAL", publicDetail: "The deployment approval request is invalid." },
    ],
  })
  @apigen.auditPayload(DeploymentApprovalDecisionAuditPayload)
  denyDeploymentApproval(@path @apigen.target project: ResourceId, @path deployment: string, @path approval: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DeploymentApprovalDecisionRequest): OkJson<DeploymentApprovalResponse> | CommonErrors;

  @summary("Revoke a pending or approved deployment decision")
  @route("/{deployment}/approval-requests/{approval}/revoke")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.failsWith(DeploymentApprovalUnavailableFailure)
  @apigen.failsWith(DeploymentApprovalNotFoundFailure)
  @apigen.failsWith(ApprovalCredentialExpiredFailure)
  @apigen.failsWith(SeparationOfDutyRequiredFailure)
  @apigen.failsWith(DeploymentNotFoundFailure)
  @apigen.command(#{
    auditAction: "deployment.approval_revoked",
    failures: #[
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded approval credential is required." },
      #{ kind: "approval_conflict", statusCode: 409, code: "DEPLOYMENT_APPROVAL_REQUIRED", publicDetail: "The deployment approval is not valid for this action." },
      #{ kind: "approval_invalid", statusCode: 422, code: "INVALID_DEPLOYMENT_APPROVAL", publicDetail: "The deployment approval request is invalid." },
    ],
  })
  @apigen.auditPayload(DeploymentApprovalDecisionAuditPayload)
  revokeDeploymentApproval(@path @apigen.target project: ResourceId, @path deployment: string, @path approval: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DeploymentApprovalDecisionRequest): OkJson<DeploymentApprovalResponse> | CommonErrors;

  @summary("Activate an approved immutable deployment plan")
  @route("/{deployment}/activate")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.asyncExecution(Deployments.getDeployment, Deployments.listDeploymentEvents, #{
    guarantee: "transactional",
    jobKind: "deployment.activate",
    resourceKind: "deployment",
    initialEvent: "deployment.activation_requested",
    initialState: "queued",
    cancellation: "supported",
  })
  @apigen.failsWith(ActivationCredentialRequiredFailure)
  @apigen.failsWith(DeploymentApprovalUnavailableFailure)
  @apigen.failsWith(DeploymentApprovalNotFoundFailure)
  @apigen.failsWith(ApprovalCredentialExpiredFailure)
  @apigen.failsWith(ActivationForbiddenFailure)
  @apigen.failsWith(ApprovalForbiddenFailure)
  @apigen.failsWith(DeploymentNotFoundFailure)
  @apigen.failsWith(DeploymentConflictFailure)
  @apigen.command(#{
    auditAction: "deployment.activation_requested",
    failures: #[
      #{ kind: "approval_conflict", statusCode: 409, code: "DEPLOYMENT_APPROVAL_REQUIRED", publicDetail: "An approved deployment is required for activation." },
      #{ kind: "approval_invalid", statusCode: 422, code: "INVALID_DEPLOYMENT_APPROVAL", publicDetail: "The deployment approval is invalid." },
      #{ kind: "authorization_unavailable", statusCode: 503, code: "AUTHORIZATION_UNAVAILABLE", publicDetail: "Deployment activation authorization could not be evaluated." },
      #{ kind: "queue_unavailable", statusCode: 503, code: "ASYNC_QUEUE_UNAVAILABLE", publicDetail: "Deployment activation could not be persisted." },
    ],
  })
  @apigen.auditPayload(DeploymentQueuedAuditPayload)
  activateDeployment(@path @apigen.target project: ResourceId, @path deployment: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<DeploymentResponse> | CommonErrors;
}

@route("/projects/{project}/candidates")
@tag("Deployments")
@apigen.commandDefaults(#{ guarantee: "best-effort" })
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_EDIT" })
interface ProjectCandidates {
  @summary("Create or resume the current author's private project candidate")
  @post
  @extension("x-leapview-object-scope", "project")
  @apigen.failsWith(CandidateServiceUnavailableFailure)
  @apigen.failsWith(CandidateNotFoundFailure)
  @apigen.failsWith(CandidateConflictFailure)
  @apigen.failsWith(CandidateQuotaExceededFailure)
  @apigen.failsWith(InvalidCandidateFailure)
  @apigen.command(#{
    auditAction: "candidate.started",
  })
  @apigen.auditPayload(CandidateAuditPayload)
  startProjectCandidate(@path project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: CandidateStartRequest): CreatedJson<CandidateResponse> | CommonErrors;

  @summary("Get an owned private project candidate")
  @route("/{candidate}")
  @get
  @extension("x-leapview-object-scope", "project")
  getProjectCandidate(@path project: ResourceId, @path candidate: string): OkJson<CandidateResponse> | CommonErrors;

  @summary("Replace a private candidate artifact with optimistic concurrency")
  @route("/{candidate}/artifact")
  @put
  @extension("x-leapview-object-scope", "project")
  @apigen.failsWith(CandidateServiceUnavailableFailure)
  @apigen.failsWith(CandidateNotFoundFailure)
  @apigen.failsWith(CandidateConflictFailure)
  @apigen.failsWith(InvalidCandidateFailure)
  @apigen.command(#{
    auditAction: "candidate.artifact_replaced",
  })
  @apigen.auditPayload(CandidateAuditPayload)
  replaceProjectCandidateArtifact(@path @apigen.target project: ResourceId, @path candidate: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: CandidateArtifactRequest): OkJson<CandidateResponse> | CommonErrors;

  @summary("Retry preparation of a failed private candidate")
  @route("/{candidate}/retry")
  @post
  @extension("x-leapview-object-scope", "project")
  @apigen.failsWith(CandidateServiceUnavailableFailure)
  @apigen.failsWith(CandidateNotFoundFailure)
  @apigen.failsWith(CandidateConflictFailure)
  @apigen.failsWith(InvalidCandidateFailure)
  @apigen.command(#{
    auditAction: "candidate.retried",
  })
  @apigen.auditPayload(CandidateAuditPayload)
  retryProjectCandidate(@path @apigen.target project: ResourceId, @path candidate: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<CandidateResponse> | CommonErrors;

  @summary("Cancel an owned private project candidate")
  @route("/{candidate}/cancel")
  @post
  @extension("x-leapview-object-scope", "project")
  @apigen.failsWith(CandidateServiceUnavailableFailure)
  @apigen.failsWith(CandidateNotFoundFailure)
  @apigen.failsWith(CandidateConflictFailure)
  @apigen.failsWith(InvalidCandidateFailure)
  @apigen.command(#{
    auditAction: "candidate.cancelled",
  })
  @apigen.auditPayload(CandidateAuditPayload)
  cancelProjectCandidate(@path @apigen.target project: ResourceId, @path candidate: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<CandidateResponse> | CommonErrors;

  @summary("Publish the exact ready candidate through target policy")
  @route("/{candidate}/publish")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_PUBLISH" })
  @extension("x-leapview-object-scope", "project")
  @apigen.asyncExecution(Deployments.getDeployment, Deployments.listDeploymentEvents, #{
    guarantee: "transactional",
    jobKind: "deployment.activate",
    resourceKind: "deployment",
    initialEvent: "deployment.queued",
    initialState: "queued",
    cancellation: "supported",
  })
  @apigen.failsWith(CandidateServiceUnavailableFailure)
  @apigen.failsWith(CandidateNotFoundFailure)
  @apigen.failsWith(CandidateConflictFailure)
  @apigen.failsWith(InvalidCandidateFailure)
  @apigen.failsWith(DeploymentNotFoundFailure)
  @apigen.failsWith(DeploymentConflictFailure)
  @apigen.failsWith(ReleaseNotReadyFailure)
  @apigen.failsWith(ReleaseIncompleteFailure)
  @apigen.failsWith(PublicationManagementRequiredFailure)
  @apigen.failsWith(DeploymentServiceUnavailableFailure)
  @apigen.failsWith(InvalidDeploymentFailure)
  @apigen.command(#{
    auditAction: "deployment.queued",
    failures: #[
      #{ kind: "authorization_unavailable", statusCode: 503, code: "AUTHORIZATION_UNAVAILABLE", publicDetail: "Publication activation authorization could not be evaluated." },
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded publication credential is required." },
    ],
  })
  @apigen.auditPayload(DeploymentQueuedAuditPayload)
  publishProjectCandidate(@path @apigen.target project: ResourceId, @path candidate: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: CandidatePublishRequest): AcceptedJson<DeploymentResponse> | CommonErrors;
}

@tag("Deployments")
@summary("Review a private candidate without acquiring its data credentials")
@route("/projects/{project}/candidate-reviews/{candidate}")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_EDIT" })
@extension("x-leapview-object-scope", "project")
op reviewProjectCandidate(@path project: ResourceId, @path candidate: string): OkJson<CandidateResponse> | CommonErrors;

@tag("Deployments")
@summary("Cancel the current principal's active candidate by stable authoring key")
@route("/projects/{project}/candidate-sessions/{candidateKey}/cancel")
@post
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_EDIT" })
@extension("x-leapview-object-scope", "project")
@apigen.failsWith(CandidateServiceUnavailableFailure)
@apigen.failsWith(CandidateNotFoundFailure)
@apigen.failsWith(CandidateConflictFailure)
@apigen.failsWith(InvalidCandidateFailure)
@apigen.command(#{
  auditAction: "candidate.cancelled",
  guarantee: "best-effort",
})
@apigen.auditPayload(CandidateAuditPayload)
op cancelProjectCandidateByKey(@path @apigen.target project: ResourceId, @path candidateKey: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<CandidateResponse> | CommonErrors;

@route("/projects/{project}/candidate-sync")
@tag("Deployments")
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_EDIT" })
@apigen.commandDefaults(#{ guarantee: "best-effort" })
interface CandidateSynchronization {
  @summary("Plan content-addressed synchronization for the current author's private candidate")
  @doc("Query classification: reports missing immutable blobs without uploading or committing source state.")
  @route("/plan")
  @post
  @extension("x-leapview-object-scope", "project")
  @apigen.query
  planProjectCandidateSynchronization(
    @path project: ResourceId,
    @header("Idempotency-Key") idempotencyKey: IdempotencyKey,
    @body body: CandidateSynchronizationRequest,
  ): OkJson<CandidateSynchronizationPlanResponse> | CommonErrors;

  @summary("Retain an exact source snapshot without preparing a candidate")
  @doc("This source-only operation stores immutable bytes for a later target-owned delivery plan. It must not create a candidate, acquire physical credentials, or perform catalog work.")
  @route("/source")
  @post
  @extension("x-leapview-object-scope", "project")
  @apigen.failsWith(CandidateServiceUnavailableFailure)
  @apigen.failsWith(CandidateConflictFailure)
  @apigen.failsWith(InvalidCandidateFailure)
  @apigen.command(#{ auditAction: "candidate.source_retained" })
  @apigen.auditPayload(CandidateSourceRetainedAuditPayload)
  retainProjectCandidateSource(@path @apigen.target project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: CandidateSynchronizationRequest): CreatedJson<CandidateSourceSnapshotResponse> | CommonErrors;

  @summary("Upload a missing immutable private-candidate source blob")
  @route("/blobs/{digest}")
  @put
  @extension("x-leapview-object-scope", "project")
  @apigen.failsWith(CandidateServiceUnavailableFailure)
  @apigen.failsWith(CandidateConflictFailure)
  @apigen.failsWith(InvalidCandidateFailure)
  @apigen.failsWith(InvalidCandidateSourceBlobFailure)
  @apigen.failsWith(AuditUnavailableFailure)
  @apigen.command(#{
    auditAction: "candidate.source_blob_uploaded",
  })
  @apigen.auditPayload(CandidateSourceBlobAuditPayload)
  @apigen.manual
  uploadProjectCandidateSourceBlob(@path @apigen.target project: ResourceId, @path digest: string, @header contentType: "application/octet-stream", @header("Content-Digest") contentDigest: string, @body body: bytes): CreatedJson<CandidateSourceBlobResponse> | CommonErrors;

  @summary("Atomically commit a complete source snapshot to the current author's private candidate")
  @route("/commit")
  @post
  @extension("x-leapview-object-scope", "project")
  @apigen.failsWith(CandidateServiceUnavailableFailure)
  @apigen.failsWith(CandidateNotFoundFailure)
  @apigen.failsWith(CandidateConflictFailure)
  @apigen.failsWith(InvalidCandidateFailure)
  @apigen.command(#{
    auditAction: "candidate.ready",
  })
  @apigen.auditPayload(CandidateAuditPayload)
  commitProjectCandidateSynchronization(@path project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: CandidateSynchronizationRequest): OkJson<CandidateResponse> | CommonErrors;
}

@apigen.failureDefinition(#{
  kind: "delivery_read_not_found",
  statusCode: 404,
  code: "DELIVERY_OBJECT_NOT_FOUND",
  publicDetail: "The requested delivery object was not found.",
})
model DeliveryObjectNotFoundFailure {}

@apigen.failureDefinition(#{
  kind: "delivery_read_unavailable",
  statusCode: 503,
  code: "DELIVERY_READ_UNAVAILABLE",
  publicDetail: "Delivery status is temporarily unavailable.",
})
model DeliveryReadUnavailableFailure {}

@apigen.failureDefinition(#{
  kind: "delivery_read_invalid",
  statusCode: 422,
  code: "INVALID_DELIVERY_READ",
  publicDetail: "The delivery status request is invalid.",
})
model DeliveryReadInvalidFailure {}

@apigen.failureDefinition(#{
  kind: "delivery_mutation_conflict",
  statusCode: 409,
  code: "DELIVERY_CONFLICT",
  publicDetail: "The delivery operation conflicts with current target state.",
})
model DeliveryMutationConflictFailure {}

@apigen.failureDefinition(#{
  kind: "delivery_mutation_stale",
  statusCode: 409,
  code: "DELIVERY_STALE",
  publicDetail: "The delivery plan or target fence is stale.",
})
model DeliveryMutationStaleFailure {}

@apigen.failureDefinition(#{
  kind: "delivery_plan_expired",
  statusCode: 409,
  code: "DELIVERY_PLAN_EXPIRED",
  publicDetail: "The immutable delivery plan has expired.",
})
model DeliveryPlanExpiredFailure {}

@apigen.failureDefinition(#{
  kind: "delivery_mutation_forbidden",
  statusCode: 403,
  code: "DELIVERY_FORBIDDEN",
  publicDetail: "The caller is not authorized for this delivery operation.",
})
model DeliveryMutationForbiddenFailure {}

@apigen.failureDefinition(#{
  kind: "delivery_input_unavailable",
  statusCode: 503,
  code: "DELIVERY_INPUT_UNAVAILABLE",
  publicDetail: "Target-owned delivery inputs are temporarily unavailable.",
})
model DeliveryInputUnavailableFailure {}

@apigen.failureDefinition(#{
  kind: "delivery_idempotency_drift",
  statusCode: 409,
  code: "DELIVERY_IDEMPOTENCY_DRIFT",
  publicDetail: "The idempotency key was reused with different delivery inputs.",
})
model DeliveryIdempotencyDriftFailure {}

@apigen.failureDefinition(#{
  kind: "delivery_mutation_invalid",
  statusCode: 422,
  code: "INVALID_DELIVERY_MUTATION",
  publicDetail: "The delivery mutation request is invalid.",
})
model DeliveryMutationInvalidFailure {}

// These queries are the typed control-plane surface for plan review and
// operator diagnosis. Every response is an authorized, redacted projection;
// no endpoint returns credentials, object keys/locations, or raw observations.
@route("/projects/{project}/delivery")
@tag("Delivery")
interface DeliveryStatus {
  @summary("Create an immutable target-owned delivery plan")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project")
  @apigen.command(#{ auditAction: "delivery.plan.created", guarantee: "transactional" })
  @apigen.auditPayload(DeliveryCommandAuditPayload)
  @apigen.failsWith(DeliveryReadUnavailableFailure)
  @apigen.failsWith(DeliveryReadInvalidFailure)
  @apigen.failsWith(DeliveryMutationConflictFailure)
  @apigen.failsWith(DeliveryMutationStaleFailure)
  @apigen.failsWith(DeliveryPlanExpiredFailure)
  @apigen.failsWith(DeliveryMutationForbiddenFailure)
  @apigen.failsWith(DeliveryInputUnavailableFailure)
  @apigen.failsWith(DeliveryIdempotencyDriftFailure)
  @apigen.failsWith(DeliveryMutationInvalidFailure)
  createDeliveryPlan(@path @apigen.target project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DeliveryPlanRequest): CreatedJson<DeliveryPlanPreviewResponse> | CommonErrors;

  @summary("Build and seal one immutable delivery plan")
  @route("/plans/{plan}/build")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_USE" })
  @extension("x-leapview-object-scope", "project")
  @apigen.command(#{ auditAction: "delivery.build.sealed", guarantee: "transactional" })
  @apigen.auditPayload(DeliveryCommandAuditPayload)
  @apigen.failsWith(DeliveryObjectNotFoundFailure)
  @apigen.failsWith(DeliveryReadUnavailableFailure)
  @apigen.failsWith(DeliveryReadInvalidFailure)
  @apigen.failsWith(DeliveryMutationConflictFailure)
  @apigen.failsWith(DeliveryMutationStaleFailure)
  @apigen.failsWith(DeliveryPlanExpiredFailure)
  @apigen.failsWith(DeliveryMutationForbiddenFailure)
  @apigen.failsWith(DeliveryInputUnavailableFailure)
  @apigen.failsWith(DeliveryIdempotencyDriftFailure)
  @apigen.failsWith(DeliveryMutationInvalidFailure)
  buildDeliveryPlan(@path @apigen.target project: ResourceId, @path plan: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<DeliveryBuildStatusResponse> | CommonErrors;

  @summary("Publish a sealed candidate with the target CAS fence")
  @route("/candidates/{candidate}/publish")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_PUBLISH" })
  @extension("x-leapview-object-scope", "project")
  @apigen.command(#{ auditAction: "delivery.publication.requested", guarantee: "transactional" })
  @apigen.auditPayload(DeliveryCommandAuditPayload)
  @apigen.failsWith(DeliveryObjectNotFoundFailure)
  @apigen.failsWith(DeliveryReadUnavailableFailure)
  @apigen.failsWith(DeliveryReadInvalidFailure)
  @apigen.failsWith(DeliveryMutationConflictFailure)
  @apigen.failsWith(DeliveryMutationStaleFailure)
  @apigen.failsWith(DeliveryPlanExpiredFailure)
  @apigen.failsWith(DeliveryMutationForbiddenFailure)
  @apigen.failsWith(DeliveryInputUnavailableFailure)
  @apigen.failsWith(DeliveryIdempotencyDriftFailure)
  @apigen.failsWith(DeliveryMutationInvalidFailure)
  publishDeliveryCandidate(@path @apigen.target project: ResourceId, @path candidate: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<DeliveryPublicationEvidenceResponse> | CommonErrors;

  @summary("Request approval for a pending canonical delivery publication")
  @route("/publications/{publication}/approval-requests")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_PUBLISH" })
  @extension("x-leapview-object-scope", "project")
  @apigen.command(#{
    auditAction: "delivery.publication.approval_requested",
    guarantee: "transactional",
  })
  @apigen.auditPayload(DeploymentApprovalRequestedAuditPayload)
  @apigen.failsWith(DeploymentApprovalUnavailableFailure)
  @apigen.failsWith(DeploymentApprovalNotFoundFailure)
  @apigen.failsWith(ApprovalCredentialExpiredFailure)
  @apigen.failsWith(DeploymentApprovalConflictFailure)
  @apigen.failsWith(SeparationOfDutyRequiredFailure)
  requestDeliveryPublicationApproval(@path @apigen.target project: ResourceId, @path publication: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): CreatedJson<DeploymentApprovalResponse> | CommonErrors;

  @summary("Approve a canonical delivery publication")
  @route("/publications/{publication}/approval-requests/{approval}/approve")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.command(#{
    auditAction: "delivery.publication.approved",
    guarantee: "transactional",
  })
  @apigen.auditPayload(DeploymentApprovalDecisionAuditPayload)
  @apigen.failsWith(DeploymentApprovalUnavailableFailure)
  @apigen.failsWith(DeploymentApprovalNotFoundFailure)
  @apigen.failsWith(ApprovalCredentialExpiredFailure)
  @apigen.failsWith(DeploymentApprovalConflictFailure)
  @apigen.failsWith(SeparationOfDutyRequiredFailure)
  approveDeliveryPublicationApproval(@path @apigen.target project: ResourceId, @path publication: string, @path approval: string, @body body: DeploymentApprovalDecisionRequest, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<DeploymentApprovalResponse> | CommonErrors;

  @summary("Deny a canonical delivery publication")
  @route("/publications/{publication}/approval-requests/{approval}/deny")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.command(#{
    auditAction: "delivery.publication.denied",
    guarantee: "transactional",
  })
  @apigen.auditPayload(DeploymentApprovalDecisionAuditPayload)
  @apigen.failsWith(DeploymentApprovalUnavailableFailure)
  @apigen.failsWith(DeploymentApprovalNotFoundFailure)
  @apigen.failsWith(ApprovalCredentialExpiredFailure)
  @apigen.failsWith(DeploymentApprovalConflictFailure)
  @apigen.failsWith(SeparationOfDutyRequiredFailure)
  denyDeliveryPublicationApproval(@path @apigen.target project: ResourceId, @path publication: string, @path approval: string, @body body: DeploymentApprovalDecisionRequest, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<DeploymentApprovalResponse> | CommonErrors;

  @summary("Revoke a canonical delivery publication approval")
  @route("/publications/{publication}/approval-requests/{approval}/revoke")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.command(#{
    auditAction: "delivery.publication.approval_revoked",
    guarantee: "transactional",
  })
  @apigen.auditPayload(DeploymentApprovalDecisionAuditPayload)
  @apigen.failsWith(DeploymentApprovalUnavailableFailure)
  @apigen.failsWith(DeploymentApprovalNotFoundFailure)
  @apigen.failsWith(ApprovalCredentialExpiredFailure)
  @apigen.failsWith(DeploymentApprovalConflictFailure)
  revokeDeliveryPublicationApproval(@path @apigen.target project: ResourceId, @path publication: string, @path approval: string, @body body: DeploymentApprovalDecisionRequest, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<DeploymentApprovalResponse> | CommonErrors;

  @summary("Get canonical delivery publication approval")
  @route("/publications/{publication}/approval-requests/{approval}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project")
  getDeliveryPublicationApproval(@path @apigen.target project: ResourceId, @path publication: string, @path approval: string): OkJson<DeploymentApprovalResponse> | CommonErrors;

  @summary("Rollback by selecting a retained generation with the target CAS fence")
  @route("/generations/{generation}/rollback")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.command(#{ auditAction: "delivery.rollback.requested", guarantee: "transactional" })
  @apigen.auditPayload(DeliveryCommandAuditPayload)
  @apigen.failsWith(DeliveryObjectNotFoundFailure)
  @apigen.failsWith(DeliveryReadUnavailableFailure)
  @apigen.failsWith(DeliveryReadInvalidFailure)
  @apigen.failsWith(DeliveryMutationConflictFailure)
  @apigen.failsWith(DeliveryMutationStaleFailure)
  @apigen.failsWith(DeliveryPlanExpiredFailure)
  @apigen.failsWith(DeliveryMutationForbiddenFailure)
  @apigen.failsWith(DeliveryInputUnavailableFailure)
  @apigen.failsWith(DeliveryIdempotencyDriftFailure)
  @apigen.failsWith(DeliveryMutationInvalidFailure)
  rollbackDeliveryGeneration(@path @apigen.target project: ResourceId, @path generation: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<DeliveryPublicationEvidenceResponse> | CommonErrors;

  @summary("Preview one immutable delivery plan and its durable evidence")
  @route("/plans/{plan}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project")
  @extension("x-leapview-audit-action", "delivery.plan.read")
  @apigen.failsWith(DeliveryObjectNotFoundFailure)
  @apigen.failsWith(DeliveryReadUnavailableFailure)
  @apigen.failsWith(DeliveryReadInvalidFailure)
  getDeliveryPlanPreview(@path project: ResourceId, @path plan: string): OkJson<DeliveryPlanPreviewResponse> | CommonErrors;

  @summary("Get build-attempt status and exact sealed identities")
  @route("/builds/{build}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project")
  @extension("x-leapview-audit-action", "delivery.build.read")
  @apigen.failsWith(DeliveryObjectNotFoundFailure)
  @apigen.failsWith(DeliveryReadUnavailableFailure)
  @apigen.failsWith(DeliveryReadInvalidFailure)
  getDeliveryBuildStatus(@path project: ResourceId, @path build: string): OkJson<DeliveryBuildStatusResponse> | CommonErrors;

  @summary("Get catalog-seal verification status and evidence digests")
  @route("/seals/{seal}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project")
  @extension("x-leapview-audit-action", "delivery.seal.read")
  @apigen.failsWith(DeliveryObjectNotFoundFailure)
  @apigen.failsWith(DeliveryReadUnavailableFailure)
  @apigen.failsWith(DeliveryReadInvalidFailure)
  getDeliverySealStatus(@path project: ResourceId, @path seal: string): OkJson<DeliverySealStatusResponse> | CommonErrors;

  @summary("Get candidate status and exact serving-artifact identities")
  @route("/candidates/{candidate}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project")
  @extension("x-leapview-audit-action", "delivery.candidate.read")
  @apigen.failsWith(DeliveryObjectNotFoundFailure)
  @apigen.failsWith(DeliveryReadUnavailableFailure)
  @apigen.failsWith(DeliveryReadInvalidFailure)
  getDeliveryCandidateStatus(@path project: ResourceId, @path candidate: string): OkJson<DeliveryCandidateStatusResponse> | CommonErrors;

  @summary("Get generation status and rollback evidence")
  @route("/generations/{generation}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project")
  @extension("x-leapview-audit-action", "delivery.generation.read")
  @apigen.failsWith(DeliveryObjectNotFoundFailure)
  @apigen.failsWith(DeliveryReadUnavailableFailure)
  @apigen.failsWith(DeliveryReadInvalidFailure)
  getDeliveryGenerationStatus(@path project: ResourceId, @path generation: string): OkJson<DeliveryGenerationStatusResponse> | CommonErrors;

  @summary("Get publication or rollback CAS result evidence")
  @route("/publications/{publication}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project")
  @extension("x-leapview-audit-action", "delivery.publication.read")
  @apigen.failsWith(DeliveryObjectNotFoundFailure)
  @apigen.failsWith(DeliveryReadUnavailableFailure)
  @apigen.failsWith(DeliveryReadInvalidFailure)
  getDeliveryPublicationEvidence(@path project: ResourceId, @path publication: string): OkJson<DeliveryPublicationEvidenceResponse> | CommonErrors;

  @summary("Inspect physical-pool admissions, roots, leases, GC, and degraded state")
  @route("/operator")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @extension("x-leapview-audit-action", "delivery.operator.read")
  @apigen.failsWith(DeliveryObjectNotFoundFailure)
  @apigen.failsWith(DeliveryReadUnavailableFailure)
  @apigen.failsWith(DeliveryReadInvalidFailure)
  getDeliveryOperatorSnapshot(@path project: ResourceId): OkJson<DeliveryOperatorSnapshotResponse> | CommonErrors;
}
