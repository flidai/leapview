// Package release models immutable, project-wide deployment releases.
package release

import apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
import (
	"fmt"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrInvalid    = apigenfailure.New("invalid", "invalid release")
	ErrNotFound   = apigenfailure.New("not_found", "release not found")
	ErrConflict   = apigenfailure.New("conflict", "release conflict")
	ErrIncomplete = apigenfailure.New("incomplete", "release artifact is incomplete")
	ErrImmutable  = apigenfailure.New("immutable", "release is immutable")
	ErrDigest     = apigenfailure.New("digest_mismatch", "content digest mismatch")
)

type Status string

const (
	StatusDraft      Status = "draft"
	StatusValidating Status = "validating"
	StatusReady      Status = "ready"
	StatusFailed     Status = "failed"
)

type ConnectionPin struct {
	ConnectionID string `json:"connection"`
	RevisionID   string `json:"revisionId"`
}
type Manifest struct {
	Connections []ConnectionPin `json:"connections"`
}

// Release is one immutable project artifact bound to one exact serving
// identity. A release never contains workspace sets or target selectors.
type Release struct {
	ID                 string
	ProjectID          string
	Environment        string
	GenerationID       string
	ProjectDigest      string
	ArtifactDigest     string
	ActualDigest       string
	ArtifactSizeBytes  int64
	ArtifactUploadedAt string
	RequestDigest      string
	IdempotencyKey     string
	Status             Status
	Manifest           Manifest
	Provenance         *Provenance
	CreatedBy          string
	CreatedAt          string
	FinalizedAt        string
	Error              string
}

func (r Release) Identity() (projectgraph.ServingIdentity, error) {
	identity := projectgraph.ServingIdentity{ProjectID: projectgraph.ResourceID(r.ProjectID), Environment: r.Environment, GenerationID: r.GenerationID}
	if err := identity.Validate(); err != nil {
		return projectgraph.ServingIdentity{}, err
	}
	return identity, nil
}

type Artifact struct {
	ReleaseID      string
	ProjectID      string
	Environment    string
	GenerationID   string
	ExpectedDigest string
	ActualDigest   string
	SizeBytes      int64
	UploadedAt     string
}

func (a Artifact) Identity() (projectgraph.ServingIdentity, error) {
	identity := projectgraph.ServingIdentity{ProjectID: projectgraph.ResourceID(a.ProjectID), Environment: a.Environment, GenerationID: a.GenerationID}
	if err := identity.Validate(); err != nil {
		return projectgraph.ServingIdentity{}, err
	}
	return identity, nil
}

type CreateInput struct {
	ID             string
	ProjectID      string
	Environment    string
	GenerationID   string
	ProjectDigest  string
	ArtifactDigest string
	RequestDigest  string
	IdempotencyKey string
	CreatedBy      string
	Connections    []ConnectionPin
	Provenance     *Provenance
}

func (input CreateInput) Identity() (projectgraph.ServingIdentity, error) {
	if input.ProjectID == "" || input.Environment == "" || input.GenerationID == "" {
		return projectgraph.ServingIdentity{}, fmt.Errorf("project, environment, and generation are required")
	}
	identity := projectgraph.ServingIdentity{ProjectID: projectgraph.ResourceID(input.ProjectID), Environment: input.Environment, GenerationID: input.GenerationID}
	if err := identity.Validate(); err != nil {
		return projectgraph.ServingIdentity{}, err
	}
	return identity, nil
}
