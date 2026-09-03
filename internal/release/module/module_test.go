package module

import (
	"context"
	"strings"
	"testing"

	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releasegen "github.com/flidai/leapview/internal/release/api/gen"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/google/uuid"
)

var _ NativePersistence = (*releasepostgres.Repository)(nil)

func TestBuildProductionRequiresNativeAuthority(t *testing.T) {
	_, err := Build(t.Context(), Config{Production: true})
	if err == nil || !strings.Contains(err.Error(), "persistence is required") {
		t.Fatalf("production missing authority error = %v", err)
	}
}

func TestBuildRequiresPersistence(t *testing.T) {
	_, err := Build(t.Context(), Config{})
	if err == nil || !strings.Contains(err.Error(), "persistence is required") {
		t.Fatalf("missing persistence error = %v", err)
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

// nativeServingStateReaderStub models the immutable graph-owned authority
// injected by production composition. It intentionally has no lifecycle
// mutation methods from the serving-state repository.
type nativeServingStateReaderStub struct{}

func (nativeServingStateReaderStub) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	return servingstate.State{}, servingstate.ErrNotFound
}
func (nativeServingStateReaderStub) ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error) {
	return servingstate.Artifact{}, servingstate.ErrNotFound
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
	// Deliberately bypass NewPostgresPersistence to ensure Build still validates
	// the native authority marker.
	// Deliberately hide the marker while retaining every release contract.
	var unmarked NativePersistence = unmarkedNativeStub{
		Repository:                       stub.Repository,
		FinalizationUnitOfWork:           stub.FinalizationUnitOfWork,
		CandidateProvenanceRepository:    stub.CandidateProvenanceRepository,
		ServingStateProvenanceRepository: stub.ServingStateProvenanceRepository,
	}
	_, err := Build(t.Context(), Config{Production: true, Persistence: &Persistence{Repository: unmarked, Finalization: unmarked, CandidateProvenance: unmarked, ServingProvenance: unmarked, native: unmarked}, Environment: "dev"})
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL release persistence") {
		t.Fatalf("unmarked native error = %v", err)
	}
}

func TestBuildProductionRejectsUnconfiguredNativeAuthority(t *testing.T) {
	stub := nativeReleaseStub{configured: false, audit: true, events: true, workflow: true}
	_, err := Build(t.Context(), Config{Production: true, Persistence: &Persistence{Repository: stub, Finalization: stub, CandidateProvenance: stub, ServingProvenance: stub, native: stub}, Environment: "dev"})
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL release persistence") {
		t.Fatalf("unconfigured native error = %v", err)
	}
}

func TestBuildProductionAcceptsConfiguredNativeAuthority(t *testing.T) {
	stub := nativeReleaseStub{configured: true, audit: true, events: true, workflow: true}
	persistence, err := NewPostgresPersistence(stub)
	if err != nil {
		t.Fatal(err)
	}
	config := configuredNativeReleaseConfig(t, &persistence)
	module, err := Build(t.Context(), config)
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

func TestBuildProductionRequiresNativeArtifactLifecycle(t *testing.T) {
	stub := nativeReleaseStub{configured: true, audit: true, events: true, workflow: true}
	persistence, err := NewPostgresPersistence(stub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(t.Context(), Config{
		Production: true, Persistence: &persistence, Catalog: nativeCatalogStub{configured: true},
		States: nativeServingStateReaderStub{}, Environment: "dev",
	}); err == nil || !strings.Contains(err.Error(), "candidate source reader, artifact store, and storage security domain") {
		t.Fatalf("missing native artifact lifecycle error = %v", err)
	}
	config := configuredNativeReleaseConfig(t, &persistence)
	module, err := Build(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if module.nativeCandidatePhases == nil {
		t.Fatal("production native candidate artifact lifecycle is unavailable")
	}
	config.StorageSecurityDomain = " invalid "
	if _, err := Build(t.Context(), config); err == nil || !strings.Contains(err.Error(), "canonical candidate artifact storage security domain") {
		t.Fatalf("invalid native storage domain error = %v", err)
	}
}

func TestBuildProductionRequiresConfiguredNativeCatalog(t *testing.T) {
	stub := nativeReleaseStub{configured: true, audit: true, events: true, workflow: true}
	persistence, err := NewPostgresPersistence(stub)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Build(t.Context(), Config{Production: true, Persistence: &persistence, Environment: "dev"})
	if err == nil || !strings.Contains(err.Error(), "native PostgreSQL catalog") {
		t.Fatalf("missing catalog error = %v", err)
	}
}

func TestBuildProductionRejectsUnmarkedDeploymentAuthority(t *testing.T) {
	stub := nativeReleaseStub{configured: true, audit: true, events: true, workflow: true}
	persistence, err := NewPostgresPersistence(stub)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Build(t.Context(), Config{Production: true, Persistence: &persistence, Catalog: nativeCatalogStub{configured: true}, Deployments: unmarkedDeploymentStub{}, Environment: "dev"})
	if err == nil || !strings.Contains(err.Error(), "native PostgreSQL deployments") {
		t.Fatalf("unmarked deployments error = %v", err)
	}
}

func TestBuildProductionAcceptsConfiguredDeploymentAuthority(t *testing.T) {
	stub := nativeReleaseStub{configured: true, audit: true, events: true, workflow: true}
	persistence, err := NewPostgresPersistence(stub)
	if err != nil {
		t.Fatal(err)
	}
	config := configuredNativeReleaseConfig(t, &persistence)
	config.Deployments = deploymentStub{configured: true}
	module, err := Build(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if module.DeploymentLinkage() == nil {
		t.Fatal("configured deployment authority was not exposed")
	}
}

func configuredNativeReleaseConfig(t *testing.T, persistence *Persistence) Config {
	t.Helper()
	store, err := platformobjectstore.NewMemoryStore(platformobjectstore.MemoryStoreConfig{StorageSecurityDomain: "release-test"})
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Production: true, Persistence: persistence, Catalog: nativeCatalogStub{configured: true},
		States: nativeServingStateReaderStub{}, Environment: "dev",
		CandidateSourceReader: &nativeInspectReaderStub{}, CandidateArtifactStore: store,
		StorageSecurityDomain: "release-test",
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
