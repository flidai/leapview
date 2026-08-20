package servergo

import (
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func TestPrepareDocumentForEmitValidatesAndDetaches(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "test", Version: "1"},
		Endpoints: []ir.Endpoint{{
			Method: "get", Path: "/healthz", OperationID: "getHealth",
			Tags: []string{"health"}, Responses: []ir.Response{{StatusCode: 200, Description: "ok"}},
		}},
	}

	prepared, err := prepareDocumentForEmit(doc)
	require.NoError(t, err)
	prepared.Endpoints[0].Tags[0] = "changed"
	require.Equal(t, "health", doc.Endpoints[0].Tags[0], "preparation must not alias caller-owned slices")

	doc.SchemaVersion = "v3"
	_, err = prepareDocumentForEmit(doc)
	require.Error(t, err)
}

func TestDiscoverEmissionPlanDerivesStableFeatures(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "test", Version: "1"},
		Endpoints: []ir.Endpoint{{
			Method: "post", Path: "/items", OperationID: "createItem",
			RequestBody: &ir.RequestBody{Required: true, Contents: []ir.BodyContent{{
				ContentType: "application/json", BodyKind: "json", Schema: &ir.SchemaRef{Type: "object"},
			}}},
			Responses: []ir.Response{{StatusCode: 201, Description: "created"}},
		}},
	}

	plan, err := discoverEmissionPlan(doc, Options{PackageName: "items", EmbeddedOpenAPISpecJSON: "{}"})
	require.NoError(t, err)
	require.Equal(t, "items", plan.packageName)
	require.True(t, plan.hasStrictOperations)
	require.True(t, plan.hasRequestBodies)
	require.False(t, plan.hasMultipartBodies)
	require.True(t, plan.usesFmt)
	require.False(t, plan.hasTools)
	require.Equal(t, "{}", plan.specJSON)
}

func TestEmitIsByteStableAcrossRuns(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "stable", Version: "1"},
		Endpoints: []ir.Endpoint{
			{Method: "get", Path: "/b", OperationID: "getB", Responses: []ir.Response{{StatusCode: 200, Description: "ok"}}},
			{Method: "get", Path: "/a", OperationID: "getA", Responses: []ir.Response{{StatusCode: 200, Description: "ok"}}},
		},
	}

	first, err := Emit(doc, Options{EmbeddedOpenAPISpecJSON: "{}"})
	require.NoError(t, err)
	second, err := Emit(doc, Options{EmbeddedOpenAPISpecJSON: "{}"})
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestGeneratedNamingPolicyIsSharedByValidationAndRendering(t *testing.T) {
	require.Equal(t, "CreateRoleBinding", exportedName("create-role_binding"))
	require.Equal(t, "requestId", lowerCamelName("request_id"))
	require.Error(t, ValidateOperationIDs(ir.Document{Endpoints: []ir.Endpoint{
		{OperationID: "create-role"},
		{OperationID: "create_role"},
	}}))
}
