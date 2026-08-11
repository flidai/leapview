# APIGen v0.4.0

APIGen v0.4.0 establishes JSON IR v3 as a clean compatibility boundary and adds two contract-first generation lanes: generic data contracts and typed endpoint-derived agent tools.

## Typed Agent Tools

- Adds `@apigen.tool` for TypeSpec operations with portable names, effects, confirmation requirements, tags, typed input bindings, recursive output projections, and `x-*` metadata.
- Derives strict model-visible input schemas from path, query, header, and JSON body fields.
- Supports trusted context bindings, omitted/defaulted fields, aliases, scalar bodies, object bodies, counts, map/array/object projection, and cursors.
- Keeps defaults and transport formats in typed bindings while exporting provider-portable validation schemas without `default` or `format` annotations.
- Rejects unsupported transports, duplicate names, unsafe confirmation, input collisions, incompatible success responses, invalid pointers, and malformed metadata.
- Emits normalized `x-apigen-tool` metadata in canonical OpenAPI.
- Generates defensive `GetAPIGenToolContracts` and `GetAPIGenToolContract` registries in Go server output.
- Adds SDK-neutral `runtime/agenttool` request construction, recursive argument/output validation, stable runtime errors, HTTP failure preservation, and successful response projection.

## Data Contracts

- Adds generic `contracts[]` roots to IR v3 using the shared schema registry.
- Adds `@apigen.package`, `@apigen.contract`, and `@apigen.metadata` TypeSpec decorators.
- Preserves validated schema, property, and contract `x-*` metadata for downstream rules.
- Adds Go model, TypeScript model, and draft 2020-12 JSON Schema emitters.
- Adds contract target support to manifests and the `typespec-compile`/`all` CLI flow.
- Includes a checked-in dashboard signal-envelope example without making the feature signal-specific.

## Breaking Changes

- IR v3 is the only accepted schema version. IR v2 loading, normalization, fixtures, and tests are removed.
- Legacy raw `x-agent` authoring is rejected. Consumers must use typed tool descriptors.
- Tool metadata no longer lives in `GenOperationContract.Extensions`; use the generated tool registry.
- Contract targets do not support HTTP-only `openapi`, `server`, or `cli` commands.

## Verification

Release verification includes TypeSpec emitter tests and type checking, distribution drift checks, all Go package tests, HTTP and contract example smoke tests, and the dependent LibreDash migration against the release candidate.
