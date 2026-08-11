package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	requestmodelgoemit "github.com/Yacobolo/toolbelt/apigen/emit/requestmodelgo"
	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func TestPartitionContractImports_AllocatesStableAliases(t *testing.T) {
	t.Helper()

	genA := "github.com/acme/a/gen"
	genB := "github.com/acme/b/gen"
	jsonPackage := "github.com/acme/json"
	projection := goPackageProjection{
		Dependencies: []goPackageDependency{
			dependencyForImport("gen", genB, "ExampleAPI.B", "B"),
			dependencyForImport("accessapi", "github.com/acme/accessapi", "ExampleAPI.Access", "Principal"),
			dependencyForImport("json", jsonPackage, "ExampleAPI.JSON", "Document"),
			dependencyForImport("gen", genA, "ExampleAPI.A", "A"),
		},
	}

	imports, err := partitionContractImports(projection)
	require.NoError(t, err)
	require.Equal(t, map[string]contractImportSpec{
		"ExampleAPI.A": {
			GoPackage:      genA,
			GoAlias:        "gen_" + shortImportDigest(genA),
			ExactNamespace: true,
		},
		"ExampleAPI.Access": {
			GoPackage:      "github.com/acme/accessapi",
			GoAlias:        "accessapi",
			ExactNamespace: true,
		},
		"ExampleAPI.B": {
			GoPackage:      genB,
			GoAlias:        "gen_" + shortImportDigest(genB),
			ExactNamespace: true,
		},
		"ExampleAPI.JSON": {
			GoPackage:      jsonPackage,
			GoAlias:        "json_" + shortImportDigest(jsonPackage),
			ExactNamespace: true,
		},
	}, imports)

	reversed := projection
	reversed.Dependencies = append([]goPackageDependency(nil), projection.Dependencies...)
	for left, right := 0, len(reversed.Dependencies)-1; left < right; left, right = left+1, right-1 {
		reversed.Dependencies[left], reversed.Dependencies[right] = reversed.Dependencies[right], reversed.Dependencies[left]
	}
	reversed.Dependencies = append(reversed.Dependencies,
		dependencyForImport("unrelated", "github.com/acme/unrelated", "ExampleAPI.Unrelated", "Other"))

	reorderedImports, err := partitionContractImports(reversed)
	require.NoError(t, err)
	for namespace, binding := range imports {
		require.Equal(t, binding, reorderedImports[namespace])
	}
}

func TestPartitionContractImports_RejectsAmbiguousNamespaceOwners(t *testing.T) {
	t.Helper()

	projection := goPackageProjection{
		Dependencies: []goPackageDependency{
			dependencyForImport("first", "github.com/acme/first", "ExampleAPI.Shared", "First"),
			dependencyForImport("second", "github.com/acme/second", "ExampleAPI.Shared", "Second"),
		},
	}

	_, err := partitionContractImports(projection)
	require.ErrorContains(t, err, `dependency namespace "ExampleAPI.Shared" has multiple package owners`)
}

func TestPartitionContractImports_CoalescesNamespacesOwnedByOnePackage(t *testing.T) {
	t.Helper()

	projection := goPackageProjection{
		Dependencies: []goPackageDependency{{
			Output: resolvedGoPackageOutput{
				Dir: "identity", Package: "identityapi", ImportPath: "github.com/acme/identityapi",
			},
			Schemas: []goPackageDependencySchema{
				{Name: "Principal", Namespace: "ExampleAPI.Access"},
				{Name: "Session", Namespace: "ExampleAPI.Sessions"},
			},
		}},
	}

	imports, err := partitionContractImports(projection)
	require.NoError(t, err)
	binding := contractImportSpec{
		GoPackage: "github.com/acme/identityapi", GoAlias: "identityapi", ExactNamespace: true,
	}
	require.Equal(t, map[string]contractImportSpec{
		"ExampleAPI.Access":   binding,
		"ExampleAPI.Sessions": binding,
	}, imports)
}

