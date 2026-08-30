package devloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"golang.org/x/sync/errgroup"
)

type ArtifactReference struct {
	Path      string
	Digest    string
	SizeBytes int64
}

type SynchronizationPlanRequest struct {
	ProjectID      projectgraph.ResourceID
	ProjectFile    string
	ArtifactDigest string
	// SourceOnly asks the target to retain bytes without candidate preparation.
	// It is used by canonical delivery plan before Build performs physical work.
	SourceOnly             bool
	CandidateKey           string
	ExpectedCandidateID    string
	ExpectedArtifactDigest string
	PlanID                 string
	IdempotencyKey         string
	Artifacts              []ArtifactReference
	SourceRevision         *SourceRevision
}

type SynchronizationPlan struct {
	PlanID         string
	MissingDigests []string
}

// SynchronizationTransport is the target-facing protocol port. Plan transfers
// only the immutable source manifest, Upload transfers target-requested blobs,
// and Commit atomically advances the owned private candidate.
type SynchronizationTransport interface {
	Plan(context.Context, SynchronizationPlanRequest) (SynchronizationPlan, error)
	Upload(context.Context, SynchronizationPlanRequest, Artifact) error
	Commit(context.Context, SynchronizationPlanRequest) (Candidate, error)
}

// RetainedSource is the target-verified identity returned by the source-only
// retention command. It intentionally has no candidate or physical storage
// authority.
type RetainedSource struct {
	ProjectID               projectgraph.ResourceID
	SourceDigest            string
	SourceAttestationDigest string
	ProjectDigest           string
	TargetID                string
	Environment             string
}

type SourceRetentionTransport interface {
	RetainSource(context.Context, SynchronizationPlanRequest) (RetainedSource, error)
}

type TransportRemote struct {
	transport          SynchronizationTransport
	maxParallelUploads int
}

func NewTransportRemote(transport SynchronizationTransport, maxParallelUploads int) (*TransportRemote, error) {
	if transport == nil || maxParallelUploads < 1 || maxParallelUploads > 16 {
		return nil, fmt.Errorf("project synchronization requires a transport and 1-16 parallel uploads")
	}
	return &TransportRemote{transport: transport, maxParallelUploads: maxParallelUploads}, nil
}

