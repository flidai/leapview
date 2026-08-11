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

model CandidateSourceBlobResponse {
  digest: string;
  sizeBytes: int64;
}

model CandidateResponse {
  id: string;
  projectId: string;
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
  projectId: string;
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

model DeploymentManagedDataPinEvidence {
  connectionId: string;
  revisionId: string;
}

model DeploymentBindingEvidence {
  bindingId: string;
  logicalConnection: string;
  connectorKind: string;
  revision: int64;
  validatedVersion: string;
  endpointConfigHash: string;
}

model DeploymentWorkspacePublishEvidence {
  workspaceId: string;
  servingStateId: string;
  artifactDigest: string;
  dataRevision: string;
  dataMode: string;
  managedDataPins: DeploymentManagedDataPinEvidence[];
  bindings: DeploymentBindingEvidence[];
}

model DeploymentPublishEvidence {
  releaseDigest: string;
  artifactDigest: string;
  planDigest: string;
  candidateId: string;
  candidateRevision: int64;
  targetId: string;
  baseGeneration: string;
  runtimeVersion: string;
  policyDigest: string;
  sourceRevision?: CandidateSourceRevision;
  workspaces: DeploymentWorkspacePublishEvidence[];
}

model DeploymentTargetResponse {
  workspaceId: string;
  servingStateId?: string;
  priorServingStateId?: string;
  status: string;
  error?: string;
}

model DeploymentConnectionResponse {
  connectionId: string;
  revisionId: string;
  priorRevisionId?: string;
}

model DeploymentProgress {
  current: int32;
  total: int32;
  unit: string;
}

model DeploymentVerification {
  digest: string;
  verifiedAt: utcDateTime;
}

model DeploymentResponse {
  id: string;
  projectId: string;
  releaseId: string;
  environment: string;
  requestDigest: string;
  evidence: DeploymentPublishEvidence;
  status: DeploymentStatus;
  progress?: DeploymentProgress;
  targets: DeploymentTargetResponse[];
  connections: DeploymentConnectionResponse[];
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

@route("/projects/{project}/deployments")
@tag("Deployments")
interface Deployments {
  @summary("List project deployments")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "VIEW_ITEM" })
  @extension("x-leapview-object-scope", "project-environment")
  @operationId("listDeployments")
  listDeployments(@path project: string, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<DeploymentListResponse> | CommonErrors;

  @summary("Deploy a ready project release")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "REQUEST_DEPLOYMENT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "deployment.queued", guarantee: "best-effort" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "DEPLOYMENT_NOT_FOUND", publicDetail: "Deployment not found." },
      #{ kind: "conflict", statusCode: 409, code: "DEPLOYMENT_CONFLICT", publicDetail: "The deployment conflicts with its current state." },
      #{ kind: "release_not_ready", statusCode: 409, code: "RELEASE_NOT_READY", publicDetail: "Only ready releases can be deployed." },
      #{ kind: "release_incomplete", statusCode: 409, code: "RELEASE_INCOMPLETE", publicDetail: "The release is missing a workspace artifact." },
      #{ kind: "publication_forbidden", statusCode: 403, code: "PUBLICATION_MANAGEMENT_REQUIRED", publicDetail: "MANAGE_PUBLICATIONS is required to activate this publication." },
      #{ kind: "authorization_unavailable", statusCode: 503, code: "AUTHORIZATION_UNAVAILABLE", publicDetail: "Publication activation authorization could not be evaluated." },
      #{ kind: "service_unavailable", statusCode: 503, code: "DEPLOYMENT_SERVICE_UNAVAILABLE", publicDetail: "Deployment service is unavailable." },
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded publication credential is required." },
      #{ kind: "approval_unavailable", statusCode: 503, code: "DEPLOYMENT_APPROVAL_UNAVAILABLE", publicDetail: "Deployment approvals are unavailable." },
      #{ kind: "approval_credential_expired", statusCode: 401, code: "APPROVAL_CREDENTIAL_EXPIRED", publicDetail: "The deployment approval credential has expired." },
      #{ kind: "approval_conflict", statusCode: 409, code: "DEPLOYMENT_APPROVAL_CONFLICT", publicDetail: "The deployment approval conflicts with its current state." },
      #{ kind: "approval_invalid", statusCode: 422, code: "INVALID_DEPLOYMENT_APPROVAL", publicDetail: "The deployment approval request is invalid." },
      #{ kind: "invalid", statusCode: 422, code: "INVALID_DEPLOYMENT", publicDetail: "The deployment request is invalid." },
    ],
    execution: #{
      mode: "async",
      guarantee: "transactional",
      jobKind: "deployment.activate",
      resourceKind: "deployment",
      initialEvent: "deployment.queued",
      initialState: "queued",
      statusOperation: "getDeployment",
      eventsOperation: "listDeploymentEvents",
      cancellation: "supported",
    },
  })
  @operationId("createDeployment")
  createDeployment(@path project: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DeploymentCreateRequest): AcceptedJson<DeploymentResponse> | CommonErrors;

  @summary("Get a project deployment")
  @route("/{deployment}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "VIEW_ITEM" })
  @extension("x-leapview-object-scope", "project-environment")
  @operationId("getDeployment")
  getDeployment(@path project: string, @path deployment: string): OkJson<DeploymentResponse> | CommonErrors;

  @summary("List or stream deployment events")
  @route("/{deployment}/events")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "VIEW_ITEM" })
  @extension("x-leapview-object-scope", "project-environment")
  @operationId("listDeploymentEvents")
  listDeploymentEvents(@path project: string, @path deployment: string, @header accept?: string, @header("Last-Event-ID") lastEventId?: string, @query limit?: ListLimit, @query pageToken?: PageToken): EventHistoryResponse<AsyncEventListResponse> | CommonErrors;

  @summary("Cancel a deployment before cutover")
  @route("/{deployment}/cancel")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "REQUEST_DEPLOYMENT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "deployment.cancelled", guarantee: "best-effort" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "DEPLOYMENT_NOT_FOUND", publicDetail: "Deployment not found." },
      #{ kind: "conflict", statusCode: 409, code: "DEPLOYMENT_CONFLICT", publicDetail: "The deployment conflicts with its current state." },
      #{ kind: "service_unavailable", statusCode: 503, code: "DEPLOYMENT_SERVICE_UNAVAILABLE", publicDetail: "Deployment service is unavailable." },
      #{ kind: "queue_unavailable", statusCode: 503, code: "ASYNC_QUEUE_UNAVAILABLE", publicDetail: "Deployment cancellation could not be persisted." },
      #{ kind: "invalid", statusCode: 422, code: "INVALID_DEPLOYMENT", publicDetail: "The deployment request is invalid." },
    ],
    targetParameter: "project",
  })
  @operationId("cancelDeployment")
  cancelDeployment(@path project: string, @path deployment: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<DeploymentResponse> | CommonErrors;

  @summary("Retry a terminal publication request without changing its immutable release")
  @route("/{deployment}/retry")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "REQUEST_DEPLOYMENT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "deployment.queued", guarantee: "best-effort" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "DEPLOYMENT_NOT_FOUND", publicDetail: "Deployment not found." },
      #{ kind: "conflict", statusCode: 409, code: "DEPLOYMENT_CONFLICT", publicDetail: "The deployment conflicts with its current state." },
      #{ kind: "release_not_ready", statusCode: 409, code: "RELEASE_NOT_READY", publicDetail: "Only ready releases can be deployed." },
      #{ kind: "release_incomplete", statusCode: 409, code: "RELEASE_INCOMPLETE", publicDetail: "The release is missing a workspace artifact." },
      #{ kind: "publication_forbidden", statusCode: 403, code: "PUBLICATION_MANAGEMENT_REQUIRED", publicDetail: "MANAGE_PUBLICATIONS is required to activate this publication." },
      #{ kind: "authorization_unavailable", statusCode: 503, code: "AUTHORIZATION_UNAVAILABLE", publicDetail: "Publication activation authorization could not be evaluated." },
      #{ kind: "service_unavailable", statusCode: 503, code: "DEPLOYMENT_SERVICE_UNAVAILABLE", publicDetail: "Deployment service is unavailable." },
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded publication credential is required." },
      #{ kind: "invalid", statusCode: 422, code: "INVALID_DEPLOYMENT", publicDetail: "The deployment request is invalid." },
    ],
    execution: #{
      mode: "async",
      guarantee: "transactional",
      jobKind: "deployment.activate",
      resourceKind: "deployment",
      initialEvent: "deployment.queued",
      initialState: "queued",
      statusOperation: "getDeployment",
      eventsOperation: "listDeploymentEvents",
      cancellation: "supported",
    },
    targetParameter: "project",
  })
  @operationId("retryDeployment")
  retryDeployment(@path project: string, @path deployment: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<DeploymentResponse> | CommonErrors;

  @summary("Roll back by creating a deployment of the prior release")
  @route("/{deployment}/rollback")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "ROLLBACK_DEPLOYMENT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "deployment.queued", guarantee: "best-effort" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "DEPLOYMENT_NOT_FOUND", publicDetail: "Deployment not found." },
      #{ kind: "conflict", statusCode: 409, code: "DEPLOYMENT_CONFLICT", publicDetail: "The deployment conflicts with its current state." },
      #{ kind: "release_not_ready", statusCode: 409, code: "RELEASE_NOT_READY", publicDetail: "Only ready releases can be deployed." },
      #{ kind: "release_incomplete", statusCode: 409, code: "RELEASE_INCOMPLETE", publicDetail: "The release is missing a workspace artifact." },
      #{ kind: "publication_forbidden", statusCode: 403, code: "PUBLICATION_MANAGEMENT_REQUIRED", publicDetail: "MANAGE_PUBLICATIONS is required to activate this publication." },
      #{ kind: "authorization_unavailable", statusCode: 503, code: "AUTHORIZATION_UNAVAILABLE", publicDetail: "Publication activation authorization could not be evaluated." },
      #{ kind: "service_unavailable", statusCode: 503, code: "DEPLOYMENT_SERVICE_UNAVAILABLE", publicDetail: "Deployment service is unavailable." },
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded publication credential is required." },
      #{ kind: "invalid", statusCode: 422, code: "INVALID_DEPLOYMENT", publicDetail: "The deployment request is invalid." },
    ],
    execution: #{
      mode: "async",
      guarantee: "transactional",
      jobKind: "deployment.activate",
      resourceKind: "deployment",
      initialEvent: "deployment.queued",
      initialState: "queued",
      statusOperation: "getDeployment",
      eventsOperation: "listDeploymentEvents",
      cancellation: "supported",
    },
    targetParameter: "project",
  })
  @operationId("rollbackDeployment")
  rollbackDeployment(@path project: string, @path deployment: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<DeploymentResponse> | CommonErrors;

  @summary("Request approval for an immutable deployment plan")
  @route("/{deployment}/approval-requests")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "REQUEST_DEPLOYMENT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "deployment.approval_requested", guarantee: "best-effort" },
    failures: #[
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded publication credential is required." },
      #{ kind: "approval_unavailable", statusCode: 503, code: "DEPLOYMENT_APPROVAL_UNAVAILABLE", publicDetail: "Deployment approvals are unavailable." },
      #{ kind: "approval_not_found", statusCode: 404, code: "DEPLOYMENT_APPROVAL_NOT_FOUND", publicDetail: "Deployment approval not found." },
      #{ kind: "approval_credential_expired", statusCode: 401, code: "APPROVAL_CREDENTIAL_EXPIRED", publicDetail: "The deployment approval credential has expired." },
      #{ kind: "approval_conflict", statusCode: 409, code: "DEPLOYMENT_APPROVAL_REQUIRED", publicDetail: "The deployment approval is not valid for this action." },
      #{ kind: "conflict", statusCode: 409, code: "DEPLOYMENT_CONFLICT", publicDetail: "The deployment conflicts with its current state." },
      #{ kind: "not_found", statusCode: 404, code: "DEPLOYMENT_NOT_FOUND", publicDetail: "Deployment not found." },
      #{ kind: "invalid", statusCode: 422, code: "INVALID_DEPLOYMENT", publicDetail: "The deployment request is invalid." },
    ],
    targetParameter: "project",
  })
  @operationId("requestDeploymentApproval")
  requestDeploymentApproval(@path project: string, @path deployment: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): CreatedJson<DeploymentApprovalResponse> | CommonErrors;

  @summary("Approve an immutable deployment plan")
  @route("/{deployment}/approval-requests/{approval}/approve")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "APPROVE_DEPLOYMENT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "deployment.approved", guarantee: "best-effort" },
    failures: #[
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded approval credential is required." },
      #{ kind: "approval_unavailable", statusCode: 503, code: "DEPLOYMENT_APPROVAL_UNAVAILABLE", publicDetail: "Deployment approvals are unavailable." },
      #{ kind: "approval_not_found", statusCode: 404, code: "DEPLOYMENT_APPROVAL_NOT_FOUND", publicDetail: "Deployment approval not found." },
      #{ kind: "approval_credential_expired", statusCode: 401, code: "APPROVAL_CREDENTIAL_EXPIRED", publicDetail: "The deployment approval credential has expired." },
      #{ kind: "approval_conflict", statusCode: 409, code: "DEPLOYMENT_APPROVAL_REQUIRED", publicDetail: "The deployment approval is not valid for this action." },
      #{ kind: "approval_invalid", statusCode: 422, code: "INVALID_DEPLOYMENT_APPROVAL", publicDetail: "The deployment approval request is invalid." },
      #{ kind: "separation_of_duty", statusCode: 409, code: "SEPARATION_OF_DUTY_REQUIRED", publicDetail: "A separate principal must approve this deployment." },
      #{ kind: "not_found", statusCode: 404, code: "DEPLOYMENT_NOT_FOUND", publicDetail: "Deployment not found." },
    ],
    targetParameter: "project",
  })
  @operationId("approveDeployment")
  approveDeployment(@path project: string, @path deployment: string, @path approval: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DeploymentApprovalDecisionRequest): OkJson<DeploymentApprovalResponse> | CommonErrors;

  @summary("Deny an immutable deployment plan")
  @route("/{deployment}/approval-requests/{approval}/deny")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "APPROVE_DEPLOYMENT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "deployment.denied", guarantee: "best-effort" },
    failures: #[
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded approval credential is required." },
      #{ kind: "approval_unavailable", statusCode: 503, code: "DEPLOYMENT_APPROVAL_UNAVAILABLE", publicDetail: "Deployment approvals are unavailable." },
      #{ kind: "approval_not_found", statusCode: 404, code: "DEPLOYMENT_APPROVAL_NOT_FOUND", publicDetail: "Deployment approval not found." },
      #{ kind: "approval_credential_expired", statusCode: 401, code: "APPROVAL_CREDENTIAL_EXPIRED", publicDetail: "The deployment approval credential has expired." },
      #{ kind: "approval_conflict", statusCode: 409, code: "DEPLOYMENT_APPROVAL_REQUIRED", publicDetail: "The deployment approval is not valid for this action." },
      #{ kind: "approval_invalid", statusCode: 422, code: "INVALID_DEPLOYMENT_APPROVAL", publicDetail: "The deployment approval request is invalid." },
      #{ kind: "separation_of_duty", statusCode: 409, code: "SEPARATION_OF_DUTY_REQUIRED", publicDetail: "A separate principal must approve this deployment." },
      #{ kind: "not_found", statusCode: 404, code: "DEPLOYMENT_NOT_FOUND", publicDetail: "Deployment not found." },
    ],
    targetParameter: "project",
  })
  @operationId("denyDeploymentApproval")
  denyDeploymentApproval(@path project: string, @path deployment: string, @path approval: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DeploymentApprovalDecisionRequest): OkJson<DeploymentApprovalResponse> | CommonErrors;

  @summary("Revoke a pending or approved deployment decision")
  @route("/{deployment}/approval-requests/{approval}/revoke")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "APPROVE_DEPLOYMENT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "deployment.approval_revoked", guarantee: "best-effort" },
    failures: #[
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded approval credential is required." },
      #{ kind: "approval_unavailable", statusCode: 503, code: "DEPLOYMENT_APPROVAL_UNAVAILABLE", publicDetail: "Deployment approvals are unavailable." },
      #{ kind: "approval_not_found", statusCode: 404, code: "DEPLOYMENT_APPROVAL_NOT_FOUND", publicDetail: "Deployment approval not found." },
      #{ kind: "approval_credential_expired", statusCode: 401, code: "APPROVAL_CREDENTIAL_EXPIRED", publicDetail: "The deployment approval credential has expired." },
      #{ kind: "approval_conflict", statusCode: 409, code: "DEPLOYMENT_APPROVAL_REQUIRED", publicDetail: "The deployment approval is not valid for this action." },
      #{ kind: "approval_invalid", statusCode: 422, code: "INVALID_DEPLOYMENT_APPROVAL", publicDetail: "The deployment approval request is invalid." },
      #{ kind: "separation_of_duty", statusCode: 409, code: "SEPARATION_OF_DUTY_REQUIRED", publicDetail: "A separate principal must approve this deployment." },
      #{ kind: "not_found", statusCode: 404, code: "DEPLOYMENT_NOT_FOUND", publicDetail: "Deployment not found." },
    ],
    targetParameter: "project",
  })
  @operationId("revokeDeploymentApproval")
  revokeDeploymentApproval(@path project: string, @path deployment: string, @path approval: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DeploymentApprovalDecisionRequest): OkJson<DeploymentApprovalResponse> | CommonErrors;

  @summary("Activate an approved immutable deployment plan")
  @route("/{deployment}/activate")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "ACTIVATE_DEPLOYMENT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "deployment.activation_requested", guarantee: "best-effort" },
    failures: #[
      #{ kind: "approval_credential_required", statusCode: 401, code: "ACTIVATION_CREDENTIAL_REQUIRED", publicDetail: "A bounded activation credential is required." },
      #{ kind: "approval_unavailable", statusCode: 503, code: "DEPLOYMENT_APPROVAL_UNAVAILABLE", publicDetail: "Deployment approvals are unavailable." },
      #{ kind: "approval_not_found", statusCode: 404, code: "DEPLOYMENT_APPROVAL_NOT_FOUND", publicDetail: "Deployment approval not found." },
      #{ kind: "approval_credential_expired", statusCode: 401, code: "APPROVAL_CREDENTIAL_EXPIRED", publicDetail: "The deployment approval credential has expired." },
      #{ kind: "approval_conflict", statusCode: 409, code: "DEPLOYMENT_APPROVAL_REQUIRED", publicDetail: "An approved deployment is required for activation." },
      #{ kind: "approval_invalid", statusCode: 422, code: "INVALID_DEPLOYMENT_APPROVAL", publicDetail: "The deployment approval is invalid." },
      #{ kind: "activation_forbidden", statusCode: 403, code: "ACTIVATION_FORBIDDEN", publicDetail: "Deployment activation is forbidden." },
      #{ kind: "approval_forbidden", statusCode: 403, code: "APPROVAL_FORBIDDEN", publicDetail: "Deployment approval is forbidden." },
      #{ kind: "authorization_unavailable", statusCode: 503, code: "AUTHORIZATION_UNAVAILABLE", publicDetail: "Deployment activation authorization could not be evaluated." },
      #{ kind: "queue_unavailable", statusCode: 503, code: "ASYNC_QUEUE_UNAVAILABLE", publicDetail: "Deployment activation could not be persisted." },
      #{ kind: "not_found", statusCode: 404, code: "DEPLOYMENT_NOT_FOUND", publicDetail: "Deployment not found." },
      #{ kind: "conflict", statusCode: 409, code: "DEPLOYMENT_CONFLICT", publicDetail: "The deployment conflicts with its current state." },
    ],
    execution: #{
      mode: "async",
      guarantee: "transactional",
      jobKind: "deployment.activate",
      resourceKind: "deployment",
      initialEvent: "deployment.activation_requested",
      initialState: "queued",
      statusOperation: "getDeployment",
      eventsOperation: "listDeploymentEvents",
      cancellation: "supported",
    },
    targetParameter: "project",
  })
  @operationId("activateDeployment")
  activateDeployment(@path project: string, @path deployment: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<DeploymentResponse> | CommonErrors;
}

