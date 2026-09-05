package offline

import (
	"bytes"
	"testing"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

func testQualificationArtifacts(t *testing.T) QualificationPoolArtifacts {
	t.Helper()
	compatibility := physicalpool.Compatibility{
		DuckDBRuntime: "duckdb:test", DuckLakeExtension: "ducklake:test", CatalogFormat: "ducklake-catalog:v1",
		StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1",
	}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{
		Compatibility: compatibility, ConformanceVersion: "test/v1",
		Checks: []physicalpool.EvidenceCheck{{
			ID: "shared_pool_check", Passed: true,
			ObservationDigest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return QualificationPoolArtifacts{
		SchemaVersion: QualificationPoolArtifactsSchemaVersion,
		Pool: physicalpool.PoolIdentity{
			StorageLocation: "/var/lib/leapview/data", StorageNamespace: "delivery", Region: "local", Tenant: "qualification", EncryptionDomain: "local",
			IsolationBoundary: "qualification", RetentionAuthority: "qualification",
			RetentionPolicy: physicalpool.RetentionPolicy{ReaderGracePeriodSeconds: 1800, OrphanGracePeriodSeconds: 3600, BuildGracePeriodSeconds: 3600},
			Compatibility:   compatibility,
		},
		Evidence: physicalpool.EvidenceArtifact{SchemaVersion: physicalpool.EvidenceArtifactSchemaVersion, Evidence: evidence},
	}
}

func TestQualificationPoolArtifactsRequiresDeliveryOwnershipIdentity(t *testing.T) {
	for _, field := range []string{"tenant", "region"} {
		t.Run(field, func(t *testing.T) {
			artifacts := testQualificationArtifacts(t)
			switch field {
			case "tenant":
				artifacts.Pool.Tenant = ""
			case "region":
				artifacts.Pool.Region = ""
			}
			if _, err := MarshalQualificationPoolArtifacts(artifacts); err == nil {
				t.Fatalf("missing %s unexpectedly accepted", field)
			}
		})
	}
}

func TestQualificationPoolArtifactsRoundTripValidatesIdentityAndEvidence(t *testing.T) {
	artifacts := testQualificationArtifacts(t)
	encoded, err := MarshalQualificationPoolArtifacts(artifacts)
	if err != nil {
		t.Fatalf("marshal qualification artifacts: %v", err)
	}
	decoded, err := UnmarshalQualificationPoolArtifacts(encoded)
	if err != nil {
		t.Fatalf("unmarshal qualification artifacts: %v", err)
	}
	if err := decoded.Pool.Validate(); err != nil {
		t.Fatalf("decoded pool identity: %v", err)
	}
	if err := decoded.Evidence.Evidence.Verify(); err != nil {
		t.Fatalf("decoded evidence: %v", err)
	}
	if !bytes.Equal(encoded, mustMarshalQualificationArtifacts(t, decoded)) {
		t.Fatal("qualification artifact encoding is not deterministic")
	}
}

func TestQualificationPoolArtifactsRejectsTrailingAndMismatchedEvidence(t *testing.T) {
	artifacts := testQualificationArtifacts(t)
	encoded, err := MarshalQualificationPoolArtifacts(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnmarshalQualificationPoolArtifacts(append(encoded, []byte("{}")...)); err == nil {
		t.Fatal("trailing JSON value unexpectedly accepted")
	}
	artifacts.SchemaVersion = 0
	if _, err := MarshalQualificationPoolArtifacts(artifacts); err == nil {
		t.Fatal("missing schema version unexpectedly accepted")
	}
	artifacts.SchemaVersion = QualificationPoolArtifactsSchemaVersion
	artifacts.Evidence.Evidence.Compatibility.DuckLakeExtension = "ducklake:other"
	if _, err := MarshalQualificationPoolArtifacts(artifacts); err == nil {
		t.Fatalf("mismatched evidence error = %v", err)
	}
}

func mustMarshalQualificationArtifacts(t *testing.T, artifacts QualificationPoolArtifacts) []byte {
	t.Helper()
	encoded, err := MarshalQualificationPoolArtifacts(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
