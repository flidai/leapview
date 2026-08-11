# APIGen 0.3.1 Findings

## Summary

APIGen v0.3.0 successfully moved authoring from CUE to TypeSpec, but some generator constraints still reflect the old CUE/JSON-first model. For v0.3.1, the main goal should be to follow `@typespec/http` semantics more directly, then adapt those resolved HTTP shapes into APIGen IR, OpenAPI, Go server code, request models, and CLI metadata.

The guiding rule should be:

```text
TypeSpec HTTP semantics -> APIGen IR transport shape -> Go/OpenAPI/CLI emitters
```

APIGen should not invent a browser-body abstraction directly. Browser `BodyInit` is useful context, but TypeSpec's HTTP library is the contract source of truth.

## Example Contract

Use the LibreDash TypeSpec files as the working 0.3.1 example set:

```text
/Users/yacobolo/.codex/worktrees/95b3/libredash/api/typespec
```

That contract includes the concrete migration cases this review is about: conventional TypeSpec service files split by domain, repeated JSON success/error responses, optional request bodies, and a deployment artifact upload endpoint that wants an octet-stream body without pretending the payload is JSON.

## TypeSpec Alignment Notes

The current LibreDash syntax is mostly valid TypeSpec, but it is more APIGen-shaped than it needs to be. The local TypeSpec HTTP library already defines reusable HTTP response and body models such as `Response<Status>`, `Body<Type>`, `OkResponse`, `CreatedResponse`, `NoContentResponse`, `BadRequestResponse`, `UnauthorizedResponse`, `ForbiddenResponse`, `NotFoundResponse`, and `ConflictResponse`.

APIGen should support those standard helpers directly. For example, users should be able to write this TypeSpec-native response shape:

```typespec
model ListPrincipalsOK {
  ...OkResponse;
  ...Body<PrincipalListResponse>;
}

model RateLimited {
  ...Response<429>;
  ...Body<Error>;
}
```

instead of repeating `@statusCode` and `@body` properties manually in every operation-specific response model.

The same applies to shared response unions. LibreDash currently has many long return unions. APIGen should support normal TypeSpec aliases so API authors can write:

```typespec
alias CommonErrors =
  BadRequest | Unauthorized | Forbidden | NotFound | Conflict | RateLimited | InternalServerError;

op listPrincipals(...): ListPrincipalsOK | CommonErrors;
```

Route organization should also follow TypeSpec conventions. `@route` can be applied to operations, namespaces, and interfaces, so APIGen should tolerate route containers instead of forcing every operation to repeat the full path prefix.

## Findings

1. Request bodies are still named-schema and JSON biased.

   The TypeSpec emitter only accepts `bodyKind === "single"` and maps request bodies through `namedSchemaRef`. This blocks TypeSpec-native raw bytes, `Http.File`, multipart bodies, and some scalar body shapes even when `@typespec/http` has already resolved them safely.

2. `bytes` is not media-type aware.

   The emitter maps `bytes` to OpenAPI-style `type: string`, `format: byte` everywhere. That is right for JSON/base64 contexts, but TypeSpec treats bytes in `application/octet-stream` or file-body contexts as raw bytes. APIGen needs to distinguish base64 bytes from binary bytes.

3. Server generation assumes JSON request bodies.

   Generated strict server code names request body aliases `JSONBody` and always decodes bodies with JSON decoding. This prevents accurate support for binary upload, plain text, file, form, or multipart request bodies.

4. Response generation is also JSON biased.

   APIGen IR has `content_type`, and OpenAPI emission respects it, but generated server response writers still tend to set `Content-Type: application/json` and JSON-encode response values. File downloads, text responses, and binary responses should be represented by TypeSpec HTTP semantics instead.

5. Multipart and form bodies are currently absent.

   TypeSpec supports multipart requests through `@multipartBody` and `HttpPart<T>`, and content type selection through `@header contentType`. APIGen currently rejects these body kinds before they can reach IR.

6. Vendor extensions are a useful escape hatch, but should not carry core HTTP semantics.

   Extensions such as `x-libredash-dispatch: raw-body` are fine for application-specific dispatch policy. They should not be required to describe standard wire contracts such as octet-stream uploads.

