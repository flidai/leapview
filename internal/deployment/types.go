// Package deployment coordinates one project-generation cutover.
package deployment

import (
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
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
