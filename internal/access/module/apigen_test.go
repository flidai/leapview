package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func TestAPIGenReplayAuthorizationSelectsCurrentOperationPolicy(t *testing.T) {
	authorizer := testAPIGenAuthorizer(t)
	contract, ok := authorizer.operations["createGroup"]
	if !ok {
		t.Fatal("createGroup contract is missing")
	}
	routeContext := chi.NewRouteContext()
	routeContext.RoutePatterns = append(routeContext.RoutePatterns, contract.Path)
	request := httptest.NewRequest(contract.Method, "/api/v1/workspaces/test/groups", nil)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	if !authorizer.AuthorizeReplay(request) {
		t.Fatal("current generated operation authorization rejected dev-bypass request")
	}
	request.Method = http.MethodDelete
	if authorizer.AuthorizeReplay(request) {
		t.Fatal("mismatched operation method was authorized")
	}
}

func testAPIGenAuthorizer(t *testing.T) *APIGenAuthorizer {
	t.Helper()
	resolver := func(*http.Request, string) []ObjectRef { return nil }
	authorizer, err := (&Module{}).APIGenAuthorizer(testAPIGenContracts(), APIGenObjectResolvers{
		Dashboard: resolver, SemanticModel: resolver, WorkspaceAsset: resolver,
		ProjectEnvironment: resolver,
	})
	require.NoError(t, err)
	return authorizer
}

func testAPIGenContracts() map[string]APIGenOperationContract {
	generated := apiaggregate.GetAPIGenOperationContracts()
	contracts := make(map[string]APIGenOperationContract, len(generated))
	for operationID, contract := range generated {
		var command *APIGenCommandContract
		if contract.Command != nil {
			command = &APIGenCommandContract{
				Owner:       contract.Command.Owner,
				AuthzMode:   contract.Command.AuthzMode,
				Privilege:   contract.Command.Privilege,
				Idempotency: contract.Command.Idempotency,
				Concurrency: contract.Command.Concurrency,
			}
			if contract.Command.Target != nil {
				command.Target = &APIGenCommandTarget{
					Parameter: contract.Command.Target.Parameter,
					Type:      contract.Command.Target.Type,
				}
			}
		}
		contracts[operationID] = APIGenOperationContract{
			OperationID: contract.OperationID, Method: contract.Method, Path: contract.Path, Protected: contract.Protected,
			AuthzMode: contract.AuthzMode, Command: command, Extensions: contract.Extensions,
		}
	}
	return contracts
}

func TestAPIGenCommandTargetSelectsWorkspaceAuthorizationObject(t *testing.T) {
	authorizer := testAPIGenAuthorizer(t)
	contract := APIGenOperationContract{
		Path: "/api/v1/commands/{workspace}/execute",
		Command: &APIGenCommandContract{
			Target: &APIGenCommandTarget{Parameter: "workspace", Type: "workspace"},
		},
		// Legacy query scope metadata must not override a typed command target.
		Extensions: map[string]any{apiGenObjectScopeExtension: "platform"},
	}
	resolver, ok := authorizer.objectResolverForContract(contract)
	require.True(t, ok)
	require.NotNil(t, resolver)

	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("workspace", "contract-workspace")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/commands/contract-workspace/execute", nil)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	require.Equal(t, []ObjectRef{WorkspaceObject("contract-workspace")}, resolver(request, "fallback-workspace"))
}

func TestAPIGenPrincipalTargetPreservesPlatformAndCredentialWorkspaceBoundaries(t *testing.T) {
	authorizer := testAPIGenAuthorizer(t)
	resolver, ok := authorizer.objectResolverForContract(APIGenOperationContract{
		Command: &APIGenCommandContract{Target: &APIGenCommandTarget{Parameter: "principal", Type: "principal"}},
	})
	require.True(t, ok)
	require.Equal(t, []ObjectRef{PlatformObject(), WorkspaceObject("sales")}, resolver(httptest.NewRequest(http.MethodPatch, "/api/v1/principals/p1", nil), "sales"))
}

func TestAPIGenOperationPrivilegeUsesTypedCommandAuthorization(t *testing.T) {
	contract := APIGenOperationContract{
		AuthzMode: "privilege",
		Command:   &APIGenCommandContract{AuthzMode: "privilege", Privilege: string(access.PrivilegeManagePlatform)},
		Extensions: map[string]any{
			"x-authz": map[string]any{"mode": "privilege", "privilege": string(access.PrivilegeUseWorkspace)},
		},
	}
	privilege, ok := apiGenOperationPrivilege(contract)
	if !ok || privilege != access.PrivilegeManagePlatform {
		t.Fatalf("typed command privilege = %q, %t", privilege, ok)
	}
	contract.Command.AuthzMode = "authenticated"
	if _, ok := apiGenOperationPrivilege(contract); ok {
		t.Fatal("mismatched typed command authorization was accepted")
	}
}

