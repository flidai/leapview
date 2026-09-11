# apigen

`apigen` compiles authored API and data contracts into versioned JSON IR, canonical OpenAPI, generated Go server code, optional typed Go clients, generated request-model types, generated Cobra CLI registries, and generated model artifacts.

Module path: `github.com/Yacobolo/toolbelt/apigen`

## Model

APIGen has two contract layers:

- TypeSpec authoring input for humans
- JSON IR `v4` for generators

Canonical OpenAPI is the published API artifact for HTTP targets. JSON IR is the compatibility boundary between TypeSpec and emitters. Repo-owned OpenAPI extensions such as `x-authz` are preserved there. Generic data-contract roots live in the same IR under `contracts[]` and reuse the shared `schemas` registry.

## CLI

Install the CLI:

```bash
go install github.com/Yacobolo/toolbelt/apigen/cmd/apigen@v0.7.3
```

Or run from this module during local development:

```bash
go run ./cmd/apigen --help
```

Commands:

- `typespec-compile`: TypeSpec -> JSON IR, plus OpenAPI for HTTP targets
- `openapi`: JSON IR -> OpenAPI
- `server`: JSON IR -> server + request models
- `cli`: JSON IR -> Cobra registry
- `all`: JSON IR -> all configured outputs

The CLI supports direct flags or a manifest selected with `-manifest <file>` and `-target <name>`.

Recommended grouped manifest shape:

```yaml
targets:
  - name: example
    kind: http
    typespec_dir: api/typespec
    typespec_entrypoint: public/main.tsp
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      dir: internal/api/gen
      client_file: client.apigen.gen.go
    cli_out:
      dir: cmd/cli/gen
    contract_imports:
      ExampleVisualization:
        go_package: example.com/project/internal/visualization
        go_alias: visualization
        typescript_module: ../visualization

  - name: data-contracts
    kind: contracts
    typespec_dir: contracts/typespec
    ir_out: contracts/gen/json-ir.json
    go_models_out: internal/contracts/models.gen.go
    go_models_package: contracts
    ts_out: contracts/gen/contracts.ts
    json_schema_out: contracts/gen/contracts.schema.json
```

Manifest target fields:

- `kind` (`http` by default, or `contracts`)
- `typespec_dir`
- `typespec_entrypoint` (optional path within `typespec_dir`)
- `ir_out`
- `openapi_out`
- `go_out.dir`
- `go_out.package`
- `go_out.server_file`
- `go_out.request_models_file`
- `go_out.client_file` (optional; enables typed client generation)
- `go_out.default`
- `go_out.aggregate`
- `go_out.packages`
- `go_out.packages.*.import_path`
- `go_out.unmatched`
- `cli_out.dir`
- `cli_out.package`
- `cli_out.file`
- `go_models_out`
- `go_models_package`
- `ts_out`
- `json_schema_out`
- `contract_imports`
- `strict_operation_kinds`

`contract_imports` maps TypeSpec namespaces owned by another generated target
to their canonical Go package and TypeScript module. Consumer models reference
those packages instead of regenerating the imported declarations. OpenAPI and
JSON Schema remain self-contained. Every imported namespace used by a target
must have a mapping; aliases must be unique within the target.

HTTP targets require `typespec_dir`, `ir_out`, and `openapi_out`. Contract targets require `typespec_dir`, `ir_out`, and at least one of `go_models_out`, `ts_out`, or `json_schema_out`. `openapi`, `server`, and `cli` are HTTP-target commands and fail clearly for contract targets.

Direct flags support the same split with `-kind http` or `-kind contracts`.

### Typed command contracts

Use `@apigen.command` on a mutating operation when its stable operation ID is
also the transport-neutral application command identity:

