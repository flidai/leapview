package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/app/adminpostgres"
	"github.com/flidai/leapview/internal/app/config"
	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Starts production from the first process, with the real TLS source and no
// environment credential or development profile bootstrap.
func TestFirstSourceProductionPublicationJourney(t *testing.T) {
	runFirstSourceProductionPublicationJourney(t, false)
}

func TestFirstSourceProductionPublicationInterruptedCompletion(t *testing.T) {
	runFirstSourceProductionPublicationJourney(t, true)
}

func runFirstSourceProductionPublicationJourney(t *testing.T, interrupt bool) (*sourceCredentialHTTPJourney, string) {
	f := newSourceCredentialHTTPJourneyProfile(t, true)
	token := f.bootstrapProject(t)
	identity, err := f.graph.Access.CredentialForAPIToken(t.Context(), token)
	require.NoError(t, err)
	browser := newFirstSourceJourneyBrowser(t, f, f.initial.Email, "source-journey-replacement-password")
	reviewer, err := f.graph.Access.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "first-source-reviewer@example.test"})
	require.NoError(t, err)
	_, err = f.graph.Access.ChangeLocalPassword(t.Context(), reviewer.Principal.ID, reviewer.Password, "first-source-reviewer-password-42!")
	require.NoError(t, err)
	policy, err := f.graph.Access.AuthorizationPolicy(t.Context(), access.AuthorizationPolicyScope{TargetID: f.instance, ProjectID: sourceJourneyProject, Environment: f.config.Environment})
	require.NoError(t, err)
	browser.command(t, "/admin/access/command?section=principals", "createProjectRoleBinding", map[string]any{"adminAccessCommand": map[string]any{"action": "grant_role", "bindingId": "initial-first-source-reviewer", "subjectType": "principal", "subjectId": reviewer.Principal.ID, "role": "release_approver", "expectedRevision": policy.Revision}})
	policy, err = f.graph.Access.AuthorizationPolicy(t.Context(), access.AuthorizationPolicyScope{TargetID: f.instance, ProjectID: sourceJourneyProject, Environment: f.config.Environment})
	require.NoError(t, err)
	require.Len(t, policy.RoleBindings, 4, "independent reviewer nomination must persist")
	owner, err := f.graph.Bootstrap.CustomerOwner(t.Context())
	require.NoError(t, err)
	e := f.source.endpoint
	intent := credentialmodule.FirstSourceAdmissionIntent{Version: 1, OperationID: uuid.NewString(), TargetID: f.instance, ProjectID: sourceJourneyProject, Environment: f.config.Environment, CustomerOwnerID: owner, OperatorPrincipalID: identity.Principal.ID, ConnectionID: "connection:warehouse", BindingID: "binding:first-production", ExpectedPolicyRevision: policy.Revision, ExpectedPolicyDigest: policy.Digest,
		Endpoint:            credentialmodule.FirstSourceEndpoint{Host: *e.Host, Port: int(*e.Port), Database: *e.Database, SourceIdentity: *e.SourceIdentity, TLSMode: *e.TlsMode},
		CredentialReference: credentialmodule.FirstSourceCredentialReference{ProjectID: sourceJourneyProject, Environment: f.config.Environment, SecretPath: "/customer/warehouse", SecretKey: "password"}}
	require.NoError(t, f.target.Shutdown(context.Background()))
	f.target = nil
	operations := adminpostgres.New(adminpostgres.Dependencies{LoadConfig: func() (config.Config, error) { return f.config, nil }})
	require.NoError(t, operations.AdmitFirstSource(t.Context(), admincli.FirstSourceAdmissionRequest{Intent: intent, Apply: true}, io.Discard))
	f.start(t)
	page := browser.request(t, http.MethodGet, "/connections/connection:warehouse/first-source", "", "", nil)
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), "lv-first-source-credentials")
	require.Equal(t, "no-store", page.Header().Get("Cache-Control"))
	stream := browser.request(t, http.MethodGet, "/updates?route=first_source_credentials&connection=connection%3Awarehouse", "", "", nil)
	require.Equal(t, http.StatusOK, stream.Code)
	require.Contains(t, stream.Body.String(), "firstSourceCredentials")
	require.Equal(t, "no-store", stream.Header().Get("Cache-Control"))
	retained := f.retainSource(t, token)
	saved := browser.signalCommand(t, "saveCredentialDraft", projectsignals.FirstSourceCredentialCommandSignal{Action: "save", Password: f.source.password})
	draft := credentialgen.CredentialDraftResponse{VersionId: saved.Command.VersionID}
	require.NotEmpty(t, draft.VersionId)
	validationResponse := browser.command(t, "/connections/connection:warehouse/credential-drafts/"+draft.VersionId+"/validate", "validateCredentialDraft", credentialgen.CredentialValidationRequest{ExpectedBindingRevision: 1})
	var receipt credentialgen.CredentialValidationResponse
	require.NoError(t, json.Unmarshal(validationResponse.Body.Bytes(), &receipt))
	preparation := uuid.NewString()
	planKey := "first-source-production-plan"
	aborted := uuid.NewString()
	browser.command(t, "/connections/connection:warehouse/credential-drafts/"+draft.VersionId+"/prepare-first-source", "prepareFirstSourceCredential", credentialgen.FirstSourcePreparationRequest{PreparationId: aborted, ReceiptId: receipt.ReceiptId, SourceDigest: retained.SourceDigest, SourceAttestationDigest: retained.SourceAttestationDigest, PlanIdempotencyKey: "aborted-first-source-plan", ExpectedTargetRevision: 1})
	abortResponse := browser.command(t, "/connections/connection:warehouse/first-source-preparations/"+aborted+"/abort", "abortCredentialActivation", map[string]any{})
	var abortResult credentialgen.CredentialActivationResponse
	require.NoError(t, json.Unmarshal(abortResponse.Body.Bytes(), &abortResult))
	require.Equal(t, "aborted", abortResult.State)
	// Cancellation releases the operation, never its consumed validation receipt.
	validationResponse = browser.command(t, "/connections/connection:warehouse/credential-drafts/"+draft.VersionId+"/validate", "validateCredentialDraft", credentialgen.CredentialValidationRequest{ExpectedBindingRevision: 1})
	require.NoError(t, json.Unmarshal(validationResponse.Body.Bytes(), &receipt))
	browser.command(t, "/connections/connection:warehouse/credential-drafts/"+draft.VersionId+"/prepare-first-source", "prepareFirstSourceCredential", credentialgen.FirstSourcePreparationRequest{PreparationId: preparation, ReceiptId: receipt.ReceiptId, SourceDigest: retained.SourceDigest, SourceAttestationDigest: retained.SourceAttestationDigest, PlanIdempotencyKey: planKey, ExpectedTargetRevision: 1})
	client := deploymentgen.NewGenClient(f.transport(token))
	plan, err := client.CreateDeliveryPlan(t.Context(), deploymentgen.GenCreateDeliveryPlanClientRequest{Project: sourceJourneyProject, Headers: deploymentgen.GenCreateDeliveryPlanClientHeaders{IdempotencyKey: planKey}, Body: deploymentgen.DeliveryPlanRequest{TargetId: f.instance, Operation: deploymentgen.DeliveryOperationKindCodeChange, SourceDigest: retained.SourceDigest, SourceAttestationDigest: retained.SourceAttestationDigest, FirstSourcePreparationId: &preparation}})
	require.NoError(t, err)
	build, err := client.BuildDeliveryPlan(t.Context(), deploymentgen.GenBuildDeliveryPlanClientRequest{Project: sourceJourneyProject, Plan: plan.Body.Id, Headers: deploymentgen.GenBuildDeliveryPlanClientHeaders{IdempotencyKey: "first-source-build"}})
	require.NoError(t, err)
	require.NotNil(t, build.Body.CandidateId)
	publication, err := client.PublishDeliveryCandidate(t.Context(), deploymentgen.GenPublishDeliveryCandidateClientRequest{Project: sourceJourneyProject, Candidate: *build.Body.CandidateId, Headers: deploymentgen.GenPublishDeliveryCandidateClientHeaders{IdempotencyKey: "first-source-publish"}})
	require.NoError(t, err)
	require.Equal(t, deploymentgen.DeliveryPublicationStatusPending, publication.Body.Status)
	approval, err := client.RequestDeliveryPublicationApproval(t.Context(), deploymentgen.GenRequestDeliveryPublicationApprovalClientRequest{Project: sourceJourneyProject, Publication: publication.Body.Id, Headers: deploymentgen.GenRequestDeliveryPublicationApprovalClientHeaders{IdempotencyKey: "first-source-approval"}})
	require.NoError(t, err)
	// The publisher cannot satisfy its own independent review.
	decision := deploymentgen.GenApproveDeliveryPublicationApprovalClientRequest{Project: sourceJourneyProject, Publication: publication.Body.Id, Approval: approval.Body.Id, Headers: deploymentgen.GenApproveDeliveryPublicationApprovalClientHeaders{IdempotencyKey: "first-source-decision"}, Body: deploymentgen.DeploymentApprovalDecisionRequest{ExpectedRevision: approval.Body.Revision}}
	_, err = client.ApproveDeliveryPublicationApproval(t.Context(), decision)
	require.Error(t, err)
	actions, ok := access.PermissionRoleActions(access.PermissionRoleReleaseApprover)
	require.True(t, ok)
	permissions, err := access.ProjectPermissionPairsForActions(sourceJourneyProject, actions)
	require.NoError(t, err)
	reviewerToken, _, err := f.graph.Access.CreateScopedAPITokenWithMetadata(t.Context(), access.ScopedAPITokenInput{PrincipalID: reviewer.Principal.ID, Name: "first-source-reviewer", Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)})
	require.NoError(t, err)
	reviewClient := deploymentgen.NewGenClient(f.transport(reviewerToken))
	var restoreCompletion func()
	if interrupt {
		restoreCompletion = f.interruptSourceCompletion(t)
	}
	_, err = reviewClient.ApproveDeliveryPublicationApproval(t.Context(), decision)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		row, e := f.graph.DeploymentRepository.Publication(t.Context(), publication.Body.Id)
		return e == nil && row.State == "committed"
	}, time.Minute, 50*time.Millisecond)
	if interrupt {
		admin, err := pgx.Connect(t.Context(), f.control.AdminURL())
		require.NoError(t, err)
		defer admin.Close(context.Background())
		require.Eventually(t, func() bool {
			var called bool
			err := admin.QueryRow(t.Context(), "SELECT is_called FROM credential.fixture_completion_attempts").Scan(&called)
			return err == nil && called
		}, time.Minute, 50*time.Millisecond)
		require.NoError(t, f.target.Shutdown(context.Background()))
		f.target = nil
		restoreCompletion()
		f.start(t)
	}
	require.Eventually(t, func() bool {
		response := httptest.NewRecorder()
		f.target.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://localhost/readyz", nil))
		return response.Code == http.StatusOK
	}, time.Minute, 50*time.Millisecond, "committed publication must install its production runtime")
	_ = f.querySource(t, token, "30")
	f.restartWithoutEnvironment(t, true)
	_ = f.querySource(t, token, "30")
	return f, token
}
