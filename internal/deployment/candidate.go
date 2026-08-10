package deployment

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/digest"
)

var (
	ErrCandidateNotFound         = errors.New("candidate not found")
	ErrCandidateConflict         = errors.New("candidate conflict")
	ErrCandidateQuota            = errors.New("candidate quota exceeded")
	ErrCandidateInvalid          = errors.New("candidate invalid")
	ErrCandidateUnavailable      = errors.New("candidate service unavailable")
	ErrCandidateAuditUnavailable = errors.New("candidate audit unavailable")
)

type CandidateStatus string

const (
	CandidatePreparing CandidateStatus = "preparing"
	CandidateReady     CandidateStatus = "ready"
	CandidateFailed    CandidateStatus = "failed"
	CandidateCancelled CandidateStatus = "cancelled"
	CandidateExpired   CandidateStatus = "expired"
)

// Candidate is the durable, Deployment-owned identity of one author's private
// project runtime. It never changes active serving-state pointers.
type Candidate struct {
	ID               string
	ProjectID        string
	Key              string
	TargetID         string
	Environment      string
	OwnerID          string
	BaseGeneration   string
	ArtifactDigest   string
	ProvenanceDigest string
	Status           CandidateStatus
	FailureReason    string
	ExpiresAt        time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ReadyAt          time.Time
	CancelledAt      time.Time
	ExpiredAt        time.Time
	Revision         int64
}

type CandidateStartInput struct {
	ID             string
	ProjectID      string
	Key            string
	TargetID       string
	Environment    string
	OwnerID        string
	BaseGeneration string
	ArtifactDigest string
	ExpiresAt      time.Time
	Now            time.Time
}

func (candidate Candidate) Validate() error {
	if strings.TrimSpace(candidate.ID) == "" ||
		strings.TrimSpace(candidate.ProjectID) == "" ||
		!canonicalCandidateKey(candidate.Key) ||
		strings.TrimSpace(candidate.TargetID) == "" ||
		strings.TrimSpace(candidate.Environment) == "" ||
		strings.TrimSpace(candidate.OwnerID) == "" ||
		strings.TrimSpace(candidate.BaseGeneration) == "" ||
		!canonicalCandidateDigest(candidate.ArtifactDigest) ||
		candidate.Revision < 1 ||
		candidate.CreatedAt.IsZero() ||
		candidate.UpdatedAt.IsZero() ||
		candidate.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: candidate identity is incomplete", ErrCandidateInvalid)
	}
	switch candidate.Status {
	case CandidatePreparing, CandidateFailed, CandidateCancelled, CandidateExpired:
		if strings.TrimSpace(candidate.ProvenanceDigest) != "" {
			return fmt.Errorf(
				"%w: only a ready candidate may reference provenance",
				ErrCandidateInvalid,
			)
		}
	case CandidateReady:
		if !canonicalCandidateDigest(candidate.ProvenanceDigest) {
			return fmt.Errorf(
				"%w: ready candidate provenance must be canonical sha256",
				ErrCandidateInvalid,
			)
		}
	default:
		return fmt.Errorf("%w: candidate status is invalid", ErrCandidateInvalid)
	}
	return nil
}