```typespec
@route("/projects/{project}/role-bindings")
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
@apigen.commandDefaults(#{ guarantee: "transactional" })
@apigen.auditPayload(RoleBindingAuditPayload)
interface RoleBindings {
  @post
  @apigen.ui("project.access.role-binding.create")
  @apigen.failsWith(RoleBindingConflict)
  @apigen.command(#{
    auditAction: "role_binding.created",
  })
  createRoleBinding(
    @path @apigen.target project: string,
    @header("Idempotency-Key") idempotencyKey: string,
    @body body: RoleBindingRequest,
  ): RoleBindingResponse;
}

@apigen.failureDefinition(#{
  kind: "conflict",
  statusCode: 409,
  code: "ROLE_BINDING_CONFLICT",
  publicDetail: "The role binding conflicts with current state.",
})
model RoleBindingConflict {}
```

HTTP is implied by the endpoint, and generated clients provide the CLI/API
projection. `additionalExposures` therefore names only non-HTTP entry points
such as `ui`, `agent`, or `automation`. Owner and authorization are derived
from the declaring namespace and `@apigen.authz`; the target is inferred from a
single path parameter or selected explicitly with `targetParameter`.

APIGen uses the TypeSpec operation name as the operation ID by default;
`@operationId` remains available for intentional overrides. Inferred IDs must
be unique. APIGen also requires stable dotted lower-snake-case audit actions, a
required `Idempotency-Key` on POST commands, and a required
`If-Match` on PATCH commands. It emits the normalized value in IR, generated Go
operation registries, aggregate registries, and OpenAPI `x-apigen-command`.
The generated runtime registry is the transport-neutral execution policy:
API middleware selects commands by generated method/route metadata, while
direct UI, CLI, agent, and automation adapters call `command.BeginInvocation`
with the same command identity and invocation inputs. The runtime rejects an
undeclared surface, missing authorization target, missing idempotency identity,
or missing concurrency token before domain dispatch. Revisioned mutations call
`Executor.CheckConcurrency` with the canonical revision from inside their
mutation transaction; a successful generated transport response is rejected
unless both concurrency and command execution completed.

`Contract.Dependencies` derives authorization, idempotency, concurrency, audit,
and job-queue requirements from the same descriptor so composition roots can
fail closed at startup with `ValidateDependencies`. Executor observations also
derive their stable span name and low-cardinality labels from the contract;
request bodies, target values, idempotency keys, and concurrency tokens are not
logged. Applications should not maintain parallel method/path allowlists or
surface-specific command-policy tables.

Every command must explicitly declare `failures`, inherit them through
`@apigen.commandDefaults`, use one or more `@apigen.failsWith` references, or
use `#[]` when it has no operation-owned domain failures. Named failures are
declared once with `@apigen.failureDefinition`; APIGen rejects conflicting
definitions for the same public code. Each failure binds a transport-neutral stable
lower-snake-case `kind` to one documented HTTP status, stable upper-snake-case
public `code`, and safe `publicDetail`. Generated registries expose
`GetAPIGenCommandFailureContracts`; `runtime/failure` classifies domain errors
without embedding HTTP behavior in the domain. The same generated vocabulary
is therefore available to HTTP, CLI, UI, automation, and agent consumers.
Unclassified implementation errors retain the generated transport fallback and
are never allowed to leak their internal detail.

Audit `guarantee` describes runtime failure semantics. `transactional` means a
successful mutation and its audit record commit or roll back together.
`best-effort` means the configured recorder is attempted and failures are made
observable without changing an already-successful command result. APIGen
validates these values; applications may require every command to select one.

Audit metadata can be made a typed, versioned contract instead of an arbitrary
JSON map:

```typespec
@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model RoleBindingCreatedAuditPayload {
  @apigen.auditInternal operationId: string;
  @apigen.auditInternal role: string;
  @apigen.auditPii subjectId: string;
  @apigen.auditPublic surface: string;
}

@apigen.auditPayload(RoleBindingCreatedAuditPayload)
```

