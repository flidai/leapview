package deployment

import (
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrCandidateNotFound    = apigenfailure.New("candidate_not_found", "candidate not found")
	ErrCandidateConflict    = apigenfailure.New("candidate_conflict", "candidate conflict")
	ErrCandidateQuota       = apigenfailure.New("candidate_quota", "candidate quota exceeded")
	ErrCandidateInvalid     = apigenfailure.New("candidate_invalid", "candidate invalid")
	ErrCandidateUnavailable = apigenfailure.New("candidate_unavailable", "candidate service unavailable")
)

type CandidateStatus string

const (
	CandidatePreparing CandidateStatus = "preparing"
	CandidateReady     CandidateStatus = "ready"
	CandidateFailed    CandidateStatus = "failed"
	CandidateCancelled CandidateStatus = "cancelled"
	CandidateExpired   CandidateStatus = "expired"
)

// Candidate is the product's owner-bound projection of one native delivery
// candidate. Native PostgreSQL rows remain the lifecycle authority; this
// value only carries the fields needed by preview/runtime adapters and UI.
type Candidate struct {
	ID       string
	Key      string
	TargetID string
	OwnerID  string
	// PreviewURL is the canonical, opaque candidate route. It is retained on
	// the internal projection for native read paths; generated API DTOs choose
	// explicitly which fields to expose and must not derive owner data from it.
	PreviewURL       string
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

type CandidateScope = projectgraph.CandidateScope
