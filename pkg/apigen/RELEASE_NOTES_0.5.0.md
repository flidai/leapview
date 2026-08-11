# APIGen v0.5.0

APIGen v0.5.0 is a breaking contract release centered on lossless schema generation and an application-owned transport-error boundary.

## Added

- JSON IR v4 schema composition with `base`, `one_of`, and explicit discriminator mappings.
- TypeSpec support for closed `@discriminator` inheritance and named `@discriminated` unions.
- Strict generated Go tagged-union wrappers with deterministic marshal/unmarshal behavior and rejection of missing tags, unknown tags, unknown fields, and missing required variant properties.
- `@apigen.transportErrors` for selecting an authored error schema, media type, and per-failure public policy.
- Injected `GenTransportErrorResponder` support for request IDs, RFC 9457 responses, structured violations, and consumer-owned observability.

## Fixed

- `Record<T>` value types are retained recursively instead of degrading to `map[string]any` in request models.
- `int64` remains `int64` instead of narrowing to `int32` in generated request models.
- Union and inheritance semantics now reach canonical and embedded OpenAPI, Go and TypeScript models, draft 2020-12 JSON Schema, and agent-tool schemas.
- Generated CLI parameter, body-field, multipart-part, and request-body metadata now carries expanded portable JSON Schema, including nested union and typed map values.
- JSON response bodies are marshaled before response headers are committed, allowing serialization failures to follow the configured responder path.
- Internal handler and serialization causes remain available to responders but are not exposed as public details.

## Breaking changes

- JSON IR v4 is the only accepted version; v3 compatibility is intentionally removed.
- `RegisterAPIGenStrictRoutes` and strict dispatch functions require a `GenTransportErrorResponder`.
- Legacy generated shared error wrappers and the hard-coded `Error{Code, Message}` transport writer are removed.
- Inherited TypeSpec properties are represented through composition rather than copied into derived schema property lists.

Regenerate all IR, OpenAPI, server, request-model, CLI, and contract artifacts after upgrading.
