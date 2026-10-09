package app

import (
	"context"
	"net/http"
	"testing"

	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/google/uuid"
)

func (f *sourceCredentialHTTPJourney) activateSourceCredential(t *testing.T, token string) credentialgen.CredentialActivationResponse {
	t.Helper()
	client := credentialgen.NewGenClient(f.transport(token))
	bindings := analyticsgen.NewGenClient(f.transport(token))
	binding, err := bindings.GetTargetConnectionBinding(t.Context(), analyticsgen.GenGetTargetConnectionBindingClientRequest{Project: sourceJourneyProject, Target: f.instance, Connection: "connection:warehouse"})
	if err != nil {
		t.Fatalf("read current binding: %v", err)
	}
	draft, err := client.SaveCredentialDraft(t.Context(), credentialgen.GenSaveCredentialDraftClientRequest{Project: sourceJourneyProject, Target: f.instance, Connection: "connection:warehouse", Body: credentialgen.CredentialDraftSaveRequest{Fields: map[string]string{"password": f.source.password}}})
	if err != nil {
		t.Fatalf("save password draft: %v", err)
	}
	receipt, err := client.ValidateCredentialDraft(t.Context(), credentialgen.GenValidateCredentialDraftClientRequest{Project: sourceJourneyProject, Target: f.instance, Connection: "connection:warehouse", Version: draft.Body.VersionId, Body: credentialgen.CredentialValidationRequest{ExpectedBindingRevision: binding.Body.Revision}})
	if err != nil {
		t.Fatalf("validate exact saved password: %v", err)
	}
	operation := uuid.NewString()
	prepared, err := client.StartCredentialActivation(t.Context(), credentialgen.GenStartCredentialActivationClientRequest{Project: sourceJourneyProject, Target: f.instance, Connection: "connection:warehouse", Version: draft.Body.VersionId, Body: credentialgen.CredentialActivationRequest{OperationId: operation, ReceiptId: receipt.Body.ReceiptId, ExpectedBindingRevision: binding.Body.Revision}})
	if err != nil {
		t.Fatalf("prepare native credential activation: %v", err)
	}
	if prepared.Body.State != "prepared" || prepared.Body.VersionId != draft.Body.VersionId || prepared.Body.OperationId != operation {
		t.Fatalf("unexpected prepared activation: state=%s", prepared.Body.State)
	}
	if prepared.StatusCode == http.StatusAccepted && prepared.Headers.Get("Location") == "" {
		t.Fatal("accepted activation omitted status Location")
	}
	// Preparation may take long enough to expire the first receipt. Continue
	// with a separately authorized validation of the same immutable version.
	fresh, err := client.ValidateCredentialDraft(t.Context(), credentialgen.GenValidateCredentialDraftClientRequest{Project: sourceJourneyProject, Target: f.instance, Connection: "connection:warehouse", Version: draft.Body.VersionId, Body: credentialgen.CredentialValidationRequest{ExpectedBindingRevision: prepared.Body.BindingRevision}})
	if err != nil {
		t.Fatalf("revalidate prepared credential: %v", err)
	}
	activated, err := client.RetryCredentialActivation(t.Context(), credentialgen.GenRetryCredentialActivationClientRequest{Project: sourceJourneyProject, Target: f.instance, Connection: "connection:warehouse", Operation: operation, Body: credentialgen.CredentialActivationRetryRequest{ReceiptId: &fresh.Body.ReceiptId}})
	if err != nil {
		current, readErr := client.GetCredentialActivation(t.Context(), credentialgen.GenGetCredentialActivationClientRequest{Project: sourceJourneyProject, Target: f.instance, Connection: "connection:warehouse", Operation: operation})
		if readErr == nil {
			t.Logf("activation after failed retry: state=%s ready=%v", current.Body.State, current.Body.RuntimeReady)
		}
		t.Fatalf("commit native credential activation: %v", err)
	}
	if activated.Body.State != "completed" || !activated.Body.RuntimeReady || activated.Body.VersionId != draft.Body.VersionId || activated.Body.BindingRevision != binding.Body.Revision || activated.Body.GenerationId == nil || activated.Body.PublicationId == nil {
		t.Fatalf("native credential result state=%s ready=%v revision=%d", activated.Body.State, activated.Body.RuntimeReady, activated.Body.BindingRevision)
	}
	return activated.Body
}

