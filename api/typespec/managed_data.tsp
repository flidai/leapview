using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.ManagedData;

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model ManagedDataCommandAuditPayload {
  @apigen.auditInternal
  operationId: string;

  @apigen.auditInternal
  owner: string;

  @apigen.auditInternal
  projectId: ResourceId;

  @apigen.auditInternal
  connectionId: ResourceId;

  @apigen.auditPublic
  surface: string;
}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 400, code: "MANAGED_DATA_UPLOAD_INVALID", publicDetail: "The managed-data upload request is invalid." })
model ManagedDataUploadInvalidFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "MANAGED_DATA_UPLOAD_NOT_FOUND", publicDetail: "Managed-data upload resource not found." })
model ManagedDataUploadNotFoundFailure {}

@apigen.failureDefinition(#{ kind: "conflict", statusCode: 409, code: "MANAGED_DATA_UPLOAD_CONFLICT", publicDetail: "Managed-data upload conflicts with its current state." })
model ManagedDataUploadConflictFailure {}

@apigen.failureDefinition(#{ kind: "incomplete", statusCode: 409, code: "UPLOAD_INCOMPLETE", publicDetail: "Managed-data upload is incomplete." })
model ManagedDataUploadIncompleteFailure {}

@apigen.failureDefinition(#{ kind: "expired", statusCode: 409, code: "UPLOAD_EXPIRED", publicDetail: "Managed-data upload has expired." })
model ManagedDataUploadExpiredFailure {}

@apigen.failureDefinition(#{ kind: "integrity", statusCode: 409, code: "UPLOAD_INTEGRITY_FAILED", publicDetail: "Managed-data upload failed integrity verification." })
model ManagedDataUploadIntegrityFailure {}

@apigen.failureDefinition(#{ kind: "too_large", statusCode: 413, code: "MANAGED_DATA_UPLOAD_TOO_LARGE", publicDetail: "Managed-data upload request is too large." })
model ManagedDataUploadTooLargeFailure {}

@apigen.failureDefinition(#{ kind: "backend", statusCode: 502, code: "BACKEND_UNAVAILABLE", publicDetail: "Managed-data storage is unavailable." })
model ManagedDataBackendUnavailableFailure {}

@apigen.failureDefinition(#{ kind: "unavailable", statusCode: 503, code: "MANAGED_DATA_UPLOAD_SERVICE_UNAVAILABLE", publicDetail: "Managed-data upload service is unavailable." })
model ManagedDataUploadUnavailableFailure {}

@apigen.failureDefinition(#{ kind: "internal", statusCode: 500, code: "MANAGED_DATA_UPLOAD_INTERNAL_ERROR", publicDetail: "Managed-data upload could not be completed." })
model ManagedDataUploadInternalFailure {}

model ManagedConnectionResponse {
  id: ResourceId;
  projectId: ResourceId;
  title: string;
  description?: string;
  activeRevisionId?: string;
}

model ManagedConnectionListResponse {
  items: ManagedConnectionResponse[];
  page: PageInfo;
}

@tag("Managed Data")
@doc("Read-only discovery of project managed connections.")
@route("/projects/{project}/connections")
interface ManagedConnections {
  @summary("List project managed connections")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "project-environment")
  listManagedConnections(@path project: ResourceId, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<ManagedConnectionListResponse> | CommonErrors;

  @summary("Describe a project managed connection")
  @route("/{connection}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project-environment")
  getManagedConnection(@path project: ResourceId, @path @apigen.target connection: ResourceId): OkJson<ManagedConnectionResponse> | CommonErrors;
}


@minLength(1)
@maxLength(160)
scalar ManagedDataServingStateId extends string;

@minLength(1)
@maxLength(160)
scalar ManagedDataUploadSessionId extends string;

@minLength(1)
@maxLength(255)
scalar ManagedDataIdempotencyKey extends string;

@minLength(1)
@maxLength(2048)
scalar ManagedDataPageToken extends string;

@minLength(1)
@maxLength(1024)
@pattern("^(?!/)(?!.*(?:^|/)\\.\\.(?:/|$))(?!.*\\\\).+$")
scalar ManagedDataLogicalPath extends string;

@pattern("^[0-9a-f]{64}$")
@minLength(64)
@maxLength(64)
@format("sha256")
scalar ManagedDataFileSha256 extends string;

@pattern("^sha256:[0-9a-f]{64}$")
@minLength(71)
@maxLength(71)
@format("sha256-revision")
scalar ManagedDataRevisionId extends string;

@minValue(0)
scalar ManagedDataByteCount extends int64;

@minValue(1)
scalar ManagedDataPositiveByteCount extends int64;

@minValue(1)
@maxValue(10000)
scalar ManagedDataFileCount extends int32;

@minValue(1)
@maxValue(200)
scalar ManagedDataPageLimit extends int32;

@minLength(1)
@maxLength(8192)
scalar ManagedDataEndpoint extends string;

@minLength(1)
@maxLength(256)
scalar ManagedDataHeaderName extends string;

@minLength(1)
@maxLength(8192)
scalar ManagedDataHeaderValue extends string;

@minLength(1)
@maxLength(2048)
scalar ManagedDataErrorMessage extends string;

enum ManagedDataRevisionStatus {
  available;
}

enum ManagedDataUploadSessionStatus {
  open;
  finalizing;
  completed;
  cancelled;
  failed;
  expired;
}

enum ManagedDataFileUploadStatus {
  pending;
  uploading;
  uploaded;
  verified;
  skipped;
  failed;
}

enum ManagedDataUploadProtocol {
  tus;
  s3Multipart: "s3_multipart";
  alreadyPresent: "already_present";
}

model ManagedDataFileMetadata {
  @pattern("^(?!/)(?!.*(?:^|/)\\.\\.(?:/|$))(?!.*\\\\).+$")
  path: ManagedDataLogicalPath;
  size: ManagedDataByteCount;
  @doc("A lowercase 64-character hexadecimal SHA-256 digest.")
  @pattern("^[0-9a-f]{64}$")
  @minLength(64)
  @maxLength(64)
  @format("sha256")
  sha256: ManagedDataFileSha256;
}

model ManagedDataManifest {
  @minItems(1)
  @maxItems(10000)
  files: ManagedDataFileMetadata[];
}

model ManagedDataRevisionSummaryResponse {
  @doc("The immutable revision identity, formatted as sha256 followed by a colon and 64 lowercase hexadecimal characters.")
  @pattern("^sha256:[0-9a-f]{64}$")
  @minLength(71)
  @maxLength(71)
  @format("sha256-revision")
  id: ManagedDataRevisionId;
  status: ManagedDataRevisionStatus;
  fileCount: ManagedDataFileCount;
  size: ManagedDataByteCount;
  createdAt: utcDateTime;
  uploadSessionId: ManagedDataUploadSessionId;
}

model ManagedDataRevisionResponse {
  @doc("The immutable revision identity, formatted as sha256 followed by a colon and 64 lowercase hexadecimal characters.")
  @pattern("^sha256:[0-9a-f]{64}$")
  @minLength(71)
  @maxLength(71)
  @format("sha256-revision")
  id: ManagedDataRevisionId;
  status: ManagedDataRevisionStatus;
  manifest: ManagedDataManifest;
  fileCount: ManagedDataFileCount;
  size: ManagedDataByteCount;
  createdAt: utcDateTime;
  uploadSessionId: ManagedDataUploadSessionId;
}

model ManagedDataRevisionListResponse {
  @maxItems(200)
  items: ManagedDataRevisionSummaryResponse[];
  page: PageInfo;
}

model ManagedDataActiveRevisionResponse {
  revision?: ManagedDataRevisionSummaryResponse;
  deploymentId?: string;
  activatedAt?: utcDateTime;
}

model ManagedDataHTTPHeader {
  name: ManagedDataHeaderName;
  value: ManagedDataHeaderValue;
}

@doc("Backend-neutral upload instructions. Exactly one protocol-specific object is present for tus or S3 multipart; already-present files have neither.")
model ManagedDataUploadNegotiation {
  protocol: ManagedDataUploadProtocol;
  tus?: ManagedDataTusUploadNegotiation;
  s3Multipart?: ManagedDataS3MultipartNegotiation;
}

model ManagedDataFileUploadResponse {
  file: ManagedDataFileMetadata;
  status: ManagedDataFileUploadStatus;
  @doc("Upload instructions, present only while the file can still be uploaded.")
  negotiation?: ManagedDataUploadNegotiation;
  error?: ManagedDataErrorMessage;
}

model ManagedDataUploadSessionCreateRequest {
  manifest: ManagedDataManifest;
}

model ManagedDataUploadSessionResponse {
  id: ManagedDataUploadSessionId;
  project: ResourceId;
  connection: ResourceId;
  @pattern("^sha256:[0-9a-f]{64}$")
  revisionId: ManagedDataRevisionId;
  status: ManagedDataUploadSessionStatus;
  manifest: ManagedDataManifest;
  @minItems(1)
  @maxItems(10000)
  files: ManagedDataFileUploadResponse[];
  createdAt: utcDateTime;
  expiresAt: utcDateTime;
  completedAt?: utcDateTime;
  error?: ManagedDataErrorMessage;
}

model ManagedDataUploadSessionListResponse {
  items: ManagedDataUploadSessionResponse[];
  page: PageInfo;
}

@tag("Managed Data")
@doc("Managed-data revision queries and upload-session lifecycle commands. Revision reads, session reads, and event streaming do not mutate durable domain state.")
@route("/projects/{project}/connections/{connection}")
@apigen.commandDefaults(#{ guarantee: "best-effort" })
@apigen.auditPayload(ManagedDataCommandAuditPayload)
interface ManagedDataLifecycle {
  @summary("Get the active managed-data revision")
  @route("/active-revision")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project-environment")
  getActiveManagedDataRevision(
    @path project: ResourceId,
    @path @apigen.target connection: ResourceId,
  ): OkJson<ManagedDataActiveRevisionResponse> | CommonErrors;

  @summary("List immutable managed-data revisions")
  @route("/revisions")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project-environment")
  listManagedDataRevisions(
    @path project: ResourceId,
    @path @apigen.target connection: ResourceId,
    @query limit?: ManagedDataPageLimit,
    @query pageToken?: ManagedDataPageToken,
  ): OkJson<ManagedDataRevisionListResponse> | CommonErrors;

  @summary("Get an immutable managed-data revision")
  @route("/revisions/{revision}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project-environment")
  getManagedDataRevision(
    @path project: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path revision: ManagedDataRevisionId,
  ): OkJson<ManagedDataRevisionResponse> | CommonErrors;

  @summary("Create a managed-data upload session from a canonical manifest")
  @route("/upload-sessions")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_EDIT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.failsWith(ManagedDataUploadInvalidFailure)
  @apigen.failsWith(ManagedDataUploadNotFoundFailure)
  @apigen.failsWith(ManagedDataUploadConflictFailure)
  @apigen.failsWith(ManagedDataUploadIncompleteFailure)
  @apigen.failsWith(ManagedDataUploadExpiredFailure)
  @apigen.failsWith(ManagedDataUploadIntegrityFailure)
  @apigen.failsWith(ManagedDataUploadTooLargeFailure)
  @apigen.failsWith(ManagedDataBackendUnavailableFailure)
  @apigen.failsWith(ManagedDataUploadUnavailableFailure)
  @apigen.failsWith(ManagedDataUploadInternalFailure)
  @apigen.command(#{ auditAction: "managed_data.upload_session.created" })
  createManagedDataUploadSession(
    @path project: ResourceId,
    @path @apigen.target connection: ResourceId,
    @header("Idempotency-Key") idempotencyKey: ManagedDataIdempotencyKey,
    @body body: ManagedDataUploadSessionCreateRequest,
  ): CreatedJson<ManagedDataUploadSessionResponse> | CommonErrors;

  @summary("List managed-data upload sessions")
  @route("/upload-sessions")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project-environment")
  listManagedDataUploadSessions(
    @path project: ResourceId,
    @path @apigen.target connection: ResourceId,
    @query limit?: ManagedDataPageLimit,
    @query pageToken?: ManagedDataPageToken,
  ): OkJson<ManagedDataUploadSessionListResponse> | CommonErrors;

  @summary("Get a managed-data upload session")
  @route("/upload-sessions/{uploadSession}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project-environment")
  getManagedDataUploadSession(
    @path project: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path uploadSession: ManagedDataUploadSessionId,
  ): OkJson<ManagedDataUploadSessionResponse> | CommonErrors;

  @summary("Cancel a managed-data upload session")
  @route("/upload-sessions/{uploadSession}/cancel")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_EDIT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.failsWith(ManagedDataUploadInvalidFailure)
  @apigen.failsWith(ManagedDataUploadNotFoundFailure)
  @apigen.failsWith(ManagedDataUploadConflictFailure)
  @apigen.failsWith(ManagedDataUploadIncompleteFailure)
  @apigen.failsWith(ManagedDataUploadExpiredFailure)
  @apigen.failsWith(ManagedDataUploadIntegrityFailure)
  @apigen.failsWith(ManagedDataUploadTooLargeFailure)
  @apigen.failsWith(ManagedDataBackendUnavailableFailure)
  @apigen.failsWith(ManagedDataUploadUnavailableFailure)
  @apigen.failsWith(ManagedDataUploadInternalFailure)
  @apigen.command(#{ auditAction: "managed_data.upload_session.cancelled" })
  cancelManagedDataUploadSession(
    @path project: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path uploadSession: ManagedDataUploadSessionId,
    @header("Idempotency-Key") idempotencyKey: ManagedDataIdempotencyKey,
  ): OkJson<ManagedDataUploadSessionResponse> | CommonErrors;

  @summary("Finalize a managed-data upload session")
  @route("/upload-sessions/{uploadSession}/finalize")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_EDIT" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.failsWith(ManagedDataUploadInvalidFailure)
  @apigen.failsWith(ManagedDataUploadNotFoundFailure)
  @apigen.failsWith(ManagedDataUploadConflictFailure)
  @apigen.failsWith(ManagedDataUploadIncompleteFailure)
  @apigen.failsWith(ManagedDataUploadExpiredFailure)
  @apigen.failsWith(ManagedDataUploadIntegrityFailure)
  @apigen.failsWith(ManagedDataUploadTooLargeFailure)
  @apigen.failsWith(ManagedDataBackendUnavailableFailure)
  @apigen.failsWith(ManagedDataUploadUnavailableFailure)
  @apigen.failsWith(ManagedDataUploadInternalFailure)
  @apigen.asyncExecution(ManagedDataLifecycle.getManagedDataUploadSession, ManagedDataLifecycle.listManagedDataUploadSessionEvents, #{
    guarantee: "transactional",
    jobKind: "upload.finalize",
    resourceKind: "upload",
    initialEvent: "upload_session.finalizing",
    initialState: "finalizing",
    cancellation: "unsupported",
  })
  @apigen.command(#{ auditAction: "managed_data.upload_session.finalization_requested" })
  finalizeManagedDataUploadSession(
    @path project: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path uploadSession: ManagedDataUploadSessionId,
    @header("Idempotency-Key") idempotencyKey: ManagedDataIdempotencyKey,
  ): AcceptedJson<ManagedDataUploadSessionResponse> | CommonErrors;

  @summary("List or stream upload-session events")
  @route("/upload-sessions/{uploadSession}/events")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "project-environment")
  listManagedDataUploadSessionEvents(
    @path project: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path uploadSession: ManagedDataUploadSessionId,
    @header accept?: string,
    @header("Last-Event-ID") lastEventId?: string,
    @query limit?: ManagedDataPageLimit,
    @query pageToken?: ManagedDataPageToken,
  ): EventHistoryResponse<AsyncEventListResponse> | CommonErrors;
}