func TestPartitionContractImports_EmitsProjectedRequestModels(t *testing.T) {
	t.Helper()

	projection := goPackageProjection{
		Document: ir.Document{
			Info: ir.Info{Namespace: "ExampleAPI"},
			Schemas: map[string]ir.Schema{
				"Envelope": {
					Type:      "object",
					Namespace: "ExampleAPI.Access",
					Properties: map[string]ir.SchemaProperty{
						"shared": {Schema: ir.SchemaRef{Ref: "Shared"}},
					},
				},
				"Shared": {Type: "object", Namespace: "ExampleAPI.Shared"},
			},
			Endpoints: []ir.Endpoint{
				endpointWithResponse("getEnvelope", "ExampleAPI.Access", "Envelope"),
			},
		},
		Dependencies: []goPackageDependency{
			dependencyForImport("sharedapi", "github.com/acme/sharedapi", "ExampleAPI.Shared", "Shared"),
		},
	}
	imports, err := partitionContractImports(projection)
	require.NoError(t, err)

	content, err := requestmodelgoemit.Emit(projection.Document, requestmodelgoemit.Options{
		PackageName:     "accessapi",
		ContractImports: emitterContractImports(imports),
	})
	require.NoError(t, err)
	generated := string(content)
	require.Contains(t, generated, `sharedapi "github.com/acme/sharedapi"`)
	require.Contains(t, generated, "Shared *sharedapi.Shared")
}

func TestPartitionContractImports_RootDependencyDoesNotCaptureOwnedChildNamespace(t *testing.T) {
	t.Helper()

	projection := goPackageProjection{
		Document: ir.Document{
			Info: ir.Info{Namespace: "ExampleAPI"},
			Schemas: map[string]ir.Schema{
				"AgentConversation": {
					Type:      "object",
					Namespace: "ExampleAPI.Agent",
					Properties: map[string]ir.SchemaProperty{
						"page": {Schema: ir.SchemaRef{Ref: "PageInfo"}},
					},
				},
				"PageInfo": {Type: "object", Namespace: "ExampleAPI"},
			},
			Endpoints: []ir.Endpoint{
				endpointWithResponse("getAgentConversation", "ExampleAPI.Agent", "AgentConversation"),
			},
		},
		Dependencies: []goPackageDependency{
			dependencyForImport("gen", "github.com/acme/app/api/gen", "ExampleAPI", "PageInfo"),
		},
	}
	imports, err := partitionContractImports(projection)
	require.NoError(t, err)

	content, err := requestmodelgoemit.Emit(projection.Document, requestmodelgoemit.Options{
		PackageName:     "agentapi",
		ContractImports: emitterContractImports(imports),
	})
	require.NoError(t, err)
	generated := string(content)
	require.Contains(t, generated, `gen "github.com/acme/app/api/gen"`)
	require.Contains(t, generated, "type AgentConversation struct")
	require.Contains(t, generated, "Page *gen.PageInfo")
	require.Contains(t, generated, "type GenSchemaAgentConversation = AgentConversation")
	require.NotContains(t, generated, "type GenSchemaAgentConversation = gen.AgentConversation")
}

func dependencyForImport(packageName, importPath, namespace string, schemas ...string) goPackageDependency {
	dependency := goPackageDependency{
		Output: resolvedGoPackageOutput{
			Dir:        strings.TrimPrefix(importPath, "github.com/acme/"),
			Package:    packageName,
			ImportPath: importPath,
		},
	}
	for _, schema := range schemas {
		dependency.Schemas = append(dependency.Schemas, goPackageDependencySchema{
			Name: schema, Namespace: namespace,
		})
	}
	return dependency
}

func shortImportDigest(importPath string) string {
	sum := sha256.Sum256([]byte(importPath))
	return hex.EncodeToString(sum[:])[:8]
}
