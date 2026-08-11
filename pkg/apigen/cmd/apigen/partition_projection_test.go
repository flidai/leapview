package main

import (
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func TestProjectGoPackagePartitions_FiltersDocumentsAndDescribesDependencies(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/v1"},
		Info:          ir.Info{Title: "Example", Version: "1.0.0", Namespace: "ExampleAPI"},
		OpenAPI:       ir.OpenAPI{Version: "3.0.0"},
		Servers:       []ir.Server{{URL: "https://example.test"}},
		Tags:          []ir.Tag{{Name: "example"}},
		Extensions:    map[string]any{"x-example": "preserved"},
		Schemas: map[string]ir.Schema{
			"Dashboard": {Type: "object", Namespace: "ExampleAPI.Dashboard"},
			"Principal": {Type: "object", Namespace: "ExampleAPI.Access"},
			"SharedA":   {Type: "object", Namespace: "ExampleAPI.Shared"},
			"SharedB": {
				Type:      "object",
				Namespace: "ExampleAPI.Shared",
				Properties: map[string]ir.SchemaProperty{
					"parent": {Schema: ir.SchemaRef{Ref: "SharedA"}},
				},
			},
		},
		Endpoints: []ir.Endpoint{
			endpointWithResponse("getCurrentPrincipal", "ExampleAPI.Access", "Principal"),
			endpointWithResponse("getDashboard", "ExampleAPI.Dashboard", "Dashboard"),
		},
		TransportErrors: &ir.TransportErrors{
			Schema:      ir.SchemaRef{Ref: "SharedA"},
			ContentType: "application/problem+json",
		},
	}
	accessOutput := resolvedGoPackageOutput{
		Dir:        "internal/access/api/gen",
		Package:    "accessapi",
		ImportPath: "github.com/acme/example/internal/access/api/gen",
	}
	dashboardOutput := resolvedGoPackageOutput{
		Dir:        "internal/dashboard/api/gen",
		Package:    "dashboardapi",
		ImportPath: "github.com/acme/example/internal/dashboard/api/gen",
	}
	sharedOutput := resolvedGoPackageOutput{
		Dir:        "internal/platform/api/gen",
		Package:    "sharedapi",
		ImportPath: "github.com/acme/example/internal/platform/api/gen",
	}
	partitions := []goPackagePartition{
		{
			Output:                accessOutput,
			Namespaces:            []string{"ExampleAPI.Access"},
			EndpointOperationIDs:  []string{"getCurrentPrincipal"},
			OwnedSchemaNames:      []string{"Principal"},
			DependencySchemaNames: []string{"SharedA"},
		},
		{
			Output:                dashboardOutput,
			Namespaces:            []string{"ExampleAPI.Dashboard"},
			EndpointOperationIDs:  []string{"getDashboard"},
			OwnedSchemaNames:      []string{"Dashboard"},
			DependencySchemaNames: []string{"SharedA", "SharedB"},
		},
		{
			Output:           sharedOutput,
			Namespaces:       []string{"ExampleAPI.Shared"},
			OwnedSchemaNames: []string{"SharedA", "SharedB"},
		},
	}
	before := doc

	projections, err := projectGoPackagePartitions(doc, partitions)
	require.NoError(t, err)
	require.Equal(t, before, doc)
	require.Len(t, projections, 3)

	access := projections[0]
	require.Equal(t, accessOutput, access.Partition.Output)
	require.Equal(t, doc.SchemaVersion, access.Document.SchemaVersion)
	require.Equal(t, doc.API, access.Document.API)
	require.Equal(t, doc.Info, access.Document.Info)
	require.Equal(t, doc.OpenAPI, access.Document.OpenAPI)
	require.Equal(t, doc.Servers, access.Document.Servers)
	require.Equal(t, doc.Tags, access.Document.Tags)
	require.Equal(t, doc.TransportErrors, access.Document.TransportErrors)
	require.Equal(t, doc.Extensions, access.Document.Extensions)
	require.Equal(t, []string{"getCurrentPrincipal"}, endpointOperationIDs(access.Document.Endpoints))
	require.Equal(t, []string{"Principal", "SharedA"}, sortedSchemaNames(access.Document.Schemas))
	require.Equal(t, []goPackageDependency{{
		Output: sharedOutput,
		Schemas: []goPackageDependencySchema{{
			Name: "SharedA", Namespace: "ExampleAPI.Shared",
		}},
	}}, access.Dependencies)

	dashboard := projections[1]
	require.Equal(t, []string{"getDashboard"}, endpointOperationIDs(dashboard.Document.Endpoints))
	require.Equal(t, []string{"Dashboard", "SharedA", "SharedB"}, sortedSchemaNames(dashboard.Document.Schemas))
	require.Equal(t, []goPackageDependency{{
		Output: sharedOutput,
		Schemas: []goPackageDependencySchema{
			{Name: "SharedA", Namespace: "ExampleAPI.Shared"},
			{Name: "SharedB", Namespace: "ExampleAPI.Shared"},
		},
	}}, dashboard.Dependencies)

	shared := projections[2]
	require.Empty(t, shared.Document.Endpoints)
	require.Equal(t, []string{"SharedA", "SharedB"}, sortedSchemaNames(shared.Document.Schemas))
	require.Nil(t, shared.Document.TransportErrors)
	require.Empty(t, shared.Dependencies)
}