Every required command audit must declare a payload. Payload models must be
named objects with required fields and model-owned `@apigen.auditSchema`
metadata. Every field must explicitly declare `public`, `internal`, `pii`, or
`secret` sensitivity using `auditPublic`, `auditInternal`, `auditPii`, or
`auditSecret`. The longer `@apigen.sensitivity("...")` form and inline payload
options remain supported for compatibility. APIGen emits the schema, version,
retention, and classifications into IR, OpenAPI, per-package and aggregate Go
registries. It also emits a typed
`EncodeGen<Operation>AuditPayload` helper and a log-safe variant. Durable audit
encoding always redacts `secret`; log-safe encoding preserves only `public` and
redacts `internal`, `pii`, and `secret`. Both encoders reject missing or
undeclared fields, so schema drift fails the command lifecycle instead of
silently changing persisted audit data.

Auditing is required by default for concise `@apigen.command` declarations.
Interfaces can own shared `@apigen.authz`, `@apigen.commandDefaults`, and
`@apigen.auditPayload` metadata; operation metadata overrides inherited
defaults, and generated IR always contains the fully expanded policy. A truly
ephemeral mutation must opt out conspicuously with
`@apigen.unaudited("reason")`. The legacy nested `audit` form remains accepted
while contracts migrate.

Generated Go servers also expose `GetAPIGenCommandRuntimeContract`, which
normalizes every required audit into `runtime/command.Contract`. Construct a
`runtime/command.Executor` with that lookup and supply both application
capabilities; the generated guarantee selects the transactional or best-effort
path. Generated HTTP boundaries should start a command guard before dispatch
and reject successful responses whose command never completed through the
executor. This makes a newly declared command fail closed until its runtime
capability is wired.

Commands that start durable work can reference their status and event
operations by symbol:

```typespec
@apigen.asyncExecution(Releases.getRelease, Releases.listReleaseEvents, #{
  guarantee: "transactional",
  jobKind: "release.finalize",
  resourceKind: "release",
  initialEvent: "release.validating",
  initialState: "validating",
  cancellation: "unsupported",
})
```

APIGen requires a transactional execution guarantee, a `202` response, and
existing GET query operations for status and event history. Command audit and
workflow guarantees are independent: a security audit may remain best-effort
while the initial workflow state, event, and job commit transactionally. The
normalized lifecycle is emitted in JSON IR, OpenAPI, and
generated Go registries, including `runtime/command.Contract.Execution`.
Applications should derive durable event, resource, and job identities from
that runtime contract and validate registered job handlers at startup.

Set `strict_operation_kinds: true` on an HTTP manifest target to make the
operation catalog exhaustive. GET and HEAD operations are queries by default;
every POST, PUT, PATCH, or DELETE must then declare `@apigen.command` or an
explicit `@apigen.query`. The query exemption is intended for side-effect-free
POSTs whose request shape is too rich for a URL, such as analytical queries or
dry-run plans. Generated IR, Go registries, and OpenAPI expose the normalized
`command` or `query` kind as `kind`, `GenOperationContract.Kind`, and
`x-apigen-operation-kind` respectively.

### Typed clients

Set `client_file` on a flat `go_out` package or an individual
`go_out.packages` output to generate a typed client in that package:

```yaml
go_out:
  dir: internal/api/gen
  package: api
  client_file: client.apigen.gen.go
```

Client generation is opt-in. It derives solely from the target's IR and output
metadata, so the same setting works for a one-package service and a namespace
package plan. Generated methods expose operation constants, concrete parameter
and body types, and concrete successful response wrappers. The wrappers retain
status, headers, and content type alongside a typed body.

Generated clients depend only on the small
`github.com/Yacobolo/toolbelt/apigen/runtime/client.Transport` interface.
Applications inject a transport that owns base-URL resolution, authentication,
HTTP execution, error decoding, and retries. The generic `any` values needed for
JSON encoding and decoding remain confined to that transport boundary; callers
use the generated concrete method signatures.

JSON is selected when a success response also offers streaming or binary
representations. Pure text and binary successes use `string` and `[]byte`.
Generation rejects incompatible success bodies instead of weakening the public
method to an untyped result.

### Namespace package plans