func NewCandidate(input CandidateStartInput) (Candidate, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.Key = normalizeCandidateKey(input.Key)
	input.TargetID = strings.TrimSpace(input.TargetID)
	input.Environment = strings.TrimSpace(input.Environment)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.BaseGeneration = strings.TrimSpace(input.BaseGeneration)
	input.ArtifactDigest = strings.TrimSpace(input.ArtifactDigest)
	input.Now = input.Now.UTC()
	input.ExpiresAt = input.ExpiresAt.UTC()
	if input.ID == "" || input.ProjectID == "" || input.TargetID == "" || input.Environment == "" ||
		input.OwnerID == "" || input.BaseGeneration == "" {
		return Candidate{}, fmt.Errorf("%w: id, project, target, environment, owner, and base generation are required", ErrCandidateInvalid)
	}
	if !canonicalCandidateDigest(input.ArtifactDigest) {
		return Candidate{}, fmt.Errorf("%w: artifact digest must be canonical sha256", ErrCandidateInvalid)
	}
	if input.Now.IsZero() || !input.ExpiresAt.After(input.Now) {
		return Candidate{}, fmt.Errorf("%w: expiry must be after creation", ErrCandidateInvalid)
	}
	candidate := Candidate{
		ID: input.ID, ProjectID: input.ProjectID, Key: input.Key, TargetID: input.TargetID,
		Environment: input.Environment, OwnerID: input.OwnerID, BaseGeneration: input.BaseGeneration,
		ArtifactDigest: input.ArtifactDigest, Status: CandidatePreparing,
		ExpiresAt: input.ExpiresAt, CreatedAt: input.Now, UpdatedAt: input.Now, Revision: 1,
	}
	if err := candidate.Validate(); err != nil {
		return Candidate{}, err
	}
	return candidate, nil
}

func normalizeCandidateKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "default"
	}
	return value
}

func canonicalCandidateKey(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			strings.ContainsRune("._:/-", character) {
			continue
		}
		return false
	}
	return true
}

func (candidate Candidate) Terminal() bool {
	return candidate.Status == CandidateCancelled || candidate.Status == CandidateExpired
}

func (candidate Candidate) ReplaceArtifact(expectedDigest, nextDigest string, now, expiresAt time.Time) (Candidate, error) {
	expectedDigest = strings.TrimSpace(expectedDigest)
	nextDigest = strings.TrimSpace(nextDigest)
	now = now.UTC()
	expiresAt = expiresAt.UTC()
	if candidate.Terminal() {
		return Candidate{}, fmt.Errorf("%w: candidate is %s", ErrCandidateConflict, candidate.Status)
	}
	if expectedDigest != candidate.ArtifactDigest {
		return Candidate{}, fmt.Errorf("%w: candidate artifact advanced", ErrCandidateConflict)
	}
	if !canonicalCandidateDigest(nextDigest) {
		return Candidate{}, fmt.Errorf("%w: artifact digest must be canonical sha256", ErrCandidateInvalid)
	}
	if now.IsZero() || !expiresAt.After(now) {
		return Candidate{}, fmt.Errorf("%w: expiry must be after update", ErrCandidateInvalid)
	}
	if nextDigest == candidate.ArtifactDigest && candidate.Status == CandidatePreparing {
		return candidate, nil
	}
	candidate.ArtifactDigest = nextDigest
	candidate.ProvenanceDigest = ""
	candidate.Status = CandidatePreparing
	candidate.FailureReason = ""
	candidate.ReadyAt = time.Time{}
	candidate.ExpiresAt = expiresAt
	return candidate.advance(now), nil
}

func (candidate Candidate) MarkReady(
	artifactDigest,
	provenanceDigest string,
	now time.Time,
) (Candidate, error) {
	artifactDigest = strings.TrimSpace(artifactDigest)
	provenanceDigest = strings.TrimSpace(provenanceDigest)
	now = now.UTC()
	if candidate.Terminal() {
		return Candidate{}, fmt.Errorf("%w: candidate is %s", ErrCandidateConflict, candidate.Status)
	}
	if artifactDigest != candidate.ArtifactDigest {
		return Candidate{}, fmt.Errorf("%w: candidate artifact advanced", ErrCandidateConflict)
	}
	if candidate.Status == CandidateReady {
		if provenanceDigest != candidate.ProvenanceDigest {
			return Candidate{}, fmt.Errorf(
				"%w: candidate provenance changed",
				ErrCandidateConflict,
			)
		}
		return candidate, nil
	}
	if candidate.Status != CandidatePreparing {
		return Candidate{}, fmt.Errorf("%w: candidate is %s", ErrCandidateConflict, candidate.Status)
	}
	if !canonicalCandidateDigest(provenanceDigest) {
		return Candidate{}, fmt.Errorf(
			"%w: provenance digest must be canonical sha256",
			ErrCandidateInvalid,
		)
	}
	candidate.Status = CandidateReady
	candidate.ProvenanceDigest = provenanceDigest
	candidate.FailureReason = ""
	candidate.ReadyAt = now
	return candidate.advance(now), nil
}

