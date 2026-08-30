package module

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/platform/digest"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/internal/project"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const candidateSourcePlanLifetime = 5 * time.Minute
const candidateSourcePlanVersion = 2

type candidateSourceSynchronizer struct {
	store   *projectdevloop.TargetStore
	planDir string
	now     func() time.Time
	mu      sync.Mutex
	plans   map[candidateSourcePlanKey]candidateSourcePlan
}

type candidateSourcePlanKey struct {
	projectID      projectgraph.ResourceID
	ownerID        string
	candidateKey   string
	artifactDigest string
	idempotencyKey string
}

type candidateSourcePlan struct {
	planID        string
	requestDigest string
	expiresAt     time.Time
	missing       map[string]struct{}
	sizes         map[string]int64
}

type candidateSourcePlanRecord struct {
	Version        int                     `json:"version"`
	ProjectID      projectgraph.ResourceID `json:"projectId"`
	OwnerID        string                  `json:"ownerId"`
	CandidateKey   string                  `json:"candidateKey,omitempty"`
	IdempotencyKey string                  `json:"idempotencyKey,omitempty"`
	ArtifactDigest string                  `json:"artifactDigest"`
	PlanID         string                  `json:"planId"`
	RequestDigest  string                  `json:"requestDigest"`
	ExpiresAt      string                  `json:"expiresAt"`
	Missing        []string                `json:"missing"`
	Sizes          map[string]int64        `json:"sizes"`
}

func NewCandidateSourceSynchronizer(root string) (project.CandidateSourceSynchronizer, error) {
	store, err := projectdevloop.NewTargetStore(root)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", project.ErrCandidateSourceUnavailable, err)
	}
	synchronizer := &candidateSourceSynchronizer{
		store: store, planDir: filepath.Join(root, ".synchronization-plans"),
		now: time.Now, plans: make(map[candidateSourcePlanKey]candidateSourcePlan),
	}
	if err := os.MkdirAll(synchronizer.planDir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create source synchronization plan store: %v", project.ErrCandidateSourceUnavailable, err)
	}
	if err := synchronizer.loadPlans(); err != nil {
		return nil, fmt.Errorf("%w: load source synchronization plans: %v", project.ErrCandidateSourceUnavailable, err)
	}
	return synchronizer, nil
}

func (synchronizer *candidateSourceSynchronizer) Plan(
	ctx context.Context,
	scope project.CandidateSourceScope,
	request project.CandidateSynchronizationRequest,
) (project.CandidateSynchronizationPlan, error) {
	if synchronizer == nil || synchronizer.store == nil {
		return project.CandidateSynchronizationPlan{}, project.ErrCandidateSourceUnavailable
	}
	if err := scope.ProjectID.Validate(); err != nil {
		return project.CandidateSynchronizationPlan{}, candidateSourceInvalid(fmt.Errorf("project identity: %w", err))
	}
	scope.OwnerID = strings.TrimSpace(scope.OwnerID)
	if scope.OwnerID == "" {
		return project.CandidateSynchronizationPlan{}, candidateSourceInvalid(fmt.Errorf("owner identity is required"))
	}
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.IdempotencyKey == "" {
		return project.CandidateSynchronizationPlan{}, fmt.Errorf("%w: synchronization plan requires an idempotency key", project.ErrCandidateSourceInvalid)
	}
	missing, err := synchronizer.store.Missing(ctx, synchronizationPlanRequest(scope, request))
	if err != nil {
		return project.CandidateSynchronizationPlan{}, candidateSourceInvalid(err)
	}
	synchronizer.mu.Lock()
	defer synchronizer.mu.Unlock()
	synchronizer.purgeExpiredLocked()
	allowed := make(map[string]struct{}, len(missing))
	sizes := make(map[string]int64, len(request.Artifacts))
	for _, artifact := range request.Artifacts {
		sizes[artifact.Digest] = artifact.SizeBytes
	}
	for _, identity := range missing {
		allowed[identity] = struct{}{}
	}
	key := candidateSourcePlanKey{
		projectID: scope.ProjectID, ownerID: strings.TrimSpace(scope.OwnerID),
		candidateKey:   normalizeCandidateSourceKey(scope.CandidateKey),
		artifactDigest: strings.TrimSpace(request.ArtifactDigest),
		idempotencyKey: strings.TrimSpace(request.IdempotencyKey),
	}
	requestDigest := candidateSourceRequestDigest(request)
	if requestDigest == "" {
		return project.CandidateSynchronizationPlan{}, candidateSourceInvalid(fmt.Errorf("synchronization request identity is invalid"))
	}
	if existing, ok := synchronizer.plans[key]; ok {
		if existing.requestDigest != requestDigest {
			return project.CandidateSynchronizationPlan{}, fmt.Errorf("%w: idempotency key was reused for a different synchronization request", project.ErrCandidateSourceConflict)
		}
		return project.CandidateSynchronizationPlan{PlanID: existing.planID, ArtifactDigest: request.ArtifactDigest, MissingDigests: sortedPlanMissing(existing.missing)}, nil
	}
	planID, err := newCandidateSourcePlanID()
	if err != nil {
		return project.CandidateSynchronizationPlan{}, fmt.Errorf("%w: create source synchronization plan: %v", project.ErrCandidateSourceUnavailable, err)
	}
	plan := candidateSourcePlan{planID: planID, requestDigest: requestDigest,
		expiresAt: synchronizer.now().UTC().Add(candidateSourcePlanLifetime), missing: allowed, sizes: sizes,
	}
	if err := synchronizer.savePlan(key, plan); err != nil {
		return project.CandidateSynchronizationPlan{}, fmt.Errorf("%w: persist source synchronization plan: %v", project.ErrCandidateSourceUnavailable, err)
	}
	synchronizer.plans[key] = plan
	return project.CandidateSynchronizationPlan{PlanID: planID, ArtifactDigest: request.ArtifactDigest, MissingDigests: missing}, nil
}

