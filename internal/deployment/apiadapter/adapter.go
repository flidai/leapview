// Package apiadapter translates public project deployment requests to the
// generation-scoped deployment domain.
package apiadapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/pkg/jobs"
)

var ErrInvalid = apigenfailure.New("invalid", "invalid deployment request")

type Status string

const (
	StatusPending    Status = "pending"
	StatusActive     Status = "active"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
	StatusSuperseded Status = "superseded"
)

type CreateRequest struct {
	Project           string
	Environment       string
	GenerationID      string
	ArtifactDigest    string
	PriorGenerationID string
	Actor             string
	IdempotencyKey    string
	ReleaseID         string
	Evidence          PublishEvidence
	RollbackOf        string
	Workflow          func(string) (jobs.WorkflowIntent, error)
}

// PublishEvidence is the redacted immutable identity of the release and
// generation submitted for publication. Resolved credentials and provider
// references are deliberately excluded.
type PublishEvidence struct {
	ReleaseDigest            string `json:"releaseDigest"`
	ArtifactContentDigest    string `json:"artifactContentDigest"`
	ArtifactProvenanceDigest string `json:"artifactProvenanceDigest"`
	PlanDigest               string `json:"planDigest"`
	CandidateID              string `json:"candidateId"`
	CandidateRevision        int64  `json:"candidateRevision"`
	TargetID                 string `json:"targetId"`
	Environment              string `json:"environment"`
	GenerationID             string `json:"generationId"`
	BaseGenerationID         string `json:"baseGenerationId,omitempty"`
	RuntimeVersion           string `json:"runtimeVersion"`
	PolicyDigest             string `json:"policyDigest"`
}

type Scope struct {
	Project      string
	DeploymentID string
}

type ActivateRequest struct {
	Scope
	Actor          string
	IdempotencyKey string
}

// CancelRequest carries the complete immutable cancellation operation
// identity. Cancellation is a state transition, never a row deletion, and
// therefore uses the same actor/idempotency contract as create and activate.
type CancelRequest struct {
	Scope
	Actor          string
	IdempotencyKey string
	Workflow       func(string) (jobs.WorkflowIntent, error)
}

type Deployment struct {
	ID                  string
	Project             string
	Environment         string
	GenerationID        string
	ArtifactDigest      string
	PriorGenerationID   string
	RequestDigest       string
	Status              Status
	CreatedBy           string
	CreatedAt           string
	ActivatedAt         string
	ActivationPrincipal string
	VerificationDigest  string
	VerifiedAt          string
	Error               string
}

// RequestDigest computes the immutable request binding before persistence so a
// bootstrap policy can be armed before its worker payload is committed.
func RequestDigest(request CreateRequest) (string, error) { return requestDigest(request) }

func normalizePublishEvidence(evidence *PublishEvidence) error {
	if evidence.ReleaseDigest != strings.TrimSpace(evidence.ReleaseDigest) || evidence.ArtifactContentDigest != strings.TrimSpace(evidence.ArtifactContentDigest) || evidence.ArtifactProvenanceDigest != strings.TrimSpace(evidence.ArtifactProvenanceDigest) || evidence.PlanDigest != strings.TrimSpace(evidence.PlanDigest) || evidence.PolicyDigest != strings.TrimSpace(evidence.PolicyDigest) || evidence.CandidateID != strings.TrimSpace(evidence.CandidateID) || evidence.TargetID != strings.TrimSpace(evidence.TargetID) || evidence.Environment != strings.TrimSpace(evidence.Environment) || evidence.GenerationID != strings.TrimSpace(evidence.GenerationID) {
		return fmt.Errorf("%w: immutable publish evidence must be canonical", ErrInvalid)
	}
	if platformdigest.ValidateSHA256Identity(evidence.ReleaseDigest) != nil || platformdigest.ValidateSHA256Identity(evidence.ArtifactContentDigest) != nil || platformdigest.ValidateSHA256Identity(evidence.ArtifactProvenanceDigest) != nil || platformdigest.ValidateSHA256Identity(evidence.PlanDigest) != nil || platformdigest.ValidateSHA256Identity(evidence.PolicyDigest) != nil || evidence.CandidateID == "" || evidence.CandidateRevision < 1 || evidence.TargetID == "" || evidence.Environment == "" || evidence.GenerationID == "" || evidence.RuntimeVersion == "" {
		return fmt.Errorf("%w: immutable publish evidence is incomplete", ErrInvalid)
	}
	return nil
}

func requestDigest(request CreateRequest) (string, error) {
	payload := struct {
		Project           string          `json:"project"`
		Environment       string          `json:"environment"`
		GenerationID      string          `json:"generationId"`
		ArtifactDigest    string          `json:"artifactDigest"`
		PriorGenerationID string          `json:"priorGenerationId,omitempty"`
		ReleaseID         string          `json:"releaseId,omitempty"`
		Evidence          PublishEvidence `json:"evidence,omitempty"`
	}{request.Project, request.Environment, request.GenerationID, request.ArtifactDigest, request.PriorGenerationID, request.ReleaseID, request.Evidence}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode deployment request: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
