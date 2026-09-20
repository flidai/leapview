package hostinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/release/transitionoperation"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/flidai/leapview/internal/release/transitionrunner"
	"github.com/stretchr/testify/require"
)

const upgradeOperationID = "0198f2c0-7c7a-7f00-8a11-000000000999"

type upgradeStore struct {
	op                  transitionoperation.Operation
	validateErr         error
	recordErr           error
	getCount            int
	rotateFenceOnSecond bool
	enforceLease        bool
	leaseUntil          atomic.Int64
	renewCount          atomic.Int64
	renewErr            error
}

func (s *upgradeStore) Get(context.Context, string) (transitionoperation.Operation, error) {
	s.getCount++
	if s.rotateFenceOnSecond && s.getCount == 2 {
		s.op.Fence.FencingGeneration++
	}
	return s.op, nil
}
func (s *upgradeStore) Validate(context.Context, transitionoperation.Fence) error {
	if s.enforceLease && time.Now().UnixNano() >= s.leaseUntil.Load() {
		return transitionoperation.ErrStaleFence
	}
	return s.validateErr
}
func (s *upgradeStore) Renew(_ context.Context, fence transitionoperation.Fence, until time.Time) (transitionoperation.Fence, error) {
	if s.renewErr != nil {
		return transitionoperation.Fence{}, s.renewErr
	}
	s.leaseUntil.Store(until.UnixNano())
	s.renewCount.Add(1)
	fence.LeaseExpiresAt = until
	return fence, nil
}
func (s *upgradeStore) RecordPhase(_ context.Context, input transitionrunner.PhaseRecordInput) (transitionoperation.Operation, error) {
	if s.recordErr != nil {
		return transitionoperation.Operation{}, s.recordErr
	}
	if input.OperationID != s.op.OperationID || input.Fence != s.op.Fence || input.Phase != s.op.CurrentPhase {
		return transitionoperation.Operation{}, transitionoperation.ErrConflict
	}
	s.op.PhaseResults = append(s.op.PhaseResults, transitionoperation.PhaseResult{Phase: input.Phase, Status: input.Status, Result: append([]byte(nil), input.Result...)})
	switch input.Phase {
	case transitionrunner.PhaseStage:
		s.op.CurrentPhase = transitionrunner.PhaseActivate
	case transitionrunner.PhaseActivate:
		s.op.CurrentPhase = transitionrunner.PhaseRestart
	case transitionrunner.PhaseRestart:
		s.op.CurrentPhase = transitionrunner.PhasePostValidate
	}
	return s.op, nil
}
func (s *upgradeStore) Fail(_ context.Context, input transitionrunner.FailureInput) (transitionoperation.Operation, error) {
	if input.OperationID != s.op.OperationID || input.Fence != s.op.Fence || input.Phase != s.op.CurrentPhase {
		return transitionoperation.Operation{}, transitionoperation.ErrConflict
	}
	s.op.Status = transitionoperation.StatusIndeterminate
	s.op.PhaseResults = append(s.op.PhaseResults, transitionoperation.PhaseResult{Phase: input.Phase, Status: input.Status, Result: []byte(`{"uncertain":true}`)})
	return s.op, nil
}

type upgradePreflight struct {
	result transitionpreflight.ResolutionResult
	err    error
}

func (p upgradePreflight) ResolveAndEvaluate(_ context.Context, request transitionpreflight.ResolutionRequest) (transitionpreflight.ResolutionResult, error) {
	if p.err != nil {
		return transitionpreflight.ResolutionResult{}, p.err
	}
	if request.PredecessorRef != p.result.Evidence.Predecessor.Release.Image || request.CandidateRef != p.result.Evidence.Candidate.Release.Image || request.TargetRef != "target" {
		return transitionpreflight.ResolutionResult{}, errors.New("wrong authoritative selector")
	}
	return p.result, nil
}

type upgradeControl struct {
	configured      string
	running         string
	runtimeOverride string
	updateErr       error
	startErr        error
	starts          int
}

