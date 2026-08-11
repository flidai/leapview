package main

import (
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func TestPlanGoPackagePartitions_AssignsAndCoalescesNamespacesDeterministically(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"Report": {
				Type:      "object",
				Namespace: "ExampleAPI.Analytics",
				Properties: map[string]ir.SchemaProperty{
					"owner": {Schema: ir.SchemaRef{Ref: "Principal"}},
				},
			},
			"Principal": {
				Type:      "object",
				Namespace: "ExampleAPI.Access",
				Properties: map[string]ir.SchemaProperty{
					"manager": {Schema: ir.SchemaRef{Ref: "Principal"}},
				},
			},
		},
		Endpoints: []ir.Endpoint{
			endpointWithResponse("listReports", "ExampleAPI.Analytics", "Report"),
			endpointWithResponse("getCurrentPrincipal", "ExampleAPI.Access", "Principal"),
			endpointWithResponse("listGrants", "ExampleAPI.Grants", "Principal"),
		},
	}
	accessOutput := resolvedGoPackageOutput{
		Dir:               "internal/access/api/gen",
		Package:           "accessapi",
		ServerFile:        "server.apigen.gen.go",
		RequestModelsFile: "request_models.gen.go",
	}
	analyticsOutput := resolvedGoPackageOutput{
		Dir:               "internal/analytics/api/gen",
		Package:           "analyticsapi",
		ServerFile:        "server.apigen.gen.go",
		RequestModelsFile: "request_models.gen.go",
	}
	plan := goPackagePlan{
		Unmatched: unmatchedNamespaceError,
		Packages: []namespaceGoPackageOutput{
			{Namespace: "ExampleAPI.Grants", Output: accessOutput},
			{Namespace: "ExampleAPI.Analytics", Output: analyticsOutput},
			{Namespace: "ExampleAPI.Access", Output: accessOutput},
		},
	}

	partitions, err := planGoPackagePartitions(doc, plan)
	require.NoError(t, err)
	require.Equal(t, []goPackagePartition{
		{
			Output:               accessOutput,
			Namespaces:           []string{"ExampleAPI.Access", "ExampleAPI.Grants"},
			EndpointOperationIDs: []string{"getCurrentPrincipal", "listGrants"},
			OwnedSchemaNames:     []string{"Principal"},
		},
		{
			Output:                analyticsOutput,
			Namespaces:            []string{"ExampleAPI.Analytics"},
			EndpointOperationIDs:  []string{"listReports"},
			OwnedSchemaNames:      []string{"Report"},
			DependencySchemaNames: []string{"Principal"},
		},
	}, partitions)
}

func TestPlanGoPackagePartitions_RequiresExactNamespaceMatches(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Endpoints: []ir.Endpoint{
			endpointWithResponse("listReports", "ExampleAPI.Analytics.Reports", ""),
		},
	}
	plan := goPackagePlan{
		Unmatched: unmatchedNamespaceError,
		Packages: []namespaceGoPackageOutput{{
			Namespace: "ExampleAPI.Analytics",
			Output:    resolvedGoPackageOutput{Dir: "analytics", Package: "analytics"},
		}},
	}

	_, err := planGoPackagePartitions(doc, plan)
	require.ErrorContains(t, err, `endpoint "listReports" namespace "ExampleAPI.Analytics.Reports" has no package mapping`)
}

func TestPlanGoPackagePartitions_ExcludesExternalContractSchemas(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"Dashboard": {
				Type:      "object",
				Namespace: "ExampleAPI.Dashboard",
				Properties: map[string]ir.SchemaProperty{
					"visual": {Schema: ir.SchemaRef{Ref: "ExternalVisual"}},
				},
			},
			"ExternalVisual": {
				Type:      "object",
				Namespace: "External.Visualization",
			},
		},
		Endpoints: []ir.Endpoint{
			endpointWithResponse("getDashboard", "ExampleAPI.Dashboard", "Dashboard"),
		},
	}
	output := resolvedGoPackageOutput{Dir: "dashboard", Package: "dashboardapi"}
	plan := goPackagePlan{
		Unmatched: unmatchedNamespaceError,
		Packages: []namespaceGoPackageOutput{{
			Namespace: "ExampleAPI.Dashboard",
			Output:    output,
		}},
	}
	imports := map[string]contractImportSpec{
		"External.Visualization": {
			GoPackage: "example.com/visualization",
			GoAlias:   "visualization",
		},
	}

	partitions, err := planGoPackagePartitions(doc, plan, imports)
	require.NoError(t, err)
	require.Equal(t, []goPackagePartition{{
		Output:                output,
		Namespaces:            []string{"ExampleAPI.Dashboard"},
		EndpointOperationIDs:  []string{"getDashboard"},
		OwnedSchemaNames:      []string{"Dashboard"},
		DependencySchemaNames: []string{"ExternalVisual"},
	}}, partitions)
}

