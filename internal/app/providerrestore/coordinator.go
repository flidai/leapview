// Package providerrestore coordinates physical provider recovery against an
// exact RecoverySet and the PostgreSQL recovery qualification ledger.
package providerrestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/refresh/recovery"
)

const (
	ReportSchemaVersion = 1
	ReportKind          = "leapview/fai981-provider-restore"

	StatusRunning       = "running"
	StatusSucceeded     = "succeeded"
	StatusFailed        = "failed"
	StatusIndeterminate = "indeterminate"
)

var (
	ErrInvalid       = errors.New("provider restore request is invalid")
	ErrInconsistent  = errors.New("provider restore result is inconsistent with recovery set")
	ErrIndeterminate = errors.New("provider restore outcome is indeterminate")
)

type DatabaseRequest struct {
	RecoverySetID  string                           `json:"recoverySetId"`
	TargetID       string                           `json:"targetId"`
	IdempotencyKey string                           `json:"idempotencyKey"`
	Point          recoveryset.ClusterRecoveryPoint `json:"point"`
	Catalog        recoveryset.CatalogCommit        `json:"catalog,omitempty"`
}

type DatabaseResult struct {
	Provider         string                     `json:"provider"`
	OperationID      string                     `json:"operationId"`
	DatabaseRole     recoveryset.DatabaseRole   `json:"databaseRole"`
	ClusterIdentity  string                     `json:"clusterIdentity"`
	DatabaseIdentity string                     `json:"databaseIdentity"`
	RecoveryIdentity string                     `json:"recoveryIdentity"`
	StateDigest      string                     `json:"stateDigest"`
	Catalog          *recoveryset.CatalogCommit `json:"catalog,omitempty"`
	StartedAt        time.Time                  `json:"startedAt"`
	CompletedAt      time.Time                  `json:"completedAt"`
}

type ObjectRequest struct {
	RecoverySetID  string                 `json:"recoverySetId"`
	TargetID       string                 `json:"targetId"`
	IdempotencyKey string                 `json:"idempotencyKey"`
	Root           recoveryset.ObjectRoot `json:"root"`
}

type ObjectResult struct {
	Provider          string    `json:"provider"`
	OperationID       string    `json:"operationId"`
	Kind              string    `json:"kind"`
	URI               string    `json:"uri"`
	RequiredVersionID string    `json:"requiredVersionId"`
	ObservedVersionID string    `json:"observedVersionId"`
	Digest            string    `json:"digest"`
	StartedAt         time.Time `json:"startedAt"`
	CompletedAt       time.Time `json:"completedAt"`
}

type VerificationRequest struct {
	Set       recoveryset.RecoverySet `json:"set"`
	Databases []DatabaseResult        `json:"databases"`
	Objects   []ObjectResult          `json:"objects"`
}

type VerificationResult struct {
	ProviderOperationID string                    `json:"providerOperationId"`
	ControlStateDigest  string                    `json:"controlStateDigest"`
	DuckLakeStateDigest string                    `json:"duckLakeStateDigest"`
	Catalog             recoveryset.CatalogCommit `json:"catalog"`
	ObjectsConsistent   bool                      `json:"objectsConsistent"`
	Ready               bool                      `json:"ready"`
	VerifiedAt          time.Time                 `json:"verifiedAt"`
}

type AdmissionResult struct {
	ValidationAttemptID string             `json:"validationAttemptId"`
	ValidationDigest    string             `json:"validationDigest"`
	PublishedSetID      string             `json:"publishedSetId"`
	PublishedStatus     recoveryset.Status `json:"publishedStatus"`
	PublishedAt         time.Time          `json:"publishedAt"`
}

type Failure struct {
	Code    string `json:"code"`
	Summary string `json:"summary"`
}

type Report struct {
	SchemaVersion  int                `json:"schemaVersion"`
	Kind           string             `json:"kind"`
	Status         string             `json:"status"`
	OccurrenceID   string             `json:"occurrenceId"`
	RecoverySetID  string             `json:"recoverySetId"`
	FrontierDigest string             `json:"frontierDigest"`
	TargetID       string             `json:"targetId"`
	Databases      []DatabaseResult   `json:"databases"`
	Objects        []ObjectResult     `json:"objects"`
	Verification   VerificationResult `json:"verification,omitempty"`
	Admission      AdmissionResult    `json:"admission,omitempty"`
	Failure        *Failure           `json:"failure,omitempty"`
	StartedAt      time.Time          `json:"startedAt"`
	CompletedAt    time.Time          `json:"completedAt,omitempty"`
}

