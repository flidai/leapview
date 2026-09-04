// Package devloop owns coherent local project builds and synchronization of the
// last valid immutable result through an injected remote transport.
package devloop

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type Artifact struct {
	Path      string
	Digest    string
	SizeBytes int64
	Content   []byte
}

// SourceRevision is optional vendor-neutral change evidence. It deliberately
// does not participate in the candidate-set digest.
type SourceRevision struct {
	Revision   string
	Repository string
	Ref        string
	ChangeID   string
}

type Snapshot struct {
	ProjectID      projectgraph.ResourceID
	ProjectFile    string
	Digest         string
	Artifacts      []Artifact
	SourceRevision *SourceRevision
	CandidateKey   string
}

type Candidate struct {
	ID               string
	ProjectID        projectgraph.ResourceID
	OwnerID          string
	ArtifactDigest   string
	PreviewURL       string
	TargetID         string
	Environment      string
	ProvenanceDigest string
	Revision         int64
	// Native delivery transports return the plan that produced the sealed
	// candidate and its immutable execution evidence.
	PlanID          string
	PlanDigest      string
	ExecutionDigest string
	EvidenceDigest  string
}

type SyncRequest struct {
	Snapshot               Snapshot
	ExpectedCandidateID    string
	ExpectedArtifactDigest string
	SourceOnly             bool
}

type Builder interface {
	Build(context.Context) (Snapshot, error)
}

// Remote is a Project-owned port. Composition adapters may implement it with
// Deployment APIs, but this package does not import Deployment or Release.
type Remote interface {
	Synchronize(context.Context, SyncRequest) (Candidate, error)
}

type Status string

const (
	StatusSynchronized Status = "synchronized"
	StatusUnchanged    Status = "unchanged"
	StatusInvalid      Status = "invalid"
	StatusRetryable    Status = "retryable"
)

type Result struct {
	Status    Status
	Snapshot  Snapshot
	Candidate Candidate
}

type Service struct {
	mu        sync.Mutex
	builder   Builder
	remote    Remote
	snapshot  Snapshot
	candidate Candidate
}

func New(builder Builder, remote Remote) (*Service, error) {
	if builder == nil || remote == nil {
		return nil, fmt.Errorf("project dev loop requires builder and remote")
	}
	return &Service{builder: builder, remote: remote}, nil
}

// Reconcile builds a coherent snapshot before performing any remote mutation.
// Invalid or failed builds leave the last synchronized candidate untouched.
// The mutex also makes concurrent worktree/editor events idempotent inside one
// process; the remote port owns cross-process optimistic concurrency.
func (service *Service) Reconcile(ctx context.Context) (Result, error) {
	if service == nil {
		return Result{}, fmt.Errorf("project dev loop is not configured")
	}
	service.mu.Lock()
	defer service.mu.Unlock()

	snapshot, err := service.builder.Build(ctx)
	if err != nil {
		return service.result(StatusInvalid), err
	}
	snapshot, err = normalizeSnapshot(snapshot)
	if err != nil {
		return service.result(StatusInvalid), err
	}
	if service.candidate.ID != "" &&
		snapshot.Digest == service.snapshot.Digest &&
		equalSourceRevision(snapshot.SourceRevision, service.snapshot.SourceRevision) {
		result := service.result(StatusUnchanged)
		result.Snapshot = cloneSnapshot(snapshot)
		return result, nil
	}
	request := SyncRequest{
		Snapshot:               cloneSnapshot(snapshot),
		ExpectedCandidateID:    service.candidate.ID,
		ExpectedArtifactDigest: service.candidate.ArtifactDigest,
	}
	candidate, err := service.remote.Synchronize(ctx, request)
	if err != nil {
		result := service.result(StatusRetryable)
		result.Snapshot = cloneSnapshot(snapshot)
		return result, err
	}
	candidate, err = normalizeCandidate(candidate, snapshot)
	if err != nil {
		result := service.result(StatusRetryable)
		result.Snapshot = cloneSnapshot(snapshot)
		return result, err
	}
	service.snapshot = cloneSnapshot(snapshot)
	service.candidate = candidate
	return service.result(StatusSynchronized), nil
}

