package deployment

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestCandidateServiceCreatesResumesAndBuildsCanonicalPreviewURL(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	repository := newCandidateMemoryRepository()
	service := newCandidateTestService(t, repository, now)
	var events []CandidateEvent
	service.audit = func(_ context.Context, event CandidateEvent) error {
		events = append(events, event)
		return nil
	}
	digest := "sha256:" + strings.Repeat("a", 64)

	started, err := service.Start(context.Background(), StartCandidateRequest{
		ProjectID: "finance", OwnerID: "principal_1", ArtifactDigest: digest,
	})
	require.NoError(t, err)
	if started.Candidate.Scope.BaseGenerationID != "deployment_7" {
		t.Fatalf("base generation = %q, want server-resolved deployment_7", started.Candidate.Scope.BaseGenerationID)
	}
	resumed, err := service.Start(context.Background(), StartCandidateRequest{
		ProjectID: "finance", OwnerID: "principal_1", ArtifactDigest: digest,
	})
	require.NoError(t, err)
	if resumed.Candidate != started.Candidate || !resumed.Resumed || started.Resumed {
		t.Fatalf("started=%#v resumed=%#v", started, resumed)
	}
	if len(events) != 2 || events[0].Action != "candidate.started" || events[1].Action != "candidate.started" ||
		!strings.Contains(events[1].MetadataJSON, `"resumed":true`) {
		t.Fatalf("start audit events = %#v", events)
	}
	wantURL := "https://prod.leapview.example/candidates/" + started.Candidate.ID
	if started.PreviewURL != wantURL || strings.Contains(started.PreviewURL, digest) ||
		strings.Contains(started.PreviewURL, "principal_1") {
		t.Fatalf("preview URL = %q, want %q without sensitive state", started.PreviewURL, wantURL)
	}

	_, err = service.Start(context.Background(), StartCandidateRequest{
		ProjectID: "finance", OwnerID: "principal_1",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64),
	})
	if !errors.Is(err, ErrCandidateConflict) {
		t.Fatalf("changed start error = %v, want explicit update conflict", err)
	}
}

func TestCandidateServiceRetiresRuntimeOnCancelAndReconcile(t *testing.T) {
	repository := newCandidateMemoryRepository()
	lifecycle := &candidateRuntimeLifecycleStub{}
	now := time.Now().UTC()
	service, err := NewCandidateService(repository, CandidateServiceConfig{
		TargetID: "lvinst_prod", CanonicalOrigin: "https://prod.leapview.example", Environment: "prod",
		Lifetime: time.Hour, MaxActivePerOwner: 4, Now: func() time.Time { return now },
		NewID: func() (string, error) { return "cand_lifecycle", nil }, RuntimeLifecycle: lifecycle,
		Audit: func(context.Context, CandidateEvent) error { return nil },
	})
	require.NoError(t, err)
	started, err := service.Start(t.Context(), StartCandidateRequest{ProjectID: "finance", OwnerID: "owner", ArtifactDigest: "sha256:" + strings.Repeat("a", 64)})
	require.NoError(t, err)
	_, err = service.Cancel(t.Context(), candidateScopeForService(started.Candidate))
	require.NoError(t, err)
	if len(lifecycle.retired) != 1 || lifecycle.retired[0] != started.Candidate.ID {
		t.Fatalf("retired candidates = %#v", lifecycle.retired)
	}
	_, err = service.Reconcile(t.Context())
	require.NoError(t, err)
	if lifecycle.reaped != 1 {
		t.Fatalf("reap calls = %d, want 1", lifecycle.reaped)
	}
}

func TestCandidateServiceRequiresAuditRecorder(t *testing.T) {
	_, err := NewCandidateService(newCandidateMemoryRepository(), CandidateServiceConfig{
		TargetID: "lvinst_prod", CanonicalOrigin: "https://prod.leapview.example", Environment: "prod",
	})
	if !errors.Is(err, ErrCandidateAuditUnavailable) {
		t.Fatalf("NewCandidateService() error = %v, want ErrCandidateAuditUnavailable", err)
	}
}

