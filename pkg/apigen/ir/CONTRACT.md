# JSON IR Contract (`v4`)

`github.com/Yacobolo/toolbelt/apigen/ir` defines the versioned JSON intermediate representation consumed by APIGen.

## Versioning

- Current emitted version: `v4`
- The root document must contain `schema_version: "v4"`
- `ir.Load` rejects every other schema version
- Breaking IR changes require a new schema version
- `v4` defines HTTP endpoints, generic data contract roots, schema composition, explicit transport-error policy, schema metadata, typed endpoint tools, and optional typed command metadata. The command field is additive for synchronized APIGen producers and consumers; older v4 readers that reject unknown fields must be upgraded with the producer.

## Root Document

Required fields:

- `schema_version`
- `info.title`
- `info.version`
- at least one `endpoints` entry or one `contracts` entry

Optional fields:

- `api`
- `info.description`
- `servers`
- `tags`
- `schemas`
- `contracts`
- `endpoints`
- `transport_errors`
- `extensions`

`schemas` is the shared named schema registry for HTTP bodies, parameters, generated model types, and generic contract roots.

## Contracts

`contracts[]` declares named data-contract roots. Contracts are intentionally generic and are not tied to HTTP, Datastar, signals, or any single runtime.

Each contract must define:

- `name`
- `schema` with a resolvable named `ref`

Optional fields:

- `kind`
- `tags`
- `description`
- `extensions`

Contract routes are unique by `name`. Contract-level `extensions` preserve downstream metadata. Generic extensions must use `x-*` keys and JSON-compatible values. APIGen-owned `x-apigen-*` keys are reserved.

HTTP generators ignore `contracts[]`. Model generators select contract roots and the transitive schema dependencies reachable from those roots.

## Endpoints

Each endpoint must define:

- `method`
- `path`
- `operation_id`
- `responses`

Current producers also emit `kind` as `command` or `query`. Consumers must
accept older v4 documents without it; normalization infers `command` when a
command contract exists and `query` otherwise.

Endpoint routes are unique by `lower(method) + " " + path`.
`operation_id` values are unique across the document.
Each endpoint may contain at most one response entry per `status_code`; multiple media variants for the same status belong in that response's ordered `contents`.

An endpoint may define `namespace` as optional ownership metadata. The TypeSpec emitter uses the operation's fully qualified declaring namespace. Consumers must continue to accept v4 documents without endpoint namespaces. Operations coalesced into one shared route must have the same namespace.

Endpoint-level `extensions` preserve operation vendor metadata. Generic extensions must use OpenAPI-style `x-*` keys and JSON-compatible values.

Endpoint parameters support `path`, `query`, and `header` locations. Other locations, including `cookie`, are rejected by IR validation until all generated surfaces support them consistently.

An endpoint may define an optional typed `command` object. It contains normalized ownership, audit policy, optional async execution lifecycle, additional non-HTTP exposures, target path parameter, idempotency and concurrency policies, and authorization mode/privilege. POST commands require a required `Idempotency-Key` header; PATCH commands require a required `If-Match` header. Audit actions use stable dotted lower-snake-case names. Audit `guarantee`, when present, is `transactional` or `best-effort`: transactional audit shares the mutation commit/rollback lifecycle, while best-effort audit failure must be observable without changing an already-successful command result. Additional exposures are limited to `ui`, `agent`, and `automation`.

`command.execution` describes a durable asynchronous workflow. Its current mode is `async`, with a required `transactional` workflow guarantee; stable `job_kind`, `resource_kind`, `initial_event`, and `initial_state` identities; referenced `status_operation` and `events_operation` query operation IDs; and a `supported` or `unsupported` cancellation policy. Async commands require a documented `202` response. Both referenced operations must exist in the same IR document and be GET queries. Command-audit and workflow guarantees are deliberately independent: an application may use best-effort security auditing while still committing the initial workflow state, operational event, and job atomically. These constraints make the asynchronous lifecycle one generated, enforceable contract rather than unrelated runtime literals.

When strict operation-kind authoring is enabled, non-read HTTP methods must
provide a command contract or carry the explicit TypeSpec `@apigen.query`
marker. This prevents an omitted command declaration from silently becoming a
query while retaining POST queries for side-effect-free analytical and dry-run
operations.

Supported security schemes are HTTP Bearer auth and `ApiKeyAuth<ApiKeyLocation.header, "X-API-Key">`. Unsupported schemes fail closed before IR emission.

