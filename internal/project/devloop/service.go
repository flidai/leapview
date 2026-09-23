// Package devloop owns coherent local project builds and synchronization of the
// last valid immutable result through an injected remote transport.
package devloop

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/project/developmentsession"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	configschema "github.com/flidai/leapview/internal/project/schema"
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
	ProjectID projectgraph.ResourceID
	Digest    string
	// GraphDigest is the whole compiled graph identity observed from the same
	// immutable source capture as Digest. It is local orchestration evidence
	// and is not substituted for the source artifact digest on transport.
	GraphDigest    string
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
	Snapshot   Snapshot
	SourceOnly bool
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
	mu           sync.Mutex
	builder      Builder
	remote       Remote
	snapshot     Snapshot
	candidate    Candidate
	sessionStore developmentsession.Store
	sessionKey   developmentsession.Key
}

func New(builder Builder, remote Remote) (*Service, error) {
	if builder == nil || remote == nil {
		return nil, fmt.Errorf("project dev loop requires builder and remote")
	}
	return &Service{builder: builder, remote: remote}, nil
}

// NewWithSession enables the durable owner-scoped checkpoint for this loop.
// Existing callers may continue using New; no process-local state is treated
// as authoritative when this option is configured.
func NewWithSession(builder Builder, remote Remote, store developmentsession.Store, key developmentsession.Key) (*Service, error) {
	service, err := New(builder, remote)
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, fmt.Errorf("development session store is required")
	}
	if err := key.Validate(); err != nil {
		return nil, err
	}
	service.sessionStore, service.sessionKey = store, key
	return service, nil
}

// SessionPreviewURL returns the stable pointer URL. It intentionally never
// incorporates a candidate ID; callers resolve the pointer and then use the
// exact immutable candidate URL returned by the candidate authority.
func (service *Service) SessionPreviewURL(origin string) string {
	if service == nil || service.sessionStore == nil {
		return ""
	}
	return service.sessionKey.PreviewURL(origin)
}

// Reconcile builds a coherent snapshot before performing any remote mutation.
// Invalid or failed builds leave the last synchronized candidate untouched.
// The mutex also makes concurrent worktree/editor events idempotent inside one
// process; the remote source/plan protocol owns cross-process idempotency.
func (service *Service) Reconcile(ctx context.Context) (Result, error) {
	if service == nil {
		return Result{}, fmt.Errorf("project dev loop is not configured")
	}
	service.mu.Lock()
	defer service.mu.Unlock()

	preAttemptRevision, err := service.markSessionPreAttempt(ctx)
	if err != nil {
		return service.result(StatusRetryable), err
	}
	snapshot, err := service.builder.Build(ctx)
	if err != nil {
		service.recordSessionFailure(ctx, preAttemptRevision, developmentsession.Identity{}, err)
		return service.result(StatusInvalid), err
	}
	snapshot, err = normalizeSnapshot(snapshot)
	if err != nil {
		service.recordSessionFailure(ctx, preAttemptRevision, developmentsession.Identity{}, err)
		return service.result(StatusInvalid), err
	}
	if service.candidate.ID != "" &&
		snapshot.Digest == service.snapshot.Digest &&
		equalSourceRevision(snapshot.SourceRevision, service.snapshot.SourceRevision) {
		result := service.result(StatusUnchanged)
		result.Snapshot = cloneSnapshot(snapshot)
		return result, nil
	}
	attemptRevision, err := service.markSessionAttempt(ctx, snapshot, preAttemptRevision)
	if err != nil {
		return service.result(StatusRetryable), err
	}
	request := SyncRequest{Snapshot: cloneSnapshot(snapshot)}
	candidate, err := service.remote.Synchronize(ctx, request)
	if err != nil {
		service.recordSessionFailure(ctx, attemptRevision, sessionIdentity(snapshot), err)
		result := service.result(StatusRetryable)
		result.Snapshot = cloneSnapshot(snapshot)
		return result, err
	}
	candidate, err = normalizeCandidate(candidate, snapshot)
	if err != nil {
		service.recordSessionFailure(ctx, attemptRevision, sessionIdentity(snapshot), err)
		result := service.result(StatusRetryable)
		result.Snapshot = cloneSnapshot(snapshot)
		return result, err
	}
	if err := service.markSessionValid(ctx, attemptRevision, snapshot, candidate); err != nil {
		// A newer process may have advanced the pointer while this remote
		// operation was in flight. The obsolete completion must not replace it.
		return service.result(StatusRetryable), err
	}
	service.snapshot = cloneSnapshot(snapshot)
	service.candidate = candidate
	return service.result(StatusSynchronized), nil
}

func sessionIdentity(snapshot Snapshot) developmentsession.Identity {
	return developmentsession.Identity{ArtifactDigest: snapshot.Digest, GraphDigest: snapshot.GraphDigest}
}