func TestCandidateServicePreservesCommittedMutationWhenBestEffortAuditFails(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	repository := newCandidateMemoryRepository()
	var logs bytes.Buffer
	service, err := NewCandidateService(repository, CandidateServiceConfig{
		TargetID: "lvinst_prod", CanonicalOrigin: "https://prod.leapview.example", Environment: "prod",
		Lifetime: time.Hour, MaxActivePerOwner: 4, Now: func() time.Time { return now },
		NewID: func() (string, error) { return "cand_audit_failure", nil },
		Audit: func(context.Context, CandidateEvent) error {
			return errors.New("audit store unavailable")
		},
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	require.NoError(t, err)

	started, err := service.Start(t.Context(), StartCandidateRequest{
		ProjectID: "finance", OwnerID: "principal_1",
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64),
	})
	require.NoError(t, err)
	if started.Candidate.ID != "cand_audit_failure" {
		t.Fatalf("started candidate = %#v", started.Candidate)
	}
	persisted, err := repository.CandidateByID(t.Context(), started.Candidate.ID)
	require.NoError(t, err)
	if persisted != started.Candidate {
		t.Fatalf("persisted candidate = %#v, want %#v", persisted, started.Candidate)
	}
	if output := logs.String(); !strings.Contains(output, "candidate audit failed") ||
		!strings.Contains(output, "audit_action=candidate.started") ||
		!strings.Contains(output, "candidate_id=cand_audit_failure") ||
		!strings.Contains(output, "audit store unavailable") {
		t.Fatalf("audit failure log = %q", output)
	}
}

func TestCandidateServiceUsesNestedAuditContractDuringSynchronization(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	repository := newCandidateMemoryRepository()
	var events []CandidateEvent
	var logs bytes.Buffer
	service, err := NewCandidateService(repository, CandidateServiceConfig{
		TargetID: "lvinst_prod", CanonicalOrigin: "https://prod.leapview.example", Environment: "prod",
		Lifetime: time.Hour, MaxActivePerOwner: 4, Now: func() time.Time { return now },
		NewID: func() (string, error) { return "cand_nested_audit", nil },
		Audit: func(_ context.Context, event CandidateEvent) error {
			events = append(events, event)
			return nil
		},
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	require.NoError(t, err)

	contract, ok := deploymentgen.GetAPIGenCommandRuntimeContract(string(deploymentgen.GenOperationCommitProjectCandidateSynchronization))
	if !ok {
		t.Fatal("commit synchronization command contract is missing")
	}
	ctx, guard, err := apigencommand.Begin(t.Context(), contract)
	require.NoError(t, err)
	digest := "sha256:" + strings.Repeat("a", 64)
	started, err := service.Start(ctx, StartCandidateRequest{
		ProjectID: "finance", OwnerID: "principal_1", ArtifactDigest: digest,
	})
	require.NoError(t, err)
	if guard.Completed() {
		t.Fatal("nested candidate start completed the outer synchronization command")
	}
	_, err = service.MarkReady(ctx, candidateScopeForService(started.Candidate), digest, "sha256:"+strings.Repeat("b", 64))
	require.NoError(t, err)
	if !guard.Completed() {
		t.Fatal("candidate ready audit did not complete the outer synchronization command")
	}
	if len(events) != 2 || events[0].Action != CandidateAuditStarted || events[1].Action != CandidateAuditReady {
		t.Fatalf("nested synchronization audit events = %#v", events)
	}
	if !strings.Contains(events[0].MetadataJSON, `"operationId":"startProjectCandidate"`) ||
		!strings.Contains(events[1].MetadataJSON, `"operationId":"commitProjectCandidateSynchronization"`) {
		t.Fatalf("nested synchronization audit metadata = %#v", events)
	}
	if strings.Contains(logs.String(), "does not match generated action") {
		t.Fatalf("nested synchronization logged an audit mismatch: %s", logs.String())
	}
}

func TestCandidateServiceIsolatesAutomationKeysAndCancelsByKey(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	repository := newCandidateMemoryRepository()
	service := newCandidateTestService(t, repository, now)
	digest := "sha256:" + strings.Repeat("a", 64)
	first, err := service.Start(t.Context(), StartCandidateRequest{
		ProjectID: "finance", OwnerID: "principal_1",
		ArtifactDigest: digest, Key: "github:pull/41",
	})
	require.NoError(t, err)
	second, err := service.Start(t.Context(), StartCandidateRequest{
		ProjectID: "finance", OwnerID: "principal_1",
		ArtifactDigest: digest, Key: "github:pull/42",
	})
	require.NoError(t, err)
	if first.Candidate.ID == second.Candidate.ID ||
		first.Candidate.Key == second.Candidate.Key {
		t.Fatalf("isolated candidates = %#v / %#v", first, second)
	}
	cancelled, err := service.CancelActive(
		t.Context(),
		"finance",
		"principal_1",
		"github:pull/41",
	)
	if err != nil || cancelled.ID != first.Candidate.ID ||
		cancelled.Status != CandidateCancelled {
		t.Fatalf("CancelActive() = %#v, %v", cancelled, err)
	}
	remaining, err := service.Get(t.Context(), CandidateAccessScope{
		ProjectID: "finance", CandidateID: second.Candidate.ID,
		OwnerID: "principal_1",
	})
	if err != nil || remaining.Status != CandidatePreparing {
		t.Fatalf("remaining candidate = %#v, %v", remaining, err)
	}
}

func TestCandidateServiceConcealsForeignCandidatesAndUsesOptimisticReplacement(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	repository := newCandidateMemoryRepository()
	service := newCandidateTestService(t, repository, now)
	first := "sha256:" + strings.Repeat("a", 64)
	second := "sha256:" + strings.Repeat("b", 64)
	started, err := service.Start(context.Background(), StartCandidateRequest{
		ProjectID: "finance", OwnerID: "principal_1", ArtifactDigest: first,
	})
	require.NoError(t, err)
	for name, scope := range map[string]CandidateAccessScope{
		"owner":   {ProjectID: "finance", CandidateID: started.Candidate.ID, OwnerID: "principal_2"},
		"project": {ProjectID: "marketing", CandidateID: started.Candidate.ID, OwnerID: "principal_1"},
		"target":  {ProjectID: "finance", CandidateID: started.Candidate.ID, OwnerID: "principal_1", TargetID: "lvinst_other"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Get(context.Background(), scope); !errors.Is(err, ErrCandidateNotFound) {
				t.Fatalf("Get() error = %v, want concealed ErrCandidateNotFound", err)
			}
		})
	}

	updated, err := service.ReplaceArtifact(context.Background(), CandidateAccessScope{
		ProjectID: "finance", CandidateID: started.Candidate.ID, OwnerID: "principal_1",
	}, first, second)
	require.NoError(t, err)
	if updated.ArtifactDigest != second || updated.Status != CandidatePreparing {
		t.Fatalf("updated = %#v", updated)
	}
	if _, err := service.ReplaceArtifact(context.Background(), CandidateAccessScope{
		ProjectID: "finance", CandidateID: started.Candidate.ID, OwnerID: "principal_1",
	}, first, second); !errors.Is(err, ErrCandidateConflict) {
		t.Fatalf("stale replacement error = %v, want ErrCandidateConflict", err)
	}
}

func TestCandidateServiceEnforcesQuotaCancelExpiryAndRestartDurability(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	repository := newCandidateMemoryRepository()
	service := newCandidateTestService(t, repository, now)
	service.maxActivePerOwner = 1
	first, err := service.Start(context.Background(), StartCandidateRequest{
		ProjectID: "finance", OwnerID: "principal_1",
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64),
	})
	require.NoError(t, err)
	if _, err := service.Start(context.Background(), StartCandidateRequest{
		ProjectID: "marketing", OwnerID: "principal_1",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64),
	}); !errors.Is(err, ErrCandidateQuota) {
		t.Fatalf("quota error = %v, want ErrCandidateQuota", err)
	}

	restarted := newCandidateTestService(t, repository, now.Add(10*time.Minute))
	resumed, err := restarted.Get(context.Background(), CandidateAccessScope{
		ProjectID: "finance", CandidateID: first.Candidate.ID, OwnerID: "principal_1",
	})
	if err != nil || resumed.ID != first.Candidate.ID {
		t.Fatalf("restart Get() = %#v, %v", resumed, err)
	}
	cancelled, err := restarted.Cancel(context.Background(), CandidateAccessScope{
		ProjectID: "finance", CandidateID: first.Candidate.ID, OwnerID: "principal_1",
	})
	if err != nil || cancelled.Status != CandidateCancelled {
		t.Fatalf("Cancel() = %#v, %v", cancelled, err)
	}
	replayed, err := restarted.Cancel(context.Background(), CandidateAccessScope{
		ProjectID: "finance", CandidateID: first.Candidate.ID, OwnerID: "principal_1",
	})
	if err != nil || replayed != cancelled {
		t.Fatalf("replayed Cancel() = %#v, %v", replayed, err)
	}

	expiring := newCandidateTestService(t, repository, now)
	expiring.maxActivePerOwner = 2
	expiring.lifetime = time.Minute
	second, err := expiring.Start(context.Background(), StartCandidateRequest{
		ProjectID: "marketing", OwnerID: "principal_1",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64),
	})
	require.NoError(t, err)
	expiring.now = func() time.Time { return now.Add(2 * time.Minute) }
	if count, err := expiring.Reconcile(context.Background()); err != nil || count != 1 {
		t.Fatalf("Reconcile() = %d, %v", count, err)
	}
	expired, err := expiring.Get(context.Background(), CandidateAccessScope{
		ProjectID: "marketing", CandidateID: second.Candidate.ID, OwnerID: "principal_1",
	})
	if err != nil || expired.Status != CandidateExpired {
		t.Fatalf("expired Get() = %#v, %v", expired, err)
	}
}