type DatabaseProvider interface {
	RestoreDatabase(context.Context, DatabaseRequest) (DatabaseResult, error)
}

type ObjectProvider interface {
	RestoreObject(context.Context, ObjectRequest) (ObjectResult, error)
}

type Verifier interface {
	Verify(context.Context, VerificationRequest) (VerificationResult, error)
}

type EvidenceStore interface {
	Load(context.Context, string) (Report, bool, error)
	Save(context.Context, Report) (recovery.EvidenceReference, error)
}

type RecoverySetAuthority interface {
	ReadExact(context.Context, string) (recoveryset.RecoverySet, error)
	ValidationResult(context.Context, string) (recoveryset.ValidationResult, error)
	BeginValidation(context.Context, recoveryset.ValidationAttempt) (recoveryset.ValidationAttempt, error)
	RecordValidationResult(context.Context, recoveryset.ValidationResult) error
	CompleteValidation(context.Context, recoveryset.ValidationAttempt) error
	Publish(context.Context, string, string, int64, string) (recoveryset.RecoverySet, error)
}

type Dependencies struct {
	Ledger    recovery.Repository
	Sets      RecoverySetAuthority
	Databases DatabaseProvider
	Objects   ObjectProvider
	Verifier  Verifier
	Evidence  EvidenceStore
	Now       func() time.Time
}

type Coordinator struct{ dependencies Dependencies }

