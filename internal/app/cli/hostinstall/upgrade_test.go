package hostinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/release/transitionoperation"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/flidai/leapview/internal/release/transitionrunner"
	"github.com/stretchr/testify/require"
)

const upgradeOperationID = "0198f2c0-7c7a-7f00-8a11-000000000999"

type upgradeStore struct {
	op          transitionoperation.Operation
	validateErr error
}

func (s *upgradeStore) Get(context.Context, string) (transitionoperation.Operation, error) {
	return s.op, nil
}
func (s *upgradeStore) Validate(context.Context, transitionoperation.Fence) error {
	return s.validateErr
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
	writeConfig(t, paths.Config, Config{SchemaVersion: 1, Domain: "dash.example.com", AdminEmail: "admin@example.com", Environment: "prod", Image: predecessor, HTTPS: boolPointer(true)})
	installer, err := New(Options{Paths: paths, LifecycleFactory: func(string) (Lifecycle, error) { return &recordingLifecycle{}, nil }})
	require.NoError(t, err)
	require.NoError(t, installer.Install(t.Context()))
	evidenceBytes, err := result.Evidence.CanonicalJSON()
	require.NoError(t, err)
	predDigest, err := result.Evidence.Predecessor.Digest()
	require.NoError(t, err)
	candidateDigest, err := result.Evidence.Candidate.Digest()
	require.NoError(t, err)
	store := &upgradeStore{op: transitionoperation.Operation{OperationID: upgradeOperationID, TargetIdentityDigest: result.Evidence.TargetIdentityDigest, PredecessorArtifactDigest: predDigest, CandidateArtifactDigest: candidateDigest, PreflightEvidence: evidenceBytes, PreflightEvidenceDigest: result.EvidenceDigest, Status: transitionoperation.StatusRunning, CurrentPhase: transitionoperation.PhaseCandidateStaged, Fence: transitionoperation.Fence{OperationID: upgradeOperationID, OwnerID: "owner", FencingGeneration: 1}}}
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
