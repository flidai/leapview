package module

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releasegen "github.com/flidai/leapview/internal/release/api/gen"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/google/uuid"
)

var _ NativePersistence = (*releasepostgres.Repository)(nil)

func TestReleaseStoresAreConstructedInsideModule(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	releases, finalization, catalog, deployments, err := releaseStores(store.SQLDB())
	if err != nil {
		t.Fatal(err)
	}
	if releases == nil || finalization == nil || catalog == nil || deployments == nil {
		t.Fatalf("release stores = %#v, %#v, %#v, %#v", releases, finalization, catalog, deployments)
	}
}

func TestReleaseStoresRequireDatabase(t *testing.T) {
	if _, _, _, _, err := releaseStores(nil); err == nil {
		t.Fatal("release module accepted missing persistence")
	}
}

func TestBuildProductionRejectsSQLiteAuthority(t *testing.T) {
	_, err := Build(t.Context(), Config{Production: true, Database: &sql.DB{}})
	if err == nil || !strings.Contains(err.Error(), "rejects SQLite") {
		t.Fatalf("production SQLite build error = %v", err)
	}
}

func TestBuildProductionRequiresNativeAuthority(t *testing.T) {
	_, err := Build(t.Context(), Config{Production: true})
	if err == nil || !strings.Contains(err.Error(), "requires native PostgreSQL") {
		t.Fatalf("production missing authority error = %v", err)
	}
}

func TestBuildSQLiteRequiresExplicitLegacyOptIn(t *testing.T) {
	_, err := Build(t.Context(), Config{Database: &sql.DB{}})
	if err == nil || !strings.Contains(err.Error(), "LegacySQLite=true") {
		t.Fatalf("implicit SQLite build error = %v", err)
	}
}

type nativeReleaseStub struct {
	release.Repository
	release.FinalizationUnitOfWork
	release.CandidateProvenanceRepository
	release.ServingStateProvenanceRepository
	configured, audit, events, workflow bool
}

func (nativeReleaseStub) PostgreSQLAuthority()    {}
func (s nativeReleaseStub) Configured() bool      { return s.configured }
func (s nativeReleaseStub) AuditCapable() bool    { return s.audit }
func (s nativeReleaseStub) EventCapable() bool    { return s.events }
func (s nativeReleaseStub) WorkflowCapable() bool { return s.workflow }
func (s nativeReleaseStub) Get(ctx context.Context, projectID projectgraph.ResourceID, releaseID string) (release.Release, error) {
	return s.Repository.Get(ctx, projectID, releaseID)
}

type unmarkedNativeStub struct {
	release.Repository
	release.FinalizationUnitOfWork
	release.CandidateProvenanceRepository
	release.ServingStateProvenanceRepository
}

func (s unmarkedNativeStub) Get(ctx context.Context, projectID projectgraph.ResourceID, releaseID string) (release.Release, error) {
	return s.Repository.Get(ctx, projectID, releaseID)
}

type nativeCatalogStub struct {
	release.CatalogRepository
	configured bool
}

func (nativeCatalogStub) PostgreSQLAuthority() {}
func (s nativeCatalogStub) Configured() bool   { return s.configured }

type deploymentStub struct {
	release.DeploymentLinkage
	configured bool
}

func (deploymentStub) PostgreSQLAuthority() {}
func (s deploymentStub) Configured() bool   { return s.configured }

type unmarkedDeploymentStub struct{ release.DeploymentLinkage }

func TestBuildProductionRejectsUnmarkedNativeAuthority(t *testing.T) {
	stub := nativeReleaseStub{configured: true, audit: true, events: true, workflow: true}
	// Deliberately hide the marker while retaining every release contract.
	var unmarked NativePersistence = unmarkedNativeStub{
		Repository:                       stub.Repository,
		FinalizationUnitOfWork:           stub.FinalizationUnitOfWork,
		CandidateProvenanceRepository:    stub.CandidateProvenanceRepository,
		ServingStateProvenanceRepository: stub.ServingStateProvenanceRepository,
	}
	_, err := Build(t.Context(), Config{Production: true, Persistence: unmarked, Environment: "dev"})
	if err == nil || !strings.Contains(err.Error(), "configured native PostgreSQL") {
		t.Fatalf("unmarked native error = %v", err)
	}
}