func (c *upgradeControl) ConfiguredImage() (string, error) { return c.configured, nil }
func (c *upgradeControl) UpdateImage(image string) error {
	if c.updateErr != nil {
		return c.updateErr
	}
	c.configured = image
	return nil
}
func (c *upgradeControl) Start(context.Context) error {
	c.starts++
	if c.startErr != nil {
		return c.startErr
	}
	c.running = c.configured
	if c.runtimeOverride != "" {
		c.running = c.runtimeOverride
	}
	return nil
}
func (c *upgradeControl) RunningImage(context.Context) (string, error) { return c.running, nil }

func upgradeFixture(t *testing.T) (*Upgrader, UpgradeRequest, *upgradeStore, *upgradeControl, Paths) {
	t.Helper()
	paths := testPaths(t)
	writeTestPayload(t, paths.Payload)
	result := upgradePreflightFixture(t)
	predecessor := result.Evidence.Predecessor.Release.Image
	writeConfig(t, paths.Config, Config{SchemaVersion: 1, Domain: "dash.example.com", AdminEmail: "admin@example.com", Environment: "prod", Image: predecessor, TargetID: "target", HTTPS: boolPointer(true)})
	installer, err := New(Options{Paths: paths, LifecycleFactory: func(string) (Lifecycle, error) { return &recordingLifecycle{}, nil }})
	require.NoError(t, err)
	require.NoError(t, installer.Install(t.Context()))
	evidenceBytes, err := result.Evidence.CanonicalJSON()
	require.NoError(t, err)
	predDigest, err := result.Evidence.Predecessor.Digest()
	require.NoError(t, err)
	candidateDigest, err := result.Evidence.Candidate.Digest()
	require.NoError(t, err)
	store := &upgradeStore{op: transitionoperation.Operation{OperationID: upgradeOperationID, TargetIdentityDigest: result.Evidence.TargetIdentityDigest, PredecessorArtifactDigest: predDigest, CandidateArtifactDigest: candidateDigest, PreflightEvidence: evidenceBytes, PreflightEvidenceDigest: result.EvidenceDigest, Status: transitionoperation.StatusRunning, CurrentPhase: transitionoperation.PhaseCandidateStaged, Fence: transitionoperation.Fence{OperationID: upgradeOperationID, OwnerID: "owner", FencingGeneration: 1, LeaseExpiresAt: time.Now().Add(15 * time.Minute)}}}
	control := &upgradeControl{configured: predecessor, running: predecessor}
	upgrader, err := NewUpgrader(UpgradeOptions{Paths: paths, Operations: store, Preflight: upgradePreflight{result: result}, Control: UpgradeControl{ConfiguredImage: control.ConfiguredImage, UpdateImage: control.UpdateImage, Start: control.Start, RunningImage: control.RunningImage}, Payload: func(context.Context, string) (map[string][]byte, error) { return testPayload("candidate-"), nil }})
	require.NoError(t, err)
	return upgrader, UpgradeRequest{OperationID: upgradeOperationID, CandidateImage: result.Evidence.Candidate.Release.Image, TargetID: "target", Phase: transitionrunner.PhaseStage}, store, control, paths
}