func (synchronizer *candidateSourceSynchronizer) Upload(
	ctx context.Context,
	scope project.CandidateSourceScope,
	planID string,
	identity string,
	source io.Reader,
) error {
	if synchronizer == nil || synchronizer.store == nil {
		return project.ErrCandidateSourceUnavailable
	}
	if err := scope.ProjectID.Validate(); err != nil {
		return candidateSourceInvalid(fmt.Errorf("project identity: %w", err))
	}
	projectID := scope.ProjectID
	ownerID := strings.TrimSpace(scope.OwnerID)
	if ownerID == "" {
		return candidateSourceInvalid(fmt.Errorf("owner identity is required"))
	}
	planID = strings.TrimSpace(planID)
	identity = strings.TrimSpace(identity)
	synchronizer.mu.Lock()
	synchronizer.purgeExpiredLocked()
	authorized := false
	var expectedSize int64
	for key, plan := range synchronizer.plans {
		if key.projectID != projectID || key.ownerID != ownerID || plan.planID != planID {
			continue
		}
		if _, exists := plan.missing[identity]; exists {
			authorized = true
			expectedSize = plan.sizes[identity]
			break
		}
	}
	synchronizer.mu.Unlock()
	if !authorized {
		return fmt.Errorf(
			"%w: source blob was not requested by an active synchronization plan",
			project.ErrCandidateSourceConflict,
		)
	}
	content, err := io.ReadAll(io.LimitReader(source, maxCandidateSourceUploadBytes+1))
	if err != nil {
		return candidateSourceInvalid(err)
	}
	if int64(len(content)) != expectedSize {
		return fmt.Errorf("%w: source blob size does not match synchronization plan", project.ErrCandidateSourceConflict)
	}
	if err := synchronizer.store.Put(ctx, identity, bytes.NewReader(content)); err != nil {
		return candidateSourceInvalid(err)
	}
	return nil
}