func TestCandidateServiceExpiresOwnedCandidateOnRead(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	repository := newCandidateMemoryRepository()
	service := newCandidateTestService(t, repository, now)
	service.lifetime = time.Minute
	started, err := service.Start(context.Background(), StartCandidateRequest{
		ProjectID: "finance", OwnerID: "principal_1",
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64),
	})
	require.NoError(t, err)

	service.now = func() time.Time { return now.Add(2 * time.Minute) }
	expired, err := service.GetOwned(context.Background(), started.Candidate.ID, "principal_1")
	require.NoError(t, err)
	if expired.Status != CandidateExpired || expired.Revision != started.Candidate.Revision+1 {
		t.Fatalf("expired candidate = %#v", expired)
	}
	persisted, err := repository.CandidateByID(context.Background(), started.Candidate.ID)
	if err != nil || persisted != expired {
		t.Fatalf("persisted candidate = %#v, %v", persisted, err)
	}
}

func TestCandidateServiceAuditsLifecycleWithoutArtifactDigest(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	repository := newCandidateMemoryRepository()
	var events []CandidateEvent
	service := newCandidateTestService(t, repository, now)
	service.audit = func(_ context.Context, event CandidateEvent) error {
		events = append(events, event)
		return nil
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	started, err := service.Start(context.Background(), StartCandidateRequest{
		ProjectID: "finance", OwnerID: "principal_1", ArtifactDigest: digest,
	})
	require.NoError(t, err)
	if _, err := service.Cancel(context.Background(), CandidateAccessScope{
		ProjectID: "finance", CandidateID: started.Candidate.ID, OwnerID: "principal_1",
	}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Action != "candidate.started" || events[1].Action != "candidate.cancelled" {
		t.Fatalf("events = %#v", events)
	}
	for _, event := range events {
		if strings.Contains(event.MetadataJSON, digest) {
			t.Fatalf("audit leaked artifact digest: %#v", event)
		}
	}
}

func newCandidateTestService(t *testing.T, repository CandidateRepository, now time.Time) *CandidateService {
	t.Helper()
	counter := 0
	service, err := NewCandidateService(repository, CandidateServiceConfig{
		TargetID: "lvinst_prod", CanonicalOrigin: "https://prod.leapview.example", Environment: "prod",
		Lifetime: time.Hour, MaxActivePerOwner: 4,
		Now: func() time.Time { return now },
		NewID: func() (string, error) {
			counter++
			return "cand_test_" + string(rune('0'+counter)), nil
		},
		Audit: func(context.Context, CandidateEvent) error { return nil },
	})
	require.NoError(t, err)
	return service
}

type candidateMemoryRepository struct {
	candidates      map[string]Candidate
	baseGenerations map[string]string
}

type candidateRuntimeLifecycleStub struct {
	retired []string
	reaped  int
}

func (stub *candidateRuntimeLifecycleStub) RetireCandidate(id string) int {
	stub.retired = append(stub.retired, id)
	return 1
}

func (stub *candidateRuntimeLifecycleStub) ReapExpiredCandidates(time.Time) int {
	stub.reaped++
	return 1
}

func newCandidateMemoryRepository() *candidateMemoryRepository {
	return &candidateMemoryRepository{
		candidates: map[string]Candidate{},
		baseGenerations: map[string]string{
			"finance":   "deployment_7",
			"marketing": "deployment_8",
		},
	}
}

func (repository *candidateMemoryRepository) ActiveCandidateBaseScope(_ context.Context, projectID projectgraph.ResourceID, environment string) (CandidateScope, error) {
	if generation := repository.baseGenerations[projectID.String()]; generation != "" {
		return CandidateScope{ProjectID: projectID, Environment: environment, BaseGenerationID: generation}, nil
	}
	return CandidateScope{ProjectID: projectID, Environment: environment}, nil
}

func (repository *candidateMemoryRepository) StartCandidate(_ context.Context, candidate Candidate, maxActivePerOwner int) (Candidate, bool, error) {
	active := 0
	for _, existing := range repository.candidates {
		if existing.OwnerID == candidate.OwnerID && !existing.Terminal() {
			active++
		}
		if existing.OwnerID == candidate.OwnerID && existing.Scope.ProjectID == candidate.Scope.ProjectID &&
			existing.TargetID == candidate.TargetID && existing.Key == candidate.Key &&
			!existing.Terminal() {
			if existing.Scope.BaseGenerationID == candidate.Scope.BaseGenerationID && existing.ArtifactDigest == candidate.ArtifactDigest {
				return existing, true, nil
			}
			return Candidate{}, false, ErrCandidateConflict
		}
	}
	if active >= maxActivePerOwner {
		return Candidate{}, false, ErrCandidateQuota
	}
	repository.candidates[candidate.ID] = candidate
	return candidate, false, nil
}

func (repository *candidateMemoryRepository) ActiveCandidate(
	_ context.Context,
	targetID,
	projectID projectgraph.ResourceID,
	ownerID,
	key string,
) (Candidate, error) {
	for _, candidate := range repository.candidates {
		if candidate.TargetID == targetID && candidate.Scope.ProjectID == projectID &&
			candidate.OwnerID == ownerID && candidate.Key == key &&
			!candidate.Terminal() {
			return candidate, nil
		}
	}
	return Candidate{}, ErrCandidateNotFound
}

func (repository *candidateMemoryRepository) CandidateByID(_ context.Context, id string) (Candidate, error) {
	candidate, ok := repository.candidates[id]
	if !ok {
		return Candidate{}, ErrCandidateNotFound
	}
	return candidate, nil
}

func (repository *candidateMemoryRepository) SaveCandidate(_ context.Context, candidate Candidate, expectedRevision int64) (Candidate, error) {
	existing, ok := repository.candidates[candidate.ID]
	if !ok {
		return Candidate{}, ErrCandidateNotFound
	}
	if existing.Revision != expectedRevision {
		return Candidate{}, ErrCandidateConflict
	}
	repository.candidates[candidate.ID] = candidate
	return candidate, nil
}

func (repository *candidateMemoryRepository) ExpireCandidates(_ context.Context, targetID string, now time.Time) (int64, error) {
	var count int64
	for id, candidate := range repository.candidates {
		if candidate.TargetID != targetID {
			continue
		}
		expired, changed, err := candidate.Expire(now)
		if err != nil {
			return count, err
		}
		if changed {
			repository.candidates[id] = expired
			count++
		}
	}
	return count, nil
}
