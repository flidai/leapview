// Package release models immutable, project-wide deployment releases.
package release

import apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"

var (
	ErrInvalid    = apigenfailure.New("invalid", "invalid release")
	ErrNotFound   = apigenfailure.New("not_found", "release not found")
	ErrConflict   = apigenfailure.New("conflict", "release conflict")
	ErrIncomplete = apigenfailure.New("incomplete", "release artifacts are incomplete")
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

type WorkspaceManifest struct {
	WorkspaceID    string `json:"workspace"`
	ArtifactDigest string `json:"artifactDigest"`
	ServingStateID string `json:"servingStateId,omitempty"`
}

type ConnectionPin struct {
	ConnectionID string `json:"connection"`
	RevisionID   string `json:"revisionId"`
}

type Manifest struct {
	Workspaces  []WorkspaceManifest `json:"workspaces"`
	Connections []ConnectionPin     `json:"connections"`
}

type CreateInput struct {
	ID             string
	ProjectID      string
	ProjectDigest  string
	RequestDigest  string
	IdempotencyKey string
	CreatedBy      string
	Workspaces     []WorkspaceManifest
	Connections    []ConnectionPin
	Provenance     *Provenance
}

type Artifact struct {
	ReleaseID      string
	WorkspaceID    string
	ExpectedDigest string
	ServingStateID string
	ActualDigest   string
	SizeBytes      int64
	UploadedAt     string
}

type Release struct {
	ID             string
	ProjectID      string
	ProjectDigest  string
	RequestDigest  string
	IdempotencyKey string
	Status         Status
	Manifest       Manifest
	Artifacts      []Artifact
	Provenance     *Provenance
	CreatedBy      string
	CreatedAt      string
	FinalizedAt    string
	Error          string
}