func upgradePreflightFixture(t *testing.T) transitionpreflight.ResolutionResult {
	t.Helper()
	digest := func(char string) string { return "sha256:" + strings.Repeat(char, 64) }
	pred := transitionpreflight.ArtifactIdentity{Release: compatibility.ReleaseIdentity{ReleaseID: "pred", Version: "1", SourceRevision: strings.Repeat("1", 40), Image: "ghcr.io/example@" + digest("a"), Distribution: "public", Platform: "linux/amd64"}, ArchitectureMarker: transitionpreflight.ArchitecturePostgreSQL, ArtifactAdmissionDigest: digest("d")}
	candidate := transitionpreflight.ArtifactIdentity{Release: compatibility.ReleaseIdentity{ReleaseID: "cand", Version: "2", SourceRevision: strings.Repeat("2", 40), Image: "ghcr.io/example@" + digest("b"), Distribution: "public", Platform: "linux/amd64"}, ArchitectureMarker: transitionpreflight.ArchitecturePostgreSQL, ArtifactAdmissionDigest: digest("e")}
	pd, _ := pred.Digest()
	cd, _ := candidate.Digest()
	policy := transitionpreflight.ReleasePolicy{Version: transitionpreflight.ReleasePolicyVersion, Rules: []transitionpreflight.ReleasePolicyRule{{PredecessorArtifactDigest: pd, CandidateArtifactDigest: cd, RollbackFromArtifactDigest: cd, RollbackToArtifactDigest: pd, Decision: transitionpreflight.DecisionBinaryRollbackCompatible}}}
	policy.Digest, _ = policy.ContentDigest()
	target := digest("f")
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "catalog:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	evidence, err := transitionpreflight.Evaluate(transitionpreflight.Input{SchemaVersion: 1, TargetIdentityDigest: target, Predecessor: pred, Candidate: candidate, MigrationOwnership: transitionpreflight.MigrationOwnership{GooseControlSchemaOwner: transitionpreflight.OwnerLeapView, RiverOperationalSchemaOwner: transitionpreflight.OwnerRiver, RiverJobHistoryOwner: transitionpreflight.OwnerLeapView}, Control: transitionpreflight.PostgreSQLControlProjection{Compatibility: transitionpreflight.CompatibilityBackwardCompatible, PredecessorSchemaVersion: "019", CandidateSchemaVersion: "020", TargetIdentityDigest: target}, River: transitionpreflight.RiverJobProjection{SchemaCompatibility: transitionpreflight.CompatibilityBackwardCompatible, JobHistoryCompatibility: transitionpreflight.CompatibilityBackwardCompatible, ExistingSchemaVersion: "river/v1", RequiredSchemaVersion: "river/v2", ExistingJobHistoryVersion: "jobs/v1", RequiredJobHistoryVersion: "jobs/v2", TargetIdentityDigest: target}, DuckLake: transitionpreflight.DuckLakeProjection{Compatibility: transitionpreflight.CompatibilityBackwardCompatible, Predecessor: tuple, Candidate: tuple, TargetIdentityDigest: target}, RecoveryFrontier: &transitionpreflight.RecoveryFrontierRef{SetID: "018f3f83-7b2f-7b37-9f9e-000000000010", Digest: digest("c"), TargetIdentityDigest: target}, ReleasePolicy: policy})
	require.NoError(t, err)
	evidenceDigest, err := evidence.Digest()
	require.NoError(t, err)
	return transitionpreflight.ResolutionResult{Evidence: evidence, EvidenceDigest: evidenceDigest}
}

func assertUpgradeResult(t *testing.T, result transitionrunner.EffectResult, request UpgradeRequest, store *upgradeStore) {
	t.Helper()
	require.Equal(t, store.op.TargetIdentityDigest, result.TargetIdentityDigest)
	require.Equal(t, store.op.CandidateArtifactDigest, result.CandidateArtifactDigest)
	var evidence upgradePhaseEvidence
	require.NoError(t, json.Unmarshal(result.Payload, &evidence))
	require.Equal(t, request.CandidateImage, evidence.CandidateImage)
	require.Equal(t, store.op.PreflightEvidenceDigest, evidence.PreflightDigest)
	require.Equal(t, "sha256-"+strings.Repeat("b", 64), evidence.Generation)
}