func TestAPIGenAuthorizationContractCoverage(t *testing.T) {
	authorizer := testAPIGenAuthorizer(t)
	contracts := authorizer.operations
	if len(contracts) == 0 {
		t.Fatal("no generated operation contracts")
	}
	publicAuthoringAuth := map[string]bool{
		"getInstance": true,
	}
	authenticatedOnly := map[string]bool{
		"decideDeviceAuthorization": true,
		"getCapabilities":           true,
	}
	for operationID, contract := range contracts {
		if publicAuthoringAuth[operationID] {
			if contract.Protected || contract.AuthzMode != "none" {
				t.Fatalf("%s authorization = protected:%t mode:%q, want public credential exchange", operationID, contract.Protected, contract.AuthzMode)
			}
			continue
		}
		if !contract.Protected {
			t.Fatalf("%s auth contract is not protected", operationID)
		}
		_, ok := apiGenOperationPrivilege(contract)
		if !ok {
			t.Fatalf("%s has invalid authorization metadata", operationID)
		}
		if authenticatedOnly[operationID] {
			if contract.AuthzMode != "authenticated" {
				t.Fatalf("%s auth mode = %q, want authenticated", operationID, contract.AuthzMode)
			}
			continue
		}
		if contract.AuthzMode != "privilege" {
			t.Fatalf("%s auth mode = %q, want privilege", operationID, contract.AuthzMode)
		}
		if (contract.Command != nil && strings.HasSuffix(contract.Command.Owner, ".Agent")) || (contract.Command == nil && isGlobalAgentQuery(operationID)) {
			if _, hasScope := contract.Extensions[apiGenObjectScopeExtension]; hasScope {
				t.Fatalf("%s global operation retains object-scope metadata", operationID)
			}
			continue
		}
		if _, ok := authorizer.objectResolverForContract(contract); !ok {
			t.Fatalf("%s has invalid object scope for %q", operationID, contract.Path)
		}
	}
	contract, ok := contracts["uploadReleaseArtifact"]
	if !ok {
		t.Fatal("uploadReleaseArtifact contract is missing")
	}
	if got, _ := apiGenOperationPrivilege(contract); got != access.PrivilegePublishRelease {
		t.Fatalf("uploadReleaseArtifact privilege = %q, want %q", got, access.PrivilegePublishRelease)
	}
}

func TestDeviceAuthorizationApprovalRequiresCSRF(t *testing.T) {
	for operationID := range testAPIGenContracts() {
		got := apiGenRequiresCSRF(operationID)
		if got != (operationID == "decideDeviceAuthorization") {
			t.Errorf("apiGenRequiresCSRF(%q) = %t", operationID, got)
		}
	}
}

