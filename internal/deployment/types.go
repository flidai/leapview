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
	ServingIdentity     graph.ServingIdentity
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
	ServingIdentity   graph.ServingIdentity
	ArtifactDigest    string
	PriorGenerationID string
	RequestDigest     string
	CreatedBy         string
	ReleaseID         string
	RollbackOf        string
	Workflow          jobs.WorkflowIntent
}

type Scope struct {
	ProjectID    graph.ResourceID
	DeploymentID string
}
type ActivationRequest struct {
	Scope
	ActorID string
}
type ActivationInput struct {
	DeploymentID        string
	ServingIdentity     graph.ServingIdentity
	ArtifactDigest      string
	PriorGenerationID   string
	ActivationPrincipal string
	VerificationDigest  string
}
type Verification struct{ Digest string }

func ValidateCreate(input CreateInput) error {
	rawID, rawArtifact, rawRequest, rawActor, rawPrior := input.ID, input.ArtifactDigest, input.RequestDigest, input.CreatedBy, input.PriorGenerationID
	input.ID, input.ArtifactDigest, input.RequestDigest, input.CreatedBy = strings.TrimSpace(input.ID), strings.TrimSpace(input.ArtifactDigest), strings.TrimSpace(input.RequestDigest), strings.TrimSpace(input.CreatedBy)
	input.PriorGenerationID = strings.TrimSpace(input.PriorGenerationID)
	if rawID != input.ID || rawArtifact != input.ArtifactDigest || rawRequest != input.RequestDigest || rawActor != input.CreatedBy || rawPrior != input.PriorGenerationID {
		return fmt.Errorf("deployment fields must be canonical")
	}
	if input.ID == "" || input.RequestDigest == "" || input.CreatedBy == "" {
		return fmt.Errorf("deployment id, project, environment, generation, request digest, and actor are required")
	}
	if digest.ValidateSHA256Identity(input.RequestDigest) != nil {
		return fmt.Errorf("request digest must be canonical sha256")
	}
	identity, err := input.Identity()
	if err != nil {
		return err
	}
	if err := identity.Validate(); err != nil {
		return err
	}
	if digest.ValidateSHA256Identity(input.ArtifactDigest) != nil {
		return fmt.Errorf("artifact digest must be canonical sha256")
	}
	if input.PriorGenerationID != "" {
		prior := graph.ServingIdentity{ProjectID: input.ServingIdentity.ProjectID, Environment: input.ServingIdentity.Environment, GenerationID: input.PriorGenerationID}
		if err := prior.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func ValidateActivation(input ActivationInput) error {
	rawDeployment, rawArtifact, rawPrior, rawPrincipal, rawVerification := input.DeploymentID, input.ArtifactDigest, input.PriorGenerationID, input.ActivationPrincipal, input.VerificationDigest
	input.DeploymentID, input.ArtifactDigest, input.PriorGenerationID, input.ActivationPrincipal, input.VerificationDigest = strings.TrimSpace(input.DeploymentID), strings.TrimSpace(input.ArtifactDigest), strings.TrimSpace(input.PriorGenerationID), strings.TrimSpace(input.ActivationPrincipal), strings.TrimSpace(input.VerificationDigest)
	if rawDeployment != input.DeploymentID || rawArtifact != input.ArtifactDigest || rawPrior != input.PriorGenerationID || rawPrincipal != input.ActivationPrincipal || rawVerification != input.VerificationDigest {
		return fmt.Errorf("deployment fields must be canonical")
	}
	if input.DeploymentID == "" || input.ArtifactDigest == "" || input.ActivationPrincipal == "" || digest.ValidateSHA256Identity(input.VerificationDigest) != nil {
		return fmt.Errorf("deployment, activation principal, and verification digest are required")
	}
	identity, err := input.Identity()
	if err != nil {
		return err
	}
	if err := identity.Validate(); err != nil {
		return err
	}
	if digest.ValidateSHA256Identity(input.ArtifactDigest) != nil {
		return fmt.Errorf("artifact digest must be canonical sha256")
	}
	if input.PriorGenerationID != "" {
		prior := graph.ServingIdentity{ProjectID: input.ServingIdentity.ProjectID, Environment: input.ServingIdentity.Environment, GenerationID: input.PriorGenerationID}
		if err := prior.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (d Deployment) Identity() (graph.ServingIdentity, error) {
	if err := d.ServingIdentity.Validate(); err != nil {
		return graph.ServingIdentity{}, err
	}
	return d.ServingIdentity, nil
}
func (input CreateInput) Identity() (graph.ServingIdentity, error) {
	if err := input.ServingIdentity.Validate(); err != nil {
		return graph.ServingIdentity{}, err
	}
	return input.ServingIdentity, nil
}
func (input ActivationInput) Identity() (graph.ServingIdentity, error) {
	if err := input.ServingIdentity.Validate(); err != nil {
		return graph.ServingIdentity{}, err
	}
	return input.ServingIdentity, nil
}