APIGen-owned endpoint extensions in current consumers:

- `x-authz`
- `x-apigen-manual`

Legacy `x-agent` extensions are reserved and rejected. Typed tool metadata belongs in the endpoint's `tool` field.

## Endpoint Tools

An endpoint with `tool` is exposed as an SDK-neutral agent tool. Tool names match `^[a-z][a-z0-9_]{0,63}$` and are unique across the document.

Required tool fields:

- `name`
- `effect`: `read`, `idempotent-write`, `write`, or `destructive`
- `output`

Confirmation defaults are resolved into IR: `never` for reads, `policy` for idempotent writes and writes, and `always` for destructive operations. Authored confirmation may strengthen but never weaken the effect's minimum.

`input.fields[]` overrides endpoint fields by `source` (`path`, `query`, `header`, or `body`) and wire `name`. `$` identifies an entire scalar JSON body. Modes are:

- `model`: visible in the portable input schema; may define `alias`, `description`, or a JSON-compatible `default`
- `context`: hidden from model input and supplied by a consumer-owned `context_key`
- `omit`: hidden from model input; required transport fields must define a valid default

Defaults and wire formats are retained in generated bindings. They are deliberately omitted from model-visible JSON Schemas so those schemas contain only portable validation keywords.

APIGen flattens endpoint parameters and JSON object-body properties into the model-visible input. Tool endpoints may have no body or exactly one JSON request shape. Binary, file, form-urlencoded, and multipart tool inputs are rejected.

Output modes are `raw`, `project`, and `empty`. Project mode uses recursive selection nodes with an RFC 6901 `source`, optional `target`, optional child `select`, and optional `count_as`. Child selection applies to objects, array items, and map values according to the resolved schema. Optional cursor metadata defines a source pointer plus `target` and `has_more_target` names.

The successful response set must be consistently bodyless or provide a JSON representation for every status, and all JSON representations must share one compatible schema. Additional non-JSON representations remain available to server and CLI generation but do not participate in the tool output schema. Generated tool requests select the JSON representation, including overriding an endpoint `Accept` parameter. Projection pointers, node kinds, aliases, count targets, cursor targets, and collisions are validated against the shared JSON schema. Common union arrays with different item schemas resolve to an unconstrained array container, allowing raw and `count_as` projection while nested selection still requires a common item schema. CLI collection validation retains the original object item branches for rendering and common-column validation. Explicit non-collection output modes suppress inferred collection pagination and columns. Tool `metadata` accepts only JSON-compatible `x-*` keys.

Generated runtime descriptors include operation ID, transport, effect, resolved confirmation, tags, input/output JSON Schemas, bindings, projection, and metadata. Canonical OpenAPI emits the normalized value as `x-apigen-tool`.

## Body Contents

Request and response bodies use ordered `contents` entries. TypeSpec `@sharedRoute` and same-endpoint `@overload` declarations are coalesced before IR emission when they describe one compatible HTTP operation.

Each content entry defines:

- `content_type`
- `body_kind`

Supported `body_kind` values:

- `json`
- `text`
- `binary`
- `file`
- `form_urlencoded`
- `multipart`

Schema-bearing content uses `schema`. Multiple response alternatives may use `any_of`. Multipart content uses `parts`. `SchemaRef.enum` is supported for inline string-literal parameter schemas such as coalesced `Accept` headers.

Within a single request body or response, `contents[].content_type` values must be unique case-insensitively. TypeSpec emission deduplicates identical duplicate variants, but rejects incompatible same-status variants that reuse the same media type.

JSON `bytes` values are represented as `type: string`, `format: byte`. Raw binary/file payloads are represented as `type: string`, `format: binary`.

Generated Go treats raw `bytes` as `[]byte`. TypeSpec `Http.File` uses generated `GenFile`, which supports both simple `Contents []byte` and streaming `Reader io.ReadCloser` payloads plus `ContentType`, optional `Filename`, and optional `Size` metadata.

## Multipart Parts

Each multipart part defines:

- `name`: generated field/property name
- `body_kind`: `json`, `text`, `binary`, or `file`
- optional `wire_name`: HTTP part name; omitted for unnamed `multipart/mixed` tuple parts
- optional `part_kind`: `model` or `tuple`
- optional `repeated`: true for repeated TypeSpec `HttpPart<T>[]` parts
- optional `required`
- optional `content_type`
- optional `filename`: true when a TypeSpec `Http.File` filename is available
- optional `schema`

