// Package transitionoperation contains the canonical durable identity for one
// PostgreSQL release transition.  It deliberately contains no database or
// execution dependency; the PostgreSQL adapter is responsible for leasing
// and persisting these values.
package transitionoperation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/google/uuid"
)

const (
	SchemaVersion                              = 1
	DigestDomain                               = "leapview/release-transition-operation/v1\n"
	MaxEvidenceBytes                           = 1 << 20
	PhasePreflight           Phase             = "preflight"
	PhaseMigrations          Phase             = "migrations"
	PhaseCandidateStaged     Phase             = "candidate-staged"
	PhaseCandidateActivated  Phase             = "candidate-activated"
	PhaseCandidateRestarted  Phase             = "candidate-restarted"
	PhasePostValidated       Phase             = "post-validated"
	PhaseSuccess             Phase             = "success"
	StatusPending            Status            = "pending"
	StatusRunning            Status            = "running"
	StatusCompleted          Status            = "completed"
	StatusFailed             Status            = "failed"
	StatusIndeterminate      Status            = "indeterminate"
	PhaseResultSucceeded     PhaseResultStatus = "succeeded"
	PhaseResultFailed        PhaseResultStatus = "failed"
	PhaseResultIndeterminate PhaseResultStatus = "indeterminate"
)

var (
	ErrInvalid         = errors.New("invalid release transition operation")
	ErrNotFound        = errors.New("release transition operation not found")
	ErrConflict        = errors.New("release transition operation conflict")
	ErrBusy            = errors.New("release transition operation is busy")
	ErrStaleFence      = errors.New("release transition operation fence is stale")
	ErrLeaseExpired    = errors.New("release transition operation lease expired")
	ErrAlreadyTerminal = errors.New("release transition operation is already terminal")
	ErrIndeterminate   = errors.New("release transition operation outcome is indeterminate")
)

type Phase string
type Status string
type PhaseResultStatus string

var phaseOrder = []Phase{PhasePreflight, PhaseMigrations, PhaseCandidateStaged, PhaseCandidateActivated, PhaseCandidateRestarted, PhasePostValidated, PhaseSuccess}

func PhaseNames() []Phase { return append([]Phase(nil), phaseOrder...) }

func phaseRank(p Phase) int {
	for i, candidate := range phaseOrder {
		if candidate == p {
			return i
		}
	}
	return -1
}

// CreateInput is the immutable transition identity. Evidence is retained as
// canonical JSON bytes so a restart can replay exactly what was admitted.
type CreateInput struct {
	OperationID               string
	TargetIdentityDigest      string
	PredecessorArtifactDigest string
	CandidateArtifactDigest   string
	RecoveryFrontierID        string
	RecoveryFrontierDigest    string
	PreflightEvidence         []byte
	PreflightEvidenceDigest   string
	IdempotencyKey            string
	RequestDigest             string
}

type canonicalRequest struct {
	SchemaVersion             int    `json:"schemaVersion"`
	TargetIdentityDigest      string `json:"targetIdentityDigest"`
	PredecessorArtifactDigest string `json:"predecessorArtifactDigest"`
	CandidateArtifactDigest   string `json:"candidateArtifactDigest"`
	RecoveryFrontierID        string `json:"recoveryFrontierId"`
	RecoveryFrontierDigest    string `json:"recoveryFrontierDigest"`
	PreflightEvidenceDigest   string `json:"preflightEvidenceDigest"`
	IdempotencyKey            string `json:"idempotencyKey"`
}

