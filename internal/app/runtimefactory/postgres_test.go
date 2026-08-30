package runtimefactory

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/app/testing/extensionfixture"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	dashboardruntimefactory "github.com/flidai/leapview/internal/dashboard/runtimefactory"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestPostgresSealedFactoryRequiresTargetCapabilities(t *testing.T) {
	factory := NewPostgresSealedFactory(PostgresSealedFactoryConfig{})
	sealed, ok := factory.(interface {
		PrepareSealed(context.Context, runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error)
	})
	if !ok {
		t.Fatal("PostgreSQL factory does not implement sealed runtime seam")
	}
	if _, err := sealed.PrepareSealed(context.Background(), runtimehost.RuntimeInput{}); err == nil || !strings.Contains(err.Error(), "resolver") {
		t.Fatalf("error=%v, want missing capability error", err)
	}
}

func TestPostgresLeaseHandleReportsStaleFenceAndReleases(t *testing.T) {
	repo := &leaseProbe{renewErr: errors.New("stale fence")}
	failed := make(chan error, 1)
	h := newPostgresLeaseHandle(repo, "lease", time.Now().Add(20*time.Millisecond), func(err error) {
		if err != nil {
			failed <- err
		}
	})
	defer h.Close()
	select {
	case err := <-failed:
		if !errors.Is(err, repo.renewErr) {
			t.Fatalf("renewal error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stale lease renewal was not reported")
	}
	if h.Err() == nil {
		t.Fatal("lease handle did not retain renewal error")
	}
	if repo.renewedID != "lease" {
		t.Fatalf("renewal lease ID=%q, want canonical ID lease", repo.renewedID)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if repo.released != 1 {
		t.Fatalf("release calls=%d, want 1", repo.released)
	}
	if repo.releaseCtx == nil {
		t.Fatal("lease release did not receive a context")
	}
	if repo.releasedID != "lease" {
		t.Fatalf("release lease ID=%q, want canonical ID lease", repo.releasedID)
	}
	if _, ok := repo.releaseCtx.Deadline(); !ok {
		t.Fatal("lease release context is unbounded")
	}
}

func TestPostgresLeaseHandleRetriesTransientFailureBeforeExpiry(t *testing.T) {
	callback := make(chan error, 2)
	repo := &sequenceLeaseProbe{results: []error{errors.New("provider timeout"), nil}}
	h := newPostgresLeaseHandle(repo, "lease", time.Now().UTC().Add(120*time.Millisecond), func(err error) {
		callback <- err
	})
	defer h.Close()
	select {
	case err := <-callback:
		if err != nil {
			t.Fatalf("successful retry callback error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lease heartbeat did not retry before expiry")
	}
	if calls := repo.Calls(); calls < 2 {
		t.Fatalf("renewal calls=%d, want transient retry", calls)
	}
	if id := repo.LastID(); id != "lease" {
		t.Fatalf("renewal lease ID=%q, want canonical ID lease", id)
	}
	if err := h.Err(); err != nil {
		t.Fatalf("lease health after successful retry=%v, want nil", err)
	}
}

func TestPostgresLeaseHandleBoundsBlockedRenewalByExpiry(t *testing.T) {
	started := make(chan struct{}, 1)
	callback := make(chan error, 1)
	repo := &blockingLeaseProbe{started: started}
	leaseStart := time.Now().UTC()
	h := newPostgresLeaseHandle(repo, "lease", leaseStart.Add(60*time.Millisecond), func(err error) {
		if err != nil {
			callback <- err
		}
	})
	defer h.Close()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocked renewal did not start")
	}
	select {
	case err := <-callback:
		if err == nil || !errors.Is(err, ErrPostgresLeaseRenewal) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked renewal callback=%v, want bounded expiry error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked renewal exceeded lease expiry")
	}
	if elapsed := time.Since(leaseStart); elapsed > 300*time.Millisecond {
		t.Fatalf("blocked renewal elapsed=%v, want bounded by expiry", elapsed)
	}
	if err := h.Err(); err == nil || !errors.Is(err, ErrPostgresLeaseRenewal) {
		t.Fatalf("blocked renewal health=%v, want ErrPostgresLeaseRenewal", err)
	}
}

func TestPostgresSealedFactoryAcquiresAuthorizesAndReleasesOnAttachFailure(t *testing.T) {
	contract := deliveryCredentialTestContract(t)
	dataPath, err := contract.Pool.DataPath()
	if err != nil {
		t.Fatal(err)
	}
	compatibilityDigest, err := contract.Tuple.Digest()
	if err != nil {
		t.Fatal(err)
	}
	stateID := servingstate.ID("state-postgres")
	artifactDigest := runtimeFactoryDigest("artifact")
	root := SealedServingRoot{
		GenerationID: "generation", CandidateID: "candidate", AttemptID: "attempt", ServingStateID: string(stateID), ServingArtifactID: "artifact", ServingArtifactDigest: artifactDigest,
		SealID: "seal", QualificationDigest: runtimeFactoryDigest("qualification"), ClosureDigest: runtimeFactoryDigest("closure"),
		PhysicalPoolID: contract.Pool.ID.String(), PoolContract: contract, Compatibility: contract.Tuple,
		CatalogDatabase: "ducklake", CatalogID: "catalog", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000008", CatalogMetadataSchema: ducklake.MetadataSchemaForPool(contract.Pool.ID.String()),
		CatalogSnapshotID: 7, DeliveryID: "delivery", FencingEpoch: 3, DataPath: dataPath,
		CompatibilityDigest: compatibilityDigest, RuntimeVersion: "runtime-v1", SecurityDomainFingerprint: runtimeFactoryDigest("security"),
		CatalogVersion: "1", CatalogVersionNumber: 1, DuckDBVersion: "v1", DuckLakeExtensionVersion: "v1", DuckLakeSpecVersion: "spec-v1", CatalogSchemaVersion: "schema-v1", RelationNamespace: "candidate/attempt/fence", RelationManifestDigest: runtimeFactoryDigest("manifest"), ObjectRoot: "objects/tenant", ObjectRootDigest: runtimeFactoryDigest("object-root"), ArtifactRoot: "artifacts/tenant", ArtifactRootDigest: runtimeFactoryDigest("artifact-root"), CompiledGraphDigest: runtimeFactoryDigest("graph"), CompiledConfigDigest: runtimeFactoryDigest("config"), RequestDigest: runtimeFactoryDigest("request"), PlanDigest: runtimeFactoryDigest("plan"), TenantDomain: "tenant", Region: "region", EncryptionDomain: "encryption", ObjectNamespace: "namespace",
	}
	leases := &leaseProbe{}
	authorized := false
	var authorization PostgresServingAuthorizationInput
	factory := NewPostgresSealedFactory(PostgresSealedFactoryConfig{
		Resolve: func(context.Context, runtimehost.RuntimeInput) (SealedServingRoot, error) { return root, nil },
		BuildRuntime: func(context.Context, dashboardruntimefactory.Input, *ducklake.Environment) (*dashboardruntime.Service, error) {
			return nil, errors.New("unexpected dashboard access")
		},
		PoolContract: contract, SnapshotLeases: leases,
		Authorize: func(_ context.Context, in PostgresServingAuthorizationInput) error {
			authorized = true
			authorization = in
			return nil
		},
		CatalogDatabase: "ducklake", CatalogID: "catalog", LeaseHolder: "runtime",
		RuntimeVersion: "runtime-v1", SecurityDomainFingerprint: root.SecurityDomainFingerprint,
		CredentialBootstrap: func(context.Context, driver.ExecerContext) error { return nil },
		ExtensionAdmission:  extensionfixture.New(t, "ducklake").Admission,
		DuckLakeSecret:      "lake_secret", PostgresSecret: "pg_secret",
	})
	sealed := factory.(interface {
		PrepareSealed(context.Context, runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error)
	})
	_, err = sealed.PrepareSealed(context.Background(), runtimehost.RuntimeInput{State: servingstate.State{ID: stateID, ProjectID: "project", Environment: "prod", DuckLakeSnapshotID: 7}, Artifact: servingstate.Artifact{ID: "artifact", ServingStateID: stateID, Digest: artifactDigest}})
	if err == nil {
		t.Fatal("factory unexpectedly attached unavailable PostgreSQL catalog")
	}
	if !authorized {
		t.Fatalf("authorization callback was not invoked after lease acquisition (err=%v)", err)
	}
	if got := leases.createInput.ServingStateID; got != stateID {
		t.Fatalf("lease serving state ID=%q, want requested state %q", got, stateID)
	}
	if got := leases.createInput.DuckLakeSnapshotID; got != root.CatalogSnapshotID {
		t.Fatalf("lease snapshot=%d, want exact root snapshot %d", got, root.CatalogSnapshotID)
	}
	if got := leases.createInput.OwnerID; got != "runtime" {
		t.Fatalf("lease owner=%q, want configured owner runtime", got)
	}
	if authorization.LeaseID != leases.leaseID || authorization.OwnerID != leases.createInput.OwnerID || authorization.Fence != root.FencingEpoch {
		t.Fatalf("authorization lease identity=%+v, want lease=%q owner=%q fence=%d", authorization, leases.leaseID, leases.createInput.OwnerID, root.FencingEpoch)
	}
	if leases.released != 1 {
		t.Fatalf("lease release calls=%d, want 1 after attach failure", leases.released)
	}
	if leases.releasedID != leases.leaseID {
		t.Fatalf("release lease ID=%q, want returned canonical ID %q", leases.releasedID, leases.leaseID)
	}
}

func TestPostgresSealedFactoryRejectsIncompleteOrMixedSealIdentityBeforeLease(t *testing.T) {
	contract := deliveryCredentialTestContract(t)
	dataPath, err := contract.Pool.DataPath()
	if err != nil {
		t.Fatal(err)
	}
	compatibilityDigest, err := contract.Tuple.Digest()
	if err != nil {
		t.Fatal(err)
	}
	stateID := servingstate.ID("state-postgres-adversarial")
	artifactDigest := runtimeFactoryDigest("artifact")
	baseRoot := SealedServingRoot{
		GenerationID: "generation", CandidateID: "candidate", AttemptID: "attempt", ServingStateID: string(stateID), ServingArtifactID: "artifact", ServingArtifactDigest: artifactDigest,
		SealID: "seal", QualificationDigest: runtimeFactoryDigest("qualification"), ClosureDigest: runtimeFactoryDigest("closure"),
		PhysicalPoolID: contract.Pool.ID.String(), PoolContract: contract, Compatibility: contract.Tuple,
		CatalogDatabase: "ducklake", CatalogID: "catalog", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000008", CatalogMetadataSchema: ducklake.MetadataSchemaForPool(contract.Pool.ID.String()),
		CatalogSnapshotID: 7, DeliveryID: "delivery", FencingEpoch: 3, DataPath: dataPath,
		CompatibilityDigest: compatibilityDigest, RuntimeVersion: "runtime-v1", SecurityDomainFingerprint: runtimeFactoryDigest("security"),
		CatalogVersion: "1", CatalogVersionNumber: 1, DuckDBVersion: "v1", DuckLakeExtensionVersion: "v1", DuckLakeSpecVersion: "spec-v1", CatalogSchemaVersion: "schema-v1", RelationNamespace: "candidate/attempt/fence", RelationManifestDigest: runtimeFactoryDigest("manifest"), ObjectRoot: "objects/tenant", ObjectRootDigest: runtimeFactoryDigest("object-root"), ArtifactRoot: "artifacts/tenant", ArtifactRootDigest: runtimeFactoryDigest("artifact-root"), CompiledGraphDigest: runtimeFactoryDigest("graph"), CompiledConfigDigest: runtimeFactoryDigest("config"), RequestDigest: runtimeFactoryDigest("request"), PlanDigest: runtimeFactoryDigest("plan"), TenantDomain: "tenant", Region: "region", EncryptionDomain: "encryption", ObjectNamespace: "namespace",
	}
	input := runtimehost.RuntimeInput{State: servingstate.State{ID: stateID, ProjectID: "project", Environment: "prod", DuckLakeSnapshotID: 7}, Artifact: servingstate.Artifact{ID: "artifact", ServingStateID: stateID, Digest: artifactDigest}}
	cases := []struct {
		name   string
		mutate func(*SealedServingRoot)
	}{
		{"delivery identity", func(r *SealedServingRoot) { r.DeliveryID = "" }},
		{"generation identity", func(r *SealedServingRoot) { r.GenerationID = "" }},
		{"candidate identity", func(r *SealedServingRoot) { r.CandidateID = "" }},
		{"attempt identity", func(r *SealedServingRoot) { r.AttemptID = "" }},
		{"catalog UUID", func(r *SealedServingRoot) { r.CatalogUUID = "not-a-uuid" }},
		{"catalog version cross-binding", func(r *SealedServingRoot) { r.CatalogVersionNumber = 2 }},
		{"relation namespace", func(r *SealedServingRoot) { r.RelationNamespace = "" }},
		{"relation manifest digest", func(r *SealedServingRoot) { r.RelationManifestDigest = "invalid" }},
		{"object root", func(r *SealedServingRoot) { r.ObjectRoot = "" }},
		{"object root digest", func(r *SealedServingRoot) { r.ObjectRootDigest = "invalid" }},
		{"artifact root", func(r *SealedServingRoot) { r.ArtifactRoot = "" }},
		{"artifact root digest", func(r *SealedServingRoot) { r.ArtifactRootDigest = "invalid" }},
		{"compiled graph digest", func(r *SealedServingRoot) { r.CompiledGraphDigest = "invalid" }},
		{"compiled config digest", func(r *SealedServingRoot) { r.CompiledConfigDigest = "invalid" }},
		{"request digest", func(r *SealedServingRoot) { r.RequestDigest = "invalid" }},
		{"plan digest", func(r *SealedServingRoot) { r.PlanDigest = "invalid" }},
		{"DuckLake spec version", func(r *SealedServingRoot) { r.DuckLakeSpecVersion = "" }},
		{"catalog schema version", func(r *SealedServingRoot) { r.CatalogSchemaVersion = "" }},
		{"serving artifact cross-binding", func(r *SealedServingRoot) { r.ServingArtifactDigest = runtimeFactoryDigest("other-artifact") }},
		{"snapshot cross-binding", func(r *SealedServingRoot) { r.CatalogSnapshotID = 8 }},
		{"compatibility cross-binding", func(r *SealedServingRoot) { r.CompatibilityDigest = runtimeFactoryDigest("other-compatibility") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := baseRoot
			tc.mutate(&root)
			leases := &leaseProbe{}
			factory := NewPostgresSealedFactory(PostgresSealedFactoryConfig{
				Resolve: func(context.Context, runtimehost.RuntimeInput) (SealedServingRoot, error) { return root, nil },
				BuildRuntime: func(context.Context, dashboardruntimefactory.Input, *ducklake.Environment) (*dashboardruntime.Service, error) {
					return nil, errors.New("unexpected dashboard access")
				},
				PoolContract: contract, SnapshotLeases: leases,
				Authorize:       func(context.Context, PostgresServingAuthorizationInput) error { return nil },
				CatalogDatabase: "ducklake", CatalogID: "catalog", RuntimeVersion: "runtime-v1", SecurityDomainFingerprint: baseRoot.SecurityDomainFingerprint,
				CredentialBootstrap: func(context.Context, driver.ExecerContext) error { return nil },
				ExtensionAdmission:  extensionfixture.New(t, "ducklake").Admission,
				DuckLakeSecret:      "lake_secret", PostgresSecret: "pg_secret",
			})
			sealed := factory.(interface {
				PrepareSealed(context.Context, runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error)
			})
			if _, err := sealed.PrepareSealed(context.Background(), input); err == nil {
				t.Fatal("mixed/incomplete PostgreSQL root was accepted")
			}
			if leases.acquired != 0 {
				t.Fatalf("lease acquired=%d before rejecting root", leases.acquired)
			}
		})
	}
}

func runtimeFactoryDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type leaseProbe struct {
	leaseID        string
	acquired       int
	createInput    servingstate.SnapshotLeaseInput
	renewErr       error
	renewedID      string
	renewedExpires time.Time
	released       int
	releasedID     string
	releaseCtx     context.Context
}

func (p *leaseProbe) CreateQuerySnapshotLease(_ context.Context, in servingstate.SnapshotLeaseInput) (string, error) {
	p.acquired++
	p.createInput = in
	if p.leaseID == "" {
		p.leaseID = "canonical-lease"
	}
	return p.leaseID, nil
}
func (p *leaseProbe) ExtendQuerySnapshotLease(_ context.Context, id string, expires time.Time) error {
	p.renewedID = id
	p.renewedExpires = expires
	return p.renewErr
}
func (p *leaseProbe) ReleaseQuerySnapshotLease(ctx context.Context, id string) error {
	p.releaseCtx = ctx
	p.releasedID = id
	p.released++
	return nil
}

type sequenceLeaseProbe struct {
	mu      sync.Mutex
	results []error
	calls   int
	lastID  string
}

func (p *sequenceLeaseProbe) CreateQuerySnapshotLease(context.Context, servingstate.SnapshotLeaseInput) (string, error) {
	return "", errors.New("unexpected acquire")
}

func (p *sequenceLeaseProbe) ExtendQuerySnapshotLease(_ context.Context, id string, _ time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.lastID = id
	if len(p.results) == 0 {
		return nil
	}
	err := p.results[0]
	p.results = p.results[1:]
	return err
}

func (p *sequenceLeaseProbe) ReleaseQuerySnapshotLease(context.Context, string) error {
	return nil
}

func (p *sequenceLeaseProbe) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *sequenceLeaseProbe) LastID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastID
}

type blockingLeaseProbe struct{ started chan<- struct{} }

func (p *blockingLeaseProbe) CreateQuerySnapshotLease(context.Context, servingstate.SnapshotLeaseInput) (string, error) {
	return "", errors.New("unexpected acquire")
}

func (p *blockingLeaseProbe) ExtendQuerySnapshotLease(ctx context.Context, _ string, _ time.Time) error {
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

func (p *blockingLeaseProbe) ReleaseQuerySnapshotLease(context.Context, string) error {
	return nil
}