func (candidate Candidate) MarkFailed(artifactDigest, failureCode string, now time.Time) (Candidate, error) {
	artifactDigest = strings.TrimSpace(artifactDigest)
	failureCode = strings.TrimSpace(failureCode)
	now = now.UTC()
	if candidate.Terminal() {
		return Candidate{}, fmt.Errorf("%w: candidate is %s", ErrCandidateConflict, candidate.Status)
	}
	if artifactDigest != candidate.ArtifactDigest {
		return Candidate{}, fmt.Errorf("%w: candidate artifact advanced", ErrCandidateConflict)
	}
	if !canonicalCandidateFailureCode(failureCode) {
		return Candidate{}, fmt.Errorf("%w: failure code must contain 1-64 uppercase letters, digits, or underscores", ErrCandidateInvalid)
	}
	if candidate.Status == CandidateFailed && candidate.FailureReason == failureCode {
		return candidate, nil
	}
	if candidate.Status != CandidatePreparing {
		return Candidate{}, fmt.Errorf("%w: candidate is %s", ErrCandidateConflict, candidate.Status)
	}
	candidate.Status = CandidateFailed
	candidate.ProvenanceDigest = ""
	candidate.FailureReason = failureCode
	candidate.ReadyAt = time.Time{}
	return candidate.advance(now), nil
}

func (candidate Candidate) Retry(now, expiresAt time.Time) (Candidate, error) {
	now = now.UTC()
	expiresAt = expiresAt.UTC()
	if candidate.Status == CandidatePreparing {
		return candidate, nil
	}
	if candidate.Status != CandidateFailed {
		return Candidate{}, fmt.Errorf("%w: candidate is %s", ErrCandidateConflict, candidate.Status)
	}
	if now.IsZero() || !expiresAt.After(now) {
		return Candidate{}, fmt.Errorf("%w: expiry must be after retry", ErrCandidateInvalid)
	}
	candidate.Status = CandidatePreparing
	candidate.ProvenanceDigest = ""
	candidate.FailureReason = ""
	candidate.ReadyAt = time.Time{}
	candidate.ExpiresAt = expiresAt
	return candidate.advance(now), nil
}

func (candidate Candidate) Cancel(now time.Time) (Candidate, error) {
	now = now.UTC()
	if candidate.Status == CandidateCancelled {
		return candidate, nil
	}
	if candidate.Status == CandidateExpired {
		return Candidate{}, fmt.Errorf("%w: candidate is expired", ErrCandidateConflict)
	}
	candidate.Status = CandidateCancelled
	candidate.ProvenanceDigest = ""
	candidate.CancelledAt = now
	candidate.FailureReason = ""
	candidate.ReadyAt = time.Time{}
	return candidate.advance(now), nil
}

func (candidate Candidate) Expire(now time.Time) (Candidate, bool, error) {
	now = now.UTC()
	if now.IsZero() {
		return Candidate{}, false, fmt.Errorf("%w: expiry time is required", ErrCandidateInvalid)
	}
	if candidate.Terminal() || now.Before(candidate.ExpiresAt) {
		return candidate, false, nil
	}
	candidate.Status = CandidateExpired
	candidate.ProvenanceDigest = ""
	candidate.ExpiredAt = now
	candidate.FailureReason = ""
	candidate.ReadyAt = time.Time{}
	return candidate.advance(now), true, nil
}

func (candidate Candidate) advance(now time.Time) Candidate {
	candidate.UpdatedAt = now.UTC()
	candidate.Revision++
	return candidate
}

func canonicalCandidateDigest(value string) bool {
	return digest.ValidateSHA256Identity(strings.TrimSpace(value)) == nil
}

func canonicalCandidateFailureCode(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}
