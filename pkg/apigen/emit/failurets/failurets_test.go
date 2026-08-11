package failurets

import (
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func TestEmitGeneratesDiscriminatedFailureUnionAndRequiredMatcher(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "Release", Version: "1.0.0"},
		Endpoints: []ir.Endpoint{{
			Method: "delete", Path: "/releases/{release}", OperationID: "finalizeRelease", Kind: "command",
			Parameters: []ir.Parameter{{Name: "release", In: "path", Required: true, Schema: ir.SchemaRef{Type: "string"}}},
			Responses:  []ir.Response{{StatusCode: 204, Description: "ok"}, {StatusCode: 404, Description: "missing"}, {StatusCode: 409, Description: "conflict"}},
			Command: &ir.Command{
				Owner: "Release", Audit: ir.AuditPolicy{Required: true, SuccessAction: "release.finalized", Guarantee: "best-effort"},
				Failures: []ir.CommandFailure{
					{Kind: "conflict", StatusCode: 409, Code: "RELEASE_CONFLICT", PublicDetail: "Conflict."},
					{Kind: "not_found", StatusCode: 404, Code: "RELEASE_NOT_FOUND", PublicDetail: "Missing."},
				},
			},
		}},
	}

	content, err := Emit(doc)
	require.NoError(t, err)
	generated := string(content)
	require.Contains(t, generated, `export type FinalizeReleaseFailure =`)
	require.Contains(t, generated, `{ kind: "not_found"; code: "RELEASE_NOT_FOUND"; status: 404; problem: APIGenProblemDetails }`)
	require.Contains(t, generated, `export function matchFinalizeReleaseFailure<T>`)
	require.Contains(t, generated, `"conflict": (problem: APIGenProblemDetails) => T;`)
	require.Contains(t, generated, `"not_found": (problem: APIGenProblemDetails) => T;`)
}
