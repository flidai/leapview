// Package apigen documents the supported public APIGen module surfaces.
//
// Supported library packages:
//   - github.com/Yacobolo/toolbelt/apigen/ir
//   - github.com/Yacobolo/toolbelt/apigen/emit/openapi
//   - github.com/Yacobolo/toolbelt/apigen/emit/requestmodelgo
//   - github.com/Yacobolo/toolbelt/apigen/emit/servergo
//   - github.com/Yacobolo/toolbelt/apigen/emit/cligo
//   - github.com/Yacobolo/toolbelt/apigen/emit/modelgo
//   - github.com/Yacobolo/toolbelt/apigen/emit/modelts
//   - github.com/Yacobolo/toolbelt/apigen/emit/jsonschema
//   - github.com/Yacobolo/toolbelt/apigen/runtime/chi
//   - github.com/Yacobolo/toolbelt/apigen/runtime/cobra
//   - github.com/Yacobolo/toolbelt/apigen/runtime/agenttool
//
// JSON IR is the generator input contract. Canonical OpenAPI is the published
// API contract artifact and may carry repo-owned extensions such as x-authz.
package apigen