func TestUpgradeStagesThenActivatesAndRestartsExactCandidate(t *testing.T) {
	upgrader, request, store, control, paths := upgradeFixture(t)
	result, err := upgrader.Upgrade(t.Context(), request)
	require.NoError(t, err)
	assertUpgradeResult(t, result, request, store)
	active, err := activeGeneration(paths)
	require.NoError(t, err)
	require.Equal(t, "sha256-"+strings.Repeat("a", 64), active, "staging must not activate")
	result, err = upgrader.Upgrade(t.Context(), request)
	require.NoError(t, err, "staging retry must validate the same immutable payload")
	assertUpgradeResult(t, result, request, store)
	store.op.CurrentPhase = transitionoperation.PhaseCandidateActivated // runner persisted staging
	request.Phase = transitionrunner.PhaseActivate
	upgrader.options.Activate = func(paths Paths, generation string) error {
		_, err := os.Stat(filepath.Join(paths.Root, "releases", generation, "compose.yaml"))
		require.NoError(t, err, "candidate must be staged before activation")
		return activateGeneration(paths, generation)
	}
	result, err = upgrader.Upgrade(t.Context(), request)
	require.NoError(t, err)
	assertUpgradeResult(t, result, request, store)
	active, err = activeGeneration(paths)
	require.NoError(t, err)
	require.Equal(t, "sha256-"+strings.Repeat("b", 64), active)
	require.Equal(t, request.CandidateImage, control.configured)
	marker, _, err := readAndValidateConfig(filepath.Join(paths.Root, installMarkerName))
	require.NoError(t, err)
	require.Equal(t, request.CandidateImage, marker.Image)
	// An activation retry before the runner records the phase is idempotent.
	result, err = upgrader.Upgrade(t.Context(), request)
	require.NoError(t, err)
	assertUpgradeResult(t, result, request, store)
	store.op.CurrentPhase = transitionoperation.PhaseCandidateRestarted // runner persisted activation
	request.Phase = transitionrunner.PhaseRestart
	result, err = upgrader.Upgrade(t.Context(), request)
	require.NoError(t, err)
	assertUpgradeResult(t, result, request, store)
	require.Equal(t, request.CandidateImage, control.running)
	require.Equal(t, 1, control.starts)
}

func TestUpgradeAndRecordAdvancesDurableHostPhases(t *testing.T) {
	upgrader, request, store, control, _ := upgradeFixture(t)
	result, err := upgrader.UpgradeAndRecord(t.Context(), request)
	require.NoError(t, err)
	assertUpgradeResult(t, result, request, store)
	require.Equal(t, transitionrunner.PhaseActivate, store.op.CurrentPhase)
	require.Len(t, store.op.PhaseResults, 1)
	require.Equal(t, transitionoperation.PhaseResultSucceeded, store.op.PhaseResults[0].Status)

	request.Phase = transitionrunner.PhaseActivate
	result, err = upgrader.UpgradeAndRecord(t.Context(), request)
	require.NoError(t, err, "activation must use the phase persisted by staging")
	assertUpgradeResult(t, result, request, store)
	require.Equal(t, transitionrunner.PhaseRestart, store.op.CurrentPhase)
	require.Equal(t, request.CandidateImage, control.configured)
	require.Len(t, store.op.PhaseResults, 2)
}

func TestUpgradeAndRecordFailsIndeterminateWhenPhaseWriteFails(t *testing.T) {
	upgrader, request, store, _, paths := upgradeFixture(t)
	store.recordErr = errors.New("phase write unavailable")
	result, err := upgrader.UpgradeAndRecord(t.Context(), request)
	require.ErrorIs(t, err, transitionoperation.ErrIndeterminate)
	require.Empty(t, result.Payload)
	require.Equal(t, transitionoperation.StatusIndeterminate, store.op.Status)
	require.Equal(t, transitionoperation.PhaseResultIndeterminate, store.op.PhaseResults[0].Status)
	_, err = os.Stat(filepath.Join(paths.Root, "releases", "sha256-"+strings.Repeat("b", 64)))
	require.NoError(t, err, "the external stage effect completed before the failed phase write")
}

func TestUpgradeAndRecordRejectsFenceChangeBeforeEffect(t *testing.T) {
	upgrader, request, store, _, _ := upgradeFixture(t)
	store.rotateFenceOnSecond = true
	called := false
	upgrader.options.Payload = func(context.Context, string) (map[string][]byte, error) { called = true; return nil, nil }
	_, err := upgrader.UpgradeAndRecord(t.Context(), request)
	require.ErrorIs(t, err, transitionoperation.ErrStaleFence)
	require.False(t, called)
	require.Empty(t, store.op.PhaseResults)
}