func TestPlanGoPackagePartitions_RejectsLocalMappingInsideExternalContract(t *testing.T) {
	t.Helper()

	plan := goPackagePlan{
		Unmatched: unmatchedNamespaceError,
		Packages: []namespaceGoPackageOutput{{
			Namespace: "External.Visualization.Chart",
			Output:    resolvedGoPackageOutput{Dir: "chart", Package: "chart"},
		}},
	}
	imports := map[string]contractImportSpec{
		"External.Visualization": {
			GoPackage: "example.com/visualization",
			GoAlias:   "visualization",
		},
	}

	_, err := planGoPackagePartitions(ir.Document{}, plan, imports)
	require.ErrorContains(t, err, `package namespace "External.Visualization.Chart" conflicts with external contract import "External.Visualization"`)
}

func TestPlanGoPackagePartitions_RejectsEndpointInsideExternalContract(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Endpoints: []ir.Endpoint{{
			OperationID: "getExternalVisual",
			Namespace:   "External.Visualization",
		}},
	}
	plan := goPackagePlan{
		Unmatched: unmatchedNamespaceDefault,
		Default: &resolvedGoPackageOutput{
			Dir:     "default",
			Package: "defaultapi",
		},
	}
	imports := map[string]contractImportSpec{
		"External.Visualization": {
			GoPackage: "example.com/visualization",
			GoAlias:   "visualization",
		},
	}

	_, err := planGoPackagePartitions(doc, plan, imports)
	require.ErrorContains(t, err, `endpoint "getExternalVisual" namespace "External.Visualization" conflicts with external contract import "External.Visualization"`)
}

func TestPlanGoPackagePartitions_AssignsUnmatchedDeclarationsToDefault(t *testing.T) {
	t.Helper()

	defaultOutput := resolvedGoPackageOutput{Dir: "internal/api/gen", Package: "gen"}
	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"Legacy": {Type: "object"},
		},
		Endpoints: []ir.Endpoint{
			endpointWithResponse("legacy", "", "Legacy"),
		},
	}
	plan := goPackagePlan{
		Default:   &defaultOutput,
		Unmatched: unmatchedNamespaceDefault,
		Packages: []namespaceGoPackageOutput{{
			Namespace: "ExampleAPI.Access",
			Output:    resolvedGoPackageOutput{Dir: "internal/access/api/gen", Package: "accessapi"},
		}},
	}

	partitions, err := planGoPackagePartitions(doc, plan)
	require.NoError(t, err)
	require.Equal(t, []goPackagePartition{
		{
			Output:               defaultOutput,
			EndpointOperationIDs: []string{"legacy"},
			OwnedSchemaNames:     []string{"Legacy"},
		},
		{
			Output:     resolvedGoPackageOutput{Dir: "internal/access/api/gen", Package: "accessapi"},
			Namespaces: []string{"ExampleAPI.Access"},
		},
	}, partitions)
}

func TestPlanGoPackagePartitions_RejectsUnmatchedSchemas(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"Orphan": {Type: "object", Namespace: "ExampleAPI.Orphan"},
		},
	}
	plan := goPackagePlan{
		Unmatched: unmatchedNamespaceError,
		Packages: []namespaceGoPackageOutput{{
			Namespace: "ExampleAPI.Access",
			Output:    resolvedGoPackageOutput{Dir: "internal/access/api/gen", Package: "accessapi"},
		}},
	}

	_, err := planGoPackagePartitions(doc, plan)
	require.ErrorContains(t, err, `schema "Orphan" namespace "ExampleAPI.Orphan" has no package mapping`)
}