7. Optional request body semantics are not fully honored by generated Go server code.

   The TypeSpec emitter records request body `required` from the resolved HTTP body property, but strict server generation still decodes a body whenever an endpoint has a request body shape. TypeSpec `@body body?: T` should allow an absent or empty body and pass a nil body pointer to handlers.

8. Standard TypeSpec response helpers should be first-class.

   APIGen should support `Response<Status>`, `Body<T>`, `OkResponse`, `CreatedResponse`, and the standard error response helpers as normal response model building blocks. Users should not need to hand-author equivalent `@statusCode` properties just to fit APIGen.

9. Response content handling currently loses TypeSpec information.

   The TypeSpec emitter selects the first response content variant. TypeSpec can represent multiple content variants for a response, and APIGen should either preserve them in IR or reject unsupported combinations with a clear diagnostic.

10. Reusable TypeSpec syntax should work without APIGen-specific flattening workarounds.

   Aliased response unions, container routes, and shared response/body templates are quality and maintainability features in TypeSpec. APIGen should adapt to those resolved shapes instead of requiring copied long unions or repeated route prefixes.

## Recommended 0.3.1 Scope

Prioritize a small TypeSpec-aligned slice:

1. Honor optional request bodies.
   - Treat TypeSpec `@body body?: T` as genuinely optional in generated Go handlers.
   - Allow absent or empty bodies for optional request bodies.
   - Keep required JSON body behavior unchanged for `@body body: T`.

2. Support raw binary request bodies.
   - Accept TypeSpec `bytes` request bodies with explicit non-JSON content type, especially `application/octet-stream`.
   - Prefer a stable named schema in APIGen IR where generated code needs one.
   - Emit OpenAPI as `type: string`, `format: binary` for raw binary bodies.

3. Support `Http.File` request/response bodies enough to describe file upload/download contracts accurately.
   - Treat file contents as raw body bytes.
   - Preserve content type from TypeSpec HTTP metadata where available.

4. Make Go server request-body generation transport-aware.
   - JSON bodies continue to use strict JSON decoding.
   - Binary/file bodies should expose `[]byte` or a reader-oriented type.
   - Avoid calling non-JSON bodies `JSONBody`.

5. Support standard TypeSpec response helpers in response models.
   - Accept response models composed from `Response<Status>` and `Body<T>`.
   - Accept common status helpers such as `OkResponse`, `CreatedResponse`, and `NoContentResponse`.
   - Preserve current explicit `@statusCode`/`@body` syntax for compatibility.

6. Keep failure-closed behavior.
   - Reject unsupported body kinds with clear TypeSpec/APIGen diagnostics.
   - Do not silently coerce multipart, form, or arbitrary media types into JSON.

## Recommended Authoring Style

For 0.3.1 examples and docs, prefer TypeSpec-native HTTP constructs:

- Use `Response<Status>` and `Body<T>` or built-in response helpers instead of hand-writing status/body properties everywhere.
- Use aliases for repeated response unions such as common error sets.
- Use `Http.File` or raw `bytes` with explicit media type for octet-stream payloads.
- Use optional request bodies with `@body body?: T` where the wire contract allows no body.
- Use route containers for repeated path prefixes once APIGen has test coverage for namespace/interface `@route` composition.

## Later Work

These are valuable, but should probably follow after the binary/file slice:

- `text/plain` scalar string bodies.
- `application/x-www-form-urlencoded` model bodies.
- `multipart/form-data` with `@multipartBody` and `HttpPart<T>`.
- Non-JSON response writers for text, binary, and file responses.
- Multiple response content variants and content negotiation.
- Shared query/path parameter models if TypeSpec's resolved HTTP operation shape preserves them cleanly enough for APIGen.
- CLI behavior for non-JSON bodies, especially whether file upload input should be path-based, stdin-based, or both.

## Issue Context

Issue #15 shows the concrete migration gap:

- LibreDash has an artifact upload endpoint that accepts raw bytes with `Content-Type: application/octet-stream`.
- CUE/APIGen v0.2.0 could describe this as a named string-like schema plus octet-stream request body.
- APIGen v0.3.0 TypeSpec rejects natural raw `bytes` bodies or forces a fake JSON wrapper model.

The 0.3.1 fix should let TypeSpec describe the real wire contract without requiring handwritten raw-body extensions for standard HTTP semantics.