func TestUpgradeAndRecordRecordsUncertainHostMutation(t *testing.T) {
	for _, phase := range []transitionrunner.Phase{transitionrunner.PhaseActivate, transitionrunner.PhaseRestart} {
		t.Run(string(phase), func(t *testing.T) {
			upgrader, request, store, control, paths := upgradeFixture(t)
			_, err := upgrader.UpgradeAndRecord(t.Context(), request)
			require.NoError(t, err)
			request.Phase = transitionrunner.PhaseActivate
			if phase == transitionrunner.PhaseActivate {
				upgrader.options.Activate = func(paths Paths, generation string) error {
					require.NoError(t, activateGeneration(paths, generation))
					return errors.New("activation response lost")
				}
			} else {
				_, err = upgrader.UpgradeAndRecord(t.Context(), request)
				require.NoError(t, err)
				request.Phase = transitionrunner.PhaseRestart
				control.startErr = errors.New("health failed after Compose up")
			}
			_, err = upgrader.UpgradeAndRecord(t.Context(), request)
			require.ErrorIs(t, err, transitionoperation.ErrIndeterminate)
			require.Equal(t, transitionoperation.StatusIndeterminate, store.op.Status)
			require.Equal(t, phase, store.op.PhaseResults[len(store.op.PhaseResults)-1].Phase)
			require.Equal(t, transitionoperation.PhaseResultIndeterminate, store.op.PhaseResults[len(store.op.PhaseResults)-1].Status)
			if phase == transitionrunner.PhaseActivate {
				active, activeErr := activeGeneration(paths)
				require.NoError(t, activeErr)
				require.Equal(t, "sha256-"+strings.Repeat("b", 64), active)
			} else {
				require.Equal(t, 1, control.starts)
			}
		})
	}
}

