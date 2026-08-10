# APIGen 0.3.2 Release Notes

## Breaking Changes

- JSON IR moves from schema version `v1` to `v2`.
- Request and response bodies are no longer assumed to be JSON.
- `request_body.schema` and `request_body.content_type` are replaced by `request_body.contents[]`.
- `response.schema`, `response.content_type`, and `response.any_of` are replaced by `response.contents[]`.
- Generated non-JSON request and response types no longer use `JSONBody` or `JSONResponse` names.
- Same-status response variants are represented as one IR response with multiple ordered contents.
- Same-status variants with the same `content_type` must be identical; incompatible duplicates now fail closed rather than generating ambiguous Go response types.
- Multi-content generated responses use media-specific concrete names such as `ApplicationJSONResponse` and `ApplicationOctetStreamResponse`.
- Raw `bytes` bodies stay `[]byte`; TypeSpec `Http.File` bodies now generate `GenFile` with `Contents []byte`, optional streaming `Reader io.ReadCloser`, content type, optional filename, and optional size metadata.
- Multipart generated body fields now distinguish required, optional, repeated, named form-data, and ordered mixed tuple parts.
- Generated strict request structs now include typed `Gen<Operation>Headers` for header parameters.
- `GenericRequest` inference is removed; TypeSpec contracts must name the schema they want generated.

## TypeSpec-Native HTTP

APIGen now follows resolved `@typespec/http` semantics for:

- JSON bodies
- `text/plain`
- `application/octet-stream`
- `Http.File`
- `application/x-www-form-urlencoded`
- `multipart/form-data`
- optional request bodies
- multiple response content variants
- path, query, and header parameters in generated server and CLI output
- content negotiation with `@sharedRoute` and same-endpoint `@overload`
- `HttpPart<T>[]` repeated multipart parts
- `HttpPart<T[]>` single JSON-array multipart parts
- `multipart/mixed` tuple parts
- generated CLI multipart input with repeated `--part name=value`, `--part name=@file`, or `--part name=-`
- standard response helpers such as `Response<Status>`, `Body<T>`, `OkResponse`, `CreatedResponse`, and `NoContentResponse`
- aliased response unions
- namespace/interface route containers

Vendor extensions remain available for application-specific metadata, but standard HTTP wire contracts should use TypeSpec HTTP constructs instead of custom raw-body extensions.

OpenAPI output remains valid OpenAPI 3.0. `multipart/form-data` emits object schemas plus encoding metadata. `multipart/mixed` emits a best-effort schema plus APIGen vendor metadata `x-apigen-multipart-kind: "mixed"` and ordered `x-apigen-multipart-parts`.

## Migration

Before:

```json
{
  "request_body": {
    "required": true,
    "content_type": "application/json",
    "schema": {"ref": "CreateWidgetRequest"}
  },
  "responses": [{
    "status_code": 200,
    "description": "ok",
    "schema": {"ref": "Widget"}
  }]
}
```

After:

```json
{
  "request_body": {
    "required": true,
    "contents": [{
      "content_type": "application/json",
      "body_kind": "json",
      "schema": {"ref": "CreateWidgetRequest"}
    }]
  },
  "responses": [{
    "status_code": 200,
    "description": "ok",
    "contents": [{
      "content_type": "application/json",
      "body_kind": "json",
      "schema": {"ref": "Widget"}
    }]
  }]
}
```

Generated Go migration:

