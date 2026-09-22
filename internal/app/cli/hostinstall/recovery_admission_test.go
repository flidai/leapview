package hostinstall

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/recoveryset"
)

type admissionProbeFunc func(context.Context, providerrestore.Report, providerrestore.CredentialBundle) (RecoveryProviderProbeResult, error)

func (function admissionProbeFunc) Probe(ctx context.Context, report providerrestore.Report, bundle providerrestore.CredentialBundle) (RecoveryProviderProbeResult, error) {
	return function(ctx, report, bundle)
}

func TestHostCommandExposesRecoveryAdmissionSurface(t *testing.T) {
	host := Command(t.Context(), CommandOptions{})
	command, _, err := host.Find([]string{"admit-recovery"})
	if err != nil {
		t.Fatal(err)
	}
	if command.Name() != "admit-recovery" {
		t.Fatalf("host command = %q", command.Name())
	}
	for _, name := range []string{"report", "secret-root", "output", "occurrence-id", "target-id", "recovery-set-id", "frontier-digest", "artifact"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("admit-recovery flag --%s is absent", name)
		}
	}
}

func TestRecoveryAdmissionBindsHandoffAndPersistsCredentialFreeEvidence(t *testing.T) {
	report, bundle, request := recoveryAdmissionFixture(t)
	request = persistRecoveryAdmissionFixture(t, report, bundle, request)
	verifiedAt := time.Date(2026, 9, 22, 15, 0, 0, 0, time.UTC)
	admission := RecoveryAdmission{
		Now: func() time.Time { return verifiedAt.Add(time.Minute) },
		Probe: admissionProbeFunc(func(_ context.Context, actual providerrestore.Report, credentials providerrestore.CredentialBundle) (RecoveryProviderProbeResult, error) {
			if actual.OccurrenceID != report.OccurrenceID || credentials.ControlURL != bundle.ControlURL {
				t.Fatal("probe did not receive the admitted report and credential bundle")
			}
			return RecoveryProviderProbeResult{ControlDatabase: "control", DuckLakeDatabase: "ducklake", ObjectCount: 1, TLSVerified: true, VerifiedAt: verifiedAt}, nil
		}),
	}
	evidence, err := admission.Admit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Status != "admitted" || evidence.OccurrenceID != report.OccurrenceID || evidence.TargetID != report.TargetID || evidence.ArtifactIdentity != report.Handoff.Artifact.Image || !evidence.Probe.TLSVerified {
		t.Fatalf("unexpected admission evidence: %#v", evidence)
	}
	if recoveryAdmissionContainsSecret(evidence, "control-secret", "duck-secret", "object-access", "object-secret") {
		t.Fatal("recovery admission evidence contains provider credentials")
	}
	raw, err := os.ReadFile(request.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"control-secret", "duck-secret", "object-access", "object-secret", "BEGIN CERTIFICATE"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("persisted admission evidence contains %q", secret)
		}
	}
}

func TestRecoveryAdmissionFailsClosedBeforeProbe(t *testing.T) {
	report, bundle, request := recoveryAdmissionFixture(t)
	request = persistRecoveryAdmissionFixture(t, report, bundle, request)
	probeCalls := 0
	admission := RecoveryAdmission{Probe: admissionProbeFunc(func(context.Context, providerrestore.Report, providerrestore.CredentialBundle) (RecoveryProviderProbeResult, error) {
		probeCalls++
		return RecoveryProviderProbeResult{}, nil
	})}

	for name, mutate := range map[string]func(*RecoveryAdmissionRequest){
		"target":       func(value *RecoveryAdmissionRequest) { value.TargetID = "wrong-target" },
		"recovery set": func(value *RecoveryAdmissionRequest) { value.RecoverySetID = "wrong-set" },
		"frontier":     func(value *RecoveryAdmissionRequest) { value.FrontierDigest = "sha256:" + strings.Repeat("f", 64) },
		"artifact": func(value *RecoveryAdmissionRequest) {
			value.ArtifactIdentity = "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("d", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := request
			invalid.OutputPath = filepath.Join(t.TempDir(), "admission.json")
			mutate(&invalid)
			if _, err := admission.Admit(t.Context(), invalid); err == nil {
				t.Fatalf("mismatched %s was admitted", name)
			}
			if _, err := os.Stat(invalid.OutputPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed admission persisted output: %v", err)
			}
		})
	}
	if probeCalls != 0 {
		t.Fatalf("provider probe ran %d times for identity failures", probeCalls)
	}
}

func TestRecoveryAdmissionRejectsUnreachableProviderWithoutPersistingSuccess(t *testing.T) {
	report, bundle, request := recoveryAdmissionFixture(t)
	request = persistRecoveryAdmissionFixture(t, report, bundle, request)
	admission := RecoveryAdmission{Probe: admissionProbeFunc(func(context.Context, providerrestore.Report, providerrestore.CredentialBundle) (RecoveryProviderProbeResult, error) {
		return RecoveryProviderProbeResult{}, errors.New("provider unreachable")
	})}
	if _, err := admission.Admit(t.Context(), request); err == nil || !strings.Contains(err.Error(), "provider unreachable") {
		t.Fatalf("unreachable provider admission = %v", err)
	}
	if _, err := os.Stat(request.OutputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreachable provider persisted success: %v", err)
	}
}

