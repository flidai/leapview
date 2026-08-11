# APIGen 0.3.0 Release Notes

## Breaking Changes

- TypeSpec is now the only supported APIGen authoring language.
- The CUE compiler and bootstrapper have been removed.
- The `cue-compile` and `cue-bootstrap` CLI commands have been removed.
- Manifest targets must use `typespec_dir`; `cue_dir` is no longer supported.
- The `cuegen` Go package has been removed.

## Migration

Before:

```yaml
targets:
  - name: example
    cue_dir: api/cue
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      dir: internal/api/gen
    cli_out:
      dir: cmd/cli/gen
```

After:

```yaml
targets:
  - name: example
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      dir: internal/api/gen
    cli_out:
      dir: cmd/cli/gen
```

Regenerate artifacts with:

```bash
go install github.com/Yacobolo/toolbelt/apigen/cmd/apigen@v0.3.0
apigen typespec-compile -manifest ./apigen.targets.yaml -target example
apigen all -manifest ./apigen.targets.yaml -target example
```

## TypeSpec Frontend

- Added the bundled `@yacobolo/apigen` TypeSpec emitter package.
- Added `typespec-compile` to compile TypeSpec sources into APIGen JSON IR and canonical OpenAPI.
- Added APIGen TypeSpec decorators for generator-owned metadata:
  - `@apigen.cli(...)`
  - `@apigen.authz(...)`
  - `@apigen.manual`
  - `@apigen.responseShape(...)`
- TypeSpec-native HTTP/OpenAPI decorators now describe API shape, including routes, verbs, parameters, request bodies, responses, docs, tags, servers, and auth.
- Project TypeSpec sources can use conventional package imports for `@typespec/http`, `@typespec/openapi`, and `@yacobolo/apigen`; `typespec-compile` resolves those packages from the managed cache.
- Named TypeSpec enums and inherited model properties are emitted into JSON IR.
- Unsupported constructs fail closed with TypeSpec/APIGen diagnostics instead of best-effort output.

## Operation Vendor Extensions

- TypeSpec operations can use `@TypeSpec.OpenAPI.extension("x-*", #{ ... })` for downstream metadata.
- Non-reserved operation-level `x-*` metadata is preserved through JSON IR, OpenAPI, and generated Go operation contracts.
- Generated `GenOperationContract` now includes `Extensions map[string]any`.
- Generated operation contract accessors return defensive copies for extension payloads.
- APIGen-owned keys such as `x-authz` and `x-apigen-*` remain reserved for APIGen decorators and validation.

## Robustness

- `typespec-compile` writes outputs atomically: existing IR/OpenAPI files are preserved if TypeSpec compilation, validation, normalization, or OpenAPI emission fails.
- The TypeSpec emitter is bundled in the Go module and installed into a managed writable cache; projects do not need an APIGen source checkout or local TypeScript build.
- `APIGEN_TYPESPEC_PACKAGE_DIR` remains available for local emitter development.
- The public Go server emitter validates IR before rendering and rejects invalid extension payloads instead of generating corrupt Go.
- Extension JSON preserves empty arrays, empty objects, `null`, `false`, `0`, nested objects, and arrays.

## Notes

- JSON IR remains schema version `v1`.
- Existing IR-based generation commands remain: `openapi`, `server`, `cli`, and `all`.
- The TypeSpec emitter package and bundled `dist/src` output remain checked in and validated by `npm run check:dist`.
- `typespec-compile` requires Node/npm at runtime for the TypeSpec compiler dependency install in the managed cache.
