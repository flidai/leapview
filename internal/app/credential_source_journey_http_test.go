package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/app/api/clienttransport"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const sourceJourneyProject = "project:credential-journey"

// Requests traverse the full production router and bearer middleware. Keeping
// the transport in process also makes a restarted target replace the old one.
type sourceJourneyRoundTripper struct{ fixture *sourceCredentialHTTPJourney }

func (transport sourceJourneyRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	// Match net/http's server request contract for bodyless client commands.
	request = request.Clone(request.Context())
	if request.Body == nil {
		request.Body = http.NoBody
	}
	response := httptest.NewRecorder()
	transport.fixture.target.Handler().ServeHTTP(response, request)
	return response.Result(), nil
}

func (f *sourceCredentialHTTPJourney) transport(token string) clienttransport.Transport {
	return clienttransport.Transport{Target: "https://localhost", Token: token,
		Client:         &http.Client{Transport: sourceJourneyRoundTripper{fixture: f}},
		PrepareRequest: func(request *http.Request) { request.Header.Set("X-LeapView-Invocation-Surface", "cli") },
	}
}

func (f *sourceCredentialHTTPJourney) bootstrapProject(t *testing.T) string {
	t.Helper()
	ctx := t.Context()
	identity, err := f.graph.Access.CredentialForAPIToken(ctx, f.initial.ProjectClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	owner := identity.Principal.ID
	if _, err = f.graph.Access.ChangeLocalPassword(ctx, owner, f.initial.TemporaryPassword, "source-journey-replacement-password"); err != nil {
		t.Fatal(err)
	}
	deploymentClient := deploymentgen.NewGenClient(f.transport(f.initial.ProjectClaimToken))
	_, err = deploymentClient.BootstrapProjectClaim(ctx, deploymentgen.GenBootstrapProjectClaimClientRequest{
		Headers: deploymentgen.GenBootstrapProjectClaimClientHeaders{IdempotencyKey: "source-journey-project-claim"},
		Body:    deploymentgen.ProjectClaimBootstrapRequest{ProjectUid: sourceJourneyProject, IssuerId: "issuer:credential-journey", Environment: f.config.Environment},
	})
	if err != nil {
		t.Fatalf("bootstrap project claim: %v", err)
	}

	// Provision a normal attenuated API token through the durable access service.
	// Authorization still evaluates current target roles at each HTTP command.
	actions := map[access.Action]bool{}
	for _, role := range []access.PermissionRole{access.PermissionRoleProjectAdmin, access.PermissionRoleEditor, access.PermissionRoleReleaseOperator} {
		values, ok := access.PermissionRoleActions(role)
		if !ok {
			t.Fatal("missing permission role")
		}
		for _, action := range values {
			actions[action] = true
		}
	}
	var selected []access.Action
	for action := range actions {
		selected = append(selected, action)
	}
	permissions, err := access.ProjectPermissionPairsForActions(projectgraph.ResourceID(sourceJourneyProject), selected)
	if err != nil {
		t.Fatal(err)
	}
	f.authoringToken, _, err = f.graph.Access.CreateScopedAPITokenWithMetadata(ctx, access.ScopedAPITokenInput{PrincipalID: owner, Name: "source-authoring", Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef("connection:warehouse", projectgraph.KindConnection)
	if err != nil {
		t.Fatal(err)
	}
	manage, err := access.NewExactPermissionPair(access.ActionConnectionManage, sourceJourneyProject, resource)
	if err != nil {
		t.Fatal(err)
	}
	permissions = append(permissions, manage)
	token, _, err := f.graph.Access.CreateScopedAPITokenWithMetadata(ctx, access.ScopedAPITokenInput{PrincipalID: owner, Name: "source-journey", Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	client := accessgen.NewGenClient(f.transport(token))
	for index, role := range []struct {
		id, name string
		role     access.PermissionRole
	}{
		{access.BootstrapOwnerBindingID, access.BootstrapOwnerBindingName, access.PermissionRoleProjectAdmin},
		{access.BootstrapEditorBindingID, access.BootstrapEditorBindingName, access.PermissionRoleEditor},
		{access.BootstrapReleaseOperatorBindingID, access.BootstrapReleaseOperatorBindingName, access.PermissionRoleReleaseOperator},
	} {
		_, err = client.CreateProjectRoleBinding(ctx, accessgen.GenCreateProjectRoleBindingClientRequest{
			Project: sourceJourneyProject,
			Headers: accessgen.GenCreateProjectRoleBindingClientHeaders{IdempotencyKey: role.id},
			Body:    accessgen.RoleBindingCreateRequest{Id: role.id, Name: &role.name, SubjectType: "principal", SubjectId: owner, Role: string(role.role), ExpectedRevision: int64(index)},
		})
		if err != nil {
			t.Fatalf("bootstrap current project role %s: %v", role.role, err)
		}
	}
	// Model an already provisioned credential operator with exact durable policy
	// intent. This fixture does not qualify operator grant provisioning: manage
	// is deliberately nondelegable through the ordinary grant API. The intent
	// must still pass native graph compilation/publication before granting access.
	if f.production {
		return token
	}
	_, err = f.graph.Access.UpsertAuthorizationGrant(ctx, access.AuthorizationGrantInput{
		Scope: access.AuthorizationPolicyScope{TargetID: f.instance, ProjectID: sourceJourneyProject, Environment: f.config.Environment},
		Grant: access.AuthorizationGrant{ID: "source-credential-operator", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: owner},
			Resource: resource, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{manage}},
		ExpectedRevision: 3, IdempotencyKey: "source-credential-operator",
	})
	if err != nil {
		t.Fatalf("stage exact credential operator grant: %v", err)
	}
	return token
}