@route("/projects/{project}/candidates")
@tag("Deployments")
interface ProjectCandidates {
  @summary("Create or resume the current author's private project candidate")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "AUTHOR_PROJECT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "candidate.started", guarantee: "best-effort" },
    failures: #[
      #{ kind: "candidate_unavailable", statusCode: 503, code: "CANDIDATE_SERVICE_UNAVAILABLE", publicDetail: "Candidate service is unavailable." },
      #{ kind: "candidate_not_found", statusCode: 404, code: "CANDIDATE_NOT_FOUND", publicDetail: "Candidate not found." },
      #{ kind: "candidate_conflict", statusCode: 409, code: "CANDIDATE_CONFLICT", publicDetail: "The candidate conflicts with its current state." },
      #{ kind: "candidate_quota", statusCode: 429, code: "CANDIDATE_QUOTA_EXCEEDED", publicDetail: "Candidate quota exceeded." },
      #{ kind: "candidate_invalid", statusCode: 422, code: "INVALID_CANDIDATE", publicDetail: "The candidate request is invalid." },
    ],
  })
  @operationId("startProjectCandidate")
  startProjectCandidate(@path project: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: CandidateStartRequest): CreatedJson<CandidateResponse> | CommonErrors;

  @summary("Get an owned private project candidate")
  @route("/{candidate}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "AUTHOR_PROJECT" })
  @extension("x-leapview-object-scope", "project-environment")
  @operationId("getProjectCandidate")
  getProjectCandidate(@path project: string, @path candidate: string): OkJson<CandidateResponse> | CommonErrors;

  @summary("Replace a private candidate artifact with optimistic concurrency")
  @route("/{candidate}/artifact")
  @put
  @apigen.authz(#{ mode: "privilege", privilege: "AUTHOR_PROJECT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "candidate.artifact_replaced", guarantee: "best-effort" },
    failures: #[
      #{ kind: "candidate_unavailable", statusCode: 503, code: "CANDIDATE_SERVICE_UNAVAILABLE", publicDetail: "Candidate service is unavailable." },
      #{ kind: "candidate_not_found", statusCode: 404, code: "CANDIDATE_NOT_FOUND", publicDetail: "Candidate not found." },
      #{ kind: "candidate_conflict", statusCode: 409, code: "CANDIDATE_CONFLICT", publicDetail: "The candidate conflicts with its current state." },
      #{ kind: "candidate_invalid", statusCode: 422, code: "INVALID_CANDIDATE", publicDetail: "The candidate request is invalid." },
    ],
    targetParameter: "project",
  })
  @operationId("replaceProjectCandidateArtifact")
  replaceProjectCandidateArtifact(@path project: string, @path candidate: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: CandidateArtifactRequest): OkJson<CandidateResponse> | CommonErrors;

  @summary("Retry preparation of a failed private candidate")
  @route("/{candidate}/retry")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "AUTHOR_PROJECT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "candidate.retried", guarantee: "best-effort" },
    failures: #[
      #{ kind: "candidate_unavailable", statusCode: 503, code: "CANDIDATE_SERVICE_UNAVAILABLE", publicDetail: "Candidate service is unavailable." },
      #{ kind: "candidate_not_found", statusCode: 404, code: "CANDIDATE_NOT_FOUND", publicDetail: "Candidate not found." },
      #{ kind: "candidate_conflict", statusCode: 409, code: "CANDIDATE_CONFLICT", publicDetail: "The candidate conflicts with its current state." },
      #{ kind: "candidate_invalid", statusCode: 422, code: "INVALID_CANDIDATE", publicDetail: "The candidate request is invalid." },
    ],
    targetParameter: "project",
  })
  @operationId("retryProjectCandidate")
  retryProjectCandidate(@path project: string, @path candidate: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<CandidateResponse> | CommonErrors;

  @summary("Cancel an owned private project candidate")
  @route("/{candidate}/cancel")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "AUTHOR_PROJECT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "candidate.cancelled", guarantee: "best-effort" },
    failures: #[
      #{ kind: "candidate_unavailable", statusCode: 503, code: "CANDIDATE_SERVICE_UNAVAILABLE", publicDetail: "Candidate service is unavailable." },
      #{ kind: "candidate_not_found", statusCode: 404, code: "CANDIDATE_NOT_FOUND", publicDetail: "Candidate not found." },
      #{ kind: "candidate_conflict", statusCode: 409, code: "CANDIDATE_CONFLICT", publicDetail: "The candidate conflicts with its current state." },
      #{ kind: "candidate_invalid", statusCode: 422, code: "INVALID_CANDIDATE", publicDetail: "The candidate request is invalid." },
    ],
    targetParameter: "project",
  })
  @operationId("cancelProjectCandidate")
  cancelProjectCandidate(@path project: string, @path candidate: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<CandidateResponse> | CommonErrors;

  @summary("Publish the exact ready candidate through target policy")
  @route("/{candidate}/publish")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PUBLISH_RELEASE" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "deployment.queued", guarantee: "best-effort" },
    failures: #[
      #{ kind: "candidate_unavailable", statusCode: 503, code: "CANDIDATE_SERVICE_UNAVAILABLE", publicDetail: "Candidate service is unavailable." },
      #{ kind: "candidate_not_found", statusCode: 404, code: "CANDIDATE_NOT_FOUND", publicDetail: "Candidate not found." },
      #{ kind: "candidate_conflict", statusCode: 409, code: "CANDIDATE_CONFLICT", publicDetail: "The candidate conflicts with its current state." },
      #{ kind: "candidate_invalid", statusCode: 422, code: "INVALID_CANDIDATE", publicDetail: "The candidate request is invalid." },
      #{ kind: "not_found", statusCode: 404, code: "DEPLOYMENT_NOT_FOUND", publicDetail: "Deployment not found." },
      #{ kind: "conflict", statusCode: 409, code: "DEPLOYMENT_CONFLICT", publicDetail: "The deployment conflicts with its current state." },
      #{ kind: "release_not_ready", statusCode: 409, code: "RELEASE_NOT_READY", publicDetail: "Only ready releases can be deployed." },
      #{ kind: "release_incomplete", statusCode: 409, code: "RELEASE_INCOMPLETE", publicDetail: "The release is missing a workspace artifact." },
      #{ kind: "publication_forbidden", statusCode: 403, code: "PUBLICATION_MANAGEMENT_REQUIRED", publicDetail: "MANAGE_PUBLICATIONS is required to activate this publication." },
      #{ kind: "authorization_unavailable", statusCode: 503, code: "AUTHORIZATION_UNAVAILABLE", publicDetail: "Publication activation authorization could not be evaluated." },
      #{ kind: "service_unavailable", statusCode: 503, code: "DEPLOYMENT_SERVICE_UNAVAILABLE", publicDetail: "Deployment service is unavailable." },
      #{ kind: "approval_credential_required", statusCode: 401, code: "APPROVAL_CREDENTIAL_REQUIRED", publicDetail: "A bounded publication credential is required." },
      #{ kind: "invalid", statusCode: 422, code: "INVALID_DEPLOYMENT", publicDetail: "The deployment request is invalid." },
    ],
    execution: #{
      mode: "async",
      guarantee: "transactional",
      jobKind: "deployment.activate",
      resourceKind: "deployment",
      initialEvent: "deployment.queued",
      initialState: "queued",
      statusOperation: "getDeployment",
      eventsOperation: "listDeploymentEvents",
      cancellation: "supported",
    },
    targetParameter: "project",
  })
  @operationId("publishProjectCandidate")
  publishProjectCandidate(@path project: string, @path candidate: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: CandidatePublishRequest): AcceptedJson<DeploymentResponse> | CommonErrors;
}