func (service *Service) result(status Status) Result {
	return Result{
		Status:    status,
		Snapshot:  cloneSnapshot(service.snapshot),
		Candidate: service.candidate,
	}
}

func normalizeSnapshot(snapshot Snapshot) (Snapshot, error) {
	snapshot.ProjectFile = strings.TrimSpace(snapshot.ProjectFile)
	snapshot.CandidateKey = strings.TrimSpace(snapshot.CandidateKey)
	snapshot.Digest = strings.TrimSpace(snapshot.Digest)
	if err := snapshot.ProjectID.Validate(); err != nil || !canonicalArtifactPath(snapshot.ProjectFile) || len(snapshot.Artifacts) == 0 {
		return Snapshot{}, fmt.Errorf("project snapshot requires project, canonical entrypoint, and artifacts")
	}
	if err := digest.ValidateSHA256Identity(snapshot.Digest); err != nil {
		return Snapshot{}, fmt.Errorf("project snapshot digest is invalid: %w", err)
	}
	seen := make(map[string]struct{}, len(snapshot.Artifacts))
	for index := range snapshot.Artifacts {
		artifact := &snapshot.Artifacts[index]
		artifact.Path = strings.TrimSpace(artifact.Path)
		artifact.Digest = strings.TrimSpace(artifact.Digest)
		if artifact.SizeBytes == 0 {
			artifact.SizeBytes = int64(len(artifact.Content))
		}
		if artifact.Path == "" {
			return Snapshot{}, fmt.Errorf("project snapshot artifact requires path")
		}
		if artifact.SizeBytes != int64(len(artifact.Content)) {
			return Snapshot{}, fmt.Errorf("project artifact %q size does not match content", artifact.Path)
		}
		if !canonicalArtifactPath(artifact.Path) {
			return Snapshot{}, fmt.Errorf("project artifact path %q is not a canonical relative path", artifact.Path)
		}
		if _, duplicate := seen[artifact.Path]; duplicate {
			return Snapshot{}, fmt.Errorf("project snapshot repeats path %q", artifact.Path)
		}
		seen[artifact.Path] = struct{}{}
		if err := digest.ValidateSHA256Identity(artifact.Digest); err != nil {
			return Snapshot{}, fmt.Errorf("project artifact %q digest is invalid: %w", artifact.Path, err)
		}
		if actual := contentArtifact(artifact.Path, artifact.Content).Digest; artifact.Digest != actual {
			return Snapshot{}, fmt.Errorf("project artifact %q content does not match digest", artifact.Path)
		}
	}
	sort.Slice(snapshot.Artifacts, func(i, j int) bool {
		return snapshot.Artifacts[i].Path < snapshot.Artifacts[j].Path
	})
	if _, exists := seen[snapshot.ProjectFile]; !exists {
		return Snapshot{}, fmt.Errorf("project snapshot entrypoint %q is not an artifact", snapshot.ProjectFile)
	}
	if actual := candidateSetDigest(snapshot.ProjectID, snapshot.ProjectFile, snapshot.Artifacts); snapshot.Digest != actual {
		return Snapshot{}, fmt.Errorf("project snapshot content does not match candidate-set digest")
	}
	var err error
	snapshot.SourceRevision, err = normalizeSourceRevision(snapshot.SourceRevision)
	if err != nil {
		return Snapshot{}, err
	}
	return cloneSnapshot(snapshot), nil
}

