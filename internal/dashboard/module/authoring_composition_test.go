package module

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/access"
	dashboardappearancepostgres "github.com/flidai/leapview/internal/dashboard/appearance/postgres"
	authoringpostgres "github.com/flidai/leapview/internal/dashboard/authoring/postgres"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	dashboardpublicationpostgres "github.com/flidai/leapview/internal/dashboard/publication/postgres"
	dashboardsessionpostgres "github.com/flidai/leapview/internal/dashboard/session/postgres"
	dashboardusagepostgres "github.com/flidai/leapview/internal/dashboard/usage/postgres"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehost "github.com/flidai/leapview/internal/runtimehost"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// nativeCompositionDB is intentionally only a composition probe: BuildAuthoring
// must preserve the supplied PostgreSQL repository without opening a transaction.
// The embedded transaction interface keeps the fake small while ensuring the
// native constructor still sees a transaction-capable DB handle.
type nativeCompositionDB struct{}

func (nativeCompositionDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (nativeCompositionDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}
func (nativeCompositionDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (nativeCompositionDB) Begin(context.Context) (pgx.Tx, error) {
	return nil, nil
}

type nativeCompositionAudit struct{}

func (nativeCompositionAudit) RecordAuditIntent(context.Context, authoringpostgres.Tx, access.AuditIntent) error {
	return nil
}

type nativeCompositionEvents struct{}

func (nativeCompositionEvents) AppendEvent(_ context.Context, _ authoringpostgres.Tx, input authoringpostgres.EventInput) (authoringpostgres.Event, error) {
	return authoringpostgres.Event{EventID: input.EventID, ProjectID: input.ProjectID, DashboardID: input.DashboardID, ActorID: input.ActorID, CorrelationID: input.CorrelationID, Revision: input.Revision, AggregateVersion: input.Revision, Type: input.Type, Payload: input.Payload}, nil
}

type nativeCompositionFence struct{}

func (nativeCompositionFence) ValidateActiveGeneration(context.Context, authoringpostgres.Tx, projectgraph.ServingIdentity) error {
	return nil
}

type nativeCompositionAppearanceAudit struct{}

func (nativeCompositionAppearanceAudit) RecordAuditEvent(context.Context, dashboardappearancepostgres.Tx, dashboardappearancepostgres.AuditInput) error {
	return nil
}

type nativeCompositionAppearanceEvents struct{}

func (nativeCompositionAppearanceEvents) AppendEvent(_ context.Context, _ dashboardappearancepostgres.Tx, input dashboardappearancepostgres.EventInput) (dashboardappearancepostgres.Event, error) {
	return dashboardappearancepostgres.Event{EventID: input.EventID, ProjectID: input.ProjectID, DashboardID: input.DashboardID, ActorID: input.ActorID, Revision: input.Revision, Patch: input.Patch, AggregateVersion: input.Revision}, nil
}

type nativeCompositionPublicationAudit struct{}

func (nativeCompositionPublicationAudit) RecordAuditIntent(context.Context, dashboardpublicationpostgres.Tx, access.AuditIntent) error {
	return nil
}

type nativeCompositionPublicationEvents struct{}

func (nativeCompositionPublicationEvents) AppendEvent(_ context.Context, _ dashboardpublicationpostgres.Tx, input dashboardpublicationpostgres.EventInput) (dashboardpublicationpostgres.Event, error) {
	return dashboardpublicationpostgres.Event{EventID: input.EventID, ProjectID: input.ProjectID, PublicationID: input.PublicationID, ActorID: input.ActorID, CorrelationID: input.CorrelationID, Revision: input.Revision, AggregateVersion: input.Revision, Type: input.Type, ServingStateID: input.ServingStateID, Payload: input.Payload}, nil
}

func nativeCompositionPersistence(t *testing.T, authoring *authoringpostgres.Repository) *NativePersistence {
	t.Helper()
	db := nativeCompositionDB{}
	session, err := dashboardsessionpostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := dashboardusagepostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	appearance, err := dashboardappearancepostgres.New(db, dashboardappearancepostgres.Options{
		Audit: nativeCompositionAppearanceAudit{}, Events: nativeCompositionAppearanceEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	publication, err := dashboardpublicationpostgres.New(db, nativeCompositionPublicationAudit{}, nativeCompositionPublicationEvents{})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := NewNativePersistence(NativePersistenceOptions{
		Session: session, Usage: usage, Appearance: appearance, Authoring: authoring,
		Publication: publication, Streams: dashboardpublicationpostgres.NewStreamRegistry(db),
		Broker: dashboardpublicationpostgres.NewBroker(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestBuildAuthoringNativeUsesSuppliedRepositoryWithoutSQLiteAuditRecorder(t *testing.T) {
	repository, err := authoringpostgres.New(nativeCompositionDB{}, nativeCompositionAudit{}, nativeCompositionEvents{}, nativeCompositionFence{})
	if err != nil {
		t.Fatal(err)
	}
	application, err := BuildAuthoring(AuthoringConfig{
		Persistence: nativeCompositionPersistence(t, repository),
		AuthorizeResource: func(context.Context, string, projectgraph.ResourceID, access.ResourceRef, access.Capability) (bool, error) {
			return true, nil
		},
		AuthorizeProjectCapability: func(context.Context, string, projectgraph.ResourceID, access.Capability) (bool, error) {
			return true, nil
		},
		AcquireRuntime: func(context.Context) (runtimehost.Lease, error) { return nil, errors.New("runtime unavailable") },
	})
	if err != nil {
		t.Fatalf("native composition rejected repository-owned audit wiring: %v", err)
	}
	if application == nil || application.PublishedCompilationReader() != repository {
		t.Fatalf("native composition did not preserve supplied repository: application=%#v", application)
	}
	if !application.MatchesRepository(repository) {
		t.Fatal("native composition did not match its exact authoring repository")
	}
	otherRepository, err := authoringpostgres.New(nativeCompositionDB{}, nativeCompositionAudit{}, nativeCompositionEvents{}, nativeCompositionFence{})
	if err != nil {
		t.Fatal(err)
	}
	if application.MatchesRepository(otherRepository) || application.MatchesRepository(nil) {
		t.Fatal("native composition matched a substituted or nil authoring repository")
	}
	for name, generate := range map[string]func() (string, error){
		"dashboard": func() (string, error) { id, err := authoringpostgres.NewDashboardID(); return string(id), err },
		"draft":     func() (string, error) { id, err := authoringpostgres.NewDraftID(); return string(id), err },
		"revision":  func() (string, error) { id, err := authoringpostgres.NewRevisionID(); return string(id), err },
	} {
		value, err := generate()
		if err != nil {
			t.Fatalf("generate %s id: %v", name, err)
		}
		parsed, err := uuid.Parse(value)
		if err != nil || parsed.Version() != 7 {
			t.Fatalf("%s id=%q, want UUIDv7: parse=%v version=%v", name, value, err, parsed.Version())
		}
	}
}

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