func TestUpgradeAndRecordRenewsLeaseDuringSlowEffect(t *testing.T) {
	upgrader, request, store, _, _ := upgradeFixture(t)
	store.op.Fence.LeaseExpiresAt = time.Now().Add(250 * time.Millisecond)
	store.leaseUntil.Store(store.op.Fence.LeaseExpiresAt.UnixNano())
	store.enforceLease = true
	upgrader.options.Payload = func(ctx context.Context, _ string) (map[string][]byte, error) {
		select {
		case <-time.After(700 * time.Millisecond):
			return testPayload("candidate-"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	_, err := upgrader.UpgradeAndRecord(t.Context(), request)
	require.NoError(t, err)
	require.Greater(t, store.renewCount.Load(), int64(0))
	require.Equal(t, transitionrunner.PhaseActivate, store.op.CurrentPhase)
}

func TestUpgradeAndRecordLostLeaseAfterEffectBeginsIsIndeterminate(t *testing.T) {
	upgrader, request, store, _, _ := upgradeFixture(t)
	store.op.Fence.LeaseExpiresAt = time.Now().Add(250 * time.Millisecond)
	store.renewErr = transitionoperation.ErrStaleFence
	upgrader.options.Payload = func(ctx context.Context, _ string) (map[string][]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_, err := upgrader.UpgradeAndRecord(t.Context(), request)
	require.ErrorIs(t, err, transitionoperation.ErrIndeterminate)
	require.ErrorIs(t, err, transitionoperation.ErrStaleFence)
	require.Equal(t, transitionoperation.StatusIndeterminate, store.op.Status)
}

func TestUpgradeAndRecordExactCompletedRetryUsesDurableResult(t *testing.T) {
	upgrader, request, store, _, _ := upgradeFixture(t)
	first, err := upgrader.UpgradeAndRecord(t.Context(), request)
	require.NoError(t, err)
	upgrader.options.Payload = func(context.Context, string) (map[string][]byte, error) {
		t.Fatal("completed retry must not execute candidate payload effect")
		return nil, nil
	}
	retry, err := upgrader.UpgradeAndRecord(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, first, retry)
	require.Len(t, store.op.PhaseResults, 1)

	wrongTarget := request
	wrongTarget.TargetID = "another-target"
	_, err = upgrader.UpgradeAndRecord(t.Context(), wrongTarget)
	require.ErrorIs(t, err, transitionoperation.ErrConflict)
	wrongCandidate := request
	wrongCandidate.CandidateImage = "ghcr.io/example@sha256:" + strings.Repeat("c", 64)
	_, err = upgrader.UpgradeAndRecord(t.Context(), wrongCandidate)
	require.ErrorIs(t, err, transitionoperation.ErrConflict)
	upgrader.options.Preflight = upgradePreflight{err: errors.New("revoked admission")}
	_, err = upgrader.UpgradeAndRecord(t.Context(), request)
	require.ErrorIs(t, err, transitionoperation.ErrConflict)
}

func TestUpgradeRejectsWrongOrUnboundInstalledTargetBeforeEffect(t *testing.T) {
	for _, targetID := range []string{"other-target", ""} {
		t.Run("installed-"+targetID, func(t *testing.T) {
			upgrader, request, store, _, paths := upgradeFixture(t)
			markerPath := filepath.Join(paths.Root, installMarkerName)
			marker, _, err := readAndValidateConfig(markerPath)
			require.NoError(t, err)
			marker.TargetID = targetID
			contents, err := json.Marshal(marker)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(markerPath, contents, 0o600))
			called := false
			upgrader.options.Payload = func(context.Context, string) (map[string][]byte, error) { called = true; return nil, nil }
			_, err = upgrader.UpgradeAndRecord(t.Context(), request)
			require.ErrorContains(t, err, "installed host target identity")
			require.False(t, called)
			require.Equal(t, transitionrunner.PhaseStage, store.op.CurrentPhase)
			require.Empty(t, store.op.PhaseResults)
		})
	}
}

func TestUpgradeResumesInterruptedActivation(t *testing.T) {
	for _, configuredCandidate := range []bool{false, true} {
		name := "generation-switched"
		if configuredCandidate {
			name = "compose-updated"
		}
		t.Run(name, func(t *testing.T) {
			upgrader, request, store, control, paths := upgradeFixture(t)
			_, err := upgrader.Upgrade(t.Context(), request)
			require.NoError(t, err)
			store.op.CurrentPhase = transitionoperation.PhaseCandidateActivated
			request.Phase = transitionrunner.PhaseActivate
			require.NoError(t, activateGeneration(paths, "sha256-"+strings.Repeat("b", 64)))
			if configuredCandidate {
				require.NoError(t, control.UpdateImage(request.CandidateImage))
			}
			upgrader.options.Activate = func(Paths, string) error { return errors.New("candidate generation must not be activated twice") }
			result, err := upgrader.Upgrade(t.Context(), request)
			require.NoError(t, err)
			assertUpgradeResult(t, result, request, store)
			marker, _, err := readAndValidateConfig(filepath.Join(paths.Root, installMarkerName))
			require.NoError(t, err)
			require.Equal(t, request.CandidateImage, marker.Image)
			require.Equal(t, request.CandidateImage, control.configured)
		})
	}
}

func TestUpgradeRejectsMismatchesBeforePayloadEffect(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Upgrader, *UpgradeRequest, *upgradeStore)
	}{
		{"wrong candidate", func(_ *Upgrader, r *UpgradeRequest, _ *upgradeStore) {
			r.CandidateImage = "ghcr.io/example@sha256:" + strings.Repeat("c", 64)
		}},
		{"mutable candidate", func(_ *Upgrader, r *UpgradeRequest, _ *upgradeStore) { r.CandidateImage = "ghcr.io/example:latest" }},
		{"wrong admission digest", func(_ *Upgrader, _ *UpgradeRequest, s *upgradeStore) {
			s.op.CandidateArtifactDigest = "sha256:" + strings.Repeat("c", 64)
		}},
		{"wrong target", func(_ *Upgrader, r *UpgradeRequest, _ *upgradeStore) { r.TargetID = "other-target" }},
		{"stale preflight", func(u *Upgrader, _ *UpgradeRequest, _ *upgradeStore) {
			u.options.Preflight = upgradePreflight{err: errors.New("revoked admission")}
		}},
		{"preflight digest changed", func(u *Upgrader, _ *UpgradeRequest, _ *upgradeStore) {
			result := u.options.Preflight.(upgradePreflight).result
			result.EvidenceDigest = "sha256:" + strings.Repeat("c", 64)
			u.options.Preflight = upgradePreflight{result: result}
		}},
		{"lost fence", func(_ *Upgrader, _ *UpgradeRequest, s *upgradeStore) { s.validateErr = errors.New("expired") }},
		{"wrong phase", func(_ *Upgrader, r *UpgradeRequest, _ *upgradeStore) { r.Phase = transitionrunner.PhaseActivate }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upgrader, request, store, _, paths := upgradeFixture(t)
			called := false
			upgrader.options.Payload = func(context.Context, string) (map[string][]byte, error) { called = true; return nil, nil }
			tc.change(upgrader, &request, store)
			_, err := upgrader.Upgrade(t.Context(), request)
			require.Error(t, err)
			require.False(t, called)
			active, err := activeGeneration(paths)
			require.NoError(t, err)
			require.Equal(t, "sha256-"+strings.Repeat("a", 64), active)
		})
	}
}

