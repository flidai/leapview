# APIGen Module Notes

APIGen now lives in an in-repo nested module rooted at `apigen`.
This repo remains the source of truth for APIGen while the following stay green together:

- `github.com/Yacobolo/toolbelt/apigen/...` has no imports of sibling repo-local `github.com/Yacobolo/toolbelt/...` packages outside `github.com/Yacobolo/toolbelt/apigen/...`
- the bundled example fixture under `apigen/example` compiles its source API spec to OpenAPI + JSON IR
- the fixture generates Go server and CLI code using APIGen
- the fixture builds against `github.com/Yacobolo/toolbelt/apigen/runtime/chi` and `github.com/Yacobolo/toolbelt/apigen/runtime/cobra`
- JSON IR `v2` remains documented and fixture-tested

Current module target:

- Go module: `github.com/Yacobolo/toolbelt/apigen`
- optional future standalone repo/module if cross-repo distribution needs a cleaner host boundary

The intended boundary is:

- keep JSON IR as the compatibility boundary between TypeSpec authoring and Go emitters
- keep split-package compat output bound to IR-owned `GenSchema...` symbols, not server-only `Gen<Operation>Body` aliases
- keep canonical OpenAPI as the published contract artifact, including repo-owned extensions such as `x-authz`
- keep repo-local invocation concerns in thin CLI/task wiring around the nested module, not the library packages
