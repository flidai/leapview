// Package recovery owns the durable schedule and evidence contract for
// recovery qualification. Scenario execution remains with the backup,
// restore, upgrade, and rollback owners; this package records and fences it.
package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/safetext"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

const (
	StatusPending   = "pending"
	StatusClaimed   = "claimed"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"
	StatusExpired   = "expired"

	EvidencePending   = "pending"
	EvidenceClaimed   = "claimed"
	EvidencePublished = "published"
	EvidenceFailed    = "failed"

	OperationBackup   = "backup"
	OperationRestore  = "restore"
	OperationUpgrade  = "upgrade"
	OperationRollback = "rollback"

	maxEvidenceReferences = 16
	maxFailureReasonBytes = 512
)

var (
	ErrConflict = errors.New("recovery qualification occurrence conflicts with durable identity")
	ErrFenced   = errors.New("recovery qualification lease is stale or fenced")

	canonicalValuePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$`)
	immutableDigestPattern = regexp.MustCompile(`sha256:[0-9a-f]{64}$`)
	failureCodePattern     = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)
)

type Definition struct {
	ScheduleID       string        `json:"scheduleId"`
	Scenario         string        `json:"scenario"`
	Operation        string        `json:"operation"`
	PolicyVersion    string        `json:"policyVersion"`
	TargetScope      string        `json:"targetScope"`
	ArtifactIdentity string        `json:"artifactIdentity"`
	Cron             string        `json:"cron"`
	Timezone         string        `json:"timezone"`
	StaleAfter       time.Duration `json:"-"`
	Enabled          bool          `json:"enabled"`
}

func (definition Definition) Validate() error {
	for label, value := range map[string]string{
		"schedule id": definition.ScheduleID, "scenario": definition.Scenario,
		"policy version": definition.PolicyVersion, "target scope": definition.TargetScope,
	} {
		if err := validateCanonical(label, value, 256); err != nil {
			return err
		}
	}
	if !validOperation(definition.Operation) {
		return fmt.Errorf("recovery qualification operation must be backup, restore, upgrade, or rollback")
	}
	if err := ValidateArtifactIdentity(definition.ArtifactIdentity); err != nil {
		return err
	}
	if _, err := refreshschedule.ParseSchedule(definition.Cron, definition.Timezone); err != nil {
		return fmt.Errorf("recovery qualification schedule: %w", err)
	}
	if definition.StaleAfter < time.Second || definition.StaleAfter > 366*24*time.Hour || definition.StaleAfter%time.Second != 0 {
		return fmt.Errorf("recovery qualification stale-after must use whole seconds between one second and 366 days")
	}
	return nil
}

type EnqueueInput struct {
	ScheduleID       string
	ScheduleRevision string
	Scenario         string
	Operation        string
	PolicyVersion    string
	TargetScope      string
	ArtifactIdentity string
	PlannedAt        time.Time
	StaleAfter       time.Duration
}

// ScheduleRevisionID binds immutable schedule intent without treating enable
// and disable operations as a new qualification definition.
func ScheduleRevisionID(definition Definition) (string, error) {
	if err := definition.Validate(); err != nil {
		return "", err
	}
	value := strings.Join([]string{
		definition.ScheduleID, definition.Scenario, definition.Operation,
		definition.PolicyVersion, definition.TargetScope, definition.ArtifactIdentity,
		definition.Cron, definition.Timezone, definition.StaleAfter.String(),
	}, "\x00")
	sum := sha256.Sum256([]byte(value))
	return "recovery-schedule-" + hex.EncodeToString(sum[:]), nil
}

func ScheduleRevisionForInput(input EnqueueInput) (string, error) {
	if strings.TrimSpace(input.ScheduleRevision) != "" {
		if err := validateCanonical("schedule revision", input.ScheduleRevision, 256); err != nil {
			return "", err
		}
		return input.ScheduleRevision, nil
	}
	value := strings.Join([]string{input.ScheduleID, input.Scenario, input.PolicyVersion, input.TargetScope}, "\x00")
	sum := sha256.Sum256([]byte(value))
	return "recovery-occurrence-schedule-" + hex.EncodeToString(sum[:]), nil
}

func (input EnqueueInput) Validate() error {
	definition := Definition{
		ScheduleID: input.ScheduleID, Scenario: input.Scenario, Operation: input.Operation,
		PolicyVersion: input.PolicyVersion, TargetScope: input.TargetScope,
		ArtifactIdentity: input.ArtifactIdentity, Cron: "@daily", Timezone: "UTC",
		StaleAfter: input.StaleAfter, Enabled: true,
	}
	if err := definition.Validate(); err != nil {
		return err
	}
	if input.PlannedAt.IsZero() {
		return fmt.Errorf("recovery qualification planned time is required")
	}
	return nil
}

// OccurrenceID is deliberately independent of delivery attempts and worker
// identities. Re-delivery of the same scheduled intent therefore converges on
// one logical occurrence.
func OccurrenceID(input EnqueueInput) (string, error) {
	if err := input.Validate(); err != nil {
		return "", err
	}
	revision, err := ScheduleRevisionForInput(input)
	if err != nil {
		return "", err
	}
	value := strings.Join([]string{
		input.ScheduleID, input.PlannedAt.UTC().Format(time.RFC3339Nano), input.Scenario,
		input.PolicyVersion, input.TargetScope, revision,
	}, "\x00")
	sum := sha256.Sum256([]byte(value))
	return "recovery-occurrence-" + hex.EncodeToString(sum[:]), nil
}

func RequestDigest(input EnqueueInput) (string, error) {
	if err := input.Validate(); err != nil {
		return "", err
	}
	value := strings.Join([]string{
		input.ScheduleID, input.Scenario, input.Operation, input.PolicyVersion,
		input.TargetScope, input.ArtifactIdentity, input.ScheduleRevision,
		input.PlannedAt.UTC().Format(time.RFC3339Nano), input.StaleAfter.String(),
	}, "\x00")
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

type Fence struct {
	Owner      string `json:"owner"`
	Generation int64  `json:"generation"`
}

func (fence Fence) Validate() error {
	if err := validateCanonical("lease owner", fence.Owner, 256); err != nil {
		return err
	}
	if fence.Generation <= 0 {
		return fmt.Errorf("recovery qualification fence generation must be positive")
	}
	return nil
}

type EvidenceReference struct {
	Kind   string `json:"kind"`
	URI    string `json:"uri"`
	SHA256 string `json:"sha256"`
}

func CanonicalEvidenceReferences(values []EvidenceReference) ([]EvidenceReference, error) {
	if len(values) > maxEvidenceReferences {
		return nil, fmt.Errorf("recovery qualification evidence references exceed %d", maxEvidenceReferences)
	}
	canonical := append([]EvidenceReference{}, values...)
	for i := range canonical {
		item := &canonical[i]
		if err := validateCanonical("evidence kind", item.Kind, 64); err != nil {
			return nil, err
		}
		if len(item.URI) == 0 || len(item.URI) > 512 || item.URI != strings.TrimSpace(item.URI) || strings.ContainsAny(item.URI, "\r\n\x00") {
			return nil, fmt.Errorf("recovery qualification evidence URI must be canonical and at most 512 bytes")
		}
		parsed, err := url.Parse(item.URI)
		if err != nil || parsed.Scheme == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("recovery qualification evidence URI must be absolute and contain no credentials, query, or fragment")
		}
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(item.SHA256) {
			return nil, fmt.Errorf("recovery qualification evidence SHA-256 must be 64 lowercase hexadecimal characters")
		}
	}
	sort.Slice(canonical, func(i, j int) bool {
		if canonical[i].Kind != canonical[j].Kind {
			return canonical[i].Kind < canonical[j].Kind
		}
		if canonical[i].URI != canonical[j].URI {
			return canonical[i].URI < canonical[j].URI
		}
		return canonical[i].SHA256 < canonical[j].SHA256
	})
	for i := 1; i < len(canonical); i++ {
		if canonical[i] == canonical[i-1] {
			return nil, fmt.Errorf("recovery qualification evidence reference is duplicated")
		}
	}
	return canonical, nil
}

func EncodeEvidenceReferences(values []EvidenceReference) (string, error) {
	canonical, err := CanonicalEvidenceReferences(values)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(canonical)
	return string(encoded), err
}

type Result struct {
	RecoveryPointAt   time.Time
	RestoreDuration   time.Duration
	ReadinessDuration time.Duration
	Evidence          []EvidenceReference
}

func (result Result) Validate(completedAt time.Time) error {
	return result.validate(completedAt, true)
}

func (result Result) ValidateFailure(completedAt time.Time) error {
	return result.validate(completedAt, false)
}

func (result Result) validate(completedAt time.Time, requireRecoveryPoint bool) error {
	if completedAt.IsZero() {
		return fmt.Errorf("recovery qualification completion time is required")
	}
	if requireRecoveryPoint && result.RecoveryPointAt.IsZero() {
		return fmt.Errorf("recovery point is required for successful qualification")
	}
	if !result.RecoveryPointAt.IsZero() && result.RecoveryPointAt.After(completedAt) {
		return fmt.Errorf("recovery point must exist and not be after completion")
	}
	if result.RestoreDuration < 0 || result.ReadinessDuration < 0 {
		return fmt.Errorf("recovery qualification durations must not be negative")
	}
	if result.RestoreDuration != 0 || result.ReadinessDuration != 0 {
		return fmt.Errorf("recovery qualification durations are owned by persisted ledger phases")
	}
	if requireRecoveryPoint && len(result.Evidence) == 0 {
		return fmt.Errorf("successful recovery qualification requires at least one evidence reference")
	}
	_, err := CanonicalEvidenceReferences(result.Evidence)
	return err
}

type Occurrence struct {
	ID                      string              `json:"occurrenceId"`
	ScheduleID              string              `json:"scheduleId"`
	ScheduleRevision        string              `json:"scheduleRevision"`
	Scenario                string              `json:"scenario"`
	Operation               string              `json:"operation"`
	PolicyVersion           string              `json:"policyVersion"`
	TargetScope             string              `json:"targetScope"`
	ArtifactIdentity        string              `json:"artifactIdentity"`
	PlannedAt               time.Time           `json:"plannedAt"`
	ExpiresAt               time.Time           `json:"expiresAt"`
	Status                  string              `json:"status"`
	Result                  string              `json:"result"`
	AttemptCount            int64               `json:"attemptCount"`
	Fence                   Fence               `json:"fence,omitempty"`
	LeaseExpiresAt          time.Time           `json:"leaseExpiresAt,omitempty"`
	Actor                   string              `json:"actor,omitempty"`
	CreatedAt               time.Time           `json:"createdAt"`
	ClaimedAt               time.Time           `json:"claimedAt,omitempty"`
	StartedAt               time.Time           `json:"startedAt,omitempty"`
	FinishedAt              time.Time           `json:"finishedAt,omitempty"`
	RecoveryPointAt         time.Time           `json:"recoveryPointAt,omitempty"`
	RecoveryPointAgeSeconds int64               `json:"recoveryPointAgeSeconds,omitempty"`
	RestoreDurationMillis   int64               `json:"restoreDurationMillis,omitempty"`
	ReadinessDurationMillis int64               `json:"readinessDurationMillis,omitempty"`
	FailureReasonRedacted   string              `json:"failureReasonRedacted,omitempty"`
	FailureCode             string              `json:"failureCode,omitempty"`
	Evidence                []EvidenceReference `json:"evidence"`
	EvidenceStatus          string              `json:"evidenceStatus"`
	EvidenceAttemptCount    int64               `json:"evidenceAttemptCount"`
	EvidenceFence           Fence               `json:"evidenceFence,omitempty"`
	EvidenceLeaseExpiresAt  time.Time           `json:"evidenceLeaseExpiresAt,omitempty"`
	EvidencePublishedAt     time.Time           `json:"evidencePublishedAt,omitempty"`
	EvidenceFailureRedacted string              `json:"evidenceFailureReasonRedacted,omitempty"`
	EvidenceFailureCode     string              `json:"evidenceFailureCode,omitempty"`
}

type Attempt struct {
	OccurrenceID          string    `json:"occurrenceId"`
	AttemptNumber         int64     `json:"attemptNumber"`
	FenceGeneration       int64     `json:"fenceGeneration"`
	WorkerID              string    `json:"workerId"`
	Actor                 string    `json:"actor"`
	Status                string    `json:"status"`
	ClaimedAt             time.Time `json:"claimedAt"`
	StartedAt             time.Time `json:"startedAt,omitempty"`
	LeaseExpiresAt        time.Time `json:"leaseExpiresAt"`
	FinishedAt            time.Time `json:"finishedAt,omitempty"`
	FailureReasonRedacted string    `json:"failureReasonRedacted,omitempty"`
	FailureCode           string    `json:"failureCode,omitempty"`
}

type EvidenceAttempt struct {
	OccurrenceID          string    `json:"occurrenceId"`
	AttemptNumber         int64     `json:"attemptNumber"`
	FenceGeneration       int64     `json:"fenceGeneration"`
	PublisherID           string    `json:"publisherId"`
	Status                string    `json:"status"`
	ClaimedAt             time.Time `json:"claimedAt"`
	LeaseExpiresAt        time.Time `json:"leaseExpiresAt"`
	FinishedAt            time.Time `json:"finishedAt,omitempty"`
	FailureReasonRedacted string    `json:"failureReasonRedacted,omitempty"`
	FailureCode           string    `json:"failureCode,omitempty"`
}

type ClaimInput struct {
	WorkerID string
	Actor    string
	Now      time.Time
	Lease    time.Duration
}

func (input ClaimInput) Validate() error {
	if err := validateCanonical("worker id", input.WorkerID, 256); err != nil {
		return err
	}
	if err := validateCanonical("actor", input.Actor, 256); err != nil {
		return err
	}
	if input.Now.IsZero() || input.Lease <= 0 {
		return fmt.Errorf("recovery qualification claim time and positive lease are required")
	}
	return nil
}

type RetentionPolicy struct {
	ComplianceWindow time.Duration
	Now              time.Time
}

type RetentionResult struct {
	DeletedIDs   []string `json:"deletedIds"`
	PreservedIDs []string `json:"preservedIds"`
}

type OperationStatus struct {
	Operation                   string `json:"operation"`
	Pending                     int64  `json:"pending"`
	Running                     int64  `json:"running"`
	Failed                      int64  `json:"failed"`
	Expired                     int64  `json:"expired"`
	LastSuccessAgeSeconds       *int64 `json:"lastSuccessAgeSeconds"`
	LastRestoreDurationMillis   *int64 `json:"lastRestoreDurationMillis"`
	LastReadinessDurationMillis *int64 `json:"lastReadinessDurationMillis"`
	LastRecoveryPointAgeSeconds *int64 `json:"lastRecoveryPointAgeSeconds"`
}

type StatusSnapshot struct {
	GeneratedAt            time.Time         `json:"generatedAt"`
	ConfiguredSchedules    int64             `json:"configuredSchedules"`
	Unconfigured           bool              `json:"unconfigured"`
	MissingRuns            int64             `json:"missingRuns"`
	StaleExecutionLeases   int64             `json:"staleExecutionLeases"`
	StaleEvidenceLeases    int64             `json:"staleEvidenceLeases"`
	Due                    int64             `json:"due"`
	Overdue                int64             `json:"overdue"`
	Running                int64             `json:"running"`
	Failed                 int64             `json:"failed"`
	EvidencePending        int64             `json:"evidencePending"`
	EvidenceFailed         int64             `json:"evidenceFailed"`
	RecoveredExpiredLeases int64             `json:"recoveredExpiredLeases"`
	Operations             []OperationStatus `json:"operations"`
}

type Metric struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
}

func (snapshot StatusSnapshot) Metrics() []Metric {
	metrics := []Metric{
		{Name: "leapview_recovery_qualification_configured", Labels: map[string]string{}, Value: float64(snapshot.ConfiguredSchedules)},
		{Name: "leapview_recovery_qualification_missing", Labels: map[string]string{}, Value: float64(snapshot.MissingRuns)},
		{Name: "leapview_recovery_qualification_stale_leases", Labels: map[string]string{"state": "execution"}, Value: float64(snapshot.StaleExecutionLeases)},
		{Name: "leapview_recovery_qualification_stale_leases", Labels: map[string]string{"state": "evidence"}, Value: float64(snapshot.StaleEvidenceLeases)},
		{Name: "leapview_recovery_qualification_due", Labels: map[string]string{}, Value: float64(snapshot.Due)},
		{Name: "leapview_recovery_qualification_overdue", Labels: map[string]string{}, Value: float64(snapshot.Overdue)},
		{Name: "leapview_recovery_qualification_running", Labels: map[string]string{}, Value: float64(snapshot.Running)},
		{Name: "leapview_recovery_qualification_failed", Labels: map[string]string{}, Value: float64(snapshot.Failed)},
		{Name: "leapview_recovery_qualification_lease_recoveries", Labels: map[string]string{}, Value: float64(snapshot.RecoveredExpiredLeases)},
		{Name: "leapview_recovery_qualification_evidence", Labels: map[string]string{"state": EvidencePending}, Value: float64(snapshot.EvidencePending)},
		{Name: "leapview_recovery_qualification_evidence", Labels: map[string]string{"state": EvidenceFailed}, Value: float64(snapshot.EvidenceFailed)},
	}
	for _, operation := range snapshot.Operations {
		labels := map[string]string{"operation": operation.Operation}
		metrics = append(metrics,
			Metric{Name: "leapview_recovery_qualification_pending", Labels: labels, Value: float64(operation.Pending)},
			Metric{Name: "leapview_recovery_qualification_operation_failed", Labels: labels, Value: float64(operation.Failed + operation.Expired)},
		)
		if operation.LastSuccessAgeSeconds != nil {
			metrics = append(metrics, Metric{Name: "leapview_recovery_qualification_last_success_age_seconds", Labels: labels, Value: float64(*operation.LastSuccessAgeSeconds)})
		}
	}
	return metrics
}

type Repository interface {
	ReconcileSchedule(context.Context, Definition, time.Time) error
	ReconcileSchedules(context.Context, []Definition, time.Time) error
	Enqueue(context.Context, EnqueueInput, time.Time) (Occurrence, bool, error)
	EnqueueDue(context.Context, time.Time, int) ([]Occurrence, error)
	ClaimNext(context.Context, ClaimInput) (Occurrence, bool, error)
	Start(context.Context, string, Fence, time.Time) error
	Heartbeat(context.Context, string, Fence, time.Time, time.Duration) error
	Complete(context.Context, string, Fence, time.Time, Result) error
	Fail(context.Context, string, Fence, time.Time, Result, error) error
	Cancel(context.Context, string, Fence, time.Time, error) error
	ClaimEvidence(context.Context, string, time.Time, time.Duration) (Occurrence, bool, error)
	PublishEvidence(context.Context, string, Fence, time.Time) error
	FailEvidence(context.Context, string, Fence, time.Time, error) error
	Occurrence(context.Context, string) (Occurrence, error)
	Occurrences(context.Context) ([]Occurrence, error)
	Attempts(context.Context, string) ([]Attempt, error)
	EvidenceAttempts(context.Context, string) ([]EvidenceAttempt, error)
	Status(context.Context, time.Time) (StatusSnapshot, error)
	Retain(context.Context, RetentionPolicy) (RetentionResult, error)
}

type Scheduler struct {
	Repository Repository
	Clock      refreshschedule.Clock
	BatchSize  int
}

func (scheduler Scheduler) EnqueueDue(ctx context.Context) ([]Occurrence, error) {
	if scheduler.Repository == nil {
		return nil, fmt.Errorf("recovery qualification repository is required")
	}
	clock := scheduler.Clock
	if clock == nil {
		clock = refreshschedule.RealClock{}
	}
	limit := scheduler.BatchSize
	if limit <= 0 {
		limit = 100
	}
	return scheduler.Repository.EnqueueDue(ctx, clock.Now(), limit)
}

func ValidateArtifactIdentity(value string) error {
	if len(value) == 0 || len(value) > 1024 || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n\x00") || !immutableDigestPattern.MatchString(value) {
		return fmt.Errorf("recovery qualification artifact identity must be canonical and contain an immutable sha256 digest")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("recovery qualification artifact identity must not contain credentials, query, or fragment")
	}
	return nil
}

func RedactFailure(err error) string {
	if err == nil {
		return ""
	}
	_, summary := FailureDetails(err, "qualification_failed")
	return summary
}

type codedFailure struct {
	code    string
	summary string
}

func (failure codedFailure) Error() string       { return failure.summary }
func (failure codedFailure) FailureCode() string { return failure.code }
func (failure codedFailure) SafeSummary() string { return failure.summary }

// NewFailure constructs an allowlisted machine code plus a credential-scrubbed
// summary. The original error belongs in restricted transient logs, not the ledger.
func NewFailure(code, summary string) error {
	if !failureCodePattern.MatchString(code) {
		code = "qualification_failed"
	}
	return codedFailure{code: code, summary: safetext.BoundedSummary(summary, maxFailureReasonBytes)}
}

// FailureDetails converts arbitrary owner errors into a bounded safe record.
func FailureDetails(err error, fallbackCode string) (string, string) {
	if !failureCodePattern.MatchString(fallbackCode) {
		fallbackCode = "qualification_failed"
	}
	if err == nil {
		return "", ""
	}
	if failure, ok := err.(interface {
		FailureCode() string
		SafeSummary() string
	}); ok && failureCodePattern.MatchString(failure.FailureCode()) {
		return failure.FailureCode(), safetext.BoundedSummary(failure.SafeSummary(), maxFailureReasonBytes)
	}
	return fallbackCode, safetext.BoundedSummary(err.Error(), maxFailureReasonBytes)
}

func validOperation(value string) bool {
	switch value {
	case OperationBackup, OperationRestore, OperationUpgrade, OperationRollback:
		return true
	default:
		return false
	}
}

func validateCanonical(label, value string, limit int) error {
	if len(value) == 0 || len(value) > limit || value != strings.TrimSpace(value) || !canonicalValuePattern.MatchString(value) {
		return fmt.Errorf("recovery qualification %s must be canonical and at most %d bytes", label, limit)
	}
	return nil
}