The flat `go_out` mapping above is the default and remains the right choice for
small services. Larger services may optionally describe a deterministic package
plan without splitting the TypeSpec service or OpenAPI document:

```yaml
targets:
  - name: example
    kind: http
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      unmatched: error
      aggregate:
        dir: internal/app/api/gen
        package: aggregate
      packages:
        ExampleAPI.Access:
          dir: internal/access/api/gen
          package: accessapi
          import_path: github.com/acme/example/internal/access/api/gen
          client_file: client.apigen.gen.go
        ExampleAPI.Dashboard:
          dir: internal/dashboard/api/gen
          package: dashboardapi
          import_path: github.com/acme/example/internal/dashboard/api/gen
          client_file: client.apigen.gen.go
```

TypeSpec namespaces provide the grouping. The manifest owns language package
names, import paths, and filesystem paths; TypeSpec must not contain
repository-specific output paths. APIGen treats namespace names as opaque
partition keys and does not assign architectural meaning to them.

Every `go_out.packages` output requires an explicit canonical Go `import_path`.
`go_out.default` also requires one when present. APIGen does not infer import
paths from `go.mod`, the current working directory, or the output directory:
explicit paths remain deterministic in monorepos, nested modules, and generated
trees outside a module root. The flat `go_out` form needs no import path because
it does not create cross-package references. `go_out.aggregate.import_path` is
optional because generated partitions do not import the aggregate package.

`go_out.unmatched` is required for a package plan:

- `error` requires every emitted namespace to have an explicit mapping.
- `default` routes unmatched namespaces to `go_out.default`, which is then
  required.

Multiple namespaces may map to the same output when their directory, Go package,
import path, and generated filenames are identical. `go_out.aggregate`, when
present, must use a separate directory. Mappings are normalized in namespace
order so YAML map ordering cannot affect generation.

Schemas owned by configured `contract_imports` remain external to the package
plan. APIGen keeps their IR declarations available for transitive type
resolution, imports them through the configured canonical Go binding, and never
creates a local partition or generated output for them. A namespace cannot be
both locally mapped under `go_out.packages` and externally owned by a
`contract_imports` binding.

`server` and `all` render one server and request-model package per planned
output, plus a typed client wherever `client_file` is configured. Schema-only
outputs receive request models without fake server, client, or route surfaces.
The aggregate package is composition-only and does not accept `client_file`.
APIGen plans, projects, renders, and formats every package before staging
generated files, so an ownership or emitter failure cannot leave a partially
rendered package set. If an output becomes schema-only, APIGen removes only its
exact configured generated server and client filenames.

OpenAPI remains one service-wide artifact, and CLI generation remains global
over the complete operation set. Each capability server embeds the projected
OpenAPI for only the routes it registers.

When `go_out.aggregate` is configured, APIGen also emits a thin application
composition package. It imports only endpoint-bearing generated packages and
provides typed loose and strict registration inputs for each one. It does not
contain handlers, models, or business logic. It embeds the canonical global
OpenAPI document and merges capability-owned operation metadata and agent-tool
contracts into defensive global registries for application authorization, CLI,
documentation, and tool composition. Schema-only packages are excluded; if no
partition owns endpoints, APIGen removes only the aggregate's exact configured
generated server filename. Omitting `go_out.aggregate` emits no composition
layer. The flat single-package form continues to support every command
unchanged.

## Public Surface

Supported packages:

- `github.com/Yacobolo/toolbelt/apigen/ir`
- `github.com/Yacobolo/toolbelt/apigen/emit/openapi`
- `github.com/Yacobolo/toolbelt/apigen/emit/aggregatego`
- `github.com/Yacobolo/toolbelt/apigen/emit/requestmodelgo`
- `github.com/Yacobolo/toolbelt/apigen/emit/servergo`
- `github.com/Yacobolo/toolbelt/apigen/emit/cligo`
- `github.com/Yacobolo/toolbelt/apigen/emit/modelgo`
- `github.com/Yacobolo/toolbelt/apigen/emit/modelts`
- `github.com/Yacobolo/toolbelt/apigen/emit/jsonschema`
- `github.com/Yacobolo/toolbelt/apigen/runtime/chi`
- `github.com/Yacobolo/toolbelt/apigen/runtime/cobra`
- `github.com/Yacobolo/toolbelt/apigen/runtime/agenttool`