func (synchronizer *candidateSourceSynchronizer) Commit(
	ctx context.Context,
	scope project.CandidateSourceScope,
	request project.CandidateSynchronizationRequest,
) (project.CandidateSourceSnapshot, error) {
	if synchronizer == nil || synchronizer.store == nil {
		return project.CandidateSourceSnapshot{}, project.ErrCandidateSourceUnavailable
	}
	if err := scope.ProjectID.Validate(); err != nil {
		return project.CandidateSourceSnapshot{}, candidateSourceInvalid(fmt.Errorf("project identity: %w", err))
	}
	if strings.TrimSpace(scope.OwnerID) == "" {
		return project.CandidateSourceSnapshot{}, candidateSourceInvalid(fmt.Errorf("owner identity is required"))
	}
	request.PlanID = strings.TrimSpace(request.PlanID)
	if request.PlanID == "" {
		return project.CandidateSourceSnapshot{}, fmt.Errorf("%w: source synchronization plan id is required", project.ErrCandidateSourceConflict)
	}
	synchronizer.mu.Lock()
	synchronizer.purgeExpiredLocked()
	requestDigest := candidateSourceRequestDigest(request)
	var plan candidateSourcePlan
	ok := false
	for candidateKey, candidatePlan := range synchronizer.plans {
		if candidateKey.projectID == scope.ProjectID && candidateKey.ownerID == strings.TrimSpace(scope.OwnerID) &&
			candidateKey.candidateKey == normalizeCandidateSourceKey(scope.CandidateKey) && candidateKey.artifactDigest == strings.TrimSpace(request.ArtifactDigest) &&
			candidatePlan.planID == request.PlanID {
			plan, ok = candidatePlan, true
			break
		}
	}
	synchronizer.mu.Unlock()
	if !ok || plan.requestDigest != requestDigest {
		return project.CandidateSourceSnapshot{}, fmt.Errorf("%w: source synchronization plan is missing or does not match request", project.ErrCandidateSourceConflict)
	}
	stored, err := synchronizer.store.Commit(ctx, synchronizationPlanRequest(scope, request))
	if err != nil {
		return project.CandidateSourceSnapshot{}, candidateSourceInvalid(err)
	}
	return project.CandidateSourceSnapshot{
		ProjectID: stored.ProjectID, ArtifactDigest: stored.Digest,
		SourceAttestationDigest: stored.SourceAttestationDigest,
		ProjectPath:             stored.ProjectPath, ProjectDigest: stored.ProjectDigest,
		ProjectArtifactPath: stored.ProjectArtifactPath,
		SourceRevision:      cloneCandidateSourceRevisionFromDevloop(stored.SourceRevision),
	}, nil
}

// Snapshot resolves an immutable, already committed source set. The target
// store re-reads and validates the retained manifest and every source blob so
// callers receive evidence for precisely the requested digest.
func (synchronizer *candidateSourceSynchronizer) Snapshot(
	ctx context.Context,
	scope project.CandidateSourceScope,
	artifactDigest string,
) (project.CandidateSourceSnapshot, error) {
	if synchronizer == nil || synchronizer.store == nil {
		return project.CandidateSourceSnapshot{}, project.ErrCandidateSourceUnavailable
	}
	if err := scope.ProjectID.Validate(); err != nil {
		return project.CandidateSourceSnapshot{}, candidateSourceInvalid(fmt.Errorf("project identity: %w", err))
	}
	stored, err := synchronizer.store.Snapshot(ctx, scope.ProjectID, artifactDigest)
	if err != nil {
		return project.CandidateSourceSnapshot{}, candidateSourceInvalid(err)
	}
	return project.CandidateSourceSnapshot{
		ProjectID: stored.ProjectID, ArtifactDigest: stored.Digest,
		SourceAttestationDigest: stored.SourceAttestationDigest,
		ProjectPath:             stored.ProjectPath, ProjectDigest: stored.ProjectDigest,
		ProjectArtifactPath: stored.ProjectArtifactPath,
		SourceRevision:      cloneCandidateSourceRevisionFromDevloop(stored.SourceRevision),
	}, nil
}

func (synchronizer *candidateSourceSynchronizer) SnapshotAttestation(
	ctx context.Context,
	scope project.CandidateSourceScope,
	artifactDigest, attestationDigest string,
) (project.CandidateSourceSnapshot, error) {
	if synchronizer == nil || synchronizer.store == nil {
		return project.CandidateSourceSnapshot{}, project.ErrCandidateSourceUnavailable
	}
	if err := scope.ProjectID.Validate(); err != nil {
		return project.CandidateSourceSnapshot{}, candidateSourceInvalid(fmt.Errorf("project identity: %w", err))
	}
	stored, err := synchronizer.store.SnapshotAttestation(ctx, scope.ProjectID, artifactDigest, attestationDigest)
	if err != nil {
		return project.CandidateSourceSnapshot{}, candidateSourceInvalid(err)
	}
	return project.CandidateSourceSnapshot{
		ProjectID: stored.ProjectID, ArtifactDigest: stored.Digest,
		SourceAttestationDigest: stored.SourceAttestationDigest,
		ProjectPath:             stored.ProjectPath, ProjectDigest: stored.ProjectDigest,
		ProjectArtifactPath: stored.ProjectArtifactPath,
		SourceRevision:      cloneCandidateSourceRevisionFromDevloop(stored.SourceRevision),
	}, nil
}

func (synchronizer *candidateSourceSynchronizer) purgeExpiredLocked() {
	now := synchronizer.now().UTC()
	for key, plan := range synchronizer.plans {
		if !now.Before(plan.expiresAt) {
			delete(synchronizer.plans, key)
			_ = os.Remove(synchronizer.planPath(key))
		}
	}
}

