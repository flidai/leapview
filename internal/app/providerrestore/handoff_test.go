package providerrestore

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/recoveryset"
)

func TestReplacementHandoffRejectsPlaceholderAndCredentialBearingIdentity(t *testing.T) {
	set := providerRestoreSet(t)
	handoff := validReplacementHandoff(set)
	if err := handoff.Validate(set, handoff.Artifact.Image); err != nil {
		t.Fatal(err)
	}

	placeholder := handoff
	placeholder.Artifact.Image = "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	if err := placeholder.Validate(set, placeholder.Artifact.Image); err == nil {
		t.Fatal("placeholder OCI identity was accepted")
	}

	credentialBearing := handoff
	credentialBearing.Providers = append([]ProviderEndpoint(nil), handoff.Providers...)
	credentialBearing.Providers[0].Endpoint = "postgres://runtime:secret@db.example.test:5432"
	if err := credentialBearing.Validate(set, credentialBearing.Artifact.Image); err == nil {
		t.Fatal("credential-bearing provider endpoint was accepted")
	}

	credentialQuery := handoff
	credentialQuery.Providers = append([]ProviderEndpoint(nil), handoff.Providers...)
	credentialQuery.Providers[0].Endpoint = "postgres://db.example.test:5432?password=secret"
	if err := credentialQuery.Validate(set, credentialQuery.Artifact.Image); err == nil {
		t.Fatal("credential-bearing provider endpoint query was accepted")
	}

	for name, endpoint := range map[string]string{
		"plaintext postgres": "postgres://db.example.com:5432?sslmode=disable",
		"producer alias":     "postgres://fai981-postgres:5432?sslmode=verify-full",
		"loopback":           "postgres://127.0.0.1:5432?sslmode=verify-full",
		"plaintext objects":  "http://objects.example.com",
	} {
		t.Run(name, func(t *testing.T) {
			invalid := handoff
			invalid.Providers = append([]ProviderEndpoint(nil), handoff.Providers...)
			index := 0
			if name == "plaintext objects" {
				index = 2
			}
			invalid.Providers[index].Endpoint = endpoint
			if err := invalid.Validate(set, invalid.Artifact.Image); err == nil {
				t.Fatalf("insecure or producer-only endpoint %q was accepted", endpoint)
			}
		})
	}
}

func TestSecretBundleIsDigestBoundAndSeparateFromEvidence(t *testing.T) {
	privateRoot := t.TempDir()
	evidenceRoot := t.TempDir()
	consumerRoot := filepath.Join(privateRoot, "consumer")
	store := FileSecretBundleStore{Root: privateRoot}
	rootCA := testRootCA(t)
	bundle := CredentialBundle{SchemaVersion: 2, ControlURL: "postgres://runtime:control-secret@db.example.com:5432/control?sslmode=verify-full", DuckLakeURL: "postgres://runtime:duck-secret@db.example.com:5432/ducklake?sslmode=verify-full", ObjectEndpoint: "https://objects.example.com", ObjectRegion: "us-east-1", ObjectAccessKey: "access-secret", ObjectSecretKey: "object-secret", PostgresRootCA: rootCA, ObjectRootCA: rootCA}
	reference, err := store.Save(t.Context(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	sourcePath, err := store.SourcePath(reference)
	if err != nil {
		t.Fatal(err)
	}
	consumerPath := filepath.Join(consumerRoot, reference.SHA256+".json")
	if err := os.MkdirAll(filepath.Dir(consumerPath), 0o700); err != nil {
		t.Fatal(err)
	}
	rawSource, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(consumerPath, rawSource, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := (FileSecretBundleStore{Root: consumerRoot}).Load(t.Context(), reference)
	if err != nil || loaded != bundle {
		t.Fatalf("independent secret load = %#v, error=%v", loaded, err)
	}
	encoded, err := json.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"control-secret", "duck-secret", "access-secret", "object-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("secret %q leaked into the reference", secret)
		}
	}
	if relative, err := filepath.Rel(evidenceRoot, sourcePath); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatal("private bundle was written under evidence root")
	}
	raw, err := os.ReadFile(sourcePath)
	if err != nil || !strings.Contains(string(raw), "object-secret") {
		t.Fatalf("private bundle did not contain expected credential: %v", err)
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private bundle mode = %v", info.Mode().Perm())
	}
}

