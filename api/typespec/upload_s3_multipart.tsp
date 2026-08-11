using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.ManagedData;

model ManagedDataS3MultipartAuditPayload {
  @apigen.sensitivity("internal")
  operationId: string;

  @apigen.sensitivity("internal")
  owner: string;

  @apigen.sensitivity("internal")
  projectId: string;

  @apigen.sensitivity("internal")
  connectionId: string;

  @apigen.sensitivity("public")
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
interface ManagedDataS3MultipartUploads {
  @summary("Create an S3 multipart upload for a session file")
  @post
  @operationId("createManagedDataS3MultipartUpload")
  @apigen.authz(#{ mode: "privilege", privilege: "INGEST_DATA" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "managed_data.s3_multipart_upload.created", guarantee: "best-effort" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "MANAGED_DATA_UPLOAD_INVALID", publicDetail: "The managed-data upload request is invalid." },
      #{ kind: "not_found", statusCode: 404, code: "MANAGED_DATA_UPLOAD_NOT_FOUND", publicDetail: "Managed-data upload resource not found." },
      #{ kind: "conflict", statusCode: 409, code: "MANAGED_DATA_UPLOAD_CONFLICT", publicDetail: "Managed-data upload conflicts with its current state." },
      #{ kind: "incomplete", statusCode: 409, code: "UPLOAD_INCOMPLETE", publicDetail: "Managed-data upload is incomplete." },
      #{ kind: "expired", statusCode: 409, code: "UPLOAD_EXPIRED", publicDetail: "Managed-data upload has expired." },
      #{ kind: "integrity", statusCode: 409, code: "UPLOAD_INTEGRITY_FAILED", publicDetail: "Managed-data upload failed integrity verification." },
      #{ kind: "backend", statusCode: 502, code: "BACKEND_UNAVAILABLE", publicDetail: "Managed-data storage is unavailable." },
      #{ kind: "unavailable", statusCode: 503, code: "MANAGED_DATA_UPLOAD_SERVICE_UNAVAILABLE", publicDetail: "Managed-data upload service is unavailable." },
      #{ kind: "internal", statusCode: 500, code: "MANAGED_DATA_UPLOAD_INTERNAL_ERROR", publicDetail: "Managed-data upload could not be completed." },
    ],
    targetParameter: "project",
  })
  @apigen.auditPayload(ManagedDataS3MultipartAuditPayload, #{ schemaVersion: 1, retention: "security" })
  createManagedDataS3MultipartUpload(
    @path project: ManagedDataProjectId,
    @path connection: ManagedDataConnectionId,
    @path uploadSession: ManagedDataUploadSessionId,
    @header("Idempotency-Key") idempotencyKey: ManagedDataIdempotencyKey,
    @body body: ManagedDataS3MultipartCreateRequest,
  ): CreatedJson<ManagedDataS3MultipartUploadResponse> | CommonErrors;

  @summary("Sign one S3 multipart upload part")
  @doc("Query: issues a short-lived signed part-upload request and does not change durable multipart state.")
  @route("/{multipartUpload}/parts/{partNumber}/sign")
  @post
  @operationId("signManagedDataS3MultipartPart")
  @apigen.authz(#{ mode: "privilege", privilege: "INGEST_DATA" })
  @apigen.query
  @extension("x-leapview-object-scope", "project-environment")
  signManagedDataS3MultipartPart(
    @path project: ManagedDataProjectId,
    @path connection: ManagedDataConnectionId,
    @path uploadSession: ManagedDataUploadSessionId,
    @path multipartUpload: ManagedDataMultipartUploadId,
    @path partNumber: ManagedDataS3PartNumber,
    @header("Idempotency-Key") idempotencyKey: ManagedDataIdempotencyKey,
    @body body: ManagedDataS3MultipartSignPartRequest,
  ): OkJson<ManagedDataS3MultipartSignedPartResponse> | CommonErrors;

  @summary("Complete an S3 multipart upload")
  @route("/{multipartUpload}/complete")
  @post
  @operationId("completeManagedDataS3MultipartUpload")
  @apigen.authz(#{ mode: "privilege", privilege: "INGEST_DATA" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "managed_data.s3_multipart_upload.completed", guarantee: "best-effort" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "MANAGED_DATA_UPLOAD_INVALID", publicDetail: "The managed-data upload request is invalid." },
      #{ kind: "not_found", statusCode: 404, code: "MANAGED_DATA_UPLOAD_NOT_FOUND", publicDetail: "Managed-data upload resource not found." },
      #{ kind: "conflict", statusCode: 409, code: "MANAGED_DATA_UPLOAD_CONFLICT", publicDetail: "Managed-data upload conflicts with its current state." },
      #{ kind: "incomplete", statusCode: 409, code: "UPLOAD_INCOMPLETE", publicDetail: "Managed-data upload is incomplete." },
      #{ kind: "expired", statusCode: 409, code: "UPLOAD_EXPIRED", publicDetail: "Managed-data upload has expired." },
      #{ kind: "integrity", statusCode: 409, code: "UPLOAD_INTEGRITY_FAILED", publicDetail: "Managed-data upload failed integrity verification." },
      #{ kind: "too_large", statusCode: 413, code: "MANAGED_DATA_UPLOAD_TOO_LARGE", publicDetail: "Managed-data upload request is too large." },
      #{ kind: "backend", statusCode: 502, code: "BACKEND_UNAVAILABLE", publicDetail: "Managed-data storage is unavailable." },
      #{ kind: "unavailable", statusCode: 503, code: "MANAGED_DATA_UPLOAD_SERVICE_UNAVAILABLE", publicDetail: "Managed-data upload service is unavailable." },
      #{ kind: "internal", statusCode: 500, code: "MANAGED_DATA_UPLOAD_INTERNAL_ERROR", publicDetail: "Managed-data upload could not be completed." },
    ],
    targetParameter: "project",
  })
  @apigen.auditPayload(ManagedDataS3MultipartAuditPayload, #{ schemaVersion: 1, retention: "security" })
  completeManagedDataS3MultipartUpload(
    @path project: ManagedDataProjectId,
    @path connection: ManagedDataConnectionId,
    @path uploadSession: ManagedDataUploadSessionId,
    @path multipartUpload: ManagedDataMultipartUploadId,
    @header("Idempotency-Key") idempotencyKey: ManagedDataIdempotencyKey,
    @body body: ManagedDataS3MultipartCompleteRequest,
  ): OkJson<ManagedDataS3MultipartUploadResponse> | CommonErrors;

  @summary("Abort an S3 multipart upload")
  @route("/{multipartUpload}/abort")
  @post
  @operationId("abortManagedDataS3MultipartUpload")
  @apigen.authz(#{ mode: "privilege", privilege: "INGEST_DATA" })
  @extension("x-leapview-object-scope", "project-environment")
  @apigen.command(#{
    audit: #{ required: true, successAction: "managed_data.s3_multipart_upload.aborted", guarantee: "best-effort" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "MANAGED_DATA_UPLOAD_INVALID", publicDetail: "The managed-data upload request is invalid." },
      #{ kind: "not_found", statusCode: 404, code: "MANAGED_DATA_UPLOAD_NOT_FOUND", publicDetail: "Managed-data upload resource not found." },
      #{ kind: "conflict", statusCode: 409, code: "MANAGED_DATA_UPLOAD_CONFLICT", publicDetail: "Managed-data upload conflicts with its current state." },
      #{ kind: "incomplete", statusCode: 409, code: "UPLOAD_INCOMPLETE", publicDetail: "Managed-data upload is incomplete." },
      #{ kind: "expired", statusCode: 409, code: "UPLOAD_EXPIRED", publicDetail: "Managed-data upload has expired." },
      #{ kind: "integrity", statusCode: 409, code: "UPLOAD_INTEGRITY_FAILED", publicDetail: "Managed-data upload failed integrity verification." },
      #{ kind: "backend", statusCode: 502, code: "BACKEND_UNAVAILABLE", publicDetail: "Managed-data storage is unavailable." },
      #{ kind: "unavailable", statusCode: 503, code: "MANAGED_DATA_UPLOAD_SERVICE_UNAVAILABLE", publicDetail: "Managed-data upload service is unavailable." },
      #{ kind: "internal", statusCode: 500, code: "MANAGED_DATA_UPLOAD_INTERNAL_ERROR", publicDetail: "Managed-data upload could not be completed." },
    ],
    targetParameter: "project",
  })
  @apigen.auditPayload(ManagedDataS3MultipartAuditPayload, #{ schemaVersion: 1, retention: "security" })
  abortManagedDataS3MultipartUpload(
    @path project: ManagedDataProjectId,
    @path connection: ManagedDataConnectionId,
    @path uploadSession: ManagedDataUploadSessionId,
    @path multipartUpload: ManagedDataMultipartUploadId,
    @header("Idempotency-Key") idempotencyKey: ManagedDataIdempotencyKey,
  ): OkJson<ManagedDataS3MultipartUploadResponse> | CommonErrors;
}