func TestProjectGoPackagePartitions_PreservesConfiguredEmptyPartitions(t *testing.T) {
	t.Helper()

	output := resolvedGoPackageOutput{
		Dir:        "internal/access/api/gen",
		Package:    "accessapi",
		ImportPath: "github.com/acme/example/internal/access/api/gen",
	}

	projections, err := projectGoPackagePartitions(ir.Document{
		SchemaVersion: "v4",
		Info:          ir.Info{Title: "Example", Version: "1.0.0"},
	}, []goPackagePartition{{
		Output:     output,
		Namespaces: []string{"ExampleAPI.Access"},
	}})
	require.NoError(t, err)
	require.Equal(t, []goPackageProjection{{
		Partition: goPackagePartition{
			Output:     output,
			Namespaces: []string{"ExampleAPI.Access"},
		},
		Document: ir.Document{
			SchemaVersion: "v4",
			Info:          ir.Info{Title: "Example", Version: "1.0.0"},
		},
	}}, projections)
}

func TestProjectGoPackagePartitions_RejectsInvalidPlanReferences(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"Owned": {
				Type:      "object",
				Namespace: "ExampleAPI.Access",
				Properties: map[string]ir.SchemaProperty{
					"dependency": {Schema: ir.SchemaRef{Ref: "Dependency"}},
				},
			},
			"Dependency": {Type: "object", Namespace: "ExampleAPI.Shared"},
		},
		Endpoints: []ir.Endpoint{endpointWithResponse("getOwned", "ExampleAPI.Access", "Owned")},
	}
	output := resolvedGoPackageOutput{
		Dir: "internal/access/api/gen", Package: "accessapi", ImportPath: "github.com/acme/accessapi",
	}
	dependencyOutput := resolvedGoPackageOutput{
		Dir: "internal/shared/api/gen", Package: "sharedapi", ImportPath: "github.com/acme/sharedapi",
	}
	tests := []struct {
		name       string
		partitions []goPackagePartition
		wantErr    string
	}{
		{
			name: "missing endpoint",
			partitions: []goPackagePartition{{
				Output: output, EndpointOperationIDs: []string{"missing"},
			}},
			wantErr: `partition "github.com/acme/accessapi" references unknown endpoint "missing"`,
		},
		{
			name: "missing owned schema",
			partitions: []goPackagePartition{{
				Output: output, OwnedSchemaNames: []string{"Missing"},
			}},
			wantErr: `partition "github.com/acme/accessapi" references unknown schema "Missing"`,
		},
		{
			name: "dependency has no owner",
			partitions: []goPackagePartition{{
				Output: output, OwnedSchemaNames: []string{"Owned"}, DependencySchemaNames: []string{"Dependency"},
			}},
			wantErr: `dependency schema "Dependency" has no package owner`,
		},
		{
			name: "incomplete dependency closure",
			partitions: []goPackagePartition{{
				Output: output, EndpointOperationIDs: []string{"getOwned"}, OwnedSchemaNames: []string{"Owned"},
			}},
			wantErr: `partition "github.com/acme/accessapi" omits required schema "Dependency"`,
		},
		{
			name: "schema has multiple owners",
			partitions: []goPackagePartition{
				{Output: output, OwnedSchemaNames: []string{"Owned"}},
				{Output: dependencyOutput, OwnedSchemaNames: []string{"Owned"}},
			},
			wantErr: `schema "Owned" has multiple package owners`,
		},
		{
			name: "dependency owner has no import path",
			partitions: []goPackagePartition{
				{Output: output, DependencySchemaNames: []string{"Dependency"}},
				{Output: resolvedGoPackageOutput{Dir: "shared", Package: "shared"}, OwnedSchemaNames: []string{"Dependency"}},
			},
			wantErr: `dependency schema "Dependency" owner has no import path`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := projectGoPackagePartitions(doc, tt.partitions)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func endpointOperationIDs(endpoints []ir.Endpoint) []string {
	ids := make([]string, len(endpoints))
	for index, endpoint := range endpoints {
		ids[index] = endpoint.OperationID
	}
	return ids
}