func recoveryAdmissionFixture(t *testing.T) (providerrestore.Report, providerrestore.CredentialBundle, RecoveryAdmissionRequest) {
	t.Helper()
	artifact := "ghcr.io/flidai/leapview@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	rootCA := recoveryRootCA(t)
	handoff := providerrestore.ReplacementHandoff{
		SchemaVersion: providerrestore.HandoffSchemaVersion, Kind: providerrestore.HandoffKind, Status: providerrestore.HandoffAvailable,
		RecoverySetID: "recovery-set-a", FrontierDigest: "sha256:" + strings.Repeat("a", 64), TargetID: "target-a",
		Artifact: compatibility.ReleaseIdentity{Version: "0.2.0", SourceRevision: "69652fb20101de80497cbf47ecfb402f700d6552", Image: artifact, Distribution: "oci", Platform: "linux/amd64"},
		Providers: []providerrestore.ProviderEndpoint{
			{Role: "control", Provider: "postgresql", ResourceID: "postgres-a", Endpoint: "postgres://db.example.com:5432?sslmode=verify-full", Database: "control", CredentialSecretKey: "postgres.control.url", TLSRootCASecretKey: "postgres.root-ca"},
			{Role: "ducklake", Provider: "postgresql", ResourceID: "postgres-a", Endpoint: "postgres://db.example.com:5432?sslmode=verify-full", Database: "ducklake", CredentialSecretKey: "postgres.ducklake.url", TLSRootCASecretKey: "postgres.root-ca"},
			{Role: "objects", Provider: "s3", ResourceID: "objects-a", Endpoint: "https://objects.example.com", Region: "us-east-1", Bucket: "restored", CredentialSecretKey: "object.credentials", TLSRootCASecretKey: "object.root-ca"},
		},
		AvailableAt: time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC),
	}
	bundle := providerrestore.CredentialBundle{
		SchemaVersion: 2, ControlURL: "postgres://runtime:control-secret@db.example.com:5432/control?sslmode=verify-full", DuckLakeURL: "postgres://runtime:duck-secret@db.example.com:5432/ducklake?sslmode=verify-full",
		ObjectEndpoint: "https://objects.example.com", ObjectRegion: "us-east-1", ObjectAccessKey: "object-access", ObjectSecretKey: "object-secret", PostgresRootCA: rootCA, ObjectRootCA: rootCA,
	}
	report := providerrestore.Report{
		SchemaVersion: providerrestore.ReportSchemaVersion, Kind: providerrestore.ReportKind, Status: providerrestore.StatusSucceeded,
		OccurrenceID: "occurrence-a", RecoverySetID: handoff.RecoverySetID, FrontierDigest: handoff.FrontierDigest, TargetID: handoff.TargetID,
		Databases:    []providerrestore.DatabaseResult{{DatabaseRole: recoveryset.DatabaseControl, DatabaseIdentity: "control"}, {DatabaseRole: recoveryset.DatabaseDuckLake, DatabaseIdentity: "ducklake"}},
		Objects:      []providerrestore.ObjectResult{{URI: "s3://restored/state.json", ObservedVersionID: "version-a", Digest: strings.Repeat("b", 64)}},
		Verification: providerrestore.VerificationResult{Ready: true, ObjectsConsistent: true},
		Admission:    providerrestore.AdmissionResult{ValidationAttemptID: "attempt-a", ValidationDigest: "sha256:" + strings.Repeat("c", 64), PublishedSetID: handoff.RecoverySetID, PublishedStatus: recoveryset.StatusPublished, PublishedAt: time.Date(2026, 9, 22, 13, 59, 0, 0, time.UTC)},
		Handoff:      handoff,
	}
	request := RecoveryAdmissionRequest{OccurrenceID: report.OccurrenceID, TargetID: report.TargetID, RecoverySetID: report.RecoverySetID, FrontierDigest: report.FrontierDigest, ArtifactIdentity: artifact}
	return report, bundle, request
}

func persistRecoveryAdmissionFixture(t *testing.T, report providerrestore.Report, bundle providerrestore.CredentialBundle, request RecoveryAdmissionRequest) RecoveryAdmissionRequest {
	t.Helper()
	secretRoot := t.TempDir()
	reference, err := (providerrestore.FileSecretBundleStore{Root: secretRoot}).Save(t.Context(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	report.Handoff.Secrets = reference
	reportPath := filepath.Join(t.TempDir(), "provider-report.json")
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	request.ReportPath, request.SecretRoot = reportPath, secretRoot
	request.OutputPath = filepath.Join(t.TempDir(), "recovery-admission.json")
	return request
}

func recoveryRootCA(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Recovery root"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
