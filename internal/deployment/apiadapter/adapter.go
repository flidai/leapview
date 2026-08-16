// Package apiadapter translates public project deployment requests to the
// generation-scoped deployment domain.
package apiadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/deployment"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/platform/jobs"
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

// TargetRequest is retained for candidate-publication authorization. It is a
// target instance identity, never a workspace selector.
type TargetRequest struct {
	TargetID    string `json:"targetId"`
	CandidateID string `json:"candidateId"`
}

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
	ReleaseDigest     string `json:"releaseDigest"`
	ArtifactDigest    string `json:"artifactDigest"`
	PlanDigest        string `json:"planDigest"`
	CandidateID       string `json:"candidateId"`
	CandidateRevision int64  `json:"candidateRevision"`
	TargetID          string `json:"targetId"`
	Environment       string `json:"environment"`
	GenerationID      string `json:"generationId"`
	BaseGenerationID  string `json:"baseGenerationId,omitempty"`
	RuntimeVersion    string `json:"runtimeVersion"`
	PolicyDigest      string `json:"policyDigest"`
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

type Service interface {
	Create(context.Context, deployment.CreateInput) (deployment.Deployment, error)
	Get(context.Context, deployment.Scope) (deployment.Deployment, error)
	Activate(context.Context, deployment.ActivationRequest) (deployment.Deployment, error)
	Cancel(context.Context, deployment.Scope) (deployment.Deployment, error)
}

type Adapter struct{ service Service }

// Metadata is no longer consulted during deployment mapping. It remains a
// narrow constructor parameter only while callers migrate to LEA-378's exact
// managed-data identity handoff.
type Metadata interface{}

func New(service Service, _ Metadata) (*Adapter, error) {
	if service == nil {
		return nil, fmt.Errorf("deployment service is required")
	}
	return &Adapter{service: service}, nil
}

func (a *Adapter) Cancel(ctx context.Context, scope Scope) (Deployment, error) {
	row, err := a.service.Cancel(ctx, deployment.Scope{ProjectID: strings.TrimSpace(scope.Project), DeploymentID: strings.TrimSpace(scope.DeploymentID)})
	if err != nil {
		return Deployment{}, err
	}
	return mapDeployment(row), nil
}

func (a *Adapter) Create(ctx context.Context, request CreateRequest) (Deployment, error) {
	if request.Project != strings.TrimSpace(request.Project) || request.Environment != strings.TrimSpace(request.Environment) || request.GenerationID != strings.TrimSpace(request.GenerationID) || request.ArtifactDigest != strings.TrimSpace(request.ArtifactDigest) {
		return Deployment{}, fmt.Errorf("%w: identity fields must be canonical", ErrInvalid)
	}
	if request.Project == "" || request.Environment == "" || request.GenerationID == "" || request.ArtifactDigest == "" || request.Actor == "" || request.IdempotencyKey == "" {
		return Deployment{}, fmt.Errorf("%w: project, environment, generation, artifact, actor, and idempotency key are required", ErrInvalid)
	}
	if platformdigest.ValidateSHA256Identity(request.ArtifactDigest) != nil {
		return Deployment{}, fmt.Errorf("%w: artifact digest is invalid", ErrInvalid)
	}
	if request.ReleaseID != "" {
		if err := normalizePublishEvidence(&request.Evidence); err != nil {
			return Deployment{}, err
		}
	}
	digest, err := requestDigest(request)
	if err != nil {
		return Deployment{}, err
	}
	input := deployment.CreateInput{ID: stableID(request.Project, request.Actor, request.IdempotencyKey), ProjectID: request.Project, Environment: request.Environment, GenerationID: request.GenerationID, ArtifactDigest: request.ArtifactDigest, PriorGenerationID: request.PriorGenerationID, RequestDigest: digest, CreatedBy: request.Actor, ReleaseID: request.ReleaseID, RollbackOf: request.RollbackOf}
	if request.Workflow != nil {
		input.Workflow, err = request.Workflow(input.ID)
		if err != nil {
			return Deployment{}, err
		}
	}
	row, err := a.service.Create(ctx, input)
	if err != nil {
		return Deployment{}, err
	}
	return mapDeployment(row), nil
}

func (a *Adapter) Get(ctx context.Context, scope Scope) (Deployment, error) {
	row, err := a.service.Get(ctx, deployment.Scope{ProjectID: strings.TrimSpace(scope.Project), DeploymentID: strings.TrimSpace(scope.DeploymentID)})
	if err != nil {
		return Deployment{}, err
	}
	return mapDeployment(row), nil
}

func (a *Adapter) Activate(ctx context.Context, request ActivateRequest) (Deployment, error) {
	if request.Actor == "" || request.IdempotencyKey == "" {
		return Deployment{}, fmt.Errorf("%w: actor and idempotency key are required", ErrInvalid)
	}
	row, err := a.service.Activate(ctx, deployment.ActivationRequest{Scope: deployment.Scope{ProjectID: strings.TrimSpace(request.Project), DeploymentID: strings.TrimSpace(request.DeploymentID)}, ActorID: request.Actor})
	if err != nil {
		return Deployment{}, err
	}
	return mapDeployment(row), nil
}

func mapDeployment(row deployment.Deployment) Deployment {
	return Deployment{ID: row.ID, Project: row.ProjectID, Environment: row.Environment, GenerationID: row.GenerationID, ArtifactDigest: row.ArtifactDigest, PriorGenerationID: row.PriorGenerationID, RequestDigest: row.RequestDigest, Status: Status(row.Status), CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, ActivatedAt: row.ActivatedAt, Error: row.Error, ActivationPrincipal: row.ActivationPrincipal, VerificationDigest: row.VerificationDigest, VerifiedAt: row.VerifiedAt}
}

func stableID(project, actor, key string) string {
	sum := sha256.Sum256([]byte(project + "\x00" + actor + "\x00" + key))
	return "deployment_" + hex.EncodeToString(sum[:16])
}

func normalizePublishEvidence(evidence *PublishEvidence) error {
	if evidence.ReleaseDigest != strings.TrimSpace(evidence.ReleaseDigest) || evidence.ArtifactDigest != strings.TrimSpace(evidence.ArtifactDigest) || evidence.PlanDigest != strings.TrimSpace(evidence.PlanDigest) || evidence.PolicyDigest != strings.TrimSpace(evidence.PolicyDigest) || evidence.CandidateID != strings.TrimSpace(evidence.CandidateID) || evidence.TargetID != strings.TrimSpace(evidence.TargetID) || evidence.Environment != strings.TrimSpace(evidence.Environment) || evidence.GenerationID != strings.TrimSpace(evidence.GenerationID) {
		return fmt.Errorf("%w: immutable publish evidence must be canonical", ErrInvalid)
	}
	if platformdigest.ValidateSHA256Identity(evidence.ReleaseDigest) != nil || platformdigest.ValidateSHA256Identity(evidence.ArtifactDigest) != nil || platformdigest.ValidateSHA256Identity(evidence.PlanDigest) != nil || platformdigest.ValidateSHA256Identity(evidence.PolicyDigest) != nil || evidence.CandidateID == "" || evidence.CandidateRevision < 1 || evidence.TargetID == "" || evidence.Environment == "" || evidence.GenerationID == "" || evidence.RuntimeVersion == "" {
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
