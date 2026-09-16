//go:build fai518qualification

package postgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/cli/hostinstall"
	releasetransitionapp "github.com/flidai/leapview/internal/app/releasetransitionpreflightproduction"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	postgresmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	recoverysetpostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/flidai/leapview/internal/release/transitionoperation"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/flidai/leapview/internal/release/transitionrunner"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	forwardQualificationEvidenceEnv = "LEAPVIEW_TEST_FAI518_RELEASE_TRANSITION_EVIDENCE_DIR"
	qualificationProcessEnv         = "LEAPVIEW_FAI518_PROCESS_IDENTITY"
	qualificationProcessImageEnv    = "LEAPVIEW_FAI518_PROCESS_IMAGE"
	qualificationProcessTargetEnv   = "LEAPVIEW_FAI518_PROCESS_TARGET"
)

// TestFAI518ForwardOnlyReleaseTransitionQualification is the bounded
// end-to-end lane for the forward runner. The only success artifact is
// published after the positive run, fresh-repository readback, and all
// negative/concurrency gates have passed.
func TestFAI518ForwardOnlyReleaseTransitionQualification(t *testing.T) {
	pool := productionPreflightDB(t)
	_, resolver, preflightRequest := forwardQualificationAuthorities(t, pool)
	transitions := releasepostgres.NewTransitionRepository(pool)
	reportPath := forwardQualificationReportPath(t)
	if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
		if err == nil {
			t.Fatalf("stale forward-transition success report exists: %s", reportPath)
		}
		t.Fatalf("inspect forward-transition report: %v", err)
	}

	effects := newForwardQualificationEffects(t, pool, productionAdmission("predecessor", 'a', '1').Release.Image, resolver, preflightRequest)
	t.Cleanup(effects.cleanup)
	runner := newForwardQualificationRunner(t, transitions, resolver, effects)
	request := transitionrunner.Request{
		OperationID:    "0198f2c0-7c7a-7f00-8a11-000000000901",
		OwnerID:        "qualification-forward-owner",
		IdempotencyKey: "qualification-forward-success",
		LeaseTTL:       2 * time.Minute,
		Preflight:      preflightRequest,
	}

	positive, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatalf("forward transition: %v", err)
	}
	if positive.Operation.Status != transitionoperation.StatusCompleted {
		t.Fatalf("forward transition status = %q, want completed", positive.Operation.Status)
	}
	if !effects.migrationsApplied {
		t.Fatal("forward transition did not apply the real PostgreSQL migration path")
	}
	if effects.predecessor == nil || effects.candidate == nil || effects.predecessor.Identity.PID == effects.candidate.Identity.PID {
		t.Fatalf("restart identity did not change process: predecessor=%#v candidate=%#v", effects.predecessor, effects.candidate)
	}
	if effects.candidate.Identity.Image != effects.candidateImage || effects.candidate.Identity.Target != positive.Evidence.TargetIdentityDigest {
		t.Fatalf("candidate restart identity = %#v", effects.candidate.Identity)
	}
	assertForwardPhaseResults(t, positive.Operation)

	// A retry after a process/repository boundary is idempotent and must not
	// execute any effect a second time.
	replayed, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatalf("completed transition replay: %v", err)
	}
	if replayed.Operation.OperationID != positive.Operation.OperationID || replayed.Operation.Status != transitionoperation.StatusCompleted {
		t.Fatalf("completed replay changed durable identity: first=%#v replay=%#v", positive.Operation, replayed.Operation)
	}

	readback := readForwardTransitionAfterRepositoryRestart(t, pool, positive.Operation.OperationID)
	if readback.RequestDigest != positive.Operation.RequestDigest || readback.PreflightEvidenceDigest != positive.Operation.PreflightEvidenceDigest || readback.Status != transitionoperation.StatusCompleted {
		t.Fatalf("durable transition readback changed identity: got=%#v want=%#v", readback, positive.Operation)
	}
	assertForwardPhaseResults(t, readback)
	assertAppliedMigrationRevision(t, pool)

	runForwardNegativeGates(t, pool, transitions, resolver, preflightRequest, reportPath)
	if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
		t.Fatalf("negative/concurrency gate published success report: %v", err)
	}

	report := forwardQualificationReport{
		SchemaVersion:             1,
		Kind:                      "leapview/fai518-forward-transition-qualification",
		Status:                    "success",
		OperationID:               positive.Operation.OperationID,
		RequestDigest:             positive.Operation.RequestDigest,
		EvidenceDigest:            positive.EvidenceDigest,
		TargetIdentityDigest:      positive.Operation.TargetIdentityDigest,
		PredecessorArtifactDigest: positive.Operation.PredecessorArtifactDigest,
		CandidateArtifactDigest:   positive.Operation.CandidateArtifactDigest,
		RecoveryFrontierID:        positive.Operation.RecoveryFrontierID,
		RecoveryFrontierDigest:    positive.Operation.RecoveryFrontierDigest,
		MigrationRev:              postgresmigrations.CurrentRevision,
		PredecessorImage:          effects.predecessor.Identity.Image,
		CandidateImage:            effects.candidate.Identity.Image,
		ActiveGeneration:          effects.generation,
		StateDigest:               effects.baselineState,
		PredecessorPID:            effects.predecessor.Identity.PID,
		CandidatePID:              effects.candidate.Identity.PID,
		Phases:                    transitionrunner.PhaseNames(),
		PublishedAt:               time.Now().UTC(),
	}
	if err := publishForwardQualificationReport(reportPath, report); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read published qualification report: %v", err)
	}
	if len(contents) > 64<<10 {
		t.Fatalf("qualification report is unbounded: %d bytes", len(contents))
	}
	if err := validateForwardQualificationReport(contents, report); err != nil {
		t.Fatalf("validate published qualification report: %v", err)
	}
}

