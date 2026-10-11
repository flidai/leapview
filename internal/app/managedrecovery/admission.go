package managedrecovery

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"reflect"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/refresh/recovery"
	"github.com/flidai/leapview/pkg/strictjson"
)

// ManagedAdmissionEvidence admits only the stopped, verified recovery frontier.
// It grants neither application activation nor public traffic authority.
type ManagedAdmissionEvidence struct {
	SchemaVersion       int       `json:"schemaVersion"`
	Kind                string    `json:"kind"`
	Status              string    `json:"status"`
	OccurrenceID        string    `json:"occurrenceId"`
	RecoverySetID       string    `json:"recoverySetId"`
	TargetID            string    `json:"targetId"`
	FrontierDigest      string    `json:"frontierDigest"`
	ArtifactIdentity    string    `json:"artifactIdentity"`
	ValidationAttemptID string    `json:"validationAttemptId"`
	ValidationDigest    string    `json:"validationDigest"`
	ReportSHA256        string    `json:"reportSha256"`
	OriginalsFenced     bool      `json:"originalsFenced"`
	ActivationQualified bool      `json:"activationQualified"`
	VerifiedAt          time.Time `json:"verifiedAt"`
}

type managedAdmissionSnapshot struct {
	set        recoveryset.RecoverySet
	occurrence recovery.Occurrence
	report     providerrestore.Report
}

// AdmitManagedRecovery consumes only the completed managed-local profile. It
// rechecks the stopped restored providers under the original fence without
// recovering missing files, changing credentials, or starting public service.
// Its caller must hold exclusive ownership of the enrolled instance home.
func AdmitManagedRecovery(ctx context.Context, config ManagedConfig, authority ManagedAuthorities) (ManagedAdmissionEvidence, error) {
	snapshot, err := readManagedAdmission(ctx, config, authority)
	if err != nil {
		return ManagedAdmissionEvidence{}, err
	}
	credentials, err := (ManagedSecretStore{Root: config.SecretRoot}).Load(ctx, snapshot.report.Handoff.Secrets)
	if err != nil || credentials != config.Credentials || credentials.ValidateForHandoff(snapshot.report.Handoff, config.Roles) != nil {
		return ManagedAdmissionEvidence{}, errors.New("managed admission requires the exact existing retained credentials")
	}
	for _, restored := range snapshot.report.Handoff.ManagedLocal.Roots {
		matches := 0
		for _, input := range config.Roots {
			if input.Root == restored.Root && input.StorageRoot == restored.StorageRoot && input.Destination == restored.Destination && input.ManifestDigest == restored.ContentManifestDigest && reflect.DeepEqual(input.ArtifactMetadata, restored.ArtifactMetadata) {
				matches++
			}
		}
		if matches != 1 {
			return ManagedAdmissionEvidence{}, errors.New("managed admission file manifests differ from the completed handoff")
		}
	}
	if config.PostgresProvider != "module-owned" && validatePrivateSecretRoot(config.Postgres.Destination) != nil {
		return ManagedAdmissionEvidence{}, errors.New("managed admission requires the existing private restored PostgreSQL directory")
	}
	composition, err := prepareManagedComposition(ctx, config, authority)
	if err != nil {
		return ManagedAdmissionEvidence{}, err
	}
	// Provider verification may read the existing immutable restore intent and
	// launch private PostgreSQL. The module adapter uses check-only admission:
	// its authenticated completed receipt forbids a missing-data provider restore.
	return verifyManagedAdmission(ctx, config, authority, snapshot, composition.fence, composition.components)
}

