# APIGen 0.2.0 Release Notes

## Breaking Changes

- APIGen now supports only grouped manifest output configuration.
- Legacy flat manifest fields such as `server_out`, `request_models_out`, `cli_package`, and `generate_cli` are rejected.
- `cli_out` must now be a mapping with `dir`, and optional `package` and `file`.
- Standalone compatibility-type generation has been removed.
- Generated server output now emits only canonical `Gen...` response and request-body types.
- Legacy generated response aliases and status-drift shims are no longer emitted.
- Request bodies used by generated server and request-model output must resolve to named IR-owned schemas.

## Manifest Migration

Before:

```yaml
targets:
  - name: example
    cue_dir: api/cue
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    server_out: internal/api/gen/server.apigen.gen.go
    request_models_out: internal/api/gen/request_models.gen.go
    cli_out: cmd/cli/gen/apigen_registry.gen.go
    cli_package: gen
    generate_cli: true
```

After:

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

## Handwritten Integration

- Strict handwritten handlers remain the primary extension seam through `GenStrictServerInterface`.
- Router-level middleware composition remains supported.
- `x-apigen-manual` and `x-authz` remain supported contract extensions.