func (synchronizer *candidateSourceSynchronizer) loadPlans() error {
	entries, err := os.ReadDir(synchronizer.planDir)
	if err != nil {
		return err
	}
	planIDs := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(synchronizer.planDir, entry.Name()))
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(bytes.NewReader(content))
		decoder.DisallowUnknownFields()
		var record candidateSourcePlanRecord
		if err := decoder.Decode(&record); err != nil {
			return fmt.Errorf("decode %s: %w", entry.Name(), err)
		}
		if record.Version != candidateSourcePlanVersion {
			return fmt.Errorf("%s has unsupported version %d", entry.Name(), record.Version)
		}
		if err := validatePersistedPlanRecord(record); err != nil {
			return fmt.Errorf("validate %s: %w", entry.Name(), err)
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, record.ExpiresAt)
		if err != nil {
			return fmt.Errorf("decode %s expiry: %w", entry.Name(), err)
		}
		key := candidateSourcePlanKey{
			projectID:      record.ProjectID,
			ownerID:        strings.TrimSpace(record.OwnerID),
			candidateKey:   normalizeCandidateSourceKey(record.CandidateKey),
			artifactDigest: strings.TrimSpace(record.ArtifactDigest),
			idempotencyKey: strings.TrimSpace(record.IdempotencyKey),
		}
		if err := key.projectID.Validate(); err != nil {
			return fmt.Errorf("decode %s project identity: %w", entry.Name(), err)
		}
		if entry.Name() != filepath.Base(synchronizer.planPath(key)) {
			return fmt.Errorf("%s identity does not match filename", entry.Name())
		}
		if previous, exists := planIDs[record.PlanID]; exists {
			return fmt.Errorf("plan id %q is duplicated by %s and %s", record.PlanID, previous, entry.Name())
		}
		planIDs[record.PlanID] = entry.Name()
		missing := make(map[string]struct{}, len(record.Missing))
		for _, identity := range record.Missing {
			missing[identity] = struct{}{}
		}
		synchronizer.plans[key] = candidateSourcePlan{
			planID: record.PlanID, requestDigest: record.RequestDigest, expiresAt: expiresAt.UTC(),
			missing: missing, sizes: record.Sizes,
		}
	}
	synchronizer.purgeExpiredLocked()
	return nil
}

func validatePersistedPlanRecord(record candidateSourcePlanRecord) error {
	planID := strings.TrimSpace(record.PlanID)
	if planID != record.PlanID || len(planID) != 32 || planID != strings.ToLower(planID) {
		return fmt.Errorf("plan id must be canonical 32-character lowercase hex")
	}
	if _, err := hex.DecodeString(planID); err != nil {
		return fmt.Errorf("plan id must be canonical 32-character lowercase hex: %w", err)
	}
	if strings.TrimSpace(record.OwnerID) == "" || record.OwnerID != strings.TrimSpace(record.OwnerID) {
		return fmt.Errorf("owner id is required")
	}
	if strings.TrimSpace(record.IdempotencyKey) == "" || record.IdempotencyKey != strings.TrimSpace(record.IdempotencyKey) {
		return fmt.Errorf("idempotency key is required")
	}
	if err := digest.ValidateSHA256Identity(record.ArtifactDigest); err != nil {
		return fmt.Errorf("artifact digest is invalid: %w", err)
	}
	if err := digest.ValidateSHA256Identity(record.RequestDigest); err != nil {
		return fmt.Errorf("request digest is invalid: %w", err)
	}
	for identity, size := range record.Sizes {
		if err := digest.ValidateSHA256Identity(identity); err != nil {
			return fmt.Errorf("size identity is invalid: %w", err)
		}
		if size < 0 || size > maxCandidateSourceUploadBytes {
			return fmt.Errorf("size for %q is out of bounds", identity)
		}
	}
	seen := make(map[string]struct{}, len(record.Missing))
	for _, identity := range record.Missing {
		if err := digest.ValidateSHA256Identity(identity); err != nil {
			return fmt.Errorf("missing digest is invalid: %w", err)
		}
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("missing digest %q is duplicated", identity)
		}
		seen[identity] = struct{}{}
		size, exists := record.Sizes[identity]
		if !exists || size < 0 || size > maxCandidateSourceUploadBytes {
			return fmt.Errorf("missing digest %q has no bounded size", identity)
		}
	}
	return nil
}