Named `multipart/form-data` parts use `wire_name`. Unnamed `multipart/mixed` tuple parts are positional and keep stable generated names such as `part1`, `part2`. `HttpPart<T[]>` is a single JSON-array part; `HttpPart<T>[]` is repeated parts.

Generated CLI metadata includes ordered multipart part specs so runtime/cobra can accept repeated `--part name=value`, `--part name=@file`, or `--part name=-` flags. For `multipart/mixed`, tuple parts are addressed by generated names such as `part1` and `part2` and emitted in IR order.

Generated server decoding is strict. Unknown form-data part names, duplicate non-repeated form-data parts, and extra mixed tuple parts produce `400` responses. Repeated form-data parts are collected in request order for that part. Mixed tuple parts decode by wire order and do not use optional part names for reordering.

Canonical OpenAPI emits ordinary object schemas and `encoding` for `multipart/form-data`. For `multipart/mixed`, OpenAPI 3.0 receives best-effort schema output plus APIGen vendor metadata:

- `x-apigen-multipart-kind: "mixed"`
- ordered `x-apigen-multipart-parts`

## Responses

Each response must define:

- `status_code`
- `description`

Optional fields:

- `headers`
- `contents`
- `extensions`

Supported APIGen-owned response extension:

- `x-apigen-response-shape`

Current supported response shape:

- `wrapped_json`
  - requires `body_type`
  - indicates the generated server should treat the response as a JSON wrapper whose body type is named explicitly by `body_type`

Response headers are unique case-insensitively per response.

## Generated Transport Errors

`transport_errors` explicitly defines failures owned by generated HTTP transport code. It contains a resolvable authored `schema`, a `content_type`, and a map of stable failure kinds to `status_code`, application `code`, and safe `public_detail`.

The TypeSpec `@apigen.transportErrors` namespace decorator authors this policy. Generated strict server registration requires a `GenTransportErrorResponder`. The responder receives the operation ID, failure kind, configured public fields, and original `Cause`; it owns serialization, request-ID/instance attachment, field violations, logging, and observability. Generated code never sends `Cause.Error()` to clients.

Known generated kinds include `path_parameter`, `query_parameter`, `header_parameter`, `malformed_body`, `unsupported_media_type`, `multipart`, `handler`, and `response_serialization`. The selected status codes are added to canonical and embedded OpenAPI responses with the selected schema and media type.

## Schemas

`schemas` is a named registry used by emitted OpenAPI, generated Go code, generated TypeScript types, generated JSON Schema, and generic contract roots.

`SchemaRef.ref` values are normalized against component-style paths and resolved against this registry.

JSON and urlencoded form object bodies intended for generated Go output should resolve to named schema entries in this registry. Text bodies generate `string`; raw binary `bytes` bodies generate `[]byte`; TypeSpec `Http.File` bodies generate streaming-capable `GenFile` with byte contents or a reader, content type, optional filename, and optional size metadata. Generators reject anonymous object bodies when they cannot be mapped to a stable generated Go type.

Schema-level and schema-property-level `extensions` preserve downstream-owned metadata. Generic extensions must use `x-*` keys and JSON-compatible values. APIGen validates and preserves this metadata; interpretation belongs to downstream application logic and emitters.

Object inheritance uses `base`. Closed tagged unions use `type: "union"`, ordered `one_of` references, and a `discriminator` with `property_name` plus literal-to-schema `mapping`. Derived variants retain literal discriminator constraints instead of flattened copies of inherited properties. Named unions and union references nested in arrays or `additional_properties` remain reusable.

`SchemaRef.additional_properties` represents typed maps. A nested schema preserves `Record<T>` recursively across Go, TypeScript, OpenAPI, JSON Schema, and agent-tool output. Integer `format` is authoritative: `int32` maps to Go `int32`, while `int64` maps to Go `int64` without narrowing.

Generated Cobra metadata includes expanded portable `schema_json` values for parameters, object fields, multipart parts, and complete request bodies. Nested arrays/maps of unions therefore retain their closed `oneOf` variants for CLI consumers instead of degrading to coarse `array` or `object` labels.

## Contract Roles

- JSON IR is the generator input contract
- canonical OpenAPI is the published API contract artifact
- TypeSpec HTTP semantics are the source of truth for body kind, content type, routes, status codes, and parameters
- canonical OpenAPI may carry repo-owned metadata extensions such as `x-authz`
- `contracts[]` declares generic model roots for generated Go models, TypeScript types, and JSON Schema