Package roles:

- `typespec`: TypeSpec emitter package used by `typespec-compile`
- `ir`: versioned generator contract
- `emit/*`: OpenAPI, server, request-model, CLI, model, and JSON Schema emitters
- `runtime/*`: thin runtime helpers used by generated code
- `cmd/apigen`: CLI entrypoint

Public packages must stay isolated from sibling `toolbelt` packages outside `apigen`.

## Using It

Recommended TypeSpec flow:

1. Author API contracts in TypeSpec.
2. Run `typespec-compile` to produce JSON IR and canonical OpenAPI.
3. Run `all` to generate server, request-model, and CLI outputs.
4. Build your service against `runtime/chi` and your CLI against `runtime/cobra`.

The runnable reference showcase lives in `example/`. It is a small todo app with checked-in `json-ir`, OpenAPI, server transport, request-model aliases, CLI registry metadata, handwritten strict handlers, and a generated Cobra CLI. The same example also includes a generic contract target shaped like dashboard UI signal envelopes, with checked-in Go models, TypeScript types, JSON Schema, and IR.

The in-repo TypeSpec emitter lives in `typespec/` with a checked-in `package-lock.json`. Use `npm ci` there for reproducible local TypeSpec development; `typespec-compile` also bootstraps that pinned toolchain when needed. Project TypeSpec sources may use conventional package imports such as `import "@typespec/http";`, `import "@typespec/openapi";`, and `import "@yacobolo/apigen";`; the CLI resolves those imports from its managed cache.

## Typed Agent Tools

Agent tools are endpoint capabilities, not standalone data contracts. Mark a TypeSpec operation with `@apigen.tool`; APIGen derives the model-visible input from its path, query, header, and JSON body fields:

```typespec
@apigen.tool(#{
  name: "list_project_assets",
  effect: "read",
  tags: #["project", "lineage"],
  input: #{ fields: #[
    #{ source: "path", name: "project", mode: "context", contextKey: "project" },
    #{ source: "query", name: "limit", default: 25 },
  ] },
  output: #{
    mode: "project",
    select: #[#{ source: "/items", countAs: "count", select: #[#{ source: "/id" }, #{ source: "/title" }] }],
    cursor: #{ source: "/page/nextCursor" },
  },
})
@route("/projects/{project}/assets")
@get
op listProjectAssets(
  @path project: string,
  @query limit?: int32,
): AssetListResponse;
```

Effects are `read`, `idempotent-write`, `write`, and `destructive`. Their minimum confirmation defaults are `never`, `policy`, `policy`, and `always`; authored confirmation may strengthen but never weaken that requirement. Tool names are portable lowercase identifiers and unique across the document.

Input overrides bind endpoint wire fields as model arguments, trusted context, or omitted/defaulted transport values. Tool endpoints accept no body or one JSON body; binary, file, form, and multipart inputs fail closed. Output modes are `raw`, `project`, and `empty`; recursive RFC 6901 projections support object fields, array items, map values, aliases, counts, and cursors.

Generated model-visible schemas use a provider-portable validation subset. Defaults and transport formats remain on typed bindings rather than appearing as `default` or `format` schema annotations.

Generated server packages expose defensive copies of SDK-neutral descriptors:

```go
contract, ok := gen.GetAPIGenToolContract("list_project_assets")
if ok {
	request, err := agenttool.BuildRequest(
		contract,
		json.RawMessage(`{"limit":10}`),
		agenttool.Context{"project": "sales"},
	)
	_ = request
	_ = err
}
```

