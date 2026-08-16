// Package deployment coordinates one project-generation cutover.
package deployment

import (
	"fmt"
	"strings"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrNotFound = apigenfailure.New("not_found", "deployment not found")
	ErrConflict = apigenfailure.New("conflict", "deployment conflict")
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusActive     Status = "active"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
	StatusSuperseded Status = "superseded"
)

type Deployment struct {
	ID                  string
	ProjectID           string
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

type CreateInput struct {
	ID                string
	ProjectID         string
	Environment       string
	GenerationID      string
	ArtifactDigest    string
	PriorGenerationID string
	RequestDigest     string
	CreatedBy         string
	ReleaseID         string
	RollbackOf        string
	Workflow          jobs.WorkflowIntent
}

type Scope struct {
	ProjectID    string
	DeploymentID string
}
type ActivationRequest struct {
	Scope
	ActorID string
}
type ActivationInput struct {
	DeploymentID        string
	ProjectID           string
	Environment         string
	GenerationID        string
	ArtifactDigest      string
	PriorGenerationID   string
	ActivationPrincipal string
	VerificationDigest  string
}
type Verification struct{ Digest string }

func ValidateCreate(input CreateInput) error {
	input.ProjectID, input.Environment, input.GenerationID, input.ArtifactDigest = strings.TrimSpace(input.ProjectID), strings.TrimSpace(input.Environment), strings.TrimSpace(input.GenerationID), strings.TrimSpace(input.ArtifactDigest)
	if input.ID == "" || input.ProjectID == "" || input.Environment == "" || input.GenerationID == "" || input.RequestDigest == "" || input.CreatedBy == "" {
		return fmt.Errorf("deployment id, project, environment, generation, request digest, and actor are required")
	}
	if _, err := graph.NewServingIdentity(graph.ResourceID(input.ProjectID), input.Environment, input.GenerationID); err != nil {
		return err
	}
	if digest.ValidateSHA256Identity(input.ArtifactDigest) != nil {
		return fmt.Errorf("artifact digest must be canonical sha256")
	}
	if input.PriorGenerationID != "" {
		if _, err := graph.NewServingIdentity(graph.ResourceID(input.ProjectID), input.Environment, input.PriorGenerationID); err != nil {
			return err
		}
	}
	return nil
}

func ValidateActivation(input ActivationInput) error {
	if strings.TrimSpace(input.DeploymentID) == "" || strings.TrimSpace(input.ActivationPrincipal) == "" || digest.ValidateSHA256Identity(input.VerificationDigest) != nil {
		return fmt.Errorf("deployment, activation principal, and verification digest are required")
	}
	if input.ProjectID != "" && input.Environment != "" && input.GenerationID != "" {
		if _, err := graph.NewServingIdentity(graph.ResourceID(input.ProjectID), input.Environment, input.GenerationID); err != nil {
			return err
		}
	}
	if input.ArtifactDigest != "" && digest.ValidateSHA256Identity(input.ArtifactDigest) != nil {
		return fmt.Errorf("artifact digest must be canonical sha256")
	}
	return nil
}
