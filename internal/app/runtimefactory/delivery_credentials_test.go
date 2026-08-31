package runtimefactory

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/candidatecatalog"
	"github.com/flidai/leapview/internal/analytics/catalogseal"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsgates "github.com/flidai/leapview/internal/analytics/gates"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/gcadapter"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/release"
)

func TestNewCatalogObjectStoreS3RequiresTargetKeysBeforeAWSConfig(t *testing.T) {
	contract := deliveryCredentialTestContract(t)
	if _, err := NewCatalogObjectStore(context.Background(), contract, gcadapter.S3Config{}); err == nil || !strings.Contains(err.Error(), "target-owned S3 access") {
		t.Fatalf("missing S3 credentials error = %v", err)
	}
}

func TestNewL3ObjectStoreS3RequiresTargetKeysBeforeAWSConfig(t *testing.T) {
	contract := deliveryCredentialTestContract(t)
	if _, err := NewL3ObjectStore(context.Background(), contract, gcadapter.S3Config{}); err == nil || !strings.Contains(err.Error(), "target-owned S3 access") {
		t.Fatalf("missing L3 S3 credentials error = %v", err)
	}
}

func TestBuildRequestFactoryS3RequiresCredentialBootstrapBeforeBuild(t *testing.T) {
	factory := BuildRequestFactory(CandidateCatalogRunnerConfig{
		PoolContract: deliveryCredentialTestContract(t),
		Materialize: func(context.Context, *candidatecatalog.WorkingCatalog, deployment.DeliveryBuildInput, release.CandidateArtifactSet, string) ([]analyticsgates.SourceInput, error) {
			return nil, nil
		},
		Connections:    deliveryCredentialConnections{},
		ObjectStore:    deliveryCredentialObjectStore{},
		SealRepository: deliveryCredentialSealRepository{},
		RemoteVerifier: deliveryCredentialVerifier{},
		VerifyLease:    func(context.Context, candidatecatalog.WriterLease) error { return nil },
	})
	plan := &deployment.DeliveryPlan{ID: "plan-1"}
	_, err := factory(context.Background(), deployment.DeliveryCandidateBuildInput{Plan: plan, Candidate: deployment.Candidate{ID: "candidate-1"}}, release.CandidateArtifactSet{})
	if err == nil || !strings.Contains(err.Error(), "credential bootstrap") {
		t.Fatalf("missing candidate S3 credential bootstrap error = %v", err)
	}
}

func TestReadOnlyCatalogVerifierS3RequiresCredentialBootstrapBeforeRemoteOpen(t *testing.T) {
	opened := false
	verifier := ReadOnlyCatalogVerifier{PoolContract: deliveryCredentialTestContract(t)}
	err := verifier.Verify(context.Background(), catalogseal.RemoteVerification{Open: func(context.Context) (catalogseal.Object, error) {
		opened = true
		return catalogseal.Object{}, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "credential bootstrap") {
		t.Fatalf("missing verifier S3 credential bootstrap error = %v", err)
	}
	if opened {
		t.Fatal("verifier opened remote object before checking target credential bootstrap")
	}
}

// deliveryCredentialTestContract creates a fully admitted S3 contract so the
// object-store constructor reaches its target-credential guard.
func deliveryCredentialTestContract(t *testing.T) *ducklake.PoolContract {
	t.Helper()
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:test", DuckLakeExtension: "ducklake:test", CatalogFormat: "ducklake:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	checks := make([]physicalpool.EvidenceCheck, 0, len(ducklake.SharedPoolConformanceChecks))
	for _, id := range ducklake.SharedPoolConformanceChecks {
		checks = append(checks, physicalpool.EvidenceCheck{ID: id, Passed: true, ObservationDigest: "sha256:" + strings.Repeat("a", 64)})
	}
	p, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{StorageLocation: "s3://bucket/prefix", StorageNamespace: "delivery", EncryptionDomain: "target", IsolationBoundary: "target", RetentionAuthority: "target", RetentionPolicy: physicalpool.RetentionPolicy{ReaderGracePeriodSeconds: 300, OrphanGracePeriodSeconds: 3600}, Compatibility: tuple})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{Compatibility: tuple, ConformanceVersion: ducklake.SharedPoolConformanceVersion, Checks: checks})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := p.Admit(evidence)
	if err != nil {
		t.Fatal(err)
	}
	p, err = p.ApplyAdmission(admission)
	if err != nil {
		t.Fatal(err)
	}
	return &ducklake.PoolContract{Pool: p, Tuple: tuple, Admission: admission, Evidence: evidence}
}

type deliveryCredentialConnections struct{}

func (deliveryCredentialConnections) Acquire(context.Context, deployment.CandidateConnectionRequest) (deployment.CandidateConnectionLeases, error) {
	return deliveryCredentialLeases{}, nil
}

type deliveryCredentialLeases struct{}

func (deliveryCredentialLeases) Close() error                                       { return nil }
func (deliveryCredentialLeases) Evidence() []deployment.CandidateConnectionEvidence { return nil }

type deliveryCredentialObjectStore struct{}

func (deliveryCredentialObjectStore) Create(context.Context, string, io.Reader, catalogseal.ObjectMetadata) error {
	return nil
}
func (deliveryCredentialObjectStore) Open(context.Context, string) (catalogseal.Object, error) {
	return catalogseal.Object{}, nil
}

type deliveryCredentialSealRepository struct{}

func (deliveryCredentialSealRepository) Lookup(context.Context, string) (catalogseal.SealRecord, error) {
	return catalogseal.SealRecord{}, nil
}
func (deliveryCredentialSealRepository) Prepare(context.Context, catalogseal.SealIdentity) (catalogseal.SealRecord, error) {
	return catalogseal.SealRecord{}, nil
}
func (deliveryCredentialSealRepository) MarkUploaded(context.Context, string) (catalogseal.SealRecord, error) {
	return catalogseal.SealRecord{}, nil
}
func (deliveryCredentialSealRepository) CompleteVerified(context.Context, catalogseal.CompleteInput) (catalogseal.Completion, error) {
	return catalogseal.Completion{}, nil
}

type deliveryCredentialVerifier struct{}

func (deliveryCredentialVerifier) Verify(context.Context, catalogseal.RemoteVerification) error {
	return nil
}