- Rename `Gen<Operation>JSONBody` usages to `Gen<Operation>Body`.
- Keep JSON response constructors named `Gen<Operation><Status>JSONResponse`.
- Use `Gen<Operation><Status>TextResponse`, `BinaryResponse`, or `FileResponse` for non-JSON responses.
- For multi-content statuses, use media-specific response constructors such as `GenGetArtifact200ApplicationJSONResponse` or `GenGetArtifact200ApplicationOctetStreamResponse`.
- Multipart request handlers receive `Gen<Operation>Body` aliased to a generated `Gen<Operation>MultipartBody` struct. Required parts are concrete fields; optional single parts are pointers; repeated parts are slices. JSON/form parts decode to generated schema types, text parts decode to `string`, raw bytes decode to `[]byte`, and `Http.File` parts decode to streaming `GenFile`.
- Raw `Http.File` request bodies pass through as `GenFile.Reader`; multipart `Http.File` parts spool to temporary files and are removed after the strict handler bridge returns.
- `GenFile` responses write the authored/default content type, honor runtime `GenFile.ContentType` when present, emit `Content-Disposition` when `Filename` is set, and stream from `Reader` when present.
- Generated CLI supports multipart request bodies with repeated `--part` flags. JSON/form parts accept raw JSON, `@file`, or stdin; text parts accept raw text, `@file`, or stdin; binary/file parts require `@file` or stdin. Repeated parts may repeat the same name and preserve user order within that part.
- Header parameters are generated as CLI flags and sent as HTTP headers. Authored `Accept` and `Content-Type` header parameters override the runtime defaults when supplied.
- Multipart server decoding rejects unknown form-data part names, duplicate non-repeated form-data parts, and extra mixed tuple parts. Repeated form-data parts preserve request order for that part.
- Replace `GenericRequest` wrappers with the concrete TypeSpec model name.

Failure-closed TypeSpec constructs:

- Cookie parameters are rejected until APIGen supports them consistently across IR, OpenAPI, generated server structs, and generated CLI.
- Status ranges such as `*` are rejected; concrete numeric status codes and numeric status-code unions are supported.
- Supported auth is exactly HTTP Bearer auth and `ApiKeyAuth<ApiKeyLocation.header, "X-API-Key">`.
- Basic/Digest/custom HTTP schemes, OAuth/OpenID auth, non-header API-key auth, and header API keys with names other than `X-API-Key` are rejected until APIGen can represent them honestly in all generated surfaces.
- Incompatible duplicate same-status response content variants with the same media type are rejected. Use distinct content types or a single explicit response model; `anyOf` merging is intentionally not inferred in v0.3.2.
- Shared routes/overloads are rejected when auth, APIGen CLI/authz/manual metadata, operation extensions, parameters, or request bodies disagree.

## Preferred TypeSpec

```typespec
using Http;

model OkJson<T> {
  ...OkResponse;
  ...Body<T>;
}

model RateLimited {
  ...Response<429>;
  ...Body<Error>;
}

alias CommonErrors = BadRequest | RateLimited;

@route("/artifacts")
namespace Artifacts {
  @route("/{id}/blob")
  @put
  op replaceBlob(
    @path id: string,
    @header contentType: "application/octet-stream",
    @body body: bytes,
  ): OkJson<Artifact> | CommonErrors;
}
```

LibreDash binary upload migration:

```typespec
// Before: JSON object plus app-specific raw-body extension.
model DeploymentArtifactUploadRequest {
  value: bytes;
}

@extension("x-libredash-dispatch", "raw-body")
op uploadDeploymentArtifact(@body body: DeploymentArtifactUploadRequest): UploadDeploymentArtifactOK | BadRequest | Unauthorized;

// After: standard TypeSpec HTTP transport.
alias CommonErrors = BadRequest | Unauthorized;

@route("/api/v1")
namespace Deployments {
  @route("/workspaces/{workspace}/deployments/{deployment}/artifact")
  @put
  op uploadDeploymentArtifact(
    @path workspace: string,
    @path deployment: string,
    @header contentType: "application/octet-stream",
    @body body: bytes,
  ): OkJson<DeploymentArtifactResponse> | CommonErrors;
}
```

## Compatibility Note

APIGen v0.3.1 remains the pinned all-JSON release. Use v0.3.2 when you want TypeSpec-native HTTP transport semantics and are ready to regenerate/migrate generated Go code.
