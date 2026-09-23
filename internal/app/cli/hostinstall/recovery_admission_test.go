package hostinstall

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
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
	"github.com/flidai/leapview/internal/refresh/recovery"
)

type admissionLedger struct{ occurrence recovery.Occurrence }

func (ledger admissionLedger) Occurrence(_ context.Context, _ string) (recovery.Occurrence, error) {
	return ledger.occurrence, nil
}

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
	for _, name := range []string{"report", "control-url-file", "secret-root", "output", "occurrence-id", "target-id", "recovery-set-id", "frontier-digest", "artifact"} {
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
		Ledger: admissionLedgerForReport(t, request),
		Now:    func() time.Time { return verifiedAt.Add(time.Minute) },
		Probe: admissionProbeFunc(func(_ context.Context, actual providerrestore.Report, credentials providerrestore.CredentialBundle) (RecoveryProviderProbeResult, error) {
			if actual.OccurrenceID != report.OccurrenceID || credentials.ControlURL != bundle.ControlURL {
				t.Fatal("probe did not receive the admitted report and credential bundle")
			}
			return RecoveryProviderProbeResult{ControlDatabase: "control", DuckLakeDatabase: "ducklake", ControlDigest: report.Verification.ControlStateDigest, DuckLakeDigest: report.Verification.DuckLakeStateDigest, ObjectCount: 1, TLSVerified: true, VerifiedAt: verifiedAt}, nil
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
	admission := RecoveryAdmission{Ledger: admissionLedgerForReport(t, request), Probe: admissionProbeFunc(func(context.Context, providerrestore.Report, providerrestore.CredentialBundle) (RecoveryProviderProbeResult, error) {
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
	admission := RecoveryAdmission{Ledger: admissionLedgerForReport(t, request), Probe: admissionProbeFunc(func(context.Context, providerrestore.Report, providerrestore.CredentialBundle) (RecoveryProviderProbeResult, error) {
		return RecoveryProviderProbeResult{}, errors.New("provider unreachable")
	})}
	if _, err := admission.Admit(t.Context(), request); err == nil || !strings.Contains(err.Error(), "provider unreachable") {
		t.Fatalf("unreachable provider admission = %v", err)
	}
	if _, err := os.Stat(request.OutputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreachable provider persisted success: %v", err)
	}
}

func TestRecoveryAdmissionRejectsEditedReportAndForeignOccurrence(t *testing.T) {
	report, bundle, request := recoveryAdmissionFixture(t)
	request = persistRecoveryAdmissionFixture(t, report, bundle, request)
	ledger := admissionLedgerForReport(t, request)
	probeCalls := 0
	admission := RecoveryAdmission{Ledger: ledger, Probe: admissionProbeFunc(func(context.Context, providerrestore.Report, providerrestore.CredentialBundle) (RecoveryProviderProbeResult, error) {
		probeCalls++
		return RecoveryProviderProbeResult{}, nil
	})}
	raw, err := os.ReadFile(request.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	var edited providerrestore.Report
	if err := json.Unmarshal(raw, &edited); err != nil {
		t.Fatal(err)
	}
	edited.Verification.ProviderOperationID = "modified-but-schema-valid"
	modified, err := json.Marshal(edited)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.ReportPath, modified, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := admission.Admit(t.Context(), request); err == nil || !strings.Contains(err.Error(), "ledger evidence digest") {
		t.Fatalf("edited report admission = %v", err)
	}
	if err := os.WriteFile(request.ReportPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ledger.occurrence.ID = "foreign-occurrence"
	admission.Ledger = ledger
	if _, err := admission.Admit(t.Context(), request); err == nil {
		t.Fatal("foreign occurrence was admitted")
	}
	ledger.occurrence.ID = request.OccurrenceID
	ledger.occurrence.Evidence[0].SHA256 = strings.Repeat("f", 64)
	admission.Ledger = ledger
	if _, err := admission.Admit(t.Context(), request); err == nil || !strings.Contains(err.Error(), "ledger evidence digest") {
		t.Fatalf("wrong ledger digest admission = %v", err)
	}
	if probeCalls != 0 {
		t.Fatalf("provider was probed %d times before report integrity passed", probeCalls)
	}
}

func TestRecoveryAdmissionRequiresBothLiveDatabaseDigests(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		wrongControl bool
		wrongDuck    bool
	}{
		{name: "both match"},
		{name: "control mismatch", wrongControl: true},
		{name: "ducklake mismatch", wrongDuck: true},
		{name: "both mismatch", wrongControl: true, wrongDuck: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			report, bundle, request := recoveryAdmissionFixture(t)
			request = persistRecoveryAdmissionFixture(t, report, bundle, request)
			controlDigest, duckDigest := report.Verification.ControlStateDigest, report.Verification.DuckLakeStateDigest
			if scenario.wrongControl {
				controlDigest = "sha256:" + strings.Repeat("1", 64)
			}
			if scenario.wrongDuck {
				duckDigest = "sha256:" + strings.Repeat("2", 64)
			}
			admission := RecoveryAdmission{
				Ledger: admissionLedgerForReport(t, request),
				Probe: admissionProbeFunc(func(context.Context, providerrestore.Report, providerrestore.CredentialBundle) (RecoveryProviderProbeResult, error) {
					return RecoveryProviderProbeResult{ControlDatabase: "control", DuckLakeDatabase: "ducklake", ControlDigest: controlDigest, DuckLakeDigest: duckDigest, ObjectCount: 1, TLSVerified: true, VerifiedAt: time.Now().UTC()}, nil
				}),
			}
			_, err := admission.Admit(t.Context(), request)
			if scenario.wrongControl || scenario.wrongDuck {
				if err == nil || !strings.Contains(err.Error(), "live PostgreSQL state") {
					t.Fatalf("incorrect live state admitted: %v", err)
				}
				if _, statErr := os.Stat(request.OutputPath); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("incorrect live state retained success: %v", statErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecoveryAdmissionFailedRetryInvalidatesPriorSuccess(t *testing.T) {
	report, bundle, request := recoveryAdmissionFixture(t)
	request = persistRecoveryAdmissionFixture(t, report, bundle, request)
	calls := 0
	admission := RecoveryAdmission{
		Ledger: admissionLedgerForReport(t, request),
		Probe: admissionProbeFunc(func(context.Context, providerrestore.Report, providerrestore.CredentialBundle) (RecoveryProviderProbeResult, error) {
			calls++
			if calls == 2 {
				return RecoveryProviderProbeResult{}, errors.New("restored provider unavailable")
			}
			return RecoveryProviderProbeResult{ControlDatabase: "control", DuckLakeDatabase: "ducklake", ControlDigest: report.Verification.ControlStateDigest, DuckLakeDigest: report.Verification.DuckLakeStateDigest, ObjectCount: 1, TLSVerified: true, VerifiedAt: time.Now().UTC()}, nil
		}),
	}
	if _, err := admission.Admit(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(request.OutputPath); err != nil {
		t.Fatalf("first successful admission was not durable: %v", err)
	}
	if _, err := admission.Admit(t.Context(), request); err == nil {
		t.Fatal("failed retry unexpectedly succeeded")
	}
	if _, err := os.Stat(request.OutputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed retry retained earlier admitted evidence: %v", err)
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
		Verification: providerrestore.VerificationResult{Ready: true, ObjectsConsistent: true, ControlStateDigest: "sha256:" + strings.Repeat("d", 64), DuckLakeStateDigest: "sha256:" + strings.Repeat("e", 64)},
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

func admissionLedgerForReport(t *testing.T, request RecoveryAdmissionRequest) admissionLedger {
	t.Helper()
	raw, err := os.ReadFile(request.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return admissionLedger{occurrence: recovery.Occurrence{
		ID: request.OccurrenceID, Operation: recovery.OperationRestore, Status: recovery.StatusSucceeded,
		TargetScope: request.TargetID, ArtifactIdentity: request.ArtifactIdentity,
		Evidence: []recovery.EvidenceReference{{Kind: "provider-restore", URI: "file:///trusted/provider-report.json", SHA256: hex.EncodeToString(sum[:])}},
	}}
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
