package deployment

import (
	"fmt"
	"strings"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrCandidateNotFound         = apigenfailure.New("candidate_not_found", "candidate not found")
	ErrCandidateConflict         = apigenfailure.New("candidate_conflict", "candidate conflict")
	ErrCandidateQuota            = apigenfailure.New("candidate_quota", "candidate quota exceeded")
	ErrCandidateInvalid          = apigenfailure.New("candidate_invalid", "candidate invalid")
	ErrCandidateUnavailable      = apigenfailure.New("candidate_unavailable", "candidate service unavailable")
	ErrCandidateAuditUnavailable = apigenfailure.New("audit_unavailable", "candidate audit unavailable")
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
	Key              string
	TargetID         string
	OwnerID          string
	Scope            CandidateScope
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
	Key            string
	TargetID       string
	OwnerID        string
	Scope          CandidateScope
	ArtifactDigest string
	ExpiresAt      time.Time
	Now            time.Time
}

type CandidateScope = projectgraph.CandidateScope

// CandidateAccessScope is the request-time ownership scope for a candidate.
// It deliberately does not duplicate the candidate's serving scope aggregate.
type CandidateAccessScope struct {
	ProjectID   projectgraph.ResourceID
	CandidateID string
	OwnerID     string
	TargetID    string
}

func (candidate Candidate) Validate() error {
	if !canonicalCandidateLiteral(candidate.ID, false) ||
		!canonicalCandidateKey(candidate.Key) ||
		!canonicalCandidateLiteral(candidate.TargetID, false) ||
		!canonicalCandidateLiteral(candidate.OwnerID, false) ||
		candidate.Scope.Validate() != nil ||
		candidate.Scope.Environment == "" ||
		!canonicalCandidateDigest(candidate.ArtifactDigest) ||
		candidate.Revision < 1 ||
		candidate.CreatedAt.IsZero() ||
		candidate.UpdatedAt.IsZero() ||
		candidate.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: candidate identity is incomplete", ErrCandidateInvalid)
	}
	switch candidate.Status {
	case CandidatePreparing, CandidateFailed, CandidateCancelled, CandidateExpired:
		if !canonicalCandidateLiteral(candidate.ProvenanceDigest, true) {
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
	if !canonicalCandidateLiteral(input.ID, false) ||
		!canonicalCandidateLiteral(input.TargetID, false) ||
		!canonicalCandidateLiteral(input.OwnerID, false) ||
		input.Scope.Validate() != nil ||
		input.Scope.Environment == "" ||
		(input.Key != "" && !canonicalCandidateLiteral(input.Key, false)) ||
		!canonicalCandidateLiteral(input.ArtifactDigest, false) {
		return Candidate{}, fmt.Errorf("%w: candidate fields must be canonical", ErrCandidateInvalid)
	}
	input.Key = normalizeCandidateKey(input.Key)
	input.Now = input.Now.UTC()
	input.ExpiresAt = input.ExpiresAt.UTC()
	if input.ID == "" || input.TargetID == "" || input.OwnerID == "" {
		return Candidate{}, fmt.Errorf("%w: id, project, target, owner, and base identity are required", ErrCandidateInvalid)
	}
	if !canonicalCandidateDigest(input.ArtifactDigest) {
		return Candidate{}, fmt.Errorf("%w: artifact digest must be canonical sha256", ErrCandidateInvalid)
	}
	if input.Now.IsZero() || !input.ExpiresAt.After(input.Now) {
		return Candidate{}, fmt.Errorf("%w: expiry must be after creation", ErrCandidateInvalid)
	}
	candidate := Candidate{
		ID: input.ID, Key: input.Key, TargetID: input.TargetID,
		OwnerID: input.OwnerID, Scope: input.Scope,
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
	if !canonicalCandidateLiteral(expectedDigest, false) || !canonicalCandidateLiteral(nextDigest, false) {
		return Candidate{}, fmt.Errorf("%w: candidate digests must be canonical", ErrCandidateInvalid)
	}
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
	if !canonicalCandidateLiteral(artifactDigest, false) || !canonicalCandidateLiteral(provenanceDigest, false) {
		return Candidate{}, fmt.Errorf("%w: candidate digests must be canonical", ErrCandidateInvalid)
	}
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
	if !canonicalCandidateLiteral(artifactDigest, false) || !canonicalCandidateLiteral(failureCode, false) {
		return Candidate{}, fmt.Errorf("%w: candidate transition fields must be canonical", ErrCandidateInvalid)
	}
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
	return canonicalCandidateLiteral(value, false) && digest.ValidateSHA256Identity(value) == nil
}

// canonicalCandidateLiteral validates an identity literal without silently
// repairing it. Transport adapters and domain transitions must both reject
// aliases rather than turning them into a different identity.
func canonicalCandidateLiteral(value string, allowEmpty bool) bool {
	if value != strings.TrimSpace(value) {
		return false
	}
	return allowEmpty || value != ""
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