func normalizeCandidate(candidate Candidate, snapshot Snapshot) (Candidate, error) {
	candidate.ID = strings.TrimSpace(candidate.ID)
	candidate.OwnerID = strings.TrimSpace(candidate.OwnerID)
	candidate.ArtifactDigest = strings.TrimSpace(candidate.ArtifactDigest)
	candidate.PreviewURL = strings.TrimSpace(candidate.PreviewURL)
	candidate.TargetID = strings.TrimSpace(candidate.TargetID)
	candidate.Environment = strings.TrimSpace(candidate.Environment)
	candidate.ProvenanceDigest = strings.TrimSpace(candidate.ProvenanceDigest)
	candidate.PlanID = strings.TrimSpace(candidate.PlanID)
	candidate.PlanDigest = strings.TrimSpace(candidate.PlanDigest)
	candidate.ExecutionDigest = strings.TrimSpace(candidate.ExecutionDigest)
	candidate.EvidenceDigest = strings.TrimSpace(candidate.EvidenceDigest)
	if err := candidate.ProjectID.Validate(); err != nil {
		return Candidate{}, fmt.Errorf("remote candidate project identity is invalid: %w", err)
	}
	if candidate.ID == "" || candidate.OwnerID == "" || candidate.PreviewURL == "" ||
		candidate.TargetID == "" || candidate.Environment == "" ||
		candidate.Revision <= 0 ||
		candidate.ProjectID != snapshot.ProjectID ||
		candidate.ArtifactDigest != snapshot.Digest {
		return Candidate{}, fmt.Errorf("remote candidate does not match synchronized project snapshot")
	}
	if err := digest.ValidateSHA256Identity(candidate.ProvenanceDigest); err != nil {
		return Candidate{}, fmt.Errorf("remote candidate provenance digest is invalid: %w", err)
	}
	hasPlanEvidence := candidate.PlanID != "" || candidate.PlanDigest != "" ||
		candidate.ExecutionDigest != "" || candidate.EvidenceDigest != ""
	if hasPlanEvidence {
		if candidate.PlanID == "" {
			return Candidate{}, fmt.Errorf("remote candidate plan evidence is missing plan identity")
		}
		for name, value := range map[string]string{
			"plan":      candidate.PlanDigest,
			"execution": candidate.ExecutionDigest,
			"evidence":  candidate.EvidenceDigest,
		} {
			if err := digest.ValidateSHA256Identity(value); err != nil {
				return Candidate{}, fmt.Errorf("remote candidate %s digest is invalid: %w", name, err)
			}
		}
	}
	return candidate, nil
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	out := Snapshot{
		ProjectID: snapshot.ProjectID, ProjectFile: snapshot.ProjectFile,
		Digest: snapshot.Digest, Artifacts: make([]Artifact, len(snapshot.Artifacts)),
		CandidateKey: snapshot.CandidateKey,
	}
	if snapshot.SourceRevision != nil {
		copied := *snapshot.SourceRevision
		out.SourceRevision = &copied
	}
	for index, artifact := range snapshot.Artifacts {
		out.Artifacts[index] = Artifact{
			Path: artifact.Path, Digest: artifact.Digest,
			SizeBytes: artifact.SizeBytes,
			Content:   append([]byte(nil), artifact.Content...),
		}
	}
	return out
}

func normalizeSourceRevision(value *SourceRevision) (*SourceRevision, error) {
	if value == nil {
		return nil, nil
	}
	normalized := *value
	normalized.Revision = strings.TrimSpace(normalized.Revision)
	normalized.Repository = strings.TrimSpace(normalized.Repository)
	normalized.Ref = strings.TrimSpace(normalized.Ref)
	normalized.ChangeID = strings.TrimSpace(normalized.ChangeID)
	if normalized.Revision == "" {
		return nil, fmt.Errorf("source revision requires a revision")
	}
	return &normalized, nil
}

func equalSourceRevision(first, second *SourceRevision) bool {
	if first == nil || second == nil {
		return first == second
	}
	return *first == *second
}

func canonicalArtifactPath(value string) bool {
	return value != "" &&
		!path.IsAbs(value) &&
		path.Clean(value) == value &&
		value != ".." &&
		!strings.HasPrefix(value, "../") &&
		!strings.Contains(value, `\`)
}