func (in CreateInput) normalize() (CreateInput, error) {
	in.OperationID = strings.TrimSpace(in.OperationID)
	in.TargetIdentityDigest = strings.TrimSpace(in.TargetIdentityDigest)
	in.PredecessorArtifactDigest = strings.TrimSpace(in.PredecessorArtifactDigest)
	in.CandidateArtifactDigest = strings.TrimSpace(in.CandidateArtifactDigest)
	in.RecoveryFrontierID = strings.TrimSpace(in.RecoveryFrontierID)
	in.RecoveryFrontierDigest = strings.TrimSpace(in.RecoveryFrontierDigest)
	in.PreflightEvidenceDigest = strings.TrimSpace(in.PreflightEvidenceDigest)
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	in.RequestDigest = strings.TrimSpace(in.RequestDigest)
	if in.OperationID != "" {
		if _, err := uuid.Parse(in.OperationID); err != nil {
			return CreateInput{}, fmt.Errorf("%w: operation id", ErrInvalid)
		}
	}
	for name, value := range map[string]string{
		"target identity digest":      in.TargetIdentityDigest,
		"predecessor artifact digest": in.PredecessorArtifactDigest,
		"candidate artifact digest":   in.CandidateArtifactDigest,
		"recovery frontier digest":    in.RecoveryFrontierDigest,
		"preflight evidence digest":   in.PreflightEvidenceDigest,
	} {
		if err := platformdigest.ValidateSHA256Identity(value); err != nil {
			return CreateInput{}, fmt.Errorf("%w: %s", ErrInvalid, name)
		}
	}
	if in.PredecessorArtifactDigest == in.CandidateArtifactDigest {
		return CreateInput{}, fmt.Errorf("%w: artifacts must differ", ErrInvalid)
	}
	if in.RecoveryFrontierID == "" || len(in.RecoveryFrontierID) > 255 || len(in.IdempotencyKey) == 0 || len(in.IdempotencyKey) > 512 {
		return CreateInput{}, fmt.Errorf("%w: text identity", ErrInvalid)
	}
	if len(in.PreflightEvidence) == 0 || len(in.PreflightEvidence) > MaxEvidenceBytes || !json.Valid(in.PreflightEvidence) {
		return CreateInput{}, fmt.Errorf("%w: preflight evidence", ErrInvalid)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(in.PreflightEvidence, &object); err != nil || object == nil {
		return CreateInput{}, fmt.Errorf("%w: preflight evidence object", ErrInvalid)
	}
	return in, nil
}

func (in CreateInput) CanonicalJSON() ([]byte, error) {
	in, err := in.normalize()
	if err != nil {
		return nil, err
	}
	return json.Marshal(canonicalRequest{SchemaVersion: SchemaVersion, TargetIdentityDigest: in.TargetIdentityDigest, PredecessorArtifactDigest: in.PredecessorArtifactDigest, CandidateArtifactDigest: in.CandidateArtifactDigest, RecoveryFrontierID: in.RecoveryFrontierID, RecoveryFrontierDigest: in.RecoveryFrontierDigest, PreflightEvidenceDigest: in.PreflightEvidenceDigest, IdempotencyKey: in.IdempotencyKey})
}

func (in CreateInput) Digest() (string, error) {
	b, err := in.CanonicalJSON()
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(append([]byte(DigestDomain), b...))
	return "sha256:" + hex.EncodeToString(h[:]), nil
}

func (in CreateInput) Validate() error {
	normalized, err := in.normalize()
	if err != nil {
		return err
	}
	digest, err := normalized.Digest()
	if err != nil {
		return err
	}
	if normalized.RequestDigest != "" && normalized.RequestDigest != digest {
		return fmt.Errorf("%w: request digest", ErrConflict)
	}
	return nil
}

type Fence struct {
	OperationID       string
	OwnerID           string
	FencingGeneration int64
	LeaseExpiresAt    time.Time
}

type PhaseResult struct {
	Phase        Phase
	Status       PhaseResultStatus
	Result       []byte
	ResultDigest string
	StartedAt    time.Time
	CompletedAt  time.Time
}

type Operation struct {
	OperationID               string
	TargetIdentityDigest      string
	PredecessorArtifactDigest string
	CandidateArtifactDigest   string
	RecoveryFrontierID        string
	RecoveryFrontierDigest    string
	PreflightEvidence         []byte
	PreflightEvidenceDigest   string
	IdempotencyKey            string
	RequestDigest             string
	Status                    Status
	CurrentPhase              Phase
	Fence                     Fence
	PhaseResults              []PhaseResult
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
	TerminalAt                time.Time
}

func (o Operation) Input() CreateInput {
	return CreateInput{OperationID: o.OperationID, TargetIdentityDigest: o.TargetIdentityDigest, PredecessorArtifactDigest: o.PredecessorArtifactDigest, CandidateArtifactDigest: o.CandidateArtifactDigest, RecoveryFrontierID: o.RecoveryFrontierID, RecoveryFrontierDigest: o.RecoveryFrontierDigest, PreflightEvidence: append([]byte(nil), o.PreflightEvidence...), PreflightEvidenceDigest: o.PreflightEvidenceDigest, IdempotencyKey: o.IdempotencyKey, RequestDigest: o.RequestDigest}
}

func (p Phase) Valid() bool { return phaseRank(p) >= 0 }
func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusIndeterminate
}
func (s PhaseResultStatus) Valid() bool {
	return s == PhaseResultSucceeded || s == PhaseResultFailed || s == PhaseResultIndeterminate
}
