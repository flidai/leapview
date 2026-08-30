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
	Path      string
	Digest    string
	SizeBytes int64
}

type CandidateSourceRevision struct {
	Revision   string
	Repository string
	Ref        string
	ChangeID   string
}

type CandidateSynchronizationRequest struct {
	ProjectFile    string
	ArtifactDigest string
	// SourceOnly retains the immutable source snapshot without invoking
	// candidate preparation or any physical writer. Delivery plan callers set
	// this explicitly so Build remains the first physical operation.
	SourceOnly             bool
	CandidateKey           string
	ExpectedCandidateID    string
	ExpectedArtifactDigest string
	PlanID                 string
	IdempotencyKey         string
	Artifacts              []CandidateSourceArtifact
	SourceRevision         *CandidateSourceRevision
}

type CandidateSourceScope struct {
	ProjectID    projectgraph.ResourceID
	OwnerID      string
	CandidateKey string
}

type CandidateSourceSnapshot struct {
	ProjectID               projectgraph.ResourceID
	ArtifactDigest          string
	SourceAttestationDigest string
	ProjectPath             string
	ProjectDigest           string
	ProjectArtifactPath     string
	SourceRevision          *CandidateSourceRevision
}

// CandidateSourceSynchronizer owns target-side retention and compiler
// validation for environment-neutral project sources.
type CandidateSourceSynchronizer interface {
	Plan(context.Context, CandidateSourceScope, CandidateSynchronizationRequest) (CandidateSynchronizationPlan, error)
	Upload(context.Context, CandidateSourceScope, string, string, io.Reader) error
	Commit(context.Context, CandidateSourceScope, CandidateSynchronizationRequest) (CandidateSourceSnapshot, error)
}

// CandidateSynchronizationPlan is the durable target-owned synchronization
// command result. PlanID is opaque and must be presented on every subsequent
// upload, retention, or commit operation.
type CandidateSynchronizationPlan struct {
	PlanID         string
	ArtifactDigest string
	MissingDigests []string
}

// CandidateSourceSnapshotReader resolves one already-retained immutable
// source snapshot by its content digest. Delivery planning uses this narrow
// read port instead of accepting a mutable worktree path or recapturing source
// bytes after the synchronization command has committed them.
type CandidateSourceSnapshotReader interface {
	Snapshot(context.Context, CandidateSourceScope, string) (CandidateSourceSnapshot, error)
}

// CandidateSourceAttestationReader resolves one exact provenance attestation
// for a retained byte snapshot. The attestation is opaque to callers and is
// checked against the immutable target-side record before it is returned.
type CandidateSourceAttestationReader interface {
	SnapshotAttestation(context.Context, CandidateSourceScope, string, string) (CandidateSourceSnapshot, error)
}
