package module

import (
	"bytes"
	"context"
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

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/internal/project"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const candidateSourcePlanLifetime = 5 * time.Minute
const candidateSourcePlanVersion = 1

type candidateSourceSynchronizer struct {
	store   *projectdevloop.TargetStore
	planDir string
	now     func() time.Time
	mu      sync.Mutex
	plans   map[candidateSourcePlanKey]candidateSourcePlan
}

type candidateSourcePlanKey struct {
	projectID      string
	ownerID        string
	candidateKey   string
	artifactDigest string
}

type candidateSourcePlan struct {
	expiresAt time.Time
	missing   map[string]struct{}
}

type candidateSourcePlanRecord struct {
	Version        int      `json:"version"`
	ProjectID      string   `json:"projectId"`
	OwnerID        string   `json:"ownerId"`
	CandidateKey   string   `json:"candidateKey,omitempty"`
	ArtifactDigest string   `json:"artifactDigest"`
	ExpiresAt      string   `json:"expiresAt"`
	Missing        []string `json:"missing"`
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
) ([]string, error) {
	if synchronizer == nil || synchronizer.store == nil {
		return nil, project.ErrCandidateSourceUnavailable
	}
	missing, err := synchronizer.store.Missing(ctx, synchronizationPlanRequest(scope, request))
	if err != nil {
		return nil, candidateSourceInvalid(err)
	}
	synchronizer.mu.Lock()
	defer synchronizer.mu.Unlock()
	synchronizer.purgeExpiredLocked()
	allowed := make(map[string]struct{}, len(missing))
	for _, identity := range missing {
		allowed[identity] = struct{}{}
	}
	key := candidateSourcePlanKey{
		projectID: strings.TrimSpace(scope.ProjectID), ownerID: strings.TrimSpace(scope.OwnerID),
		candidateKey:   normalizeCandidateSourceKey(scope.CandidateKey),
		artifactDigest: strings.TrimSpace(request.ArtifactDigest),
	}
	plan := candidateSourcePlan{
		expiresAt: synchronizer.now().UTC().Add(candidateSourcePlanLifetime), missing: allowed,
	}
	if err := synchronizer.savePlan(key, plan); err != nil {
		return nil, fmt.Errorf("%w: persist source synchronization plan: %v", project.ErrCandidateSourceUnavailable, err)
	}
	synchronizer.plans[key] = plan
	return missing, nil
}

func (synchronizer *candidateSourceSynchronizer) Upload(
	ctx context.Context,
	scope project.CandidateSourceScope,
	identity string,
	source io.Reader,
) error {
	if synchronizer == nil || synchronizer.store == nil {
		return project.ErrCandidateSourceUnavailable
	}
	projectID := strings.TrimSpace(scope.ProjectID)
	ownerID := strings.TrimSpace(scope.OwnerID)
	identity = strings.TrimSpace(identity)
	synchronizer.mu.Lock()
	synchronizer.purgeExpiredLocked()
	authorized := false
	for key, plan := range synchronizer.plans {
		if key.projectID != projectID || key.ownerID != ownerID {
			continue
		}
		if _, exists := plan.missing[identity]; exists {
			authorized = true
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
	if err := synchronizer.store.Put(ctx, identity, source); err != nil {
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
	stored, err := synchronizer.store.Commit(ctx, synchronizationPlanRequest(scope, request))
	if err != nil {
		return project.CandidateSourceSnapshot{}, candidateSourceInvalid(err)
	}
	synchronizer.mu.Lock()
	key := candidateSourcePlanKey{
		projectID: strings.TrimSpace(scope.ProjectID), ownerID: strings.TrimSpace(scope.OwnerID),
		candidateKey:   normalizeCandidateSourceKey(scope.CandidateKey),
		artifactDigest: strings.TrimSpace(request.ArtifactDigest),
	}
	delete(synchronizer.plans, key)
	_ = os.Remove(synchronizer.planPath(key))
	synchronizer.mu.Unlock()
	return project.CandidateSourceSnapshot{
		ProjectID: stored.ProjectID.String(), ArtifactDigest: stored.Digest,
		ProjectPath: stored.ProjectPath, ProjectDigest: stored.ProjectDigest,
		ProjectArtifactPath: stored.ProjectArtifactPath,
		SourceRevision:      cloneCandidateSourceRevision(request.SourceRevision),
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
		expiresAt, err := time.Parse(time.RFC3339Nano, record.ExpiresAt)
		if err != nil {
			return fmt.Errorf("decode %s expiry: %w", entry.Name(), err)
		}
		key := candidateSourcePlanKey{
			projectID:      strings.TrimSpace(record.ProjectID),
			ownerID:        strings.TrimSpace(record.OwnerID),
			candidateKey:   normalizeCandidateSourceKey(record.CandidateKey),
			artifactDigest: strings.TrimSpace(record.ArtifactDigest),
		}
		if entry.Name() != filepath.Base(synchronizer.planPath(key)) {
			return fmt.Errorf("%s identity does not match filename", entry.Name())
		}
		missing := make(map[string]struct{}, len(record.Missing))
		for _, identity := range record.Missing {
			missing[strings.TrimSpace(identity)] = struct{}{}
		}
		synchronizer.plans[key] = candidateSourcePlan{
			expiresAt: expiresAt.UTC(),
			missing:   missing,
		}
	}
	synchronizer.purgeExpiredLocked()
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
		ExpiresAt:      plan.expiresAt.UTC().Format(time.RFC3339Nano),
		Missing:        missing,
	})
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(synchronizer.planPath(key), content)
}

func (synchronizer *candidateSourceSynchronizer) planPath(key candidateSourcePlanKey) string {
	key.candidateKey = normalizeCandidateSourceKey(key.candidateKey)
	identity := key.projectID + "\x00" + key.ownerID + "\x00" + key.artifactDigest
	if key.candidateKey != "default" {
		identity += "\x00" + key.candidateKey
	}
	sum := sha256.Sum256([]byte(
		identity,
	))
	return filepath.Join(synchronizer.planDir, hex.EncodeToString(sum[:])+".json")
}

func synchronizationPlanRequest(
	scope project.CandidateSourceScope,
	request project.CandidateSynchronizationRequest,
) projectdevloop.SynchronizationPlanRequest {
	result := projectdevloop.SynchronizationPlanRequest{
		ProjectID: projectgraph.ResourceID(scope.ProjectID), ProjectFile: request.ProjectFile,
		CandidateKey:           request.CandidateKey,
		ArtifactDigest:         request.ArtifactDigest,
		ExpectedCandidateID:    request.ExpectedCandidateID,
		ExpectedArtifactDigest: request.ExpectedArtifactDigest,
		Artifacts:              make([]projectdevloop.ArtifactReference, len(request.Artifacts)),
	}
	for index, artifact := range request.Artifacts {
		result.Artifacts[index] = projectdevloop.ArtifactReference{
			Path: artifact.Path, Digest: artifact.Digest,
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

func candidateSourceInvalid(err error) error {
	return fmt.Errorf(
		"%w: synchronized project sources are invalid: %v",
		project.ErrCandidateSourceInvalid,
		err,
	)
}