func TestUpgradeFailuresPreservePredecessorOrFailClosed(t *testing.T) {
	for _, failure := range []string{"payload", "invalid-staging", "invalid-links", "activation", "restart", "wrong-runtime"} {
		t.Run(failure, func(t *testing.T) {
			upgrader, request, store, control, paths := upgradeFixture(t)
			if failure == "payload" {
				upgrader.options.Payload = func(context.Context, string) (map[string][]byte, error) { return nil, errors.New("pull failed") }
				_, err := upgrader.Upgrade(t.Context(), request)
				require.Error(t, err)
			} else if failure == "invalid-staging" {
				upgrader.options.Payload = func(context.Context, string) (map[string][]byte, error) { return map[string][]byte{}, nil }
				_, err := upgrader.Upgrade(t.Context(), request)
				require.Error(t, err)
			} else {
				_, err := upgrader.Upgrade(t.Context(), request)
				require.NoError(t, err)
				store.op.CurrentPhase = transitionoperation.PhaseCandidateActivated
				request.Phase = transitionrunner.PhaseActivate
				if failure == "invalid-links" {
					link := filepath.Join(paths.Root, "compose.yaml")
					require.NoError(t, os.Remove(link))
					require.NoError(t, os.WriteFile(link, []byte("unexpected compose"), 0o600))
				}
				if failure == "activation" {
					upgrader.options.Activate = func(Paths, string) error { return errors.New("activation failed") }
				}
				_, err = upgrader.Upgrade(t.Context(), request)
				require.Equal(t, failure == "activation" || failure == "invalid-links", err != nil)
				if failure == "restart" || failure == "wrong-runtime" {
					store.op.CurrentPhase = transitionoperation.PhaseCandidateRestarted
					request.Phase = transitionrunner.PhaseRestart
					if failure == "restart" {
						control.startErr = errors.New("health failed")
					} else {
						control.runtimeOverride = "ghcr.io/example@sha256:" + strings.Repeat("c", 64)
					}
					_, err = upgrader.Upgrade(t.Context(), request)
					require.Error(t, err)
				}
			}
			if failure == "payload" || failure == "invalid-staging" || failure == "invalid-links" || failure == "activation" {
				active, err := activeGeneration(paths)
				require.NoError(t, err)
				require.Equal(t, "sha256-"+strings.Repeat("a", 64), active)
				require.NotEqual(t, request.CandidateImage, control.running)
			}
			require.Equal(t, transitionoperation.StatusRunning, store.op.Status, "runner must classify and durably record effect failure")
		})
	}
}

func TestUpgradeRequiresExistingInstallation(t *testing.T) {
	upgrader, request, _, _, paths := upgradeFixture(t)
	require.NoError(t, os.Remove(filepath.Join(paths.Root, installMarkerName)))
	called := false
	upgrader.options.Payload = func(context.Context, string) (map[string][]byte, error) { called = true; return nil, nil }
	_, err := upgrader.Upgrade(t.Context(), request)
	require.ErrorContains(t, err, "existing host installation")
	require.False(t, called)
}
