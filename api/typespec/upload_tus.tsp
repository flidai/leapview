using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.ManagedData;

@minLength(1)
@maxLength(160)
scalar ManagedDataTusUploadId extends string;

@doc("Negotiated TUS transport. TUS protocol requests update transport progress but are not LeapView application operations; the durable command boundary remains the upload-session lifecycle.")
model ManagedDataTusUploadNegotiation {
  endpoint: ManagedDataEndpoint;
  uploadId: ManagedDataTusUploadId;
  offset: ManagedDataByteCount;
  expiresAt: utcDateTime;
}