func TestValidateHandoffReportRejectsForeignAuthoritativeIdentity(t *testing.T) {
	set := providerRestoreSet(t)
	report := Report{
		SchemaVersion: ReportSchemaVersion, Kind: ReportKind, Status: StatusSucceeded,
		OccurrenceID: "occurrence-a", RecoverySetID: set.ID, FrontierDigest: set.FrontierDigest, TargetID: set.Delivery.TargetID,
		Databases: []DatabaseResult{
			{DatabaseRole: recoveryset.DatabaseControl, DatabaseIdentity: "control"},
			{DatabaseRole: recoveryset.DatabaseDuckLake, DatabaseIdentity: "ducklake"},
		},
		Objects: []ObjectResult{{URI: "s3://restored/object"}},
		Handoff: validReplacementHandoff(set),
	}
	expected := HandoffExpectations{OccurrenceID: report.OccurrenceID, TargetID: report.TargetID, RecoverySetID: report.RecoverySetID, FrontierDigest: report.FrontierDigest, ArtifactIdentity: report.Handoff.Artifact.Image}
	if err := ValidateHandoffReport(report, expected); err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(*HandoffExpectations){
		"occurrence":   func(value *HandoffExpectations) { value.OccurrenceID = "occurrence-foreign" },
		"target":       func(value *HandoffExpectations) { value.TargetID = "target-foreign" },
		"recovery set": func(value *HandoffExpectations) { value.RecoverySetID = "recovery-set-foreign" },
		"frontier":     func(value *HandoffExpectations) { value.FrontierDigest = "sha256:" + strings64("f") },
		"artifact": func(value *HandoffExpectations) {
			value.ArtifactIdentity = "ghcr.io/flidai/leapview@sha256:" + strings64("d")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			foreign := expected
			mutate(&foreign)
			if err := ValidateHandoffReport(report, foreign); err == nil {
				t.Fatalf("foreign %s expectation was accepted", name)
			}
		})
	}
}

func validReplacementHandoff(set recoveryset.RecoverySet) ReplacementHandoff {
	return ReplacementHandoff{
		SchemaVersion: HandoffSchemaVersion, Kind: HandoffKind, Status: HandoffAvailable,
		RecoverySetID: set.ID, FrontierDigest: set.FrontierDigest, TargetID: set.Delivery.TargetID,
		Artifact: compatibility.ReleaseIdentity{Version: "0.2.0-rc.2", SourceRevision: "69652fb20101de80497cbf47ecfb402f700d6552", Image: testRunnableArtifact, Distribution: "oci", Platform: "linux/amd64"},
		Providers: []ProviderEndpoint{
			{Role: "control", Provider: "postgresql", ResourceID: "postgres-a", Endpoint: "postgres://db.example.com:5432?sslmode=verify-full", Database: "control", CredentialSecretKey: "postgres.control.url", TLSRootCASecretKey: "postgres.root-ca"},
			{Role: "ducklake", Provider: "postgresql", ResourceID: "postgres-a", Endpoint: "postgres://db.example.com:5432?sslmode=verify-full", Database: "ducklake", CredentialSecretKey: "postgres.ducklake.url", TLSRootCASecretKey: "postgres.root-ca"},
			{Role: "objects", Provider: "s3", ResourceID: "objects-a", Endpoint: "https://objects.example.com", Region: "us-east-1", Bucket: "restored", CredentialSecretKey: "object.credentials", TLSRootCASecretKey: "object.root-ca"},
		},
		Secrets:     SecretBundleReference{Provider: "host-provisioned-root-file", URI: "leapview-secret://host-provisioned/recovery/" + strings64("c"), SHA256: strings64("c"), Version: "1", Keys: []string{"object.credentials", "object.root-ca", "postgres.control.url", "postgres.ducklake.url", "postgres.root-ca"}},
		AvailableAt: time.Date(2026, 9, 22, 6, 0, 0, 0, time.UTC),
	}
}

func testRootCA(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "LeapView qualification root"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