func readManagedAdmission(ctx context.Context, config ManagedConfig, authority ManagedAuthorities) (managedAdmissionSnapshot, error) {
	if typednil.IsNil(authority.Ledger) || typednil.IsNil(authority.Sets) || config.OccurrenceID == "" || config.RecoverySetID == "" {
		return managedAdmissionSnapshot{}, errors.New("managed admission requires exact durable authorities")
	}
	occurrence, err := authority.Ledger.Occurrence(ctx, config.OccurrenceID)
	if err != nil || occurrence.ID != config.OccurrenceID || occurrence.Status != recovery.StatusSucceeded || occurrence.Operation != recovery.OperationRestore || occurrence.TargetScope != config.Credentials.TargetID || occurrence.ArtifactIdentity != config.Artifact.Image || len(occurrence.Evidence) != 1 || occurrence.Evidence[0].Kind != "provider-restore" {
		return managedAdmissionSnapshot{}, errors.New("managed admission requires the exact succeeded restore occurrence")
	}
	set, err := authority.Sets.ReadExact(ctx, config.RecoverySetID)
	if err != nil || set.Validate() != nil || set.Status != recoveryset.StatusPublished || set.PublishedValidationAttemptID == "" {
		return managedAdmissionSnapshot{}, errors.New("managed admission requires the exact published recovery set")
	}
	if err := VerifyManagedEnrollment(config.Enrollment, set, occurrence, config.InstanceHome); err != nil || authority.AuthoritySystemIdentifier != config.Enrollment.Request.AuthoritySystemID {
		return managedAdmissionSnapshot{}, errors.New("managed admission differs from the enrolled independent authority")
	}
	// Do not let a digest-valid link substitute a private retained report path.
	reference := occurrence.Evidence[0]
	if validatePrivateSecretRoot(config.EvidenceRoot) != nil {
		return managedAdmissionSnapshot{}, errors.New("managed admission requires a private retained evidence directory")
	}
	path := filepath.Join(config.EvidenceRoot, reference.SHA256+".json")
	canonical, err := recovery.CanonicalEvidenceReferences([]recovery.EvidenceReference{reference})
	if err != nil || len(canonical) != 1 || reference.URI != (&url.URL{Scheme: "file", Path: path}).String() {
		return managedAdmissionSnapshot{}, errors.New("managed admission evidence reference differs from its private digest path")
	}
	raw, err := readBoundedManagedPrivateFile(path, 16<<20)
	var report providerrestore.Report
	if err != nil || digestBytes(raw) != "sha256:"+reference.SHA256 || strictjson.DecodeWithOptions(raw, &report, strictjson.Options{MaxBytes: 16 << 20}) != nil || report.SchemaVersion != providerrestore.ReportSchemaVersion || report.Kind != providerrestore.ReportKind {
		return managedAdmissionSnapshot{}, errors.New("managed admission report does not match the durable evidence digest")
	}
	expected := providerrestore.HandoffExpectations{OccurrenceID: config.OccurrenceID, TargetID: config.Credentials.TargetID, RecoverySetID: config.RecoverySetID, FrontierDigest: set.FrontierDigest, ArtifactIdentity: config.Artifact.Image}
	if providerrestore.ValidateManagedLocalHandoffReport(report, set, expected) != nil || report.Fence.Generation != occurrence.Fence.Generation || !report.Verification.Ready || !report.Verification.ObjectsConsistent || report.Verification.Catalog != set.Catalog || report.Admission.PublishedSetID != set.ID || report.Admission.PublishedStatus != recoveryset.StatusPublished || report.Admission.PublishedAt.IsZero() || report.Admission.ValidationAttemptID != set.PublishedValidationAttemptID {
		return managedAdmissionSnapshot{}, errors.New("managed admission requires exact successful local provider evidence")
	}
	result, err := authority.Sets.ValidationResult(ctx, set.PublishedValidationAttemptID)
	if err != nil || result.AttemptID != set.PublishedValidationAttemptID || !validContentDigest(result.ResultDigest) || result.ResultDigest != report.Admission.ValidationDigest || config.OccurrenceID != report.OccurrenceID {
		return managedAdmissionSnapshot{}, errors.New("managed admission validation differs from the published attempt")
	}
	envelope, err := recoveryset.ParseValidationEvidenceEnvelope(result.Evidence)
	if err != nil || envelope.ValidateFor(set, result.AttemptID) != nil {
		return managedAdmissionSnapshot{}, errors.New("managed admission validation does not bind the exact frontier")
	}
	canonicalResult, err := recoveryset.NewValidationResult(envelope, result.RecordedAt)
	if err != nil || canonicalResult.ResultDigest != result.ResultDigest {
		return managedAdmissionSnapshot{}, errors.New("managed admission validation digest differs from its evidence")
	}
	return managedAdmissionSnapshot{set: set, occurrence: occurrence, report: report}, nil
}

func verifyManagedAdmission(ctx context.Context, config ManagedConfig, authority ManagedAuthorities, snapshot managedAdmissionSnapshot, fence providerrestore.PrimaryFence, verifier providerrestore.Verifier) (ManagedAdmissionEvidence, error) {
	if typednil.IsNil(fence) || typednil.IsNil(verifier) || fence.Verify(ctx, snapshot.set) != nil {
		return ManagedAdmissionEvidence{}, errors.New("managed admission requires freshly observed original-writer fencing")
	}
	observed, err := verifier.Verify(ctx, providerrestore.VerificationRequest{Set: snapshot.set, Databases: snapshot.report.Databases, Objects: snapshot.report.Objects})
	expected := snapshot.report.Verification
	if err != nil || !observed.Ready || !observed.ObjectsConsistent || observed.ProviderOperationID != expected.ProviderOperationID || observed.ControlStateDigest != expected.ControlStateDigest || observed.DuckLakeStateDigest != expected.DuckLakeStateDigest || observed.Catalog != expected.Catalog {
		return ManagedAdmissionEvidence{}, errors.New("managed admission restored providers changed after recovery")
	}
	if fence.Verify(ctx, snapshot.set) != nil {
		return ManagedAdmissionEvidence{}, errors.New("managed admission lost original-writer fencing during verification")
	}
	current, err := readManagedAdmission(ctx, config, authority)
	if err != nil || !reflect.DeepEqual(current, snapshot) {
		return ManagedAdmissionEvidence{}, errors.New("managed admission durable frontier changed during verification")
	}
	return ManagedAdmissionEvidence{SchemaVersion: 1, Kind: "leapview/managed-local-recovery-admission", Status: "verified-before-activation", OccurrenceID: config.OccurrenceID, RecoverySetID: snapshot.set.ID, TargetID: snapshot.set.Delivery.TargetID, FrontierDigest: snapshot.set.FrontierDigest, ArtifactIdentity: config.Artifact.Image, ValidationAttemptID: snapshot.report.Admission.ValidationAttemptID, ValidationDigest: snapshot.report.Admission.ValidationDigest, ReportSHA256: snapshot.occurrence.Evidence[0].SHA256, OriginalsFenced: true, VerifiedAt: time.Now().UTC()}, nil
}
