package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	deploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type nativePlanCollectionReader interface {
	ListPlans(context.Context, string, string, string, int32, string) (nativepostgres.DeliveryPlanPage, error)
}

type nativeBuildCollectionReader interface {
	ListBuildAttempts(context.Context, string, string, string, int32, string) (nativepostgres.DeliveryBuildAttemptPage, error)
}

type nativeCandidateCollectionReader interface {
	ListCandidates(context.Context, string, string, string, int32, string) (nativepostgres.DeliveryCandidatePage, error)
}

type nativeApprovalCollectionReader interface {
	ListApprovalRequests(context.Context, string, string, string, int32, string) (nativepostgres.ApprovalRequestPage, error)
}

var (
	_ nativePlanCollectionReader      = (*deploymentpostgres.NativeReader)(nil)
	_ nativeBuildCollectionReader     = (*deploymentpostgres.NativeReader)(nil)
	_ nativeCandidateCollectionReader = (*deploymentpostgres.NativeReader)(nil)
	_ nativeApprovalCollectionReader  = (*deploymentpostgres.NativeReader)(nil)
)

// TestPostgresNativeAdminDeliveryUsesComposedCollectionReader exercises the
// same application-owned NativeReader that production injects into the
// deployment module. An empty, correctly scoped target must produce a valid
// admin projection; it must not be mistaken for an unavailable collection
// reader merely because the repository collections are optional extensions of
// the module's point-read port.
func TestPostgresNativeAdminDeliveryUsesComposedCollectionReader(t *testing.T) {
	const targetID = "delivery_admin_target"
	projectID := projectgraph.ResourceID("project:delivery-admin")
	fixture := NewPostgresJourneyFixture(t, PostgresJourneyFixtureOptions{
		TargetID: targetID, ProjectID: projectID, SkipRouteAssembly: true,
	})
	if _, err := fixture.Graph.DeploymentRepository.CreateTarget(t.Context(), nativepostgres.TargetInput{
		TargetID: targetID, ProjectID: projectID.String(), Environment: "prod",
	}); err != nil {
		t.Fatalf("seed native delivery target: %v", err)
	}

	reader := deploymentpostgres.NewNativeReader(fixture.Graph.DeploymentRepository)
	module, err := deploymentmodule.Build(t.Context(), deploymentmodule.Config{
		Persistence:         fixture.Graph.DeploymentPersistence,
		InstanceID:          targetID,
		InstanceEnvironment: "prod",
		CurrentPrincipal: func(*http.Request) (deploymentmodule.Principal, bool) {
			return deploymentmodule.Principal{ID: "composition-test"}, true
		},
		NativeDeliveryReader:    reader,
		NativeDeliveryMutations: deploymentmodule.NativeDeliveryMutationFuncs{},
	})
	if err != nil {
		t.Fatalf("build native deployment module: %v", err)
	}

	data, err := module.AdminDeliveryData(t.Context(), projectID.String())
	if err != nil {
		t.Fatalf("read native admin delivery projection: %v", err)
	}
	if data.Operator.TargetId != targetID || data.Operator.ProjectId != projectID.String() || data.Operator.Environment != "prod" {
		t.Fatalf("operator projection = %#v, want target/project/environment scope", data.Operator)
	}
	if data.Publications == nil || data.RetainedGenerations == nil {
		t.Fatalf("collection projection slices are nil: %#v", data)
	}
	if len(data.Publications) != 0 || len(data.RetainedGenerations) != 0 {
		t.Fatalf("empty target collections = publications %d, generations %d", len(data.Publications), len(data.RetainedGenerations))
	}

	// The four collection methods below are intentionally exercised through
	// both the adapter and the generated deployment module. This catches a
	// composition regression where the PostgreSQL repository has the methods
	// but NativeReader fails to expose or forward one of them.
	limit := int32(13)
	pageToken := ""
	if page, err := reader.ListPlans(t.Context(), projectID.String(), targetID, "prod", limit, pageToken); err != nil || len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatalf("native reader plan collection = %#v, err=%v; want empty page", page, err)
	}
	if page, err := reader.ListBuildAttempts(t.Context(), projectID.String(), targetID, "prod", limit, pageToken); err != nil || len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatalf("native reader build collection = %#v, err=%v; want empty page", page, err)
	}
	if page, err := reader.ListCandidates(t.Context(), projectID.String(), targetID, "prod", limit, pageToken); err != nil || len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatalf("native reader candidate collection = %#v, err=%v; want empty page", page, err)
	}
	if page, err := reader.ListApprovalRequests(t.Context(), projectID.String(), targetID, "prod", limit, pageToken); err != nil || len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatalf("native reader approval collection = %#v, err=%v; want empty page", page, err)
	}
	invalidLimit := int32(0)
	if _, err := reader.ListPlans(t.Context(), projectID.String(), targetID, "prod", invalidLimit, pageToken); !errors.Is(err, nativepostgres.ErrInvalid) {
		t.Fatalf("native reader plan limit error = %v, want %v", err, nativepostgres.ErrInvalid)
	}
	if _, err := reader.ListBuildAttempts(t.Context(), projectID.String(), targetID, "prod", invalidLimit, pageToken); !errors.Is(err, nativepostgres.ErrInvalid) {
		t.Fatalf("native reader build limit error = %v, want %v", err, nativepostgres.ErrInvalid)
	}
	if _, err := reader.ListCandidates(t.Context(), projectID.String(), targetID, "prod", invalidLimit, pageToken); !errors.Is(err, nativepostgres.ErrInvalid) {
		t.Fatalf("native reader candidate limit error = %v, want %v", err, nativepostgres.ErrInvalid)
	}
	if _, err := reader.ListApprovalRequests(t.Context(), projectID.String(), targetID, "prod", invalidLimit, pageToken); !errors.Is(err, nativepostgres.ErrInvalid) {
		t.Fatalf("native reader approval limit error = %v, want %v", err, nativepostgres.ErrInvalid)
	}
	invalidToken := "not-a-delivery-cursor"
	if _, err := reader.ListPlans(t.Context(), projectID.String(), targetID, "prod", limit, invalidToken); !errors.Is(err, nativepostgres.ErrInvalid) {
		t.Fatalf("native reader plan token error = %v, want %v", err, nativepostgres.ErrInvalid)
	}
	if _, err := reader.ListBuildAttempts(t.Context(), projectID.String(), targetID, "prod", limit, invalidToken); !errors.Is(err, nativepostgres.ErrInvalid) {
		t.Fatalf("native reader build token error = %v, want %v", err, nativepostgres.ErrInvalid)
	}
	if _, err := reader.ListCandidates(t.Context(), projectID.String(), targetID, "prod", limit, invalidToken); !errors.Is(err, nativepostgres.ErrInvalid) {
		t.Fatalf("native reader candidate token error = %v, want %v", err, nativepostgres.ErrInvalid)
	}
	if _, err := reader.ListApprovalRequests(t.Context(), projectID.String(), targetID, "prod", limit, invalidToken); !errors.Is(err, nativepostgres.ErrInvalid) {
		t.Fatalf("native reader approval token error = %v, want %v", err, nativepostgres.ErrInvalid)
	}

	collectionHandlers := []struct {
		name string
		call func(http.ResponseWriter, *http.Request, string, *int32, *string)
	}{
		{name: "plans", call: module.ListDeliveryPlans},
		{name: "builds", call: module.ListDeliveryBuildAttempts},
		{name: "candidates", call: module.ListDeliveryCandidates},
		{name: "approval-requests", call: module.ListDeliveryApprovalRequests},
	}
	for _, collection := range collectionHandlers {
		t.Run(collection.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			collection.call(response, httptest.NewRequest(http.MethodGet, "/", nil), projectID.String(), &limit, &pageToken)
			if response.Code != http.StatusOK {
				t.Fatalf("collection status = %d, body = %s; want %d", response.Code, response.Body.String(), http.StatusOK)
			}
		})
	}
}
