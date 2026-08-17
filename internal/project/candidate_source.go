package project

import (
	"context"
	"errors"
	"io"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrCandidateSourceUnavailable = errors.New("candidate source synchronization unavailable")
	ErrCandidateSourceInvalid     = errors.New("candidate source synchronization invalid")
	ErrCandidateSourceConflict    = errors.New("candidate source synchronization conflict")
)

type CandidateSourceArtifact struct {
	Path   string
	Digest string
}

type CandidateSourceRevision struct {
	Revision   string
	Repository string
	Ref        string
	ChangeID   string
}

type CandidateSynchronizationRequest struct {
	ProjectFile            string
	ArtifactDigest         string
	CandidateKey           string
	ExpectedCandidateID    string
	ExpectedArtifactDigest string
	Artifacts              []CandidateSourceArtifact
	SourceRevision         *CandidateSourceRevision
}

type CandidateSourceScope struct {
	ProjectID    projectgraph.ResourceID
	OwnerID      string
	CandidateKey string
}

type CandidateSourceSnapshot struct {
	ProjectID           projectgraph.ResourceID
	ArtifactDigest      string
	ProjectPath         string
	ProjectDigest       string
	ProjectArtifactPath string
	SourceRevision      *CandidateSourceRevision
}

// CandidateSourceSynchronizer owns target-side retention and compiler
// validation for environment-neutral project sources.
type CandidateSourceSynchronizer interface {
	Plan(context.Context, CandidateSourceScope, CandidateSynchronizationRequest) ([]string, error)
	Upload(context.Context, CandidateSourceScope, string, io.Reader) error
	Commit(context.Context, CandidateSourceScope, CandidateSynchronizationRequest) (CandidateSourceSnapshot, error)
}