func (synchronizer *candidateSourceSynchronizer) savePlan(
	key candidateSourcePlanKey,
	plan candidateSourcePlan,
) error {
	missing := make([]string, 0, len(plan.missing))
	for identity := range plan.missing {
		missing = append(missing, identity)
	}
	sort.Strings(missing)
	content, err := json.Marshal(candidateSourcePlanRecord{
		Version:   candidateSourcePlanVersion,
		ProjectID: key.projectID, OwnerID: key.ownerID,
		CandidateKey:   key.candidateKey,
		ArtifactDigest: key.artifactDigest,
		IdempotencyKey: key.idempotencyKey,
		PlanID:         plan.planID,
		RequestDigest:  plan.requestDigest,
		ExpiresAt:      plan.expiresAt.UTC().Format(time.RFC3339Nano),
		Missing:        missing, Sizes: plan.sizes,
	})
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(synchronizer.planPath(key), content)
}

func (synchronizer *candidateSourceSynchronizer) planPath(key candidateSourcePlanKey) string {
	key.candidateKey = normalizeCandidateSourceKey(key.candidateKey)
	identity := key.projectID.String() + "\x00" + key.ownerID + "\x00" + key.artifactDigest + "\x00" + key.idempotencyKey
	if key.candidateKey != "default" {
		identity += "\x00" + key.candidateKey
	}
	sum := sha256.Sum256([]byte(
		identity,
	))
	return filepath.Join(synchronizer.planDir, hex.EncodeToString(sum[:])+".json")
}

const maxCandidateSourceUploadBytes = 16 << 20

func newCandidateSourcePlanID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func sortedPlanMissing(missing map[string]struct{}) []string {
	values := make([]string, 0, len(missing))
	for identity := range missing {
		values = append(values, identity)
	}
	sort.Strings(values)
	return values
}

func candidateSourceRequestDigest(request project.CandidateSynchronizationRequest) string {
	request.PlanID = ""
	request.IdempotencyKey = ""
	request.Artifacts = append([]project.CandidateSourceArtifact(nil), request.Artifacts...)
	sort.Slice(request.Artifacts, func(i, j int) bool { return request.Artifacts[i].Path < request.Artifacts[j].Path })
	content, err := json.Marshal(request)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func synchronizationPlanRequest(
	scope project.CandidateSourceScope,
	request project.CandidateSynchronizationRequest,
) projectdevloop.SynchronizationPlanRequest {
	result := projectdevloop.SynchronizationPlanRequest{
		ProjectID: scope.ProjectID, ProjectFile: request.ProjectFile,
		SourceOnly:             request.SourceOnly,
		CandidateKey:           request.CandidateKey,
		ArtifactDigest:         request.ArtifactDigest,
		ExpectedCandidateID:    request.ExpectedCandidateID,
		ExpectedArtifactDigest: request.ExpectedArtifactDigest,
		Artifacts:              make([]projectdevloop.ArtifactReference, len(request.Artifacts)),
		SourceRevision:         candidateSourceRevisionToDevloop(request.SourceRevision),
		PlanID:                 request.PlanID,
		IdempotencyKey:         request.IdempotencyKey,
	}
	for index, artifact := range request.Artifacts {
		result.Artifacts[index] = projectdevloop.ArtifactReference{
			Path: artifact.Path, Digest: artifact.Digest, SizeBytes: artifact.SizeBytes,
		}
	}
	return result
}

func normalizeCandidateSourceKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "default"
	}
	return value
}

func cloneCandidateSourceRevision(
	value *project.CandidateSourceRevision,
) *project.CandidateSourceRevision {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func candidateSourceRevisionToDevloop(
	value *project.CandidateSourceRevision,
) *projectdevloop.SourceRevision {
	if value == nil {
		return nil
	}
	return &projectdevloop.SourceRevision{
		Revision: value.Revision, Repository: value.Repository,
		Ref: value.Ref, ChangeID: value.ChangeID,
	}
}

func cloneCandidateSourceRevisionFromDevloop(
	value *projectdevloop.SourceRevision,
) *project.CandidateSourceRevision {
	if value == nil {
		return nil
	}
	return &project.CandidateSourceRevision{
		Revision: value.Revision, Repository: value.Repository,
		Ref: value.Ref, ChangeID: value.ChangeID,
	}
}

func candidateSourceInvalid(err error) error {
	return fmt.Errorf(
		"%w: synchronized project sources are invalid: %v",
		project.ErrCandidateSourceInvalid,
		err,
	)
}