func (f *sourceCredentialHTTPJourney) retryCompletedSource(t *testing.T, token string, completed credentialgen.CredentialActivationResponse) {
	t.Helper()
	client := credentialgen.NewGenClient(f.transport(token))
	current, err := client.GetCredentialActivation(t.Context(), credentialgen.GenGetCredentialActivationClientRequest{Project: sourceJourneyProject, Target: f.instance, Connection: "connection:warehouse", Operation: completed.OperationId})
	if err != nil {
		t.Fatalf("read restored exact activation: %v", err)
	}
	if current.Body.State != "completed" || current.Body.VersionId != completed.VersionId || current.Body.BindingRevision != completed.BindingRevision {
		t.Fatal("restored activation identity drift")
	}
	replayed, err := client.RetryCredentialActivation(t.Context(), credentialgen.GenRetryCredentialActivationClientRequest{Project: sourceJourneyProject, Target: f.instance, Connection: "connection:warehouse", Operation: completed.OperationId})
	if err != nil {
		t.Fatalf("retry completed native activation after restart: %v", err)
	}
	if replayed.Body.State != "completed" || !replayed.Body.RuntimeReady || replayed.Body.VersionId != completed.VersionId || replayed.Body.BindingRevision != completed.BindingRevision || replayed.Body.GenerationId == nil || *replayed.Body.GenerationId != *completed.GenerationId {
		t.Fatal("completed retry did not acknowledge exact restored generation")
	}
}

func (f *sourceCredentialHTTPJourney) querySource(t *testing.T, token string, expected string) string {
	t.Helper()
	client := dashboardgen.NewGenClient(f.transport(token))
	metrics := []dashboardgen.SemanticFieldRef{{Field: "total"}}
	response, err := client.QuerySemanticModel(t.Context(), dashboardgen.GenQuerySemanticModelClientRequest{Model: "semantic-model:sales", Body: &dashboardgen.SemanticQueryRequest{Metrics: &metrics}})
	if err != nil {
		t.Fatalf("query real source metric: %v", err)
	}
	// SUM(BIGINT) is exact HUGEINT and crosses the JSON boundary as a string.
	if len(response.Body.Rows) != 1 || len(response.Body.Rows[0]) != 1 || response.Body.Rows[0][0] != expected {
		t.Fatalf("source query rows=%v", response.Body.Rows)
	}
	if response.Body.ServingSnapshot == "" {
		t.Fatal("query omitted serving identity")
	}
	return response.Body.ServingSnapshot
}

func (f *sourceCredentialHTTPJourney) restartWithoutEnvironment(t *testing.T) {
	t.Helper()
	if err := f.target.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.target = nil
	t.Setenv(sourceJourneyCredentialVariable, "")
	f.config.DevelopmentCredentialVariables = ""
	f.config.LocalCheckoutID = ""
	f.config.LocalOwnerID = ""
	f.config.DevelopmentProfileName = ""
	f.config.DevelopmentGraphDigest = ""
	f.config.DevelopmentProfileDigest = ""
	target, err := BuildProduction(t.Context(), f.config)
	if err != nil {
		t.Fatalf("production rebuild with saved credential: %v", err)
	}
	if err = target.Start(t.Context()); err != nil {
		_ = target.Shutdown(context.Background())
		t.Fatalf("production restart with saved credential: %v", err)
	}
	f.target = target
	f.request(t, http.MethodGet, "/readyz", "", nil, http.StatusOK)
}
