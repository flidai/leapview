using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Release;

enum ReleaseStatus {
  draft;
  validating;
  ready;
  failed;
}

model ReleaseConnectionPin {
  connection: string;
  revisionId: string;
}

model ReleaseServingIdentity {
  projectId: ResourceId;
  environment: string;
  generationId: string;
}

model ReleaseProjectArtifactProvenance {
  sourceDigest: string;
  projectDigest: string;
  contentDigest: string;
  compilerVersion: string;
  schemaVersion: int32;
}

model ReleaseCandidateProvenance {
  id: string;
  revision: int64;
  ownerId: string;
}

model ReleaseSourceRevisionProvenance {
  revision: string;
  repository?: string;
  ref?: string;
  changeId?: string;
}

model ReleaseManagedDataPin {
  connectionId: string;
  revisionId: string;
}

model ReleaseBindingEvidence {
  bindingId: string;
  connectionId: string;
  connectorKind: string;
  revision: int64;
  validatedVersion: string;
  endpointConfigHash: string;
}

enum ReleaseGenerationDataMode {
  reuse_base;
  refresh_sources;
}

model ReleaseAuthoredConnectionEvidence {
  connectionId: string;
  connectorKind: string;
  displayName?: string;
}

model ReleaseGenerationPlanProvenance {
  identity: ReleaseServingIdentity;
  baseIdentity?: ReleaseServingIdentity;
  targetId: string;
  runtimeVersion: string;
  policyDigest: string;
  dataRevision: string;
  dataMode: ReleaseGenerationDataMode;
  managedDataPins: ReleaseManagedDataPin[];
  bindings: ReleaseBindingEvidence[];
  authoredConnections: ReleaseAuthoredConnectionEvidence[];
}

model ReleaseProvenance {
  version: int32;
  artifact: ReleaseProjectArtifactProvenance;
  candidate: ReleaseCandidateProvenance;
  sourceRevision?: ReleaseSourceRevisionProvenance;
  plan: ReleaseGenerationPlanProvenance;
  artifactProvenanceDigest: string;
  planDigest: string;
  digest: string;
}

model ReleaseCreateRequest {
  environment: string;
  generationId: string;
  artifactDigest: string;
  connections: ReleaseConnectionPin[];
  projectDigest: string;
  requestDigest?: string;
  provenance?: ReleaseProvenance;
}

model ReleaseResponse {
  id: string;
  projectId: ResourceId;
  environment: string;
  generationId: string;
  projectDigest: string;
  artifactDigest: string;
  actualDigest?: string;
  artifactSizeBytes: int64;
  status: ReleaseStatus;
  connections: ReleaseConnectionPin[];
  provenance?: ReleaseProvenance;
  createdBy: string;
  createdAt: utcDateTime;
  finalizedAt?: utcDateTime;
  error?: string;
}

model ReleaseListResponse {
  items: ReleaseResponse[];
  page: PageInfo;
}

