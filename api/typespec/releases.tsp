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

model ReleaseWorkspaceManifest {
  workspace: string;
  artifactDigest: string;
  servingStateId?: string;
}

model ReleaseConnectionPin {
  connection: string;
  revisionId: string;
}

model ReleaseProjectArtifactWorkspace {
  workspaceId: string;
  artifactDigest: string;
}

model ReleaseProjectArtifactProvenance {
  sourceDigest: string;
  projectDigest: string;
  compilerVersion: string;
  schemaVersion: int32;
  workspaces: ReleaseProjectArtifactWorkspace[];
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
  logicalConnection: string;
  connectorKind: string;
  revision: int64;
  validatedVersion: string;
  endpointConfigHash: string;
}

enum ReleaseTargetDataMode {
  reuse_snapshot;
  refresh_sources;
}

model ReleaseTargetWorkspacePlan {
  workspaceId: string;
  servingStateId: string;
  artifactDigest: string;
  dataRevision: string;
  dataMode: ReleaseTargetDataMode;
  managedDataPins: ReleaseManagedDataPin[];
  bindings: ReleaseBindingEvidence[];
}

model ReleaseTargetPlanProvenance {
  targetId: string;
  environment: string;
  baseGeneration: string;
  runtimeVersion: string;
  policyDigest: string;
  workspaces: ReleaseTargetWorkspacePlan[];
}

model ReleaseProvenance {
  version: int32;
  artifact: ReleaseProjectArtifactProvenance;
  candidate: ReleaseCandidateProvenance;
  sourceRevision?: ReleaseSourceRevisionProvenance;
  plan: ReleaseTargetPlanProvenance;
  artifactDigest: string;
  planDigest: string;
  digest: string;
}

model ReleaseCreateRequest {
  projectDigest: string;
  workspaces: ReleaseWorkspaceManifest[];
  connections: ReleaseConnectionPin[];
  provenance?: ReleaseProvenance;
}

model ReleaseResponse {
  id: string;
  projectId: string;
  projectDigest: string;
  status: ReleaseStatus;
  workspaces: ReleaseWorkspaceManifest[];
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
  workspaceId: string;
  digest: string;
  sizeBytes: int64;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model ReleaseCreatedAuditPayload {
  @apigen.auditInternal
  operationId: string;
  @apigen.auditInternal
  releaseId: string;
  @apigen.auditInternal
  projectId: string;
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
  workspaceId: string;
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
  projectId: string;
  @apigen.auditInternal
  status: string;
}

@route("/projects/{project}/releases")
@tag("Releases")
interface Releases {
  @summary("List project releases")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PUBLISH_RELEASE" })
  @extension("x-leapview-object-scope", "project-environment")
  @operationId("listReleases")
  listReleases(@path project: string, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<ReleaseListResponse> | CommonErrors;

  @summary("Create a draft project release")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PUBLISH_RELEASE" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "release.created", guarantee: "best-effort" },
    failures: #[
      #{ kind: "invalid", statusCode: 422, code: "INVALID_RELEASE", publicDetail: "The release request is invalid." },
      #{ kind: "conflict", statusCode: 409, code: "RELEASE_CONFLICT", publicDetail: "The release conflicts with its current state." },
    ],
  })
  @apigen.auditPayload(ReleaseCreatedAuditPayload)
  @operationId("createRelease")
  createRelease(@path project: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: ReleaseCreateRequest): CreatedJson<ReleaseResponse> | CommonErrors;

  @summary("Get a project release")
  @route("/{release}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PUBLISH_RELEASE" })
  @extension("x-leapview-object-scope", "project-environment")
  @operationId("getRelease")
  getRelease(@path project: string, @path release: string): OkJson<ReleaseResponse> | CommonErrors;

  @summary("Upload an immutable workspace artifact")
  @route("/{release}/workspaces/{workspace}/artifact")
  @put
  @apigen.authz(#{ mode: "privilege", privilege: "PUBLISH_RELEASE" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "release.artifact_uploaded", guarantee: "best-effort" },
    failures: #[
      #{ kind: "invalid", statusCode: 422, code: "INVALID_RELEASE", publicDetail: "The release artifact request is invalid." },
      #{ kind: "not_found", statusCode: 404, code: "RELEASE_NOT_FOUND", publicDetail: "Release or workspace artifact not found." },
      #{ kind: "conflict", statusCode: 409, code: "RELEASE_CONFLICT", publicDetail: "The release conflicts with its current state." },
      #{ kind: "immutable", statusCode: 409, code: "RELEASE_IMMUTABLE", publicDetail: "The release is immutable." },
      #{ kind: "digest_mismatch", statusCode: 422, code: "CONTENT_DIGEST_MISMATCH", publicDetail: "The release artifact digest does not match the expected content." },
    ],
    targetParameter: "project",
  })
  @apigen.auditPayload(ReleaseArtifactUploadedAuditPayload)
  @apigen.manual
  @operationId("uploadReleaseArtifact")
  uploadReleaseArtifact(@path project: string, @path release: string, @path workspace: string, @header contentType: "application/octet-stream", @header("Content-Digest") contentDigest: string, @body body: bytes): CreatedJson<ReleaseArtifactResponse> | CommonErrors;

  @summary("Finalize and validate a release")
  @route("/{release}/finalize")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PUBLISH_RELEASE" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.asyncExecution(Releases.getRelease, Releases.listReleaseEvents, #{
    guarantee: "transactional",
    jobKind: "release.finalize",
    resourceKind: "release",
    initialEvent: "release.validating",
    initialState: "validating",
    cancellation: "unsupported",
  })
  @apigen.command(#{
    auditAction: "release.validating",
    guarantee: "transactional",
    failures: #[
      #{ kind: "conflict", statusCode: 409, code: "RELEASE_CONFLICT", publicDetail: "The release conflicts with its current state." },
      #{ kind: "immutable", statusCode: 409, code: "RELEASE_IMMUTABLE", publicDetail: "The release is immutable." },
      #{ kind: "incomplete", statusCode: 409, code: "RELEASE_INCOMPLETE", publicDetail: "The release artifacts are incomplete." },
      #{ kind: "not_found", statusCode: 404, code: "RELEASE_NOT_FOUND", publicDetail: "Release not found." },
      #{ kind: "queue_unavailable", statusCode: 503, code: "ASYNC_QUEUE_UNAVAILABLE", publicDetail: "Release finalization could not be queued." },
    ],
  })
  @apigen.auditPayload(ReleaseValidatingAuditPayload)
  finalizeRelease(@path @apigen.target project: string, @path release: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<ReleaseResponse> | CommonErrors;

  @summary("List or stream release events")
  @route("/{release}/events")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PUBLISH_RELEASE" })
  @extension("x-leapview-object-scope", "project-environment")
  @operationId("listReleaseEvents")
  listReleaseEvents(@path project: string, @path release: string, @header accept?: string, @header("Last-Event-ID") lastEventId?: string, @query limit?: ListLimit, @query pageToken?: PageToken): EventHistoryResponse<AsyncEventListResponse> | CommonErrors;
}