func (service *Service) markSessionPreAttempt(ctx context.Context) (int64, error) {
	if service.sessionStore == nil {
		return 0, nil
	}
	record, err := service.sessionStore.Resolve(ctx, service.sessionKey)
	expected := int64(0)
	if errors.Is(err, developmentsession.ErrNotFound) {
		record = developmentsession.Record{ID: service.sessionKey.ID(), Key: service.sessionKey}
	} else if err != nil {
		return 0, err
	} else {
		expected = record.Revision
	}
	record.Diagnostics = nil
	updated, err := service.sessionStore.Save(ctx, record, expected)
	if err != nil {
		return 0, err
	}
	return updated.Revision, nil
}

func (service *Service) markSessionAttempt(ctx context.Context, snapshot Snapshot, expected int64) (int64, error) {
	if service.sessionStore == nil {
		return 0, nil
	}
	record, err := service.sessionStore.Resolve(ctx, service.sessionKey)
	if err != nil {
		return 0, err
	}
	if record.Revision != expected {
		return 0, developmentsession.ErrConflict
	}
	record.Attempted, record.Diagnostics = sessionIdentity(snapshot), nil
	updated, err := service.sessionStore.Save(ctx, record, expected)
	if err != nil {
		return 0, err
	}
	return updated.Revision, nil
}

func (service *Service) markSessionValid(ctx context.Context, expected int64, snapshot Snapshot, candidate Candidate) error {
	if service.sessionStore == nil {
		return nil
	}
	record, err := service.sessionStore.Resolve(ctx, service.sessionKey)
	if err != nil {
		return err
	}
	if record.Revision != expected {
		return developmentsession.ErrConflict
	}
	record.Attempted = sessionIdentity(snapshot)
	record.LastValid = developmentsession.Identity{CandidateID: candidate.ID, ArtifactDigest: candidate.ArtifactDigest, GraphDigest: snapshot.GraphDigest, PreviewURL: candidate.PreviewURL}
	record.Diagnostics = nil
	_, err = service.sessionStore.Save(ctx, record, expected)
	return err
}

func (service *Service) recordSessionFailure(ctx context.Context, expected int64, attempted developmentsession.Identity, syncErr error) {
	if service.sessionStore == nil {
		return
	}
	diagnostics := sessionDiagnostics(syncErr)
	record, err := service.sessionStore.Resolve(ctx, service.sessionKey)
	if err != nil || record.Revision != expected {
		return
	}
	record.Attempted, record.Diagnostics = attempted, diagnostics
	_, _ = service.sessionStore.Save(ctx, record, expected)
}

// sessionDiagnostics preserves safe authoring locations for the browser/API
// while applying the session package's redaction and size bounds. The schema
// package already understands CUE/YAML/compiler errors, so this keeps one
// diagnostic interpretation for CLI output and durable session state.
func sessionDiagnostics(err error) []developmentsession.Diagnostic {
	if err == nil {
		return nil
	}
	values := configschema.Diagnostics(err)
	result := make([]developmentsession.Diagnostic, 0, len(values))
	for _, value := range values {
		code := strings.TrimSpace(value.Code)
		if code == "" {
			code = "DEVELOPMENT_SYNC_ERROR"
		}
		result = append(result, developmentsession.Diagnostic{
			Code: code, Message: value.Message, Path: value.File,
			Line: value.Line, Column: value.Column,
		})
	}
	if len(result) == 0 {
		result = append(result, developmentsession.Diagnostic{
			Code: "DEVELOPMENT_SYNC_ERROR", Message: err.Error(),
		})
	}
	if len(result) > 64 {
		result = result[:64]
	}
	return result
}

func (service *Service) result(status Status) Result {
	return Result{
		Status:    status,
		Snapshot:  cloneSnapshot(service.snapshot),
		Candidate: service.candidate,
	}
}

func normalizeSnapshot(snapshot Snapshot) (Snapshot, error) {
	snapshot.CandidateKey = strings.TrimSpace(snapshot.CandidateKey)
	snapshot.Digest = strings.TrimSpace(snapshot.Digest)
	snapshot.GraphDigest = strings.TrimSpace(snapshot.GraphDigest)
	if err := snapshot.ProjectID.Validate(); err != nil || len(snapshot.Artifacts) == 0 {
		return Snapshot{}, fmt.Errorf("project snapshot requires target Project identity and artifacts")
	}
	if err := digest.ValidateSHA256Identity(snapshot.Digest); err != nil {
		return Snapshot{}, fmt.Errorf("project snapshot digest is invalid: %w", err)
	}
	if snapshot.GraphDigest != "" {
		if err := digest.ValidateSHA256Identity(snapshot.GraphDigest); err != nil {
			return Snapshot{}, fmt.Errorf("project snapshot graph digest is invalid: %w", err)
		}
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
	if actual := candidateSetDigest(snapshot.Artifacts); snapshot.Digest != actual {
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
		ProjectID: snapshot.ProjectID,
		Digest:    snapshot.Digest, GraphDigest: snapshot.GraphDigest, Artifacts: make([]Artifact, len(snapshot.Artifacts)),
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
		value != "." &&
		value != ".." &&
		!strings.HasPrefix(value, "../") &&
		!strings.Contains(value, `\`)
}