model ReleaseArtifactResponse {
  releaseId: string;
  generationId: string;
  digest: string;
  actualDigest: string;
  sizeBytes: int64;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model ReleaseCreatedAuditPayload {
  @apigen.auditInternal
  operationId: string;
  @apigen.auditInternal
  releaseId: string;
  @apigen.auditInternal
  projectId: ResourceId;
  @apigen.auditInternal
  projectDigest: string;
  @apigen.auditInternal
  status: string;
  @apigen.auditPii
  createdBy: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model ReleaseArtifactUploadedAuditPayload {
  @apigen.auditInternal
  operationId: string;
  @apigen.auditInternal
  releaseId: string;
  @apigen.auditInternal
  generationId: string;
  @apigen.auditInternal
  digest: string;
  @apigen.auditInternal
  sizeBytes: int64;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model ReleaseValidatingAuditPayload {
  @apigen.auditInternal
  operationId: string;
  @apigen.auditInternal
  releaseId: string;
  @apigen.auditInternal
  projectId: ResourceId;
  @apigen.auditInternal
  status: string;
}

@apigen.failureDefinition(#{ kind: "conflict", statusCode: 409, code: "RELEASE_CONFLICT", publicDetail: "The release conflicts with its current state." })
model ReleaseConflictFailure {}

@apigen.failureDefinition(#{ kind: "immutable", statusCode: 409, code: "RELEASE_IMMUTABLE", publicDetail: "The release is immutable." })
model ReleaseImmutableFailure {}

@apigen.failureDefinition(#{ kind: "digest_mismatch", statusCode: 422, code: "CONTENT_DIGEST_MISMATCH", publicDetail: "The release artifact digest does not match the expected content." })
model ReleaseArtifactDigestMismatchFailure {}

@route("/projects/{project}/releases")
@tag("Releases")
interface Releases {
  @summary("List project releases")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project")
  listReleases(@path project: ResourceId, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<ReleaseListResponse> | CommonErrors;

  @summary("Create a draft project release")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_PUBLISH" })
  @extension("x-leapview-object-scope", "project")
  @apigen.failsWith(ReleaseConflictFailure)
  @apigen.command(#{
    auditAction: "release.created",
    guarantee: "transactional",
    failures: #[#{ kind: "invalid", statusCode: 422, code: "INVALID_RELEASE", publicDetail: "The release request is invalid." }],
  })
  @apigen.auditPayload(ReleaseCreatedAuditPayload)
  createRelease(@path @apigen.target project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: ReleaseCreateRequest): CreatedJson<ReleaseResponse> | CommonErrors;

  @summary("Get a project release")
  @route("/{release}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project")
  getRelease(@path project: ResourceId, @path release: string): OkJson<ReleaseResponse> | CommonErrors;

  @summary("Upload an immutable project-generation artifact")
  @route("/{release}/artifact")
  @put
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_PUBLISH" })
  @extension("x-leapview-object-scope", "project")
  @apigen.failsWith(ReleaseConflictFailure)
  @apigen.failsWith(ReleaseImmutableFailure)
  @apigen.failsWith(ReleaseArtifactDigestMismatchFailure)
  @apigen.command(#{
    auditAction: "release.artifact_uploaded",
    guarantee: "transactional",
    failures: #[
      #{ kind: "invalid", statusCode: 422, code: "INVALID_RELEASE", publicDetail: "The release artifact request is invalid." },
      #{ kind: "not_found", statusCode: 404, code: "RELEASE_NOT_FOUND", publicDetail: "Release artifact not found." },
    ],
  })
  @apigen.auditPayload(ReleaseArtifactUploadedAuditPayload)
  @apigen.manual
  uploadReleaseArtifact(@path @apigen.target project: ResourceId, @path release: string, @header contentType: "application/octet-stream", @header("Content-Digest") contentDigest: string, @body body: bytes): CreatedJson<ReleaseArtifactResponse> | CommonErrors;

  @summary("Finalize and validate a release")
  @route("/{release}/finalize")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_PUBLISH" })
  @extension("x-leapview-object-scope", "project")
  @apigen.asyncExecution(Releases.getRelease, Releases.listReleaseEvents, #{
    guarantee: "transactional",
    jobKind: "release.finalize",
    resourceKind: "release",
    initialEvent: "release.validating",
    initialState: "validating",
    cancellation: "unsupported",
  })
  @apigen.failsWith(ReleaseConflictFailure)
  @apigen.failsWith(ReleaseImmutableFailure)
  @apigen.command(#{
    auditAction: "release.validating",
    guarantee: "transactional",
    failures: #[
      #{ kind: "incomplete", statusCode: 409, code: "RELEASE_INCOMPLETE", publicDetail: "The release artifacts are incomplete." },
      #{ kind: "not_found", statusCode: 404, code: "RELEASE_NOT_FOUND", publicDetail: "Release not found." },
      #{ kind: "queue_unavailable", statusCode: 503, code: "ASYNC_QUEUE_UNAVAILABLE", publicDetail: "Release finalization could not be queued." },
    ],
  })
  @apigen.auditPayload(ReleaseValidatingAuditPayload)
  finalizeRelease(@path @apigen.target project: ResourceId, @path release: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<ReleaseResponse> | CommonErrors;

  @summary("List or stream release events")
  @route("/{release}/events")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project")
  listReleaseEvents(@path project: ResourceId, @path release: string, @header accept?: string, @header("Last-Event-ID") lastEventId?: string, @query limit?: ListLimit, @query pageToken?: PageToken): EventHistoryResponse<AsyncEventListResponse> | CommonErrors;
}