func New(dependencies Dependencies) (*Coordinator, error) {
	if dependencies.Ledger == nil || dependencies.Sets == nil || dependencies.Databases == nil || dependencies.Objects == nil || dependencies.Verifier == nil || dependencies.Evidence == nil {
		return nil, fmt.Errorf("%w: ledger, recovery set, providers, verifier, and evidence store are required", ErrInvalid)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Coordinator{dependencies: dependencies}, nil
}

type Request struct {
	OccurrenceID        string
	Fence               recovery.Fence
	RecoverySetID       string
	TargetID            string
	ValidationAttemptID string
	Validator           string
	Publisher           string
}

func (request Request) validate() error {
	if strings.TrimSpace(request.OccurrenceID) == "" || strings.TrimSpace(request.RecoverySetID) == "" || strings.TrimSpace(request.TargetID) == "" || strings.TrimSpace(request.ValidationAttemptID) == "" || strings.TrimSpace(request.Validator) == "" || strings.TrimSpace(request.Publisher) == "" {
		return fmt.Errorf("%w: occurrence, set, target, validation, validator, and publisher identities are required", ErrInvalid)
	}
	if err := request.Fence.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

func (coordinator *Coordinator) Run(ctx context.Context, request Request) (Report, error) {
	if coordinator == nil {
		return Report{}, ErrInvalid
	}
	if err := request.validate(); err != nil {
		return Report{}, err
	}
	occurrence, err := coordinator.dependencies.Ledger.Occurrence(ctx, request.OccurrenceID)
	if err != nil {
		return Report{}, err
	}
	if err := validateOccurrenceIdentity(occurrence, request); err != nil {
		return Report{}, err
	}
	set, err := coordinator.dependencies.Sets.ReadExact(ctx, request.RecoverySetID)
	if err != nil {
		return Report{}, err
	}
	if set.Delivery.TargetID != request.TargetID || (set.Status != recoveryset.StatusPrepared && set.Status != recoveryset.StatusPublished) {
		return Report{}, fmt.Errorf("%w: recovery set target or status mismatch", ErrInconsistent)
	}
	report, found, err := coordinator.dependencies.Evidence.Load(ctx, request.OccurrenceID)
	if err != nil {
		return Report{}, err
	}
	if found {
		if report.OccurrenceID != request.OccurrenceID || report.RecoverySetID != request.RecoverySetID || report.TargetID != request.TargetID || report.FrontierDigest != set.FrontierDigest {
			return Report{}, fmt.Errorf("%w: durable checkpoint identity mismatch", ErrInconsistent)
		}
		if report.Status != StatusRunning && report.Status != StatusSucceeded {
			return report, fmt.Errorf("%w: prior attempt is terminal with status %s", ErrIndeterminate, report.Status)
		}
		if err := validateCheckpoint(set, request, report); err != nil {
			return report, err
		}
	}
	if occurrence.Status == recovery.StatusSucceeded {
		if !found || report.Status != StatusSucceeded {
			return Report{}, fmt.Errorf("%w: completed occurrence has no matching successful evidence", ErrInconsistent)
		}
		return report, nil
	}
	if err := coordinator.requireActiveOccurrence(occurrence); err != nil {
		return Report{}, err
	}
	if !found {
		report = Report{SchemaVersion: ReportSchemaVersion, Kind: ReportKind, Status: StatusRunning, OccurrenceID: request.OccurrenceID, RecoverySetID: set.ID, FrontierDigest: set.FrontierDigest, TargetID: request.TargetID, StartedAt: coordinator.now()}
		if _, err := coordinator.dependencies.Evidence.Save(ctx, report); err != nil {
			return Report{}, err
		}
	}
	if occurrence.RestoreStartedAt.IsZero() {
		if _, err := coordinator.activeOccurrence(ctx, request); err != nil {
			return report, err
		}
		if err := coordinator.dependencies.Ledger.RecordPhase(ctx, occurrence.ID, request.Fence, "restore", "started", coordinator.now()); err != nil {
			return report, err
		}
	}

	points := set.CanonicalPoints()
	for _, point := range points {
		if hasDatabase(report.Databases, point.DatabaseRole) {
			continue
		}
		if _, err := coordinator.activeOccurrence(ctx, request); err != nil {
			return report, err
		}
		databaseRequest := DatabaseRequest{RecoverySetID: set.ID, TargetID: request.TargetID, IdempotencyKey: providerOperationKey(request.OccurrenceID, "database", string(point.DatabaseRole), point.RecoveryIdentity), Point: point}
		if point.DatabaseRole == recoveryset.DatabaseDuckLake {
			databaseRequest.Catalog = set.Catalog
		}
		result, restoreErr := coordinator.dependencies.Databases.RestoreDatabase(ctx, databaseRequest)
		if restoreErr != nil {
			return coordinator.abort(ctx, request, report, "database_restore_indeterminate", restoreErr, true)
		}
		if _, err := coordinator.activeOccurrence(ctx, request); err != nil {
			return report, fmt.Errorf("%w: database restore completed after lease loss: %v", ErrIndeterminate, err)
		}
		if err := validateDatabaseResult(point, set.Catalog, result); err != nil {
			return coordinator.abort(ctx, request, report, "database_restore_mismatch", err, false)
		}
		report.Databases = append(report.Databases, result)
		if _, err := coordinator.dependencies.Evidence.Save(ctx, report); err != nil {
			return report, fmt.Errorf("%w: persist database restore checkpoint: %v", ErrIndeterminate, err)
		}
	}

	normalized, err := set.Normalize()
	if err != nil {
		return coordinator.abort(ctx, request, report, "recovery_set_invalid", err, false)
	}
	for _, root := range normalized.ObjectRoots {
		if hasObject(report.Objects, root) {
			continue
		}
		if _, err := coordinator.activeOccurrence(ctx, request); err != nil {
			return report, err
		}
		result, restoreErr := coordinator.dependencies.Objects.RestoreObject(ctx, ObjectRequest{RecoverySetID: set.ID, TargetID: request.TargetID, IdempotencyKey: providerOperationKey(request.OccurrenceID, "object", root.Kind, root.URI, root.VersionID), Root: root})
		if restoreErr != nil {
			return coordinator.abort(ctx, request, report, "object_restore_indeterminate", restoreErr, true)
		}
		if _, err := coordinator.activeOccurrence(ctx, request); err != nil {
			return report, fmt.Errorf("%w: object restore completed after lease loss: %v", ErrIndeterminate, err)
		}
		if err := validateObjectResult(root, result); err != nil {
			return coordinator.abort(ctx, request, report, "object_restore_mismatch", err, false)
		}
		report.Objects = append(report.Objects, result)
		if _, err := coordinator.dependencies.Evidence.Save(ctx, report); err != nil {
			return report, fmt.Errorf("%w: persist object restore checkpoint: %v", ErrIndeterminate, err)
		}
	}

	if verificationPresent(report.Verification) {
		if err := validateVerification(normalized, report.Verification); err != nil {
			return coordinator.abort(ctx, request, report, "provider_verification_checkpoint_mismatch", err, false)
		}
	} else {
		if _, err := coordinator.activeOccurrence(ctx, request); err != nil {
			return report, err
		}
		verification, err := coordinator.dependencies.Verifier.Verify(ctx, VerificationRequest{Set: normalized, Databases: slices.Clone(report.Databases), Objects: slices.Clone(report.Objects)})
		if err != nil {
			return coordinator.abort(ctx, request, report, "provider_verification_failed", err, false)
		}
		if _, err := coordinator.activeOccurrence(ctx, request); err != nil {
			return report, fmt.Errorf("%w: provider verification completed after lease loss: %v", ErrIndeterminate, err)
		}
		if err := validateVerification(normalized, verification); err != nil {
			return coordinator.abort(ctx, request, report, "provider_verification_mismatch", err, false)
		}
		report.Verification = verification
		if _, err := coordinator.dependencies.Evidence.Save(ctx, report); err != nil {
			return report, fmt.Errorf("%w: persist verification checkpoint: %v", ErrIndeterminate, err)
		}
	}

	if admissionPresent(report.Admission) {
		if !admissionComplete(normalized, request, report.Admission) {
			return coordinator.abort(ctx, request, report, "recovery_set_admission_checkpoint_mismatch", ErrInconsistent, false)
		}
	} else {
		if _, err := coordinator.activeOccurrence(ctx, request); err != nil {
			return report, err
		}
		admission, err := coordinator.admit(ctx, normalized, request)
		if err != nil {
			return coordinator.abort(ctx, request, report, "recovery_set_admission_failed", err, false)
		}
		if _, err := coordinator.activeOccurrence(ctx, request); err != nil {
			return report, fmt.Errorf("%w: recovery-set admission completed after lease loss: %v", ErrIndeterminate, err)
		}
		report.Admission = admission
	}
	report.Status = StatusSucceeded
	if report.CompletedAt.IsZero() {
		report.CompletedAt = coordinator.now()
	}
	reference, err := coordinator.dependencies.Evidence.Save(ctx, report)
	if err != nil {
		return report, fmt.Errorf("%w: persist completed provider evidence: %v", ErrIndeterminate, err)
	}
	occurrence, err = coordinator.activeOccurrence(ctx, request)
	if err != nil {
		return report, err
	}
	if occurrence.RestoreCompletedAt.IsZero() {
		if err := coordinator.dependencies.Ledger.RecordPhase(ctx, occurrence.ID, request.Fence, "restore", "completed", report.CompletedAt); err != nil {
			return report, fmt.Errorf("%w: record restore completion: %v", ErrIndeterminate, err)
		}
	}
	if _, err := coordinator.activeOccurrence(ctx, request); err != nil {
		return report, err
	}
	if err := coordinator.dependencies.Ledger.Complete(ctx, occurrence.ID, request.Fence, report.CompletedAt, recovery.Result{RecoveryPointAt: occurrence.PlannedAt, Evidence: []recovery.EvidenceReference{reference}}); err != nil {
		return report, fmt.Errorf("%w: complete durable recovery occurrence: %v", ErrIndeterminate, err)
	}
	return report, nil
}

func (coordinator *Coordinator) admit(ctx context.Context, set recoveryset.RecoverySet, request Request) (AdmissionResult, error) {
	if set.Status == recoveryset.StatusPublished {
		if set.PublishedValidationAttemptID != request.ValidationAttemptID {
			return AdmissionResult{}, ErrInconsistent
		}
		result, err := coordinator.dependencies.Sets.ValidationResult(ctx, request.ValidationAttemptID)
		if err != nil {
			return AdmissionResult{}, err
		}
		return AdmissionResult{ValidationAttemptID: request.ValidationAttemptID, ValidationDigest: result.ResultDigest, PublishedSetID: set.ID, PublishedStatus: set.Status, PublishedAt: coordinator.now()}, nil
	}
	started := coordinator.now()
	attempt, err := coordinator.dependencies.Sets.BeginValidation(ctx, recoveryset.ValidationAttempt{AttemptID: request.ValidationAttemptID, SetID: set.ID, OwnerID: request.Validator, FenceEpoch: set.FenceEpoch, AuditIdentity: set.AuditIdentity, Status: recoveryset.ValidationRunning, StartedAt: started})
	if err != nil {
		return AdmissionResult{}, err
	}
	envelope, err := recoveryset.NewValidationEvidenceEnvelope(set, request.ValidationAttemptID)
	if err != nil {
		return AdmissionResult{}, err
	}
	result, err := recoveryset.NewValidationResult(envelope, coordinator.now())
	if err != nil {
		return AdmissionResult{}, err
	}
	if err := coordinator.dependencies.Sets.RecordValidationResult(ctx, result); err != nil {
		return AdmissionResult{}, err
	}
	attempt.Status = recoveryset.ValidationPassed
	attempt.ResultDigest = result.ResultDigest
	attempt.CompletedAt = coordinator.now()
	if err := coordinator.dependencies.Sets.CompleteValidation(ctx, attempt); err != nil {
		return AdmissionResult{}, err
	}
	published, err := coordinator.dependencies.Sets.Publish(ctx, set.ID, request.Publisher, set.FenceEpoch, request.ValidationAttemptID)
	if err != nil {
		return AdmissionResult{}, err
	}
	return AdmissionResult{ValidationAttemptID: request.ValidationAttemptID, ValidationDigest: result.ResultDigest, PublishedSetID: published.ID, PublishedStatus: published.Status, PublishedAt: coordinator.now()}, nil
}

func (coordinator *Coordinator) abort(ctx context.Context, request Request, report Report, code string, cause error, indeterminate bool) (Report, error) {
	report.Status = StatusFailed
	if indeterminate {
		report.Status = StatusIndeterminate
	}
	report.CompletedAt = coordinator.now()
	report.Failure = &Failure{Code: code, Summary: recovery.RedactFailure(cause)}
	reference, saveErr := coordinator.dependencies.Evidence.Save(ctx, report)
	result := recovery.Result{}
	if saveErr == nil {
		result.Evidence = []recovery.EvidenceReference{reference}
	}
	failure := recovery.NewFailure(code, report.Failure.Summary)
	ledgerErr := coordinator.dependencies.Ledger.Fail(ctx, request.OccurrenceID, request.Fence, report.CompletedAt, result, failure)
	joined := errors.Join(cause, saveErr, ledgerErr)
	if indeterminate {
		joined = errors.Join(ErrIndeterminate, joined)
	}
	return report, joined
}

func (coordinator *Coordinator) now() time.Time {
	return coordinator.dependencies.Now().UTC().Truncate(time.Microsecond)
}

func (coordinator *Coordinator) activeOccurrence(ctx context.Context, request Request) (recovery.Occurrence, error) {
	occurrence, err := coordinator.dependencies.Ledger.Occurrence(ctx, request.OccurrenceID)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	if err := validateOccurrenceIdentity(occurrence, request); err != nil {
		return recovery.Occurrence{}, err
	}
	if err := coordinator.requireActiveOccurrence(occurrence); err != nil {
		return recovery.Occurrence{}, err
	}
	return occurrence, nil
}

func validateOccurrenceIdentity(occurrence recovery.Occurrence, request Request) error {
	if occurrence.ID != request.OccurrenceID || occurrence.Operation != recovery.OperationRestore || occurrence.TargetScope != request.TargetID || occurrence.Fence != request.Fence {
		return fmt.Errorf("%w: occurrence identity, target, or fence mismatch", ErrInvalid)
	}
	return nil
}

func (coordinator *Coordinator) requireActiveOccurrence(occurrence recovery.Occurrence) error {
	if occurrence.Status != recovery.StatusRunning {
		return fmt.Errorf("%w: occurrence is not running", ErrInvalid)
	}
	if !occurrence.LeaseExpiresAt.IsZero() && !coordinator.now().Before(occurrence.LeaseExpiresAt) {
		return fmt.Errorf("%w: occurrence lease expired", recovery.ErrFenced)
	}
	return nil
}

func providerOperationKey(values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return "fai981-" + hex.EncodeToString(sum[:])
}

func hasDatabase(results []DatabaseResult, role recoveryset.DatabaseRole) bool {
	return slices.ContainsFunc(results, func(result DatabaseResult) bool { return result.DatabaseRole == role })
}

func hasObject(results []ObjectResult, root recoveryset.ObjectRoot) bool {
	return slices.ContainsFunc(results, func(result ObjectResult) bool {
		return result.Kind == root.Kind && result.URI == root.URI && result.RequiredVersionID == root.VersionID && result.Digest == root.Digest
	})
}

func validateDatabaseResult(point recoveryset.ClusterRecoveryPoint, catalog recoveryset.CatalogCommit, result DatabaseResult) error {
	if result.Provider == "" || result.OperationID == "" || result.StateDigest == "" || result.StartedAt.IsZero() || result.CompletedAt.Before(result.StartedAt) || result.DatabaseRole != point.DatabaseRole || result.ClusterIdentity != point.ClusterIdentity || result.DatabaseIdentity != point.DatabaseIdentity || result.RecoveryIdentity != point.RecoveryIdentity {
		return fmt.Errorf("%w: database provider result does not match exact recovery point", ErrInconsistent)
	}
	if point.DatabaseRole == recoveryset.DatabaseDuckLake {
		if result.Catalog == nil || *result.Catalog != catalog {
			return fmt.Errorf("%w: DuckLake catalog result does not match exact catalog commit", ErrInconsistent)
		}
	}
	return nil
}

func validateObjectResult(root recoveryset.ObjectRoot, result ObjectResult) error {
	if result.Provider == "" || result.OperationID == "" || result.StartedAt.IsZero() || result.CompletedAt.Before(result.StartedAt) || result.Kind != root.Kind || result.URI != root.URI || result.RequiredVersionID != root.VersionID || result.ObservedVersionID != root.VersionID || result.Digest != root.Digest {
		return fmt.Errorf("%w: object provider result does not match exact root/version", ErrInconsistent)
	}
	return nil
}

func validateVerification(set recoveryset.RecoverySet, result VerificationResult) error {
	if result.ProviderOperationID == "" || result.ControlStateDigest == "" || result.DuckLakeStateDigest == "" || result.VerifiedAt.IsZero() || !result.ObjectsConsistent || !result.Ready || result.Catalog != set.Catalog {
		return fmt.Errorf("%w: coordinated post-restore verification is incomplete", ErrInconsistent)
	}
	return nil
}

func verificationPresent(result VerificationResult) bool {
	return result.ProviderOperationID != "" || result.ControlStateDigest != "" || result.DuckLakeStateDigest != "" || !result.VerifiedAt.IsZero()
}

func admissionPresent(result AdmissionResult) bool {
	return result.ValidationAttemptID != "" || result.ValidationDigest != "" || result.PublishedSetID != "" || !result.PublishedAt.IsZero()
}

func admissionComplete(set recoveryset.RecoverySet, request Request, result AdmissionResult) bool {
	return result.ValidationAttemptID == request.ValidationAttemptID && result.ValidationDigest != "" && result.PublishedSetID == set.ID && result.PublishedStatus == recoveryset.StatusPublished && !result.PublishedAt.IsZero()
}

func validateCheckpoint(set recoveryset.RecoverySet, request Request, report Report) error {
	if report.SchemaVersion != ReportSchemaVersion || report.Kind != ReportKind || report.StartedAt.IsZero() || len(report.Databases) > 2 || len(report.Objects) > len(set.ObjectRoots) {
		return fmt.Errorf("%w: durable provider checkpoint shape is invalid", ErrInconsistent)
	}
	points := set.CanonicalPoints()
	for index, result := range report.Databases {
		if index >= len(points) || result.DatabaseRole != points[index].DatabaseRole {
			return fmt.Errorf("%w: durable database checkpoints are not in canonical order", ErrInconsistent)
		}
		if err := validateDatabaseResult(points[index], set.Catalog, result); err != nil {
			return err
		}
	}
	normalized, err := set.Normalize()
	if err != nil {
		return err
	}
	for index, result := range report.Objects {
		if index >= len(normalized.ObjectRoots) {
			return fmt.Errorf("%w: durable object checkpoint count is invalid", ErrInconsistent)
		}
		if err := validateObjectResult(normalized.ObjectRoots[index], result); err != nil {
			return err
		}
	}
	if verificationPresent(report.Verification) {
		if len(report.Databases) != len(points) || len(report.Objects) != len(normalized.ObjectRoots) {
			return fmt.Errorf("%w: verification checkpoint precedes provider completion", ErrInconsistent)
		}
		if err := validateVerification(normalized, report.Verification); err != nil {
			return err
		}
	}
	if admissionPresent(report.Admission) && !admissionComplete(normalized, request, report.Admission) {
		return fmt.Errorf("%w: durable admission checkpoint is inconsistent", ErrInconsistent)
	}
	if report.Status == StatusSucceeded && (!verificationPresent(report.Verification) || !admissionPresent(report.Admission) || report.CompletedAt.IsZero()) {
		return fmt.Errorf("%w: successful checkpoint is incomplete", ErrInconsistent)
	}
	return nil
}