@tag("Deployments")
@summary("Review a private candidate without acquiring its data credentials")
@route("/projects/{project}/candidate-reviews/{candidate}")
@get
@apigen.authz(#{ mode: "privilege", privilege: "REVIEW_CANDIDATE" })
@extension("x-leapview-object-scope", "project-environment")
@operationId("reviewProjectCandidate")
op reviewProjectCandidate(@path project: string, @path candidate: string): OkJson<CandidateResponse> | CommonErrors;

@tag("Deployments")
@summary("Cancel the current principal's active candidate by stable authoring key")
@route("/projects/{project}/candidate-sessions/{candidateKey}/cancel")
@post
@apigen.authz(#{ mode: "privilege", privilege: "AUTHOR_PROJECT" })
@extension("x-leapview-object-scope", "project-environment")
@apigen.command(#{
  audit: #{ required: true, successAction: "candidate.cancelled", guarantee: "best-effort" },
  failures: #[
    #{ kind: "candidate_unavailable", statusCode: 503, code: "CANDIDATE_SERVICE_UNAVAILABLE", publicDetail: "Candidate service is unavailable." },
    #{ kind: "candidate_not_found", statusCode: 404, code: "CANDIDATE_NOT_FOUND", publicDetail: "Candidate not found." },
    #{ kind: "candidate_conflict", statusCode: 409, code: "CANDIDATE_CONFLICT", publicDetail: "The candidate conflicts with its current state." },
    #{ kind: "candidate_invalid", statusCode: 422, code: "INVALID_CANDIDATE", publicDetail: "The candidate request is invalid." },
  ],
  targetParameter: "project",
})
@operationId("cancelProjectCandidateByKey")
op cancelProjectCandidateByKey(@path project: string, @path candidateKey: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<CandidateResponse> | CommonErrors;

@route("/projects/{project}/candidate-sync")
@tag("Deployments")
interface CandidateSynchronization {
  @summary("Plan content-addressed synchronization for the current author's private candidate")
  @doc("Query classification: reports missing immutable blobs without uploading or committing source state.")
  @route("/plan")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "AUTHOR_PROJECT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.query
  @operationId("planProjectCandidateSynchronization")
  planProjectCandidateSynchronization(
    @path project: string,
    @header("Idempotency-Key") idempotencyKey: IdempotencyKey,
    @body body: CandidateSynchronizationRequest,
  ): OkJson<CandidateSynchronizationPlanResponse> | CommonErrors;

  @summary("Upload a missing immutable private-candidate source blob")
  @route("/blobs/{digest}")
  @put
  @apigen.authz(#{ mode: "privilege", privilege: "AUTHOR_PROJECT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{
      required: true,
      successAction: "candidate.source_blob_uploaded",
      guarantee: "best-effort",
    },
    failures: #[
      #{ kind: "candidate_unavailable", statusCode: 503, code: "CANDIDATE_SERVICE_UNAVAILABLE", publicDetail: "Candidate service is unavailable." },
      #{ kind: "candidate_conflict", statusCode: 409, code: "CANDIDATE_CONFLICT", publicDetail: "The candidate conflicts with its current state." },
      #{ kind: "candidate_invalid", statusCode: 422, code: "INVALID_CANDIDATE", publicDetail: "The candidate request is invalid." },
      #{ kind: "source_blob_invalid", statusCode: 422, code: "INVALID_CANDIDATE_SOURCE_BLOB", publicDetail: "Candidate source blob headers do not match the canonical content identity." },
      #{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Candidate source blob audit is temporarily unavailable." },
    ],
    targetParameter: "project",
  })
  @apigen.manual
  @operationId("uploadProjectCandidateSourceBlob")
  uploadProjectCandidateSourceBlob(@path project: string, @path digest: string, @header contentType: "application/octet-stream", @header("Content-Digest") contentDigest: string, @body body: bytes): CreatedJson<CandidateSourceBlobResponse> | CommonErrors;

  @summary("Atomically commit a complete source snapshot to the current author's private candidate")
  @route("/commit")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "AUTHOR_PROJECT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "candidate.ready", guarantee: "best-effort" },
    failures: #[
      #{ kind: "candidate_unavailable", statusCode: 503, code: "CANDIDATE_SERVICE_UNAVAILABLE", publicDetail: "Candidate service is unavailable." },
      #{ kind: "candidate_not_found", statusCode: 404, code: "CANDIDATE_NOT_FOUND", publicDetail: "Candidate not found." },
      #{ kind: "candidate_conflict", statusCode: 409, code: "CANDIDATE_CONFLICT", publicDetail: "The candidate conflicts with its current state." },
      #{ kind: "candidate_invalid", statusCode: 422, code: "INVALID_CANDIDATE", publicDetail: "The candidate request is invalid." },
    ],
  })
  @operationId("commitProjectCandidateSynchronization")
  commitProjectCandidateSynchronization(@path project: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: CandidateSynchronizationRequest): OkJson<CandidateResponse> | CommonErrors;
}