`runtime/agenttool` strictly validates arguments, builds HTTP requests, negotiates the generated JSON response representation, preserves non-2xx responses, and projects successful JSON responses. APIGen remains provider-neutral: authorization, credentials, policy decisions, confirmation UI, agent SDK conversion, and operation dispatch stay in the consumer. Canonical OpenAPI publishes normalized descriptors as `x-apigen-tool`.

Generic operation `x-*` extensions remain available for downstream metadata. `x-agent` is reserved and rejected; there is no raw compatibility parser.

Install as a dependency with:

```bash
go get github.com/Yacobolo/toolbelt/apigen@v0.6.4
```

## Contract Notes

JSON IR emits and accepts schema version `v4` only. Required root fields are `schema_version`, `info.title`, `info.version`, and at least one endpoint or contract root. Request and response bodies use ordered `contents` entries with explicit `content_type` and `body_kind`. Schema composition uses `base`, `one_of`, and `discriminator`; map value schemas remain in `additional_properties`. Endpoint extensions preserve operation-level `x-*` vendor metadata; APIGen-owned endpoint extensions include `x-authz` and `x-apigen-manual`. Typed tools live on `Endpoint.tool` and never create `contracts[]` entries.

HTTP targets can declare generator-owned failures explicitly with `@apigen.transportErrors`. Generated strict registration requires a `GenTransportErrorResponder`; the responder owns the authored wire model, media type, request IDs, logging, and other application policy. Generated code supplies a stable failure kind, configured status/code/public detail, and the original cause without exposing that cause to clients.

Contract targets use the shared `schemas` registry plus `contracts[]` roots:

```json
{
  "name": "DashboardEnvelope",
  "schema": {"ref": "DashboardEnvelope"},
  "kind": "ui-signal",
  "tags": ["dashboard"],
  "extensions": {"x-libredash-surface": "dashboard"}
}
```

Schema and schema-property `extensions` preserve downstream-owned `x-*` metadata. APIGen validates that metadata is JSON-compatible and has `x-*` keys, but does not interpret downstream rules.

Contract TypeSpec sources can use APIGen decorators:

```typespec
import "@yacobolo/apigen";

@apigen.`package`(#{ title: "Data Contracts", version: "1.0.0" })
namespace Contracts;

@apigen.contract(#{ kind: "ui-signal", tags: #["dashboard"] })
@apigen.`metadata`(#{ "x-owner": "analytics" })
model DashboardEnvelope {
  @apigen.`metadata`(#{ "x-libredash-signal-key": "page" })
  page: DashboardPageSignal;
}
```

The contract emitters generate selected contract roots and transitive dependencies:

- `emit/modelgo`: Go structs and aliases; optional TypeSpec properties become pointers.
- `emit/modelts`: TypeScript interfaces and aliases; optional properties use `?`.
- `emit/jsonschema`: draft 2020-12 JSON Schema with `$defs`, `anyOf` contract roots, required fields, maps, arrays, enums, and metadata extensions.

Endpoint parameters support TypeSpec path, query, and header parameters across IR, OpenAPI, generated server binding, and generated CLI flags. Cookie parameters intentionally fail closed.

Supported TypeSpec auth is intentionally narrow and runtime-backed: HTTP Bearer auth and `ApiKeyAuth<ApiKeyLocation.header, "X-API-Key">`. Basic/Digest/custom HTTP schemes, OAuth/OpenID, non-header API keys, and header API keys with other names fail closed instead of emitting misleading runtime or OpenAPI metadata.

Generated request bodies are contract-first:

