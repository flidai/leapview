package module

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/access"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehost "github.com/flidai/leapview/internal/runtimehost"
)

func TestBuildAuthoringCreateUsesProjectRoleBeforeAllocatedDashboardExists(t *testing.T) {
	store, err := platform.Open(context.Background(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(context.Background(), `INSERT INTO principals (id, email, display_name) VALUES (?, ?, ?)`, "owner", "owner@example.test", "Owner"); err != nil {
		t.Fatal(err)
	}
	var authorized []access.ResourceRef
	var projectCapabilities []access.Capability
	authoring, err := BuildAuthoring(AuthoringConfig{
		Database: store.SQLDB(),
		AuditIntentRecorder: access.AuditIntentRecorderFunc(func(context.Context, transaction.Transaction, access.AuditIntent) error {
			return nil
		}),
		AuthorizeResource: func(_ context.Context, _ string, _ projectgraph.ResourceID, resource access.ResourceRef, capability access.Capability) (bool, error) {
			authorized = append(authorized, resource)
			if resource.Kind() == projectgraph.KindSemanticModel && capability != access.CapabilityResourceRead {
				return false, errors.New("unexpected semantic-model authoring capability")
			}
			return resource.Kind() == projectgraph.KindSemanticModel, nil
		},
		AuthorizeProjectCapability: func(_ context.Context, _ string, _ projectgraph.ResourceID, capability access.Capability) (bool, error) {
			projectCapabilities = append(projectCapabilities, capability)
			// The allocated dashboard is intentionally absent from the active
			// graph. Production composition must authorize the target project role
			// rather than querying that future dashboard resource.
			return capability == access.CapabilityResourceEdit || capability == access.CapabilityProjectAdmin, nil
		},
		AcquireRuntime: func(context.Context) (runtimehost.Lease, error) {
			return nil, errors.New("runtime is not needed for create")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := authoring.Create(t.Context(), authoringservice.CreateRequest{
		ProjectID: "project:test", ActorID: "actor", OwnerPrincipalID: "owner",
		Title: "Orders", Slug: "orders", SemanticModel: "model:orders",
		IdempotencyKey: "composition-create-1",
	})
	if err != nil {
		t.Fatalf("composed create failed: %v", err)
	}
	if result.Lifecycle.ID == "" || len(authorized) != 1 || authorized[0].Kind() != projectgraph.KindSemanticModel || len(projectCapabilities) != 2 || projectCapabilities[0] != access.CapabilityResourceEdit || projectCapabilities[1] != access.CapabilityProjectAdmin {
		t.Fatalf("create result=%#v resources=%#v project capabilities=%#v", result, authorized, projectCapabilities)
	}
}