// TestFAI518QualificationProcessIdentityHelper is launched as a real child
// process by the restart effect. It emits its immutable startup identity and
// stays alive until the effect terminates it.
func TestFAI518QualificationProcessIdentityHelper(t *testing.T) {
	if os.Getenv(qualificationProcessEnv) != "1" {
		return
	}
	identity := qualificationProcessIdentity{PID: os.Getpid(), Image: os.Getenv(qualificationProcessImageEnv), Target: os.Getenv(qualificationProcessTargetEnv)}
	if err := json.NewEncoder(os.Stdout).Encode(identity); err != nil {
		t.Fatal(err)
	}
	select {}
}

type forwardQualificationReport struct {
	SchemaVersion             int                      `json:"schemaVersion"`
	Kind                      string                   `json:"kind"`
	Status                    string                   `json:"status"`
	OperationID               string                   `json:"operationId"`
	RequestDigest             string                   `json:"requestDigest"`
	EvidenceDigest            string                   `json:"evidenceDigest"`
	TargetIdentityDigest      string                   `json:"targetIdentityDigest"`
	PredecessorArtifactDigest string                   `json:"predecessorArtifactDigest"`
	CandidateArtifactDigest   string                   `json:"candidateArtifactDigest"`
	RecoveryFrontierID        string                   `json:"recoveryFrontierId"`
	RecoveryFrontierDigest    string                   `json:"recoveryFrontierDigest"`
	MigrationRev              int64                    `json:"migrationRevision"`
	PredecessorImage          string                   `json:"predecessorImage"`
	CandidateImage            string                   `json:"candidateImage"`
	ActiveGeneration          string                   `json:"activeGeneration"`
	StateDigest               string                   `json:"representativeStateDigest"`
	PredecessorPID            int                      `json:"predecessorPid"`
	CandidatePID              int                      `json:"candidatePid"`
	Phases                    []transitionrunner.Phase `json:"phases"`
	PublishedAt               time.Time                `json:"publishedAt"`
}

type qualificationProcessIdentity struct {
	PID    int    `json:"pid"`
	Image  string `json:"image"`
	Target string `json:"targetIdentityDigest"`
}

type qualificationProcess struct {
	Command  *exec.Cmd
	Identity qualificationProcessIdentity
}

