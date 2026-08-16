using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.ManagedData;

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model ManagedDataS3MultipartAuditPayload {
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

@minLength(1)
@maxLength(160)
scalar ManagedDataMultipartUploadId extends string;

@minValue(1)
@maxValue(10000)
scalar ManagedDataS3PartNumber extends int32;

enum ManagedDataS3MultipartStatus {
  open;
  completed;
  aborted;
}

@doc("Negotiated S3 multipart transport. Commands below manage transport only; upload-session finalization remains a separate lifecycle operation.")
model ManagedDataS3MultipartNegotiation {
  createEndpoint: ManagedDataEndpoint;
  minimumPartSize: ManagedDataPositiveByteCount;
  maximumPartSize: ManagedDataPositiveByteCount;
  maximumParts: ManagedDataS3PartNumber;
}

model ManagedDataS3MultipartCreateRequest {
  path: ManagedDataLogicalPath;
}

model ManagedDataS3MultipartUploadResponse {
  id: ManagedDataMultipartUploadId;
  uploadSessionId: ManagedDataUploadSessionId;
  file: ManagedDataFileMetadata;
  status: ManagedDataS3MultipartStatus;
  existing: boolean;
  createdAt: utcDateTime;
  expiresAt?: utcDateTime;
}

model ManagedDataS3MultipartSignPartRequest {
  size: ManagedDataPositiveByteCount;
  @pattern("^[0-9a-f]{64}$")
  sha256?: ManagedDataFileSha256;
}

model ManagedDataS3MultipartSignedPartResponse {
  partNumber: ManagedDataS3PartNumber;
  url: ManagedDataEndpoint;
  @maxItems(32)
  headers: ManagedDataHTTPHeader[];
  expiresAt: utcDateTime;
}

model ManagedDataS3CompletedPart {
  partNumber: ManagedDataS3PartNumber;
  @minLength(1)
  @maxLength(1024)
  etag: string;
  @pattern("^[0-9a-f]{64}$")
  sha256?: ManagedDataFileSha256;
}

model ManagedDataS3MultipartCompleteRequest {
  @minItems(1)
  @maxItems(10000)
  parts: ManagedDataS3CompletedPart[];
}

@tag("Managed Data")
@doc("S3 multipart transport lifecycle. Part signing is a query because it issues transient upload authorization without mutating durable multipart state.")
@route("/projects/{project}/connections/{connection}/upload-sessions/{uploadSession}/s3-multipart-uploads")
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_EDIT" })
@apigen.commandDefaults(#{ guarantee: "best-effort" })
@apigen.auditPayload(ManagedDataS3MultipartAuditPayload)
interface ManagedDataS3MultipartUploads {
  @summary("Create an S3 multipart upload for a session file")
  @post
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.failsWith(ManagedDataUploadInvalidFailure)
  @apigen.failsWith(ManagedDataUploadNotFoundFailure)
  @apigen.failsWith(ManagedDataUploadConflictFailure)
  @apigen.failsWith(ManagedDataUploadIncompleteFailure)
  @apigen.failsWith(ManagedDataUploadExpiredFailure)
  @apigen.failsWith(ManagedDataUploadIntegrityFailure)
  @apigen.failsWith(ManagedDataBackendUnavailableFailure)
  @apigen.failsWith(ManagedDataUploadUnavailableFailure)
  @apigen.failsWith(ManagedDataUploadInternalFailure)
  @apigen.command(#{ auditAction: "managed_data.s3_multipart_upload.created" })
  createManagedDataS3MultipartUpload(
    @path project: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path uploadSession: ManagedDataUploadSessionId,
    @header("Idempotency-Key") idempotencyKey: ManagedDataIdempotencyKey,
    @body body: ManagedDataS3MultipartCreateRequest,
  ): CreatedJson<ManagedDataS3MultipartUploadResponse> | CommonErrors;

  @summary("Sign one S3 multipart upload part")
  @doc("Query: issues a short-lived signed part-upload request and does not change durable multipart state.")
  @route("/{multipartUpload}/parts/{partNumber}/sign")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_EDIT" })
  @apigen.query
  @extension("x-leapview-object-scope", "project-environment")
  signManagedDataS3MultipartPart(
    @path project: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path uploadSession: ManagedDataUploadSessionId,
    @path multipartUpload: ManagedDataMultipartUploadId,
    @path partNumber: ManagedDataS3PartNumber,
    @header("Idempotency-Key") idempotencyKey: ManagedDataIdempotencyKey,
    @body body: ManagedDataS3MultipartSignPartRequest,
  ): OkJson<ManagedDataS3MultipartSignedPartResponse> | CommonErrors;

  @summary("Complete an S3 multipart upload")
  @route("/{multipartUpload}/complete")
  @post
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
  @apigen.command(#{ auditAction: "managed_data.s3_multipart_upload.completed" })
  completeManagedDataS3MultipartUpload(
    @path project: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path uploadSession: ManagedDataUploadSessionId,
    @path multipartUpload: ManagedDataMultipartUploadId,
    @header("Idempotency-Key") idempotencyKey: ManagedDataIdempotencyKey,
    @body body: ManagedDataS3MultipartCompleteRequest,
  ): OkJson<ManagedDataS3MultipartUploadResponse> | CommonErrors;

  @summary("Abort an S3 multipart upload")
  @route("/{multipartUpload}/abort")
  @post
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.failsWith(ManagedDataUploadInvalidFailure)
  @apigen.failsWith(ManagedDataUploadNotFoundFailure)
  @apigen.failsWith(ManagedDataUploadConflictFailure)
  @apigen.failsWith(ManagedDataUploadIncompleteFailure)
  @apigen.failsWith(ManagedDataUploadExpiredFailure)
  @apigen.failsWith(ManagedDataUploadIntegrityFailure)
  @apigen.failsWith(ManagedDataBackendUnavailableFailure)
  @apigen.failsWith(ManagedDataUploadUnavailableFailure)
  @apigen.failsWith(ManagedDataUploadInternalFailure)
  @apigen.command(#{ auditAction: "managed_data.s3_multipart_upload.aborted" })
  abortManagedDataS3MultipartUpload(
    @path project: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path uploadSession: ManagedDataUploadSessionId,
    @path multipartUpload: ManagedDataMultipartUploadId,
    @header("Idempotency-Key") idempotencyKey: ManagedDataIdempotencyKey,
  ): OkJson<ManagedDataS3MultipartUploadResponse> | CommonErrors;
}
