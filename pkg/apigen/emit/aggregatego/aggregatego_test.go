package aggregatego

import (
	"go/format"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmit_ComposesTypedCapabilityServers(t *testing.T) {
	t.Helper()

	generated, err := Emit(Options{
		PackageName:             "aggregate",
		EmbeddedOpenAPISpecJSON: `{"openapi":"3.0.0","paths":{"/access":{},"/dashboard":{}}}`,
		Packages: []ServerPackage{
			{
				Name:        "Dashboard",
				ImportPath:  "example.com/service/internal/dashboard/api/gen",
				PackageName: "dashboardapi",
				HasTools:    true,
			},
			{
				Name:        "Access",
				ImportPath:  "example.com/service/internal/access/api/gen",
				PackageName: "accessapi",
			},
		},
	})
	require.NoError(t, err)
	_, err = format.Source(generated)
	require.NoError(t, err)

	content := string(generated)
	require.Contains(t, content, `accessapi "example.com/service/internal/access/api/gen"`)
	require.Contains(t, content, `dashboardapi "example.com/service/internal/dashboard/api/gen"`)
	require.Contains(t, content, "type Servers struct {\n\tAccess accessapi.GenServerInterface\n\tDashboard dashboardapi.GenServerInterface\n}")
	require.Contains(t, content, "accessapi.RegisterAPIGenRoutes(router, servers.Access)")
	require.Contains(t, content, "dashboardapi.RegisterAPIGenRoutes(router, servers.Dashboard)")
	require.Contains(t, content, "type StrictServers struct {\n\tAccess accessapi.GenStrictServerInterface\n\tDashboard dashboardapi.GenStrictServerInterface\n}")
	require.Contains(t, content, "type TransportErrorResponders struct {\n\tAccess accessapi.GenTransportErrorResponder\n\tDashboard dashboardapi.GenTransportErrorResponder\n}")
	require.Contains(t, content, "accessapi.RegisterAPIGenStrictRoutes(router, servers.Access, responders.Access)")
	require.Contains(t, content, "dashboardapi.RegisterAPIGenStrictRoutes(router, servers.Dashboard, responders.Dashboard)")
	require.Contains(t, content, "func GetEmbeddedOpenAPISpec() (map[string]any, error)")
	require.Contains(t, content, "for operationID, contract := range accessapi.GetAPIGenOperationContracts()")
	require.Contains(t, content, "for operationID, contract := range dashboardapi.GetAPIGenOperationContracts()")
	require.Contains(t, content, "func GetAPIGenOperationContracts() map[string]GenOperationContract")
	require.Contains(t, content, "func GetAPIGenOperationContract(operationID string) (GenOperationContract, bool)")
	require.Contains(t, content, "func GetAPIGenCommandRuntimeContract(operationID string) (apigencommand.Contract, bool)")
	require.Contains(t, content, "func GetAPIGenOperationContractForRequest(method, path string) (GenOperationContract, bool)")
	require.Contains(t, content, "func GetAPIGenCommandRuntimeContracts() map[string]apigencommand.Contract")
	require.Contains(t, content, "Idempotency: apigencommand.IdempotencyPolicy(contract.Command.Idempotency)")
	require.Contains(t, content, "Concurrency: apigencommand.ConcurrencyPolicy(contract.Command.Concurrency)")
	require.Contains(t, content, "type GenOperationSurface string")
	require.Contains(t, content, "type GenCommandContract struct {")
	require.Contains(t, content, "type GenAsyncExecutionContract struct {")
	require.Contains(t, content, "type GenCommandFailure struct {")
	require.Contains(t, content, "Mode string; Guarantee string; JobKind string")
	require.Contains(t, content, "Namespace string")
	require.Contains(t, content, "Command *GenCommandContract")
	require.Contains(t, content, "if contract.Command != nil { command = &GenCommandContract{")
	require.Contains(t, content, "command.AdditionalExposures[index] = GenOperationSurface(exposure)")
	require.Contains(t, content, "if contract.Command.Target != nil { target := *contract.Command.Target")
	require.Contains(t, content, "if contract.Command.Execution != nil { source := contract.Command.Execution")
	require.Contains(t, content, "Guarantee: source.Guarantee")
	require.Contains(t, content, "func GetAPIGenCommandFailureContracts(operationID string) ([]apigenfailure.Contract, bool)")
	require.Contains(t, content, "func APIGenOperationAllowsStatus(operationID string, statusCode int) bool")
	require.NotContains(t, content, "accessapi.GetAPIGenToolContracts()")
	require.Contains(t, content, "for name, contract := range dashboardapi.GetAPIGenToolContracts()")
	require.Contains(t, content, "func GetAPIGenToolContracts() map[string]apigenagenttool.Contract")
	require.Contains(t, content, "func GetAPIGenToolContract(name string) (apigenagenttool.Contract, bool)")
}

func TestEmit_ResolvesNamesAndAliasesDeterministically(t *testing.T) {
	t.Helper()

	packages := []ServerPackage{
		{Name: "Shared API", ImportPath: "example.com/one/gen", PackageName: "gen"},
		{Name: "Shared-API", ImportPath: "example.com/two/gen", PackageName: "gen"},
	}
	first, err := Emit(Options{PackageName: "aggregate", EmbeddedOpenAPISpecJSON: `{}`, Packages: packages})
	require.NoError(t, err)
	second, err := Emit(Options{PackageName: "aggregate", EmbeddedOpenAPISpecJSON: `{}`, Packages: []ServerPackage{packages[1], packages[0]}})
	require.NoError(t, err)
	require.Equal(t, first, second)

	content := string(first)
	require.Contains(t, content, `"example.com/one/gen"`)
	require.Contains(t, content, `"example.com/two/gen"`)
	require.NotContains(t, content, "\tgen \"")
	require.Regexp(t, `SharedAPI[0-9A-F]{8} gen_[0-9a-f]{8}\.GenServerInterface`, content)
}

func TestEmit_RejectsEmptyOrInvalidInputs(t *testing.T) {
	t.Helper()

	_, err := Emit(Options{PackageName: "aggregate"})
	require.ErrorContains(t, err, "at least one server package")

	_, err = Emit(Options{
		PackageName:             "aggregate",
		EmbeddedOpenAPISpecJSON: `{}`,
		Packages: []ServerPackage{{
			Name:        "Access",
			ImportPath:  "../access",
			PackageName: "accessapi",
		}},
	})
	require.ErrorContains(t, err, "canonical Go import path")

	_, err = Emit(Options{
		PackageName:             "aggregate",
		EmbeddedOpenAPISpecJSON: `{}`,
		Packages: []ServerPackage{{
			Name:        "Access",
			ImportPath:  "example.com/access",
			PackageName: "_",
		}},
	})
	require.ErrorContains(t, err, `invalid Go package "_"`)

	_, err = Emit(Options{
		PackageName: "aggregate",
		Packages: []ServerPackage{{
			Name:        "Access",
			ImportPath:  "example.com/access",
			PackageName: "accessapi",
		}},
	})
	require.ErrorContains(t, err, "embedded canonical OpenAPI")
}
