package managedrecovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/platform/objectstore"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/refresh/recovery"
)

type admissionSets struct {
	providerrestore.RecoverySetAuthority
	set    recoveryset.RecoverySet
	result recoveryset.ValidationResult
}

func (s *admissionSets) ReadExact(context.Context, string) (recoveryset.RecoverySet, error) {
	return s.set, nil
}
func (s *admissionSets) ValidationResult(context.Context, string) (recoveryset.ValidationResult, error) {
	return s.result, nil
}

type admissionLedger struct {
	recovery.Repository
	occurrence recovery.Occurrence
}

func (l *admissionLedger) Occurrence(context.Context, string) (recovery.Occurrence, error) {
	return l.occurrence, nil
}

type admissionFence struct {
	calls  int
	failAt int
}

func (f *admissionFence) Verify(context.Context, recoveryset.RecoverySet) error {
	f.calls++
	if f.calls == f.failAt {
		return os.ErrPermission
	}
	return nil
}

type admissionVerifier struct {
	value providerrestore.VerificationResult
	run   func()
	err   error
}

func (v admissionVerifier) Verify(context.Context, providerrestore.VerificationRequest) (providerrestore.VerificationResult, error) {
	if v.run != nil {
		v.run()
	}
	return v.value, v.err
}

// This emits real digest-addressed managed-local handoff/secret documents and
// the canonical validation envelope. Provider probes are separate test ports;
// this fixture never claims installed-host or native-provider qualification.
func managedAdmissionFixture(t *testing.T) (ManagedConfig, ManagedAuthorities, *admissionSets, *admissionLedger) {
	t.Helper()
	raw, err := os.ReadFile("../../recoveryset/testdata/successor-legacy/recoveryset-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	set, err := recoveryset.ParseRecoverySet(raw)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	set.Serving.ObjectRoot = filepath.Join(home, "data")
	set.Serving.ObjectRootDigest = digestBytes([]byte(set.Serving.ObjectRoot))
	set.Serving.ArtifactRoot = "serving-artifacts/" + strings.TrimPrefix(set.Serving.ArtifactRootDigest, "sha256:") + ".tar.gz"
	set.ObjectRoots = []recoveryset.ObjectRoot{
		{Kind: recoveryset.ObjectRootDuckLake, URI: set.Serving.ObjectRoot, Digest: set.Serving.ObjectRootDigest, VersionID: strings.Repeat("a", 64), ProviderRecoveryFrontier: "restic:" + strings.Repeat("a", 64)},
		{Kind: recoveryset.ObjectRootServingArtifact, URI: set.Serving.ArtifactRoot, Digest: set.Serving.ArtifactRootDigest, VersionID: strings.Repeat("b", 64), ProviderRecoveryFrontier: "restic:" + strings.Repeat("b", 64)},
	}
	set, err = set.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	set.FrontierDigest, err = set.Digest()
	if err != nil {
		t.Fatal(err)
	}
	artifact := compatibility.ReleaseIdentity{Image: "ghcr.io/flidai/leapview@" + digestBytes([]byte("managed-admission-artifact")), SourceRevision: "0123456789abcdef0123456789abcdef01234567", Version: "test", Distribution: "oci", Platform: "linux/amd64"}
	now := time.Now().UTC().Truncate(time.Microsecond)
	request := ManagedEnrollmentRequest{InstanceHome: home, RecoverySetID: set.ID, FrontierDigest: set.FrontierDigest, RetentionRootID: "018f3f83-7b2f-7b37-9f9e-000000000099", SourceSystemID: "1", AuthoritySystemID: "2", ArtifactIdentity: artifact.Image, Actor: "operator", PlannedAt: now, ExpiresAt: now.Add(time.Hour)}
	canonical, err := set.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := managedEnrollmentPolicy(request, canonical)
	if err != nil {
		t.Fatal(err)
	}
	intent := managedEnrollmentIntent(request, set, policy)
	id, err := recovery.OccurrenceID(intent)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := recovery.ScheduleRevisionForInput(intent)
	if err != nil {
		t.Fatal(err)
	}
	ledger := &admissionLedger{occurrence: recovery.Occurrence{ID: id, Status: recovery.StatusSucceeded, Operation: intent.Operation, TargetScope: intent.TargetScope, ArtifactIdentity: intent.ArtifactIdentity, PolicySHA256: policy, ScheduleID: intent.ScheduleID, ScheduleRevision: revision, Scenario: intent.Scenario, PolicyVersion: intent.PolicyVersion, PlannedAt: request.PlannedAt, ExpiresAt: request.ExpiresAt, Fence: recovery.Fence{Generation: 1}}}
	_, credentials, roles := managedCredentialFixture(t)
	credentials.TargetID, credentials.RecoverySetID, credentials.OccurrenceID = set.Delivery.TargetID, set.ID, id
	credentials.ControlURL = strings.Replace(credentials.ControlURL, "/control?", "/control-db?", 1)
	credentials.DuckLakeURL = strings.Replace(credentials.DuckLakeURL, "/ducklake?", "/ducklake-db?", 1)
	providers, err := managedPostgresEndpoints(credentials, set)
	if err != nil {
		t.Fatal(err)
	}
	handoff := providerrestore.ReplacementHandoff{SchemaVersion: providerrestore.ManagedLocalHandoffSchemaVersion, Kind: providerrestore.ManagedLocalHandoffKind, Status: providerrestore.HandoffAvailable, RecoverySetID: set.ID, FrontierDigest: set.FrontierDigest, TargetID: set.Delivery.TargetID, Artifact: artifact, Providers: providers, AvailableAt: now, ManagedLocal: &providerrestore.ManagedLocalHandoff{Profile: providerrestore.ManagedLocalProfile, OccurrenceID: id}}
	for _, root := range set.ObjectRoots {
		entry := providerrestore.ManagedLocalRoot{Root: root, Destination: root.URI, ContentManifestDigest: digestBytes([]byte("manifest"))}
		if root.Kind == recoveryset.ObjectRootServingArtifact {
			entry.StorageRoot = filepath.Join(home, "artifacts")
			entry.Destination, err = providerrestore.ManagedLocalRootPath(root, entry.StorageRoot)
			if err != nil {
				t.Fatal(err)
			}
			entry.ArtifactMetadata = &objectstore.ObjectMetadata{StorageSecurityDomain: "test-domain", Digest: root.Digest, SizeBytes: 1, ContentType: "application/gzip", MetadataDigest: digestBytes([]byte("metadata"))}
		}
		handoff.ManagedLocal.Roots = append(handoff.ManagedLocal.Roots, entry)
	}
	secretRoot, evidenceRoot := filepath.Join(home, "secrets"), filepath.Join(home, "evidence")
	if err := os.Mkdir(secretRoot, 0700); err != nil {
		t.Fatal(err)
	}
	handoff.Secrets, err = (ManagedSecretStore{Root: secretRoot}).Save(t.Context(), handoff, roles, credentials)
	if err != nil {
		t.Fatal(err)
	}
	attempt := "018f3f83-7b2f-7b37-9f9e-000000000088"
	envelope, err := recoveryset.NewValidationEvidenceEnvelope(set, attempt)
	if err != nil {
		t.Fatal(err)
	}
	result, err := recoveryset.NewValidationResult(envelope, now)
	if err != nil {
		t.Fatal(err)
	}
	report := providerrestore.Report{SchemaVersion: providerrestore.ReportSchemaVersion, Kind: providerrestore.ReportKind, Status: providerrestore.StatusSucceeded, OccurrenceID: id, RecoverySetID: set.ID, TargetID: set.Delivery.TargetID, FrontierDigest: set.FrontierDigest, Fence: ledger.occurrence.Fence, Handoff: handoff, Verification: providerrestore.VerificationResult{ProviderOperationID: "operation", ControlStateDigest: digestBytes([]byte("control")), DuckLakeStateDigest: digestBytes([]byte("duck")), Catalog: set.Catalog, Ready: true, ObjectsConsistent: true, VerifiedAt: now}, Admission: providerrestore.AdmissionResult{ValidationAttemptID: attempt, ValidationDigest: result.ResultDigest, PublishedSetID: set.ID, PublishedStatus: recoveryset.StatusPublished, PublishedAt: now}}
	for _, point := range set.ClusterPoints {
		report.Databases = append(report.Databases, providerrestore.DatabaseResult{Provider: "pgbackrest-managed", OperationID: "operation", DatabaseRole: point.DatabaseRole, DatabaseIdentity: point.DatabaseIdentity})
	}
	for _, root := range set.ObjectRoots {
		report.Objects = append(report.Objects, providerrestore.ObjectResult{Provider: "restic-managed-local", OperationID: "operation", Kind: root.Kind, URI: root.URI, RequiredVersionID: root.VersionID, ObservedVersionID: root.VersionID, Digest: root.Digest})
	}
	if err := providerrestore.ValidateManagedLocalHandoffReport(report, set, providerrestore.HandoffExpectations{OccurrenceID: id, TargetID: set.Delivery.TargetID, RecoverySetID: set.ID, FrontierDigest: set.FrontierDigest, ArtifactIdentity: artifact.Image}); err != nil {
		t.Fatal(err)
	}
	reference, err := (providerrestore.FileEvidenceStore{Root: evidenceRoot}).Save(t.Context(), report)
	if err != nil {
		t.Fatal(err)
	}
	ledger.occurrence.Evidence = []recovery.EvidenceReference{reference}
	set.Status, set.PublishedValidationAttemptID = recoveryset.StatusPublished, attempt
	sets := &admissionSets{set: set, result: result}
	config := ManagedConfig{InstanceHome: home, RecoverySetID: set.ID, OccurrenceID: id, Artifact: artifact, Credentials: credentials, Roles: roles, SecretRoot: secretRoot, EvidenceRoot: evidenceRoot, Enrollment: ManagedEnrollmentReceipt{SchemaVersion: 1, Status: "prepared", Request: request, CanonicalSetSHA256: digestBytes(canonical), OccurrenceID: id, PolicySHA256: policy}}
	return config, ManagedAuthorities{AuthoritySystemIdentifier: "2", Ledger: ledger, Sets: sets}, sets, ledger
}