- JSON and form object bodies used in generated Go output should resolve to named IR-owned schemas
- text bodies generate `string`, raw `bytes` bodies generate `[]byte`, and TypeSpec `Http.File` bodies generate `GenFile`
- `GenFile` carries `Contents []byte`, optional streaming `Reader io.ReadCloser`, `ContentType string`, optional `Filename *string`, and optional `Size *int64`; response writers stream `Reader` when present and set `Content-Type`/`Content-Disposition` from that metadata
- raw `Http.File` request bodies pass `r.Body` through as `GenFile.Reader`; multipart `Http.File` parts spool to temporary files and are cleaned up after the handler returns
- multipart bodies generate a `Gen<Operation>MultipartBody` struct; JSON/form parts decode into generated schema types, text parts into `string`, raw bytes into `[]byte`, and `Http.File` parts into streaming `GenFile`
- repeated multipart parts generate slices, optional single parts generate pointers, and `multipart/mixed` tuple parts are decoded in wire order
- generated multipart server decoding is strict: unknown form-data part names, duplicate non-repeated form-data parts, and extra mixed tuple parts return `400`
- generation fails explicitly when an anonymous object body cannot be mapped to a named IR schema
- generated CLI supports multipart request bodies with repeated `--part name=value`, `--part name=@file`, or `--part name=-` flags; binary and file parts require `@file` or stdin

Generated response writers are content-aware. Single-content responses keep concise names such as `GenGetArtifact200JSONResponse`, `GenGetArtifact200TextResponse`, and `GenGetArtifact200BinaryResponse`. When one status can return multiple media types, APIGen emits one concrete type per content variant using sanitized media names, for example `GenGetArtifact200ApplicationJSONResponse` and `GenGetArtifact200ApplicationOctetStreamResponse`. Each writer sets the authored `Content-Type`. Identical duplicate content variants are deduplicated; incompatible same-status variants with the same `content_type` fail closed instead of being approximated with `anyOf`.

## Preferred TypeSpec Style

Prefer TypeSpec-native HTTP helpers and aliases over APIGen-shaped response boilerplate:

```typespec
using Http;

model Error {
  code: int32;
  message: string;
}

model OkJson<T> {
  ...OkResponse;
  ...Body<T>;
}

model BadRequest {
  ...BadRequestResponse;
  ...Body<Error>;
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

APIGen follows resolved `@typespec/http` semantics for JSON, text, binary, file, urlencoded form, multipart, optional bodies, response helpers, aliased response unions, and route containers.
Content negotiation can use TypeSpec `@sharedRoute` or `@overload`; APIGen coalesces compatible same-method/same-path operations into one endpoint, merges literal `Accept`/`contentType` headers into enum-like parameters, and fails closed when auth, APIGen CLI/authz/manual metadata, operation extensions, parameters, or request bodies disagree.

LibreDash-style contracts should use standard HTTP transport instead of raw-body extensions. Before:

```typespec
model DeploymentArtifactUploadRequest {
  value: bytes;
}

@extension("x-libredash-dispatch", "raw-body")
op uploadDeploymentArtifact(@body body: DeploymentArtifactUploadRequest): UploadDeploymentArtifactOK | BadRequest | Unauthorized | Forbidden;
```

After:

```typespec
alias CommonErrors = BadRequest | Unauthorized | Forbidden;

@route("/api/v1")
namespace CandidateSources {
  @route("/projects/{project}/candidate-sync/blobs/{digest}")
  @put
  op uploadProjectCandidateSourceBlob(
    @path project: string,
    @path digest: string,
    @header contentType: "application/octet-stream",
    @body body: bytes,
  ): OkJson<CandidateSourceBlobResponse> | CommonErrors;
}
```

## v0.5.0 Migration Notes

- JSON IR `v4` is the only accepted IR version; v3 is intentionally not loaded.
- TypeSpec inheritance and discriminated unions now remain explicit through IR, OpenAPI, Go, TypeScript, JSON Schema, and agent-tool schemas.
- Generated Go union wrappers strictly reject missing/unknown discriminators, unknown fields, and missing required variant properties.
- `Record<T>` retains `T` recursively in generated Go models, including nested arrays and maps.
- `int64` remains `int64` in every generated Go model path.
- Generated strict server registration now requires an injected `GenTransportErrorResponder`; legacy shared `Error` response helpers and the hard-coded writer were removed.
- Regenerate all checked-in IR and generated artifacts when upgrading.

See [`ir/CONTRACT.md`](./ir/CONTRACT.md) for the full IR contract and run `go test ./...` for the module smoke coverage.
