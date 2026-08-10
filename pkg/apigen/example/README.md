# example

This is the canonical APIGen showcase: a small in-memory todo app with authored TypeSpec input, checked-in generated artifacts, a handwritten strict server, and a generated CLI.

## Handwritten files

- `api/typespec/`: authored todo contract
- `apigen.targets.yaml`: regeneration target manifest
- `internal/api/server.go`: strict handler implementation and in-memory todo store
- `internal/api/router.go`: router assembly and `/openapi.json` utility route
- `cmd/server/main.go`: tiny HTTP bootstrap
- `cmd/cli/main.go`: tiny Cobra root that mounts generated commands

## Generated files

- `api/gen/json-ir.json`
- `api/gen/openapi.yaml`
- `internal/api/gen/server.apigen.gen.go`
- `internal/api/gen/request_models.gen.go`
- `cmd/cli/gen/apigen_registry.gen.go`

## Regenerate

From `apigen/`:

```bash
go run ./cmd/apigen typespec-compile -manifest ./example/apigen.targets.yaml -target example
go run ./cmd/apigen all -manifest ./example/apigen.targets.yaml -target example
```

The example manifest uses the grouped target syntax, so `go_out.dir` and `cli_out.dir` infer the standard generated filenames automatically.

## Run

From `example/`:

```bash
go run ./cmd/server
go run ./cmd/cli todos list
go run ./cmd/cli todos list --status completed
go run ./cmd/cli todos create "buy milk"
go run ./cmd/cli todos get todo-1
go run ./cmd/cli todos complete todo-1
go run ./cmd/cli todos delete todo-1 --yes
```

The server starts with two seeded todos so the example is immediately explorable before you create anything new.

Optional:

- `TODO_EXAMPLE_ADDR` overrides the server listen address
- `TODO_EXAMPLE_BASE_URL` or `--base-url` overrides the CLI target URL

## Typed agent tools

The `listTodos` and `deleteTodo` operations demonstrate typed read and destructive tools:

```typespec
@apigen.tool(#{
  name: "list_todos",
  effect: "read",
  tags: #["todos", "read"],
  output: #{ mode: "project", select: #[#{ source: "/items", countAs: "count" }] },
})
```

Regeneration compiles normalized descriptors into:

- `api/gen/json-ir.json`
- `api/gen/openapi.yaml` as `x-apigen-tool`
- `internal/api/gen/server.apigen.gen.go` via the generated tool registry

Consumers can inspect it without patching generated files:

```go
contract, ok := gen.GetAPIGenToolContract("list_todos")
if ok {
	_ = contract.Effect
	_ = contract.InputSchema
}
```

## What this shows

- TypeSpec -> JSON IR -> OpenAPI -> generated Go artifacts
- strict handler integration via `RegisterAPIGenStrictRoutes`, including an injected transport-error responder
- handwritten handlers in `internal/api` using generated request and response types from `internal/api/gen`
- generated Cobra commands with path args, query params, JSON body input, detail output, collection output, and confirmation
- typed endpoint-derived tool contracts with portable input/output schemas