type forwardQualificationEffects struct {
	pool              *pgxpool.Pool
	paths             hostinstall.Paths
	payload           map[string][]byte
	predecessorImage  string
	candidateImage    string
	preflight         transitionrunner.PreflightAuthority
	preflightRequest  transitionpreflight.ResolutionRequest
	baselineState     string
	failPhase         transitionrunner.Phase
	migrationsApplied bool
	generation        string
	predecessor       *qualificationProcess
	candidate         *qualificationProcess
	migrationStarted  chan struct{}
	migrationRelease  <-chan struct{}
	cleanupOnce       sync.Once
}

func newForwardQualificationEffects(t *testing.T, pool *pgxpool.Pool, predecessorImage string, preflight transitionrunner.PreflightAuthority, request transitionpreflight.ResolutionRequest) *forwardQualificationEffects {
	t.Helper()
	base := t.TempDir()
	effects := &forwardQualificationEffects{
		pool: pool,
		paths: hostinstall.Paths{
			Root:      filepath.Join(base, "install"),
			ConfigDir: filepath.Join(base, "etc"),
			SystemBin: filepath.Join(base, "bin"),
			Systemd:   filepath.Join(base, "systemd"),
			Systemctl: "systemctl",
		},
		payload:          qualificationPayload(),
		predecessorImage: predecessorImage,
		candidateImage:   productionAdmission("candidate", 'b', '2').Release.Image,
		preflight:        preflight,
		preflightRequest: request,
	}
	state, err := representativeStateDigest(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	effects.baselineState = state
	return effects
}

func (e *forwardQualificationEffects) Migrations(ctx context.Context, in transitionrunner.EffectInput) (transitionrunner.EffectResult, error) {
	control, err := sql.Open("pgx", e.pool.Config().ConnString())
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	defer control.Close()
	if err := postgresmigrations.ApplyRiverAndGoose(ctx, e.pool, control, nil); err != nil {
		return transitionrunner.EffectResult{}, fmt.Errorf("apply PostgreSQL migrations: %w", err)
	}
	e.migrationsApplied = true
	if e.failPhase == transitionrunner.PhaseMigrations {
		return transitionrunner.EffectResult{}, errors.New("qualification injected migration failure")
	}
	process, err := startQualificationProcess(ctx, e.predecessorImage, in.Evidence.TargetIdentityDigest)
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	e.predecessor = process
	if e.migrationStarted != nil {
		close(e.migrationStarted)
		if e.migrationRelease != nil {
			select {
			case <-e.migrationRelease:
			case <-ctx.Done():
				return transitionrunner.EffectResult{}, ctx.Err()
			}
		}
	}
	return transitionrunner.EffectResult{}, nil
}

func (e *forwardQualificationEffects) Stage(_ context.Context, in transitionrunner.EffectInput) (transitionrunner.EffectResult, error) {
	generation, err := hostinstall.QualificationStage(e.paths, in.Evidence.Candidate.Release.Image, e.payload)
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	e.generation = generation
	if e.failPhase == transitionrunner.PhaseStage {
		return transitionrunner.EffectResult{}, errors.New("qualification injected stage failure")
	}
	return transitionrunner.EffectResult{}, nil
}

func (e *forwardQualificationEffects) Activate(_ context.Context, _ transitionrunner.EffectInput) (transitionrunner.EffectResult, error) {
	if err := hostinstall.QualificationActivate(e.paths, e.generation); err != nil {
		return transitionrunner.EffectResult{}, err
	}
	if e.failPhase == transitionrunner.PhaseActivate {
		return transitionrunner.EffectResult{}, errors.New("qualification injected activate failure")
	}
	return transitionrunner.EffectResult{}, nil
}

func (e *forwardQualificationEffects) Restart(ctx context.Context, in transitionrunner.EffectInput) (transitionrunner.EffectResult, error) {
	if e.predecessor == nil {
		return transitionrunner.EffectResult{}, errors.New("predecessor process was not started")
	}
	if err := e.predecessor.stop(); err != nil {
		return transitionrunner.EffectResult{}, err
	}
	candidate, err := startQualificationProcess(ctx, e.candidateImage, in.Evidence.TargetIdentityDigest)
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	e.candidate = candidate
	candidateDigest, err := in.Evidence.Candidate.Digest()
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	result := transitionrunner.EffectResult{TargetIdentityDigest: in.Evidence.TargetIdentityDigest, CandidateArtifactDigest: candidateDigest}
	if e.failPhase == transitionrunner.PhaseRestart {
		return result, errors.New("qualification injected restart failure")
	}
	return result, nil
}

func (e *forwardQualificationEffects) PostValidate(ctx context.Context, in transitionrunner.EffectInput) (transitionrunner.EffectResult, error) {
	if e.candidate == nil {
		return transitionrunner.EffectResult{}, errors.New("candidate process was not started")
	}
	active, err := hostinstall.QualificationActiveGeneration(e.paths)
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	if active != e.generation || e.candidate.Identity.Image != e.candidateImage || e.candidate.Identity.Target != in.Evidence.TargetIdentityDigest {
		return transitionrunner.EffectResult{}, errors.New("post-restart identity did not match the activated candidate")
	}
	revalidated, err := e.preflight.ResolveAndEvaluate(ctx, e.preflightRequest)
	if err != nil || revalidated.EvidenceDigest != in.Operation.PreflightEvidenceDigest {
		return transitionrunner.EffectResult{}, errors.New("post-transition authoritative compatibility changed")
	}
	stateDigest, err := representativeStateDigest(ctx, e.pool)
	if err != nil || stateDigest != e.baselineState {
		return transitionrunner.EffectResult{}, errors.New("representative application state changed")
	}
	candidateDigest, err := in.Evidence.Candidate.Digest()
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	result := transitionrunner.EffectResult{TargetIdentityDigest: in.Evidence.TargetIdentityDigest, CandidateArtifactDigest: candidateDigest, StateIdentityDigest: stateDigest}
	if e.failPhase == transitionrunner.PhasePostValidate {
		return result, errors.New("qualification injected post-validation failure")
	}
	return result, nil
}

func (e *forwardQualificationEffects) cleanup() {
	e.cleanupOnce.Do(func() {
		if e.predecessor != nil {
			_ = e.predecessor.stop()
		}
		if e.candidate != nil {
			_ = e.candidate.stop()
		}
	})
}

func (p *qualificationProcess) stop() error {
	if p == nil || p.Command == nil || p.Command.Process == nil {
		return nil
	}
	if err := p.Command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	err := p.Command.Wait()
	if err != nil {
		return nil
	}
	return nil
}

func startQualificationProcess(ctx context.Context, image, target string) (*qualificationProcess, error) {
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFAI518QualificationProcessIdentityHelper$")
	command.Env = append(os.Environ(), qualificationProcessEnv+"=1", qualificationProcessImageEnv+"="+image, qualificationProcessTargetEnv+"="+target)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	var identity qualificationProcessIdentity
	if err := json.NewDecoder(stdout).Decode(&identity); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("read process identity: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return &qualificationProcess{Command: command, Identity: identity}, nil
}

func forwardQualificationAuthorities(t *testing.T, pool *pgxpool.Pool) (*releasepostgres.Repository, transitionrunner.PreflightAuthority, transitionpreflight.ResolutionRequest) {
	t.Helper()
	releases := releasepostgres.New(pool)
	targets := deploymentpostgres.New(pool)
	recoverySets := recoverysetpostgres.New(pool)
	capabilities, err := releasepostgres.NewMigrationCapabilityAuthority(releases, productionOwnerRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := targets.CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: "target", ProjectID: "project", Environment: "production", TargetRevision: 1}); err != nil {
		t.Fatal(err)
	}
	targetDigest, err := (transitionpreflight.TargetIdentity{TargetID: "target", TargetRevision: 1}).Digest()
	if err != nil {
		t.Fatal(err)
	}
	predecessor := productionAdmission("predecessor", 'a', '1')
	candidate := productionAdmission("candidate", 'b', '2')
	predecessorIdentity := publishProductionAdmission(t, releases, predecessor)
	candidateIdentity := publishProductionAdmission(t, releases, candidate)
	publishProductionCapabilities(t, capabilities, predecessorIdentity.ArtifactAdmissionDigest, targetDigest, false)
	publishProductionCapabilities(t, capabilities, candidateIdentity.ArtifactAdmissionDigest, targetDigest, true)
	publishProductionPolicy(t, releases, predecessorIdentity, candidateIdentity)
	frontier := productionPublishedFrontier(t, recoverySets, productionRecoverySetFixture(t))
	resolver, err := releasetransitionapp.NewProductionResolver(releasetransitionapp.ProductionDependencies{Releases: releases, Targets: targets, MigrationCapabilities: capabilities, RecoverySets: recoverySets})
	if err != nil {
		t.Fatal(err)
	}
	return releases, resolver, transitionpreflight.ResolutionRequest{PredecessorRef: predecessor.Release.Image, CandidateRef: candidate.Release.Image, TargetRef: "target", RecoveryFrontier: transitionpreflight.RecoveryFrontierRef{SetID: frontier.ID, Digest: frontier.FrontierDigest}}
}

func newForwardQualificationRunner(t *testing.T, transitions *releasepostgres.TransitionRepository, resolver transitionrunner.PreflightAuthority, effects *forwardQualificationEffects) *transitionrunner.Runner {
	t.Helper()
	runner, err := transitionrunner.New(transitionrunner.Options{Operations: transitions, Preflight: resolver, Fences: transitions, Effects: effects, LeaseTTL: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func runForwardNegativeGates(t *testing.T, pool *pgxpool.Pool, transitions *releasepostgres.TransitionRepository, resolver transitionrunner.PreflightAuthority, preflightRequest transitionpreflight.ResolutionRequest, reportPath string) {
	t.Helper()
	t.Run("migration incompatibility", func(t *testing.T) {
		rejected := &rejectedForwardPreflight{cause: errors.New("authoritative migration compatibility rejected")}
		effects := newForwardQualificationEffects(t, pool, productionAdmission("predecessor", 'a', '1').Release.Image, rejected, preflightRequest)
		defer effects.cleanup()
		runner := newForwardQualificationRunner(t, transitions, rejected, effects)
		_, err := runner.Run(t.Context(), transitionrunner.Request{OwnerID: "qualification-negative-owner", IdempotencyKey: "qualification-negative-unsupported", Preflight: preflightRequest})
		if !errors.Is(err, transitionrunner.ErrStalePreflight) {
			t.Fatalf("migration incompatibility error = %v", err)
		}
	})
	t.Run("wrong artifact pair", func(t *testing.T) {
		invalid := preflightRequest
		invalid.PredecessorRef, invalid.CandidateRef = invalid.CandidateRef, invalid.PredecessorRef
		effects := newForwardQualificationEffects(t, pool, productionAdmission("predecessor", 'a', '1').Release.Image, resolver, invalid)
		defer effects.cleanup()
		runner := newForwardQualificationRunner(t, transitions, resolver, effects)
		_, err := runner.Run(t.Context(), transitionrunner.Request{OwnerID: "qualification-negative-owner", IdempotencyKey: "qualification-negative-pair", Preflight: invalid})
		if !errors.Is(err, transitionrunner.ErrStalePreflight) {
			t.Fatalf("wrong artifact pair error = %v", err)
		}
	})
	runForwardFailureMatrix(t, pool, transitions, resolver, preflightRequest, reportPath)
	runForwardConcurrencyGate(t, pool, transitions, resolver, preflightRequest, reportPath)
}

type rejectedForwardPreflight struct{ cause error }

func (r *rejectedForwardPreflight) ResolveAndEvaluate(context.Context, transitionpreflight.ResolutionRequest) (transitionpreflight.ResolutionResult, error) {
	return transitionpreflight.ResolutionResult{}, r.cause
}

func runForwardFailureMatrix(t *testing.T, pool *pgxpool.Pool, transitions *releasepostgres.TransitionRepository, resolver transitionrunner.PreflightAuthority, request transitionpreflight.ResolutionRequest, reportPath string) {
	t.Helper()
	for _, phase := range []transitionrunner.Phase{transitionrunner.PhaseMigrations, transitionrunner.PhaseStage, transitionrunner.PhaseActivate, transitionrunner.PhaseRestart, transitionrunner.PhasePostValidate} {
		t.Run("failure-"+string(phase), func(t *testing.T) {
			effects := newForwardQualificationEffects(t, pool, productionAdmission("predecessor", 'a', '1').Release.Image, resolver, request)
			effects.failPhase = phase
			defer effects.cleanup()
			runner := newForwardQualificationRunner(t, transitions, resolver, effects)
			result, err := runner.Run(t.Context(), transitionrunner.Request{OwnerID: "qualification-failure-" + string(phase), IdempotencyKey: "qualification-failure-" + string(phase), Preflight: request})
			if !errors.Is(err, transitionrunner.ErrPhaseFailure) {
				t.Fatalf("failure phase %s error = %v", phase, err)
			}
			if result.Operation.Status != transitionoperation.StatusIndeterminate {
				t.Fatalf("failure phase %s status = %q", phase, result.Operation.Status)
			}
			if _, statErr := os.Stat(reportPath); !os.IsNotExist(statErr) {
				t.Fatalf("failure phase %s published report: %v", phase, statErr)
			}
		})
	}
}

func runForwardConcurrencyGate(t *testing.T, pool *pgxpool.Pool, transitions *releasepostgres.TransitionRepository, resolver transitionrunner.PreflightAuthority, request transitionpreflight.ResolutionRequest, reportPath string) {
	t.Helper()
	first := newForwardQualificationEffects(t, pool, productionAdmission("predecessor", 'a', '1').Release.Image, resolver, request)
	first.migrationStarted = make(chan struct{})
	releaseMigration := make(chan struct{})
	first.migrationRelease = releaseMigration
	defer first.cleanup()
	second := newForwardQualificationEffects(t, pool, productionAdmission("predecessor", 'a', '1').Release.Image, resolver, request)
	defer second.cleanup()
	firstRunner := newForwardQualificationRunner(t, transitions, resolver, first)
	secondRunner := newForwardQualificationRunner(t, transitions, resolver, second)
	firstDone := make(chan error, 1)
	go func() {
		_, err := firstRunner.Run(t.Context(), transitionrunner.Request{OwnerID: "qualification-concurrency-first", IdempotencyKey: "qualification-concurrency-first", Preflight: request})
		firstDone <- err
	}()
	select {
	case <-first.migrationStarted:
	case <-time.After(30 * time.Second):
		t.Fatal("first concurrent transition did not reach migration")
	}
	secondDone := make(chan error, 1)
	go func() {
		_, err := secondRunner.Run(t.Context(), transitionrunner.Request{OwnerID: "qualification-concurrency-second", IdempotencyKey: "qualification-concurrency-second", Preflight: request})
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if !errors.Is(err, transitionrunner.ErrCompetingOwner) {
			t.Fatalf("competing transition error = %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("competing transition did not fail closed")
	}
	close(releaseMigration)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first concurrent transition: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("first concurrent transition did not finish")
	}
	if _, statErr := os.Stat(reportPath); !os.IsNotExist(statErr) {
		t.Fatalf("concurrency gate published report: %v", statErr)
	}
}

func assertForwardPhaseResults(t *testing.T, operation transitionoperation.Operation) {
	t.Helper()
	want := transitionoperation.PhaseNames()
	if len(operation.PhaseResults) != len(want) {
		t.Fatalf("phase result count = %d, want %d", len(operation.PhaseResults), len(want))
	}
	for index, phase := range want {
		result := operation.PhaseResults[index]
		if result.Phase != phase || result.Status != transitionoperation.PhaseResultSucceeded || len(result.Result) == 0 || result.ResultDigest == "" {
			t.Fatalf("phase result %d = %#v", index, result)
		}
	}
}

func readForwardTransitionAfterRepositoryRestart(t *testing.T, pool *pgxpool.Pool, operationID string) transitionoperation.Operation {
	t.Helper()
	reopened, err := pgxpool.New(t.Context(), pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	if err := reopened.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	operation, err := releasepostgres.NewTransitionRepository(reopened).Get(t.Context(), operationID)
	if err != nil {
		t.Fatalf("read transition after repository restart: %v", err)
	}
	return operation
}

func assertAppliedMigrationRevision(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var revision int64
	if err := pool.QueryRow(t.Context(), `SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&revision); err != nil {
		t.Fatalf("read applied Goose revision: %v", err)
	}
	if revision != postgresmigrations.CurrentRevision {
		t.Fatalf("applied Goose revision = %d, want %d", revision, postgresmigrations.CurrentRevision)
	}
}

func representativeStateDigest(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var targetID, projectID, environment string
	var targetRevision int64
	if err := pool.QueryRow(ctx, `SELECT target_id,project_id,environment,target_revision FROM delivery.delivery_target WHERE target_id='target'`).Scan(&targetID, &projectID, &environment, &targetRevision); err != nil {
		return "", fmt.Errorf("read representative target state: %w", err)
	}
	canonical, err := json.Marshal(struct {
		TargetID       string `json:"targetId"`
		ProjectID      string `json:"projectId"`
		Environment    string `json:"environment"`
		TargetRevision int64  `json:"targetRevision"`
	}{targetID, projectID, environment, targetRevision})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("leapview/fai518-representative-state/v1\n"), canonical...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func forwardQualificationReportPath(t *testing.T) string {
	t.Helper()
	directory := strings.TrimSpace(os.Getenv(forwardQualificationEvidenceEnv))
	if directory == "" {
		directory = t.TempDir()
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(directory, "transition-result.json")
}

func validateForwardQualificationReport(contents []byte, expected forwardQualificationReport) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var actual forwardQualificationReport
	if err := decoder.Decode(&actual); err != nil {
		return err
	}
	if actual.SchemaVersion != 1 || actual.Kind != "leapview/fai518-forward-transition-qualification" || actual.Status != "success" ||
		actual.OperationID != expected.OperationID || actual.RequestDigest != expected.RequestDigest || actual.EvidenceDigest != expected.EvidenceDigest ||
		actual.TargetIdentityDigest != expected.TargetIdentityDigest || actual.PredecessorArtifactDigest != expected.PredecessorArtifactDigest ||
		actual.CandidateArtifactDigest != expected.CandidateArtifactDigest || actual.RecoveryFrontierID != expected.RecoveryFrontierID ||
		actual.RecoveryFrontierDigest != expected.RecoveryFrontierDigest ||
		actual.MigrationRev != expected.MigrationRev || actual.PredecessorImage != expected.PredecessorImage ||
		actual.CandidateImage != expected.CandidateImage || actual.ActiveGeneration != expected.ActiveGeneration || actual.StateDigest != expected.StateDigest ||
		actual.PredecessorPID <= 0 || actual.CandidatePID <= 0 || actual.PredecessorPID == actual.CandidatePID ||
		!actual.PublishedAt.Equal(expected.PublishedAt) {
		return errors.New("qualification report identity does not match durable result")
	}
	wantPhases := transitionrunner.PhaseNames()
	if len(actual.Phases) != len(wantPhases) {
		return errors.New("qualification report phase count is incomplete")
	}
	for index := range wantPhases {
		if actual.Phases[index] != wantPhases[index] {
			return errors.New("qualification report phase order is invalid")
		}
	}
	return nil
}

func publishForwardQualificationReport(path string, report forwardQualificationReport) error {
	contents, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if len(contents) > 64<<10 {
		return fmt.Errorf("qualification report exceeds bounded size")
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".transition-qualification-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return err
	}
	return nil
}

func qualificationPayload() map[string][]byte {
	return map[string][]byte{
		"leapviewctl":            []byte("#!/bin/sh\nexit 0\n"),
		"compose.yaml":           []byte("services: {}\n"),
		"compose.https.yaml":     []byte("services: {}\n"),
		"Caddyfile":              []byte(":80 { respond / 200 }\n"),
		"deployment.env.example": []byte("LEAPVIEW_IMAGE=unused\n"),
		"leapviewctl-wrapper":    []byte("#!/bin/sh\nexit 0\n"),
	}
}