func TestPlanGoPackagePartitions_ClosesEveryEndpointSchemaReferenceTransitively(t *testing.T) {
	t.Helper()

	sharedSchemas := []string{"Alternative", "Header", "Parameter", "Part", "Problem", "Request", "Response"}
	schemas := make(map[string]ir.Schema, len(sharedSchemas)+1)
	for _, name := range sharedSchemas {
		schemas[name] = ir.Schema{Type: "object", Namespace: "ExampleAPI.Shared"}
	}
	schemas["Request"] = ir.Schema{
		Type:      "object",
		Namespace: "ExampleAPI.Shared",
		Properties: map[string]ir.SchemaProperty{
			"response": {Schema: ir.SchemaRef{Ref: "Response"}},
		},
	}
	doc := ir.Document{
		Schemas: schemas,
		Endpoints: []ir.Endpoint{{
			Method:      "post",
			Path:        "/reports",
			OperationID: "createReport",
			Namespace:   "ExampleAPI.Analytics",
			Parameters: []ir.Parameter{{
				Name: "filter", In: "query", Schema: ir.SchemaRef{Ref: "Parameter"},
			}},
			RequestBody: &ir.RequestBody{Contents: []ir.BodyContent{{
				ContentType: "multipart/form-data",
				BodyKind:    "multipart",
				Schema:      &ir.SchemaRef{Ref: "Request"},
				AnyOf:       []ir.SchemaRef{{Ref: "Alternative"}},
				Parts:       []ir.MultipartPart{{Name: "part", Schema: &ir.SchemaRef{Ref: "Part"}}},
			}}},
			Responses: []ir.Response{{
				StatusCode:  200,
				Description: "ok",
				Headers:     []ir.Header{{Name: "X-Result", Schema: ir.SchemaRef{Ref: "Header"}}},
				Contents: []ir.BodyContent{{
					ContentType: "application/json",
					BodyKind:    "json",
					Schema:      &ir.SchemaRef{Ref: "Response"},
				}},
			}},
		}},
		TransportErrors: &ir.TransportErrors{Schema: ir.SchemaRef{Ref: "Problem"}},
	}
	plan := goPackagePlan{
		Unmatched: unmatchedNamespaceError,
		Packages: []namespaceGoPackageOutput{
			{
				Namespace: "ExampleAPI.Analytics",
				Output:    resolvedGoPackageOutput{Dir: "analytics", Package: "analytics"},
			},
			{
				Namespace: "ExampleAPI.Shared",
				Output:    resolvedGoPackageOutput{Dir: "shared", Package: "shared"},
			},
		},
	}

	partitions, err := planGoPackagePartitions(doc, plan)
	require.NoError(t, err)
	require.Equal(t, sharedSchemas, partitions[0].DependencySchemaNames)
	require.Equal(t, sharedSchemas, partitions[1].OwnedSchemaNames)
}

func TestPlanGoPackagePartitions_SharedRecursiveDependenciesAppearInEveryConsumer(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"Principal": {
				Type:      "object",
				Namespace: "ExampleAPI.Access",
				Properties: map[string]ir.SchemaProperty{
					"tenant": {Schema: ir.SchemaRef{Ref: "Tenant"}},
				},
			},
			"Report": {
				Type:      "object",
				Namespace: "ExampleAPI.Analytics",
				Properties: map[string]ir.SchemaProperty{
					"tenant": {Schema: ir.SchemaRef{Ref: "Tenant"}},
				},
			},
			"Tenant": {
				Type:      "object",
				Namespace: "ExampleAPI.Shared",
				Properties: map[string]ir.SchemaProperty{
					"parent": {Schema: ir.SchemaRef{Ref: "Tenant"}},
				},
			},
		},
	}
	plan := goPackagePlan{
		Unmatched: unmatchedNamespaceError,
		Packages: []namespaceGoPackageOutput{
			{Namespace: "ExampleAPI.Shared", Output: resolvedGoPackageOutput{Dir: "shared", Package: "shared"}},
			{Namespace: "ExampleAPI.Analytics", Output: resolvedGoPackageOutput{Dir: "analytics", Package: "analytics"}},
			{Namespace: "ExampleAPI.Access", Output: resolvedGoPackageOutput{Dir: "access", Package: "access"}},
		},
	}

	partitions, err := planGoPackagePartitions(doc, plan)
	require.NoError(t, err)
	require.Equal(t, []string{"Tenant"}, partitions[0].DependencySchemaNames)
	require.Equal(t, []string{"Tenant"}, partitions[1].DependencySchemaNames)
	require.Empty(t, partitions[2].DependencySchemaNames)
}

func endpointWithResponse(operationID, namespace, schemaName string) ir.Endpoint {
	endpoint := ir.Endpoint{
		Method:      "get",
		Path:        "/" + operationID,
		OperationID: operationID,
		Namespace:   namespace,
		Responses:   []ir.Response{{StatusCode: 200, Description: "ok"}},
	}
	if schemaName != "" {
		endpoint.Responses[0].Contents = []ir.BodyContent{{
			ContentType: "application/json",
			BodyKind:    "json",
			Schema:      &ir.SchemaRef{Ref: schemaName},
		}}
	}
	return endpoint
}