func TestAPIGenObjectResolverRejectsInvalidContracts(t *testing.T) {
	authorizer := testAPIGenAuthorizer(t)
	tests := []struct {
		name         string
		contract     APIGenOperationContract
		wantOK       bool
		wantResolver bool
	}{
		{name: "workspace scoped", contract: APIGenOperationContract{Path: "/api/v1/workspaces/{workspace}/dashboards", Extensions: map[string]any{}}, wantOK: true},
		{name: "supported exact scope", contract: APIGenOperationContract{Path: "/api/v1/workspaces/{workspace}/dashboards/{dashboard}", Extensions: map[string]any{apiGenObjectScopeExtension: "dashboard"}}, wantOK: true, wantResolver: true},
		{name: "missing exact scope", contract: APIGenOperationContract{Path: "/api/v1/workspaces/{workspace}/dashboards/{dashboard}", Extensions: map[string]any{}}},
		{name: "wrong exact scope", contract: APIGenOperationContract{Path: "/api/v1/workspaces/{workspace}/dashboards/{dashboard}", Extensions: map[string]any{apiGenObjectScopeExtension: "semantic-model"}}},
		{name: "unknown exact scope", contract: APIGenOperationContract{Path: "/api/v1/workspaces/{workspace}/dashboards/{dashboard}", Extensions: map[string]any{apiGenObjectScopeExtension: "tenant"}}},
		{
			name: "malformed exact scope",
			contract: APIGenOperationContract{
				Path:       "/api/v1/workspaces/{workspace}/dashboards/{dashboard}",
				Extensions: map[string]any{apiGenObjectScopeExtension: map[string]any{"kind": "dashboard"}},
			},
		},
		{name: "unexpected exact scope", contract: APIGenOperationContract{Path: "/api/v1/workspaces/{workspace}/dashboards", Extensions: map[string]any{apiGenObjectScopeExtension: "dashboard"}}},
		{name: "ambiguous exact scope", contract: APIGenOperationContract{Path: "/api/v1/workspaces/{workspace}/dashboards/{dashboard}/semantic-models/{model}", Extensions: map[string]any{apiGenObjectScopeExtension: "dashboard"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver, ok := authorizer.objectResolverForContract(test.contract)
			if ok != test.wantOK {
				t.Fatalf("ok = %t, want %t", ok, test.wantOK)
			}
			if got := resolver != nil; got != test.wantResolver {
				t.Fatalf("has resolver = %t, want %t", got, test.wantResolver)
			}
		})
	}
}

func TestManagedDataAndDeploymentAPIGenPrivilegesAndScopes(t *testing.T) {
	authorizer := testAPIGenAuthorizer(t)
	want := map[string]access.Privilege{
		"getActiveManagedDataRevision":          access.PrivilegeViewData,
		"listManagedConnections":                access.PrivilegeViewData,
		"getManagedConnection":                  access.PrivilegeViewData,
		"listManagedDataRevisions":              access.PrivilegeViewData,
		"getManagedDataRevision":                access.PrivilegeViewData,
		"createManagedDataUploadSession":        access.PrivilegeIngestData,
		"getManagedDataUploadSession":           access.PrivilegeIngestData,
		"listManagedDataUploadSessions":         access.PrivilegeIngestData,
		"cancelManagedDataUploadSession":        access.PrivilegeIngestData,
		"listManagedDataUploadSessionEvents":    access.PrivilegeIngestData,
		"finalizeManagedDataUploadSession":      access.PrivilegeIngestData,
		"createManagedDataS3MultipartUpload":    access.PrivilegeIngestData,
		"signManagedDataS3MultipartPart":        access.PrivilegeIngestData,
		"completeManagedDataS3MultipartUpload":  access.PrivilegeIngestData,
		"abortManagedDataS3MultipartUpload":     access.PrivilegeIngestData,
		"startProjectCandidate":                 access.PrivilegeAuthorProject,
		"getProjectCandidate":                   access.PrivilegeAuthorProject,
		"reviewProjectCandidate":                access.PrivilegeReviewCandidate,
		"replaceProjectCandidateArtifact":       access.PrivilegeAuthorProject,
		"retryProjectCandidate":                 access.PrivilegeAuthorProject,
		"cancelProjectCandidate":                access.PrivilegeAuthorProject,
		"planProjectCandidateSynchronization":   access.PrivilegeAuthorProject,
		"uploadProjectCandidateSourceBlob":      access.PrivilegeAuthorProject,
		"commitProjectCandidateSynchronization": access.PrivilegeAuthorProject,
		"createDeployment":                      access.PrivilegeRequestDeployment,
		"getDeployment":                         access.PrivilegeViewItem,
		"listDeployments":                       access.PrivilegeViewItem,
		"cancelDeployment":                      access.PrivilegeRequestDeployment,
		"rollbackDeployment":                    access.PrivilegeRollbackDeployment,
		"requestDeploymentApproval":             access.PrivilegeRequestDeployment,
		"approveDeployment":                     access.PrivilegeApproveDeployment,
		"revokeDeploymentApproval":              access.PrivilegeApproveDeployment,
		"activateDeployment":                    access.PrivilegeActivateDeployment,
	}
	for operationID, expected := range want {
		contract, ok := authorizer.operations[operationID]
		if !ok {
			t.Errorf("%s contract is missing", operationID)
			continue
		}
		if got, ok := apiGenOperationPrivilege(contract); !ok || got != expected {
			t.Errorf("%s privilege = %q, want %q", operationID, got, expected)
		}
		resolver, ok := authorizer.objectResolverForContract(contract)
		if !ok {
			t.Errorf("%s has an invalid object scope", operationID)
			continue
		}
		wantScoped := true
		if gotScoped := resolver != nil; gotScoped != wantScoped {
			t.Errorf("%s project-environment scoped = %t, want %t", operationID, gotScoped, wantScoped)
		}
	}
}

func TestProjectRoutesUseTheProjectEnvironmentAuthorizationBoundary(t *testing.T) {
	authorizer := testAPIGenAuthorizer(t)
	for operationID, contract := range authorizer.operations {
		if !strings.Contains(contract.Path, "/projects/{project}") {
			continue
		}
		require.Equal(t, "project-environment", contract.Extensions[apiGenObjectScopeExtension], operationID)
		resolver, ok := authorizer.objectResolverForContract(contract)
		require.True(t, ok, operationID)
		require.NotNil(t, resolver, operationID)
	}
}