func (remote *TransportRemote) Synchronize(ctx context.Context, request SyncRequest) (Candidate, error) {
	if remote == nil || remote.transport == nil {
		return Candidate{}, fmt.Errorf("project synchronization transport is not configured")
	}
	snapshot, err := normalizeSnapshot(request.Snapshot)
	if err != nil {
		return Candidate{}, err
	}
	planRequest := SynchronizationPlanRequest{
		ProjectID:              snapshot.ProjectID,
		ProjectFile:            snapshot.ProjectFile,
		ArtifactDigest:         snapshot.Digest,
		SourceOnly:             request.SourceOnly,
		CandidateKey:           snapshot.CandidateKey,
		ExpectedCandidateID:    strings.TrimSpace(request.ExpectedCandidateID),
		ExpectedArtifactDigest: strings.TrimSpace(request.ExpectedArtifactDigest),
		Artifacts:              make([]ArtifactReference, len(snapshot.Artifacts)),
		SourceRevision:         snapshot.SourceRevision,
	}
	artifactsByDigest := make(map[string]Artifact, len(snapshot.Artifacts))
	for index, artifact := range snapshot.Artifacts {
		planRequest.Artifacts[index] = ArtifactReference{Path: artifact.Path, Digest: artifact.Digest, SizeBytes: artifact.SizeBytes}
		if _, exists := artifactsByDigest[artifact.Digest]; !exists {
			artifactsByDigest[artifact.Digest] = artifact
		}
	}
	plan, err := remote.transport.Plan(ctx, clonePlanRequest(planRequest))
	if err != nil {
		return Candidate{}, fmt.Errorf("plan project synchronization: %w", err)
	}
	if strings.TrimSpace(plan.PlanID) == "" {
		return Candidate{}, fmt.Errorf("target synchronization plan did not return a plan id")
	}
	planRequest.PlanID = strings.TrimSpace(plan.PlanID)
	missing, err := missingArtifacts(plan.MissingDigests, artifactsByDigest)
	if err != nil {
		return Candidate{}, err
	}
	group, uploadContext := errgroup.WithContext(ctx)
	group.SetLimit(remote.maxParallelUploads)
	for _, artifact := range missing {
		artifact := artifact
		group.Go(func() error {
			if err := remote.transport.Upload(uploadContext, clonePlanRequest(planRequest), artifact); err != nil {
				return fmt.Errorf("upload project artifact %q: %w", artifact.Path, err)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return Candidate{}, err
	}
	candidate, err := remote.transport.Commit(ctx, clonePlanRequest(planRequest))
	if err != nil {
		return Candidate{}, fmt.Errorf("commit project synchronization: %w", err)
	}
	return candidate, nil
}

// RetainSource captures, uploads, and commits one exact source snapshot while
// explicitly avoiding candidate preparation. The target source endpoint owns
// byte retention and returns the verified portable digest.
func (remote *TransportRemote) RetainSource(ctx context.Context, snapshot Snapshot) (RetainedSource, error) {
	if remote == nil || remote.transport == nil {
		return RetainedSource{}, fmt.Errorf("project source retention transport is not configured")
	}
	snapshot, err := normalizeSnapshot(snapshot)
	if err != nil {
		return RetainedSource{}, err
	}
	if retainer, ok := remote.transport.(SourceRetentionTransport); ok {
		planRequest := SynchronizationPlanRequest{ProjectID: snapshot.ProjectID, ProjectFile: snapshot.ProjectFile, ArtifactDigest: snapshot.Digest, SourceOnly: true, CandidateKey: snapshot.CandidateKey, Artifacts: make([]ArtifactReference, len(snapshot.Artifacts)), SourceRevision: snapshot.SourceRevision}
		artifactsByDigest := make(map[string]Artifact, len(snapshot.Artifacts))
		for index, artifact := range snapshot.Artifacts {
			planRequest.Artifacts[index] = ArtifactReference{Path: artifact.Path, Digest: artifact.Digest, SizeBytes: artifact.SizeBytes}
			if _, exists := artifactsByDigest[artifact.Digest]; !exists {
				artifactsByDigest[artifact.Digest] = artifact
			}
		}
		plan, err := remote.transport.Plan(ctx, clonePlanRequest(planRequest))
		if err != nil {
			return RetainedSource{}, fmt.Errorf("plan project source retention: %w", err)
		}
		if strings.TrimSpace(plan.PlanID) == "" {
			return RetainedSource{}, fmt.Errorf("target source retention plan did not return a plan id")
		}
		planRequest.PlanID = strings.TrimSpace(plan.PlanID)
		missing, err := missingArtifacts(plan.MissingDigests, artifactsByDigest)
		if err != nil {
			return RetainedSource{}, err
		}
		for _, artifact := range missing {
			if err := remote.transport.Upload(ctx, clonePlanRequest(planRequest), artifact); err != nil {
				return RetainedSource{}, fmt.Errorf("upload project source %q: %w", artifact.Path, err)
			}
		}
		return retainer.RetainSource(ctx, clonePlanRequest(planRequest))
	}
	return RetainedSource{}, fmt.Errorf("target does not implement source-only retention")
}

func missingArtifacts(digests []string, available map[string]Artifact) ([]Artifact, error) {
	missing := make([]Artifact, 0, len(digests))
	seen := make(map[string]struct{}, len(digests))
	for _, value := range digests {
		value = strings.TrimSpace(value)
		if err := digest.ValidateSHA256Identity(value); err != nil {
			return nil, fmt.Errorf("target requested invalid project artifact digest: %w", err)
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		artifact, ok := available[value]
		if !ok {
			return nil, fmt.Errorf("target requested project artifact outside synchronized snapshot")
		}
		missing = append(missing, artifact)
	}
	return missing, nil
}

func clonePlanRequest(request SynchronizationPlanRequest) SynchronizationPlanRequest {
	out := request
	out.Artifacts = append([]ArtifactReference(nil), request.Artifacts...)
	if request.SourceRevision != nil {
		copied := *request.SourceRevision
		out.SourceRevision = &copied
	}
	return out
}