func TestBuildProductionRejectsUnconfiguredNativeAuthority(t *testing.T) {
	stub := nativeReleaseStub{configured: false, audit: true, events: true, workflow: true}
	_, err := Build(t.Context(), Config{Production: true, Persistence: stub, Environment: "dev"})
	if err == nil || !strings.Contains(err.Error(), "configured native PostgreSQL") {
		t.Fatalf("unconfigured native error = %v", err)
	}
}

func TestBuildProductionAcceptsConfiguredNativeAuthority(t *testing.T) {
	stub := nativeReleaseStub{configured: true, audit: true, events: true, workflow: true}
	module, err := Build(t.Context(), Config{Production: true, Persistence: stub, Catalog: nativeCatalogStub{configured: true}, Environment: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if module == nil {
		t.Fatal("production native module is nil")
	}
	if module.DeploymentLinkage() != nil {
		t.Fatal("native module exposed deployment linkage before deployment-owned adapter injection")
	}
}

func TestBuildProductionRequiresConfiguredNativeCatalog(t *testing.T) {
	stub := nativeReleaseStub{configured: true, audit: true, events: true, workflow: true}
	_, err := Build(t.Context(), Config{Production: true, Persistence: stub, Environment: "dev"})
	if err == nil || !strings.Contains(err.Error(), "native PostgreSQL catalog") {
		t.Fatalf("missing catalog error = %v", err)
	}
}

func TestBuildProductionRejectsUnmarkedDeploymentAuthority(t *testing.T) {
	stub := nativeReleaseStub{configured: true, audit: true, events: true, workflow: true}
	_, err := Build(t.Context(), Config{Production: true, Persistence: stub, Catalog: nativeCatalogStub{configured: true}, Deployments: unmarkedDeploymentStub{}, Environment: "dev"})
	if err == nil || !strings.Contains(err.Error(), "native PostgreSQL deployments") {
		t.Fatalf("unmarked deployments error = %v", err)
	}
}

func TestBuildProductionAcceptsConfiguredDeploymentAuthority(t *testing.T) {
	stub := nativeReleaseStub{configured: true, audit: true, events: true, workflow: true}
	module, err := Build(t.Context(), Config{Production: true, Persistence: stub, Catalog: nativeCatalogStub{configured: true}, Deployments: deploymentStub{configured: true}, Environment: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if module.DeploymentLinkage() == nil {
		t.Fatal("configured deployment authority was not exposed")
	}
}

func TestReleaseAuditEventIDIsCanonicalAndReplayStable(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("commerce")
	if err != nil {
		t.Fatal(err)
	}
	in := releaseAuditCommandInput{
		OperationID: string(releasegen.GenOperationCreateRelease), ProjectID: projectID,
		ReleaseID: "release_1", IdempotencyKey: "idem-1", PrincipalID: "principal_1",
		ProjectDigest: digestForTest("1"), Status: string(release.StatusDraft), CreatedBy: "principal_1",
	}
	one, err := buildReleaseCreatedAuditIntent(in)
	if err != nil {
		t.Fatal(err)
	}
	two, err := buildReleaseCreatedAuditIntent(in)
	if err != nil {
		t.Fatal(err)
	}
	if one.EventID != two.EventID {
		t.Fatalf("replay event IDs differ: %q vs %q", one.EventID, two.EventID)
	}
	parsed, err := uuid.Parse(one.EventID)
	if err != nil || parsed == uuid.Nil {
		t.Fatalf("event ID %q is not canonical UUID: %v", one.EventID, err)
	}
}

func digestForTest(ch string) string { return "sha256:" + strings.Repeat(ch, 64) }
