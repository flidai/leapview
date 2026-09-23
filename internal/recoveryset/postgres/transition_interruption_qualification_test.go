//go:build fai518qualification && fai519qualification

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
	"syscall"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/cli/hostinstall"
	releasetransitionapp "github.com/flidai/leapview/internal/app/releasetransitionpreflightproduction"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	postgresmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	recoverysetpostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/flidai/leapview/internal/refresh/recovery"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/flidai/leapview/internal/release/transitionoperation"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/flidai/leapview/internal/release/transitionrunner"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	fai519EvidenceEnv = "LEAPVIEW_TEST_FAI519_INTERRUPTION_EVIDENCE_DIR"
	fai519ChildEnv    = "LEAPVIEW_FAI519_TRANSITION_CHILD"
	fai519RuntimeEnv  = "LEAPVIEW_FAI519_TARGET_RUNTIME"
	fai519ConfigEnv   = "LEAPVIEW_FAI519_CONFIG"
	fai519DSNEnv      = "LEAPVIEW_FAI519_POSTGRES_DSN"
	fai519RuntimeLock = ".target-runtime.lock"
)

type fai519Config struct {
	DSN            string                                `json:"-"`
	Root           string                                `json:"root"`
	OperationID    string                                `json:"operationId"`
	OwnerID        string                                `json:"ownerId"`
	IdempotencyKey string                                `json:"idempotencyKey"`
	Preflight      transitionpreflight.ResolutionRequest `json:"preflight"`
	Failpoint      string                                `json:"failpoint,omitempty"`
	FailureMode    string                                `json:"failureMode,omitempty"`
	ExpectedStatus transitionoperation.Status            `json:"expectedStatus,omitempty"`
	ExpectedError  string                                `json:"expectedError,omitempty"`
	BaselineState  string                                `json:"baselineState"`
	CandidateImage string                                `json:"candidateImage"`
	TargetDigest   string                                `json:"targetIdentityDigest"`
}

type fai519Classification string

const (
	fai519PriorValid         fai519Classification = "prior-valid"
	fai519CandidateValid     fai519Classification = "candidate-valid"
	fai519RecoverablePending fai519Classification = "recoverable-pending"
)

type fai519PreResumeState struct {
	Classification        fai519Classification        `json:"classification"`
	GooseRevision         int64                       `json:"gooseRevision"`
	LedgerStatus          string                      `json:"ledgerStatus"`
	LedgerFenceGeneration int64                       `json:"ledgerFenceGeneration"`
	TransitionStatus      transitionoperation.Status  `json:"transitionStatus"`
	DurablePhases         []transitionoperation.Phase `json:"durablePhases"`
	Effects               fai519EffectState           `json:"effects"`
	Runtime               fai519RuntimeIdentity       `json:"runtime"`
	RuntimeOwnsTarget     bool                        `json:"runtimeOwnsTarget"`
	ActiveGeneration      string                      `json:"activeGeneration,omitempty"`
}

type fai519Checkpoint struct {
	Scenario                   string                     `json:"scenario"`
	Classification             fai519Classification       `json:"classification"`
	InterruptedOperationID     string                     `json:"interruptedOperationId"`
	InterruptedOperationStatus transitionoperation.Status `json:"interruptedOperationStatus"`
	ResumedOperationID         string                     `json:"resumedOperationId"`
	CompletedOperationID       string                     `json:"completedOperationId"`
	CompletedOperationStatus   transitionoperation.Status `json:"completedOperationStatus"`
	CandidateArtifactDigest    string                     `json:"candidateArtifactDigest"`
	TargetIdentityDigest       string                     `json:"targetIdentityDigest"`
}

type fai519EffectState struct {
	MigrationCount  int    `json:"migrationCount"`
	StageCount      int    `json:"stageCount"`
	ActivationCount int    `json:"activationCount"`
	RestartCount    int    `json:"restartCount"`
	ValidateCount   int    `json:"validateCount"`
	MigrationDone   bool   `json:"migrationDone"`
	Generation      string `json:"generation,omitempty"`
	Activated       bool   `json:"activated"`
	Restarted       bool   `json:"restarted"`
	Validated       bool   `json:"validated"`
}

type fai519RuntimeIdentity struct {
	PID                  int    `json:"pid"`
	Image                string `json:"image"`
	TargetIdentityDigest string `json:"targetIdentityDigest"`
}

type fai519ScenarioReport struct {
	SchemaVersion          int                           `json:"schemaVersion"`
	Kind                   string                        `json:"kind"`
	Scenario               string                        `json:"scenario"`
	Classification         fai519Classification          `json:"classification"`
	Failpoint              string                        `json:"failpoint,omitempty"`
	InterruptedOperationID string                        `json:"interruptedOperationId,omitempty"`
	ResumedOperationID     string                        `json:"resumedOperationId,omitempty"`
	PreResumeState         fai519PreResumeState          `json:"preResumeState"`
	DurablePhase           transitionoperation.Phase     `json:"durablePhase,omitempty"`
	DurablePhaseRecorded   bool                          `json:"durablePhaseRecorded"`
	DurableMutationSkipped bool                          `json:"durablePhaseMutationSkippedAfterRestart"`
	InterruptedOperation   transitionoperation.Operation `json:"interruptedOperation,omitempty"`
	Operation              transitionoperation.Operation `json:"operation"`
	Occurrence             recovery.Occurrence           `json:"occurrence"`
	Attempts               []recovery.Attempt            `json:"attempts"`
	EvidenceAttempts       []recovery.EvidenceAttempt    `json:"evidenceAttempts"`
	Effects                fai519EffectState             `json:"effects"`
	Runtime                fai519RuntimeIdentity         `json:"runtime"`
	StaleTransitionFence   bool                          `json:"staleTransitionFenceRejected"`
	StaleLedgerFence       bool                          `json:"staleLedgerFenceRejected"`
	CleanupVerified        bool                          `json:"cleanupVerified"`
	RecordedAt             time.Time                     `json:"recordedAt"`
}

type fai519MatrixReport struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Kind          string                 `json:"kind"`
	Status        string                 `json:"status"`
	Scenarios     []fai519ScenarioReport `json:"scenarios"`
	PublishedAt   time.Time              `json:"publishedAt"`
}

// TestFAI519TransitionAndStoppedTargetInterruptionQualification kills real
// runner processes at each forward effect boundary and then resumes them from
// PostgreSQL. The transition operation remains the effect authority while the
// FAI-1001 ledger exclusively owns qualification attempts and evidence.
func TestFAI519TransitionAndStoppedTargetInterruptionQualification(t *testing.T) {
	pool := productionPreflightDB(t)
	_, resolver, preflight := forwardQualificationAuthorities(t, pool)
	resolved, err := resolver.ResolveAndEvaluate(t.Context(), preflight)
	if err != nil {
		t.Fatal(err)
	}
	targetDigest := resolved.Evidence.TargetIdentityDigest
	candidateDigest, err := resolved.Evidence.Candidate.Digest()
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := representativeStateDigest(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	evidenceRoot := fai519EvidenceRoot(t)
	runRoot := filepath.Join(evidenceRoot, "runs")
	if err := os.MkdirAll(runRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runRoot, "operator-data"), 0o700); err != nil {
		t.Fatal(err)
	}
	ledger := refreshpostgres.NewRecoveryLedger(pool)

	interruptions := []struct {
		name               string
		failpoint          string
		durablePhase       transitionoperation.Phase
		durablePhaseExists bool
		classification     fai519Classification
	}{
		{"before-migrations", "before-migrations", "", false, fai519PriorValid},
		{"after-migrations-effect", "after-effect-migrations", transitionoperation.PhaseMigrations, false, fai519PriorValid},
		{"after-candidate-staged-effect", "after-effect-candidate-staged", transitionoperation.PhaseCandidateStaged, false, fai519PriorValid},
		{"after-candidate-activated-effect", "after-effect-candidate-activated", transitionoperation.PhaseCandidateActivated, false, fai519RecoverablePending},
		{"after-candidate-restarted-effect", "after-effect-candidate-restarted", transitionoperation.PhaseCandidateRestarted, false, fai519CandidateValid},
		{"after-post-validated-effect", "after-effect-post-validated", transitionoperation.PhasePostValidated, false, fai519CandidateValid},
		{"after-migrations", "after-record-migrations", transitionoperation.PhaseMigrations, true, fai519PriorValid},
		{"after-candidate-staged", "after-record-candidate-staged", transitionoperation.PhaseCandidateStaged, true, fai519PriorValid},
		{"after-candidate-activated", "after-record-candidate-activated", transitionoperation.PhaseCandidateActivated, true, fai519RecoverablePending},
		{"after-candidate-restarted", "after-record-candidate-restarted", transitionoperation.PhaseCandidateRestarted, true, fai519CandidateValid},
		{"after-post-validated", "after-record-post-validated", transitionoperation.PhasePostValidated, true, fai519CandidateValid},
	}
	reports := make([]fai519ScenarioReport, 0, len(interruptions)+3)
	for index, scenario := range interruptions {
		t.Run(scenario.name, func(t *testing.T) {
			root := filepath.Join(evidenceRoot, scenario.name)
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			predecessor := startFAI519Runtime(t, root, productionAdmission("predecessor", 'a', '1').Release.Image, targetDigest)
			defer stopFAI519Runtime(predecessor)
			operationID := fmt.Sprintf("0198f2c0-7c7a-7f00-8a11-%012d", 300+index)
			config := fai519Config{DSN: pool.Config().ConnString(), Root: root, OperationID: operationID, OwnerID: "fai519-crashed-owner", IdempotencyKey: "fai519-" + scenario.name, Preflight: preflight, Failpoint: scenario.failpoint, BaselineState: baseline, CandidateImage: resolved.Evidence.Candidate.Release.Image, TargetDigest: targetDigest}
			firstClaim := claimFAI519Occurrence(t, ledger, scenario.name, targetDigest, time.Now().UTC(), index)
			if err := ledger.Start(t.Context(), firstClaim.ID, firstClaim.Fence, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			abandonedDir := filepath.Join(runRoot, fmt.Sprintf("%s-generation-%d", firstClaim.ID, firstClaim.Fence.Generation))
			if err := os.Mkdir(abandonedDir, 0o700); err != nil {
				t.Fatal(err)
			}
			runFAI519Child(t, config, true)

			transitions := releasepostgres.NewTransitionRepository(pool)
			interrupted, err := transitions.Get(t.Context(), operationID)
			if err != nil {
				t.Fatal(err)
			}
			if interrupted.Status.Terminal() {
				t.Fatalf("interrupted operation unexpectedly terminal: %s", interrupted.Status)
			}
			preResume := observeFAI519PreResumeState(t, pool, ledger, firstClaim.ID, transitions, operationID, root, targetDigest, resolved.Evidence.Candidate.Release.Image)
			if preResume.Classification != scenario.classification {
				t.Fatalf("derived classification = %q, want %q", preResume.Classification, scenario.classification)
			}
			durablePhaseRecorded := fai519PhaseSucceeded(interrupted, scenario.durablePhase)
			if durablePhaseRecorded != scenario.durablePhaseExists {
				t.Fatalf("durable phase %q recorded = %v, want %v; phases=%v", scenario.durablePhase, durablePhaseRecorded, scenario.durablePhaseExists, preResume.DurablePhases)
			}
			beforeResumeMutationCount := fai519PhaseMutationCount(preResume.Effects, scenario.durablePhase)
			interruptedSnapshot := interrupted
			oldTransitionFence := interrupted.Fence
			if _, err := pool.Exec(t.Context(), `UPDATE release.release_transition_fence SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE operation_id = $1::uuid`, operationID); err != nil {
				t.Fatal(err)
			}
			resumeNow := firstClaim.LeaseExpiresAt.Add(time.Second)
			secondClaim, ok, err := ledger.ClaimNext(t.Context(), recovery.ClaimInput{WorkerID: "fai519-resumer", Actor: "qualification", Now: resumeNow, Lease: 2 * time.Minute})
			if err != nil || !ok || secondClaim.ID != firstClaim.ID {
				t.Fatalf("reclaim interrupted occurrence: ok=%v occurrence=%s err=%v", ok, secondClaim.ID, err)
			}
			if err := ledger.Start(t.Context(), secondClaim.ID, secondClaim.Fence, resumeNow.Add(time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			staleLedgerRejected := errors.Is(ledger.RecordPhase(t.Context(), firstClaim.ID, firstClaim.Fence, "readiness", "started", resumeNow.Add(2*time.Millisecond)), recovery.ErrFenced)
			if !staleLedgerRejected {
				t.Fatal("stale recovery-ledger fence advanced state")
			}
			config.OperationID = fmt.Sprintf("0198f2c0-7c7a-7f00-8a11-%012d", 500+index)
			config.IdempotencyKey += "-resume"
			resumedOperationID := config.OperationID
			if resumedOperationID == operationID {
				t.Fatal("interrupted and resumed operation identities must be distinct")
			}
			config.OwnerID = "fai519-resumed-owner"
			config.Failpoint = ""
			runFAI519Child(t, config, false)
			interrupted, err = transitions.Get(t.Context(), operationID)
			if err != nil {
				t.Fatal(err)
			}
			if interrupted.Status != transitionoperation.StatusIndeterminate {
				t.Fatalf("interrupted transition status = %q, want indeterminate", interrupted.Status)
			}
			completed, err := transitions.Get(t.Context(), config.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if completed.Status != transitionoperation.StatusCompleted {
				t.Fatalf("resumed transition status = %q", completed.Status)
			}
			if completed.OperationID != resumedOperationID {
				t.Fatalf("completed operation = %q, want resumed operation %q", completed.OperationID, resumedOperationID)
			}
			assertForwardPhaseResults(t, completed)
			_, staleTransitionErr := transitions.RecordPhase(t.Context(), transitionrunner.PhaseRecordInput{OperationID: operationID, OwnerID: oldTransitionFence.OwnerID, Fence: oldTransitionFence, Phase: transitionrunner.PhaseSuccess, Status: transitionoperation.PhaseResultSucceeded, Result: []byte(`{"stale":true}`)})
			staleTransitionRejected := errors.Is(staleTransitionErr, transitionoperation.ErrStaleFence)
			if !staleTransitionRejected {
				t.Fatal("stale transition fence advanced authoritative state")
			}
			state := readFAI519EffectState(t, root)
			assertFAI519EffectsExactlyOnce(t, state)
			durablePhaseSkipped := scenario.durablePhaseExists && fai519PhaseMutationCount(state, scenario.durablePhase) == beforeResumeMutationCount
			if scenario.durablePhaseExists && !durablePhaseSkipped {
				t.Fatalf("durably recorded phase %q duplicated its external mutation: before=%d after=%d", scenario.durablePhase, beforeResumeMutationCount, fai519PhaseMutationCount(state, scenario.durablePhase))
			}
			runtime := readFAI519Runtime(t, root)
			if runtime.Image != resolved.Evidence.Candidate.Release.Image || runtime.TargetIdentityDigest != targetDigest || !processAlive(runtime.PID) {
				t.Fatalf("candidate runtime identity = %#v", runtime)
			}
			if err := ledger.RecordPhase(t.Context(), secondClaim.ID, secondClaim.Fence, "readiness", "started", resumeNow.Add(3*time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if err := ledger.RecordPhase(t.Context(), secondClaim.ID, secondClaim.Fence, "readiness", "completed", resumeNow.Add(4*time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			checkpoint := fai519Checkpoint{Scenario: scenario.name, Classification: preResume.Classification, InterruptedOperationID: operationID, InterruptedOperationStatus: interruptedSnapshot.Status, ResumedOperationID: resumedOperationID, CompletedOperationID: completed.OperationID, CompletedOperationStatus: completed.Status, CandidateArtifactDigest: candidateDigest, TargetIdentityDigest: targetDigest}
			if checkpoint.CompletedOperationID != checkpoint.ResumedOperationID || checkpoint.CompletedOperationID == checkpoint.InterruptedOperationID {
				t.Fatalf("completion evidence mixed operation identities: %#v", checkpoint)
			}
			checkpointPath := filepath.Join(root, "checkpoint.json")
			writeFAI519JSON(t, checkpointPath, checkpoint)
			ref := fai519EvidenceReference(t, checkpointPath)
			if err := ledger.Complete(t.Context(), secondClaim.ID, secondClaim.Fence, resumeNow.Add(5*time.Millisecond), recovery.Result{RecoveryPointAt: firstClaim.PlannedAt, Evidence: []recovery.EvidenceReference{ref}}); err != nil {
				t.Fatal(err)
			}
			// Simulate a publisher process disappearing after it claims the evidence.
			publication, ok, err := ledger.ClaimEvidence(t.Context(), "fai519-publisher-crashed", resumeNow.Add(6*time.Millisecond), time.Second)
			if err != nil || !ok || publication.ID != secondClaim.ID {
				t.Fatalf("claim evidence: ok=%v occurrence=%s err=%v", ok, publication.ID, err)
			}
			publication2, ok, err := ledger.ClaimEvidence(t.Context(), "fai519-publisher-resumed", resumeNow.Add(2*time.Second), time.Second)
			if err != nil || !ok || publication2.ID != secondClaim.ID {
				t.Fatalf("reclaim evidence: ok=%v occurrence=%s err=%v", ok, publication2.ID, err)
			}
			if err := ledger.PublishEvidence(t.Context(), publication2.ID, publication2.EvidenceFence, resumeNow.Add(2*time.Second+time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(ledger.PublishEvidence(t.Context(), publication.ID, publication.EvidenceFence, resumeNow.Add(2*time.Second+time.Millisecond)), recovery.ErrFenced) {
				t.Fatal("stale evidence publisher was not fenced")
			}
			occurrence, err := ledger.Occurrence(t.Context(), secondClaim.ID)
			if err != nil {
				t.Fatal(err)
			}
			attempts, err := ledger.Attempts(t.Context(), secondClaim.ID)
			if err != nil {
				t.Fatal(err)
			}
			evidenceAttempts, err := ledger.EvidenceAttempts(t.Context(), secondClaim.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 2 || attempts[0].Status != "abandoned" || attempts[1].Status != recovery.StatusSucceeded || len(evidenceAttempts) != 2 || evidenceAttempts[0].Status != "abandoned" || evidenceAttempts[1].Status != "published" {
				t.Fatalf("durable attempts = %#v, evidence attempts = %#v", attempts, evidenceAttempts)
			}
			allOccurrences, err := ledger.Occurrences(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := recovery.ReclaimQualificationRunDirectories(runRoot, allOccurrences, resumeNow.Add(3*time.Second)); err != nil {
				t.Fatal(err)
			}
			_, abandonedErr := os.Stat(abandonedDir)
			cleanupVerified := os.IsNotExist(abandonedErr)
			if !cleanupVerified {
				t.Fatalf("abandoned run directory was not reclaimed: %v", abandonedErr)
			}
			if _, err := os.Stat(filepath.Join(runRoot, "operator-data")); err != nil {
				t.Fatalf("cleanup removed unrelated data: %v", err)
			}
			report := fai519ScenarioReport{SchemaVersion: 1, Kind: "leapview/fai519-interruption-scenario", Scenario: scenario.name, Classification: preResume.Classification, Failpoint: scenario.failpoint, InterruptedOperationID: operationID, ResumedOperationID: resumedOperationID, PreResumeState: preResume, DurablePhase: scenario.durablePhase, DurablePhaseRecorded: durablePhaseRecorded, DurableMutationSkipped: durablePhaseSkipped, InterruptedOperation: interruptedSnapshot, Operation: completed, Occurrence: occurrence, Attempts: attempts, EvidenceAttempts: evidenceAttempts, Effects: state, Runtime: runtime, StaleTransitionFence: staleTransitionRejected, StaleLedgerFence: staleLedgerRejected, CleanupVerified: cleanupVerified, RecordedAt: time.Now().UTC()}
			writeFAI519JSON(t, filepath.Join(root, "scenario-report.json"), report)
			reports = append(reports, report)
			stopFAI519RuntimePID(runtime.PID)
		})
	}

	t.Run("terminal-preflight-failure", func(t *testing.T) {
		reports = append(reports, runFAI519TerminalScenario(t, pool, ledger, preflight, resolved, baseline, evidenceRoot, "terminal-preflight-failure", "preflight", transitionoperation.StatusFailed, 800))
	})
	t.Run("indeterminate-effect", func(t *testing.T) {
		reports = append(reports, runFAI519TerminalScenario(t, pool, ledger, preflight, resolved, baseline, evidenceRoot, "indeterminate-effect", "stage", transitionoperation.StatusIndeterminate, 801))
	})
	t.Run("running-target-rejected", func(t *testing.T) {
		report := runFAI519TerminalScenario(t, pool, ledger, preflight, resolved, baseline, evidenceRoot, "running-target-rejected", "running-target", transitionoperation.StatusIndeterminate, 802)
		if report.Effects != (fai519EffectState{}) {
			t.Fatalf("running-target rejection executed a mutating effect: %#v", report.Effects)
		}
		reports = append(reports, report)
	})

	matrix := fai519MatrixReport{SchemaVersion: 1, Kind: "leapview/fai519-interruption-matrix", Status: "success", Scenarios: reports, PublishedAt: time.Now().UTC()}
	if len(matrix.Scenarios) != len(interruptions)+3 {
		t.Fatalf("scenario count = %d, want %d", len(matrix.Scenarios), len(interruptions)+3)
	}
	writeFAI519JSON(t, filepath.Join(evidenceRoot, "scenario-matrix.json"), matrix)
	occurrences, err := ledger.Occurrences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	writeFAI519JSON(t, filepath.Join(evidenceRoot, "ledger-export.json"), occurrences)
	assertFAI519EvidenceContainsNoCredentials(t, evidenceRoot, pool.Config().ConnString(), pool.Config().ConnConfig.Password)
}

// TestFAI519TransitionChild is a disposable runner process. A failpoint uses
// SIGKILL, ensuring deferred releases and in-memory state cannot assist resume.
func TestFAI519TransitionChild(t *testing.T) {
	if os.Getenv(fai519ChildEnv) != "1" {
		return
	}
	config := readFAI519Config(t)
	pool, err := pgxpool.New(t.Context(), config.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	resolver, err := existingFAI519Resolver(pool)
	if err != nil {
		t.Fatal(err)
	}
	preflight := transitionrunner.PreflightAuthority(resolver)
	if config.FailureMode == "preflight" {
		preflight = &fai519SecondPreflightFailure{delegate: resolver}
	}
	effects := &fai519Effects{pool: pool, config: config, resolver: resolver}
	transitions := releasepostgres.NewTransitionRepository(pool)
	operations := transitionrunner.OperationStore(transitions)
	if strings.HasPrefix(config.Failpoint, "after-record-") {
		operations = &fai519PostRecordFailpointStore{delegate: transitions, failpoint: config.Failpoint}
	}
	runner, err := transitionrunner.New(transitionrunner.Options{Operations: operations, Preflight: preflight, Fences: transitions, Effects: effects, LeaseTTL: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := runner.Run(t.Context(), transitionrunner.Request{OperationID: config.OperationID, OwnerID: config.OwnerID, IdempotencyKey: config.IdempotencyKey, LeaseTTL: 30 * time.Second, Preflight: config.Preflight})
	if config.ExpectedStatus != "" {
		if runErr == nil || result.Operation.Status != config.ExpectedStatus {
			t.Fatalf("terminal result status=%s error=%v, want %s", result.Operation.Status, runErr, config.ExpectedStatus)
		}
		if config.ExpectedError != "" && !strings.Contains(runErr.Error(), config.ExpectedError) {
			t.Fatalf("terminal error = %q, want substring %q", runErr, config.ExpectedError)
		}
		return
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
}

func TestFAI519TargetRuntimeHelper(t *testing.T) {
	if os.Getenv(fai519RuntimeEnv) != "1" {
		return
	}
	config := readFAI519Config(t)
	lock, err := instancelock.AcquireNamed(config.Root, fai519RuntimeLock)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	identity := fai519RuntimeIdentity{PID: os.Getpid(), Image: config.CandidateImage, TargetIdentityDigest: config.TargetDigest}
	if err := writeFAI519JSONFile(filepath.Join(config.Root, "runtime.json"), identity); err != nil {
		t.Fatal(err)
	}
	select {}
}

type fai519SecondPreflightFailure struct {
	delegate transitionrunner.PreflightAuthority
	calls    int
}

// fai519PostRecordFailpointStore terminates the disposable child only after
// the authoritative PostgreSQL RecordPhase transaction has committed.
type fai519PostRecordFailpointStore struct {
	delegate  transitionrunner.OperationStore
	failpoint string
}

func (s *fai519PostRecordFailpointStore) Create(ctx context.Context, input transitionoperation.CreateInput) (transitionoperation.Operation, error) {
	return s.delegate.Create(ctx, input)
}

func (s *fai519PostRecordFailpointStore) Get(ctx context.Context, operationID string) (transitionoperation.Operation, error) {
	return s.delegate.Get(ctx, operationID)
}

func (s *fai519PostRecordFailpointStore) PhaseCompleted(ctx context.Context, operationID string, phase transitionrunner.Phase) (bool, error) {
	return s.delegate.PhaseCompleted(ctx, operationID, phase)
}

func (s *fai519PostRecordFailpointStore) RecordPhase(ctx context.Context, input transitionrunner.PhaseRecordInput) (transitionoperation.Operation, error) {
	operation, err := s.delegate.RecordPhase(ctx, input)
	if err == nil && s.failpoint == fai519PostRecordFailpoint(input.Phase) {
		killFAI519Process()
	}
	return operation, err
}

func (s *fai519PostRecordFailpointStore) Complete(ctx context.Context, input transitionrunner.CompleteInput) (transitionoperation.Operation, error) {
	return s.delegate.Complete(ctx, input)
}

func (s *fai519PostRecordFailpointStore) Fail(ctx context.Context, input transitionrunner.FailureInput) (transitionoperation.Operation, error) {
	return s.delegate.Fail(ctx, input)
}

func fai519PostRecordFailpoint(phase transitionrunner.Phase) string {
	return "after-record-" + string(phase)
}

func (f *fai519SecondPreflightFailure) ResolveAndEvaluate(ctx context.Context, request transitionpreflight.ResolutionRequest) (transitionpreflight.ResolutionResult, error) {
	f.calls++
	if f.calls == 2 {
		return transitionpreflight.ResolutionResult{}, errors.New("qualification interrupted authoritative preflight")
	}
	return f.delegate.ResolveAndEvaluate(ctx, request)
}

type fai519Effects struct {
	pool     *pgxpool.Pool
	config   fai519Config
	resolver transitionrunner.PreflightAuthority
}

func (e *fai519Effects) Migrations(ctx context.Context, _ transitionrunner.EffectInput) (transitionrunner.EffectResult, error) {
	e.kill("before-migrations")
	if e.config.FailureMode == "running-target" {
		if err := rejectFAI519RunningTarget(e.config.Root); err != nil {
			return transitionrunner.EffectResult{}, err
		}
		return transitionrunner.EffectResult{}, errors.New("stopped-target operation unexpectedly accepted a running target")
	}
	control, err := sql.Open("pgx", e.config.DSN)
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	defer control.Close()
	if err := postgresmigrations.ApplyRiverAndGoose(ctx, e.pool, control, nil); err != nil {
		return transitionrunner.EffectResult{}, err
	}
	err = mutateFAI519State(e.config.Root, func(state *fai519EffectState) error {
		if !state.MigrationDone {
			state.MigrationDone = true
			state.MigrationCount++
		}
		return nil
	})
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	e.kill("after-effect-migrations")
	return transitionrunner.EffectResult{}, nil
}

func (e *fai519Effects) Stage(_ context.Context, in transitionrunner.EffectInput) (transitionrunner.EffectResult, error) {
	generation, err := hostinstall.QualificationStage(fai519Paths(e.config.Root), in.Evidence.Candidate.Release.Image, qualificationPayload())
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	err = mutateFAI519State(e.config.Root, func(state *fai519EffectState) error {
		if state.Generation == "" {
			state.Generation = generation
			state.StageCount++
		} else if state.Generation != generation {
			return errors.New("immutable staging generation changed during resume")
		}
		return nil
	})
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	e.kill("after-effect-candidate-staged")
	if e.config.FailureMode == "stage" {
		return transitionrunner.EffectResult{}, errors.New("qualification effect failed after durable staging")
	}
	return transitionrunner.EffectResult{}, nil
}

func (e *fai519Effects) Activate(_ context.Context, _ transitionrunner.EffectInput) (transitionrunner.EffectResult, error) {
	state, err := readFAI519EffectStateFile(e.config.Root)
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	active, activeErr := hostinstall.QualificationActiveGeneration(fai519Paths(e.config.Root))
	if activeErr != nil || active != state.Generation {
		identity, err := readFAI519RuntimeFile(e.config.Root)
		if err != nil {
			return transitionrunner.EffectResult{}, err
		}
		if identity.TargetIdentityDigest != e.config.TargetDigest {
			return transitionrunner.EffectResult{}, errors.New("running target identity changed")
		}
		if err := stopFAI519RuntimePID(identity.PID); err != nil {
			return transitionrunner.EffectResult{}, err
		}
		if err := awaitFAI519StoppedTarget(e.config.Root); err != nil {
			return transitionrunner.EffectResult{}, err
		}
		if err := hostinstall.QualificationActivate(fai519Paths(e.config.Root), state.Generation); err != nil {
			return transitionrunner.EffectResult{}, err
		}
	}
	err = mutateFAI519State(e.config.Root, func(state *fai519EffectState) error {
		if !state.Activated {
			state.Activated = true
			state.ActivationCount++
		}
		return nil
	})
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	e.kill("after-effect-candidate-activated")
	return transitionrunner.EffectResult{}, nil
}

func (e *fai519Effects) Restart(_ context.Context, in transitionrunner.EffectInput) (transitionrunner.EffectResult, error) {
	candidateDigest, err := in.Evidence.Candidate.Digest()
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	err = mutateFAI519State(e.config.Root, func(state *fai519EffectState) error {
		identity, identityErr := readFAI519RuntimeFile(e.config.Root)
		candidateRunning := identityErr == nil && processAlive(identity.PID) && identity.Image == e.config.CandidateImage && identity.TargetIdentityDigest == e.config.TargetDigest
		if !candidateRunning {
			if err := os.Remove(filepath.Join(e.config.Root, "runtime.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			command, err := startFAI519RuntimeProcess(e.config.Root, e.config.CandidateImage, e.config.TargetDigest)
			if err != nil {
				return err
			}
			_ = command
		}
		if !state.Restarted {
			state.Restarted = true
			state.RestartCount++
		}
		return nil
	})
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	e.kill("after-effect-candidate-restarted")
	return transitionrunner.EffectResult{TargetIdentityDigest: e.config.TargetDigest, CandidateArtifactDigest: candidateDigest}, nil
}

func (e *fai519Effects) PostValidate(ctx context.Context, in transitionrunner.EffectInput) (transitionrunner.EffectResult, error) {
	candidateDigest, err := in.Evidence.Candidate.Digest()
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	err = mutateFAI519State(e.config.Root, func(state *fai519EffectState) error {
		active, err := hostinstall.QualificationActiveGeneration(fai519Paths(e.config.Root))
		if err != nil || active != state.Generation {
			return errors.New("candidate generation is not active")
		}
		identity, err := readFAI519RuntimeFile(e.config.Root)
		if err != nil || !processAlive(identity.PID) || identity.Image != e.config.CandidateImage || identity.TargetIdentityDigest != e.config.TargetDigest {
			return errors.New("candidate runtime identity is invalid")
		}
		resolved, err := e.resolver.ResolveAndEvaluate(ctx, e.config.Preflight)
		if err != nil || resolved.EvidenceDigest != in.Operation.PreflightEvidenceDigest {
			return errors.New("authoritative preflight changed")
		}
		actual, err := representativeStateDigest(ctx, e.pool)
		if err != nil || actual != e.config.BaselineState {
			return errors.New("representative state changed")
		}
		if !state.Validated {
			state.Validated = true
			state.ValidateCount++
		}
		return nil
	})
	if err != nil {
		return transitionrunner.EffectResult{}, err
	}
	e.kill("after-effect-post-validated")
	return transitionrunner.EffectResult{TargetIdentityDigest: e.config.TargetDigest, CandidateArtifactDigest: candidateDigest, StateIdentityDigest: e.config.BaselineState}, nil
}

func (e *fai519Effects) kill(name string) {
	if e.config.Failpoint == name {
		killFAI519Process()
	}
}

func killFAI519Process() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	select {}
}

func rejectFAI519RunningTarget(root string) error {
	lock, err := instancelock.AcquireNamed(root, fai519RuntimeLock)
	if err == nil {
		_ = lock.Release()
		return nil
	}
	if !strings.Contains(err.Error(), "another process") {
		return fmt.Errorf("inspect stopped-target ownership: %w", err)
	}
	return errors.New("target is still running; stopped-target operation rejected before its first effect")
}

func existingFAI519Resolver(pool *pgxpool.Pool) (transitionrunner.PreflightAuthority, error) {
	releases := releasepostgres.New(pool)
	capabilities, err := releasepostgres.NewMigrationCapabilityAuthority(releases, productionOwnerRegistry())
	if err != nil {
		return nil, err
	}
	return releasetransitionapp.NewProductionResolver(releasetransitionapp.ProductionDependencies{Releases: releases, Targets: deploymentpostgres.New(pool), MigrationCapabilities: capabilities, RecoverySets: recoverysetpostgres.New(pool)})
}

func runFAI519TerminalScenario(t *testing.T, pool *pgxpool.Pool, ledger *refreshpostgres.RecoveryLedger, preflight transitionpreflight.ResolutionRequest, resolved transitionpreflight.ResolutionResult, baseline, evidenceRoot, name, failureMode string, status transitionoperation.Status, sequence int) fai519ScenarioReport {
	t.Helper()
	root := filepath.Join(evidenceRoot, name)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	process := startFAI519Runtime(t, root, productionAdmission("predecessor", 'a', '1').Release.Image, resolved.Evidence.TargetIdentityDigest)
	defer stopFAI519Runtime(process)
	claim := claimFAI519Occurrence(t, ledger, name, resolved.Evidence.TargetIdentityDigest, time.Now().UTC(), sequence)
	if err := ledger.Start(t.Context(), claim.ID, claim.Fence, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	operationID := fmt.Sprintf("0198f2c0-7c7a-7f00-8a11-%012d", sequence)
	config := fai519Config{DSN: pool.Config().ConnString(), Root: root, OperationID: operationID, OwnerID: "fai519-terminal-owner", IdempotencyKey: "fai519-" + name, Preflight: preflight, FailureMode: failureMode, ExpectedStatus: status, BaselineState: baseline, CandidateImage: resolved.Evidence.Candidate.Release.Image, TargetDigest: resolved.Evidence.TargetIdentityDigest}
	if failureMode == "running-target" {
		config.ExpectedError = "target is still running"
	}
	runFAI519Child(t, config, false)
	transitions := releasepostgres.NewTransitionRepository(pool)
	operation, err := transitions.Get(t.Context(), operationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status != status {
		t.Fatalf("terminal status = %s, want %s", operation.Status, status)
	}
	preResume := observeFAI519PreResumeState(t, pool, ledger, claim.ID, transitions, operationID, root, resolved.Evidence.TargetIdentityDigest, resolved.Evidence.Candidate.Release.Image)
	failure := recovery.NewFailure("transition_"+string(status), "transition reached "+string(status))
	checkpointPath := filepath.Join(root, "checkpoint.json")
	writeFAI519JSON(t, checkpointPath, map[string]any{"scenario": name, "classification": preResume.Classification, "operationId": operationID, "status": operation.Status, "candidateArtifactDigest": operation.CandidateArtifactDigest, "targetIdentityDigest": operation.TargetIdentityDigest})
	if err := ledger.Fail(t.Context(), claim.ID, claim.Fence, time.Now().UTC(), recovery.Result{Evidence: []recovery.EvidenceReference{fai519EvidenceReference(t, checkpointPath)}}, failure); err != nil {
		t.Fatal(err)
	}
	publishFAI519Evidence(t, ledger, claim.ID)
	occurrence, err := ledger.Occurrence(t.Context(), claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := ledger.Attempts(t.Context(), claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	evidenceAttempts, err := ledger.EvidenceAttempts(t.Context(), claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	report := fai519ScenarioReport{SchemaVersion: 1, Kind: "leapview/fai519-interruption-scenario", Scenario: name, Classification: preResume.Classification, InterruptedOperationID: operationID, PreResumeState: preResume, Operation: operation, Occurrence: occurrence, Attempts: attempts, EvidenceAttempts: evidenceAttempts, Effects: readFAI519EffectState(t, root), Runtime: readFAI519Runtime(t, root), CleanupVerified: true, RecordedAt: time.Now().UTC()}
	writeFAI519JSON(t, filepath.Join(root, "scenario-report.json"), report)
	return report
}

func observeFAI519PreResumeState(t *testing.T, pool *pgxpool.Pool, ledger *refreshpostgres.RecoveryLedger, occurrenceID string, transitions *releasepostgres.TransitionRepository, operationID, root, targetDigest, candidateImage string) fai519PreResumeState {
	t.Helper()
	var revision int64
	if err := pool.QueryRow(t.Context(), `SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	occurrence, err := ledger.Occurrence(t.Context(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := transitions.Get(t.Context(), operationID)
	if err != nil {
		t.Fatal(err)
	}
	state := readFAI519EffectState(t, root)
	runtime := readFAI519Runtime(t, root)
	runtimeOwnsTarget, err := fai519RuntimeOwnsTarget(root)
	if err != nil {
		t.Fatal(err)
	}
	activeGeneration := ""
	if _, err := os.Lstat(filepath.Join(fai519Paths(root).Root, "current")); err == nil {
		activeGeneration, err = hostinstall.QualificationActiveGeneration(fai519Paths(root))
		if err != nil {
			t.Fatal(err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	durablePhases := make([]transitionoperation.Phase, 0, len(operation.PhaseResults))
	for _, result := range operation.PhaseResults {
		if result.Status == transitionoperation.PhaseResultSucceeded {
			durablePhases = append(durablePhases, result.Phase)
		}
	}
	predecessorImage := productionAdmission("predecessor", 'a', '1').Release.Image
	priorValid := runtimeOwnsTarget && runtime.Image == predecessorImage && runtime.TargetIdentityDigest == targetDigest && !state.Activated
	candidateValid := runtimeOwnsTarget && runtime.Image == candidateImage && runtime.TargetIdentityDigest == targetDigest && state.Restarted && activeGeneration == state.Generation
	recoverablePending := !runtimeOwnsTarget && runtime.Image == predecessorImage && runtime.TargetIdentityDigest == targetDigest && state.Activated && !state.Restarted && activeGeneration == state.Generation
	classifications := make([]fai519Classification, 0, 1)
	if priorValid {
		classifications = append(classifications, fai519PriorValid)
	}
	if candidateValid {
		classifications = append(classifications, fai519CandidateValid)
	}
	if recoverablePending {
		classifications = append(classifications, fai519RecoverablePending)
	}
	if len(classifications) != 1 {
		t.Fatalf("pre-resume state has %d classifications, want exactly one: prior=%v candidate=%v pending=%v state=%#v runtime=%#v active=%q", len(classifications), priorValid, candidateValid, recoverablePending, state, runtime, activeGeneration)
	}
	return fai519PreResumeState{Classification: classifications[0], GooseRevision: revision, LedgerStatus: occurrence.Status, LedgerFenceGeneration: occurrence.Fence.Generation, TransitionStatus: operation.Status, DurablePhases: durablePhases, Effects: state, Runtime: runtime, RuntimeOwnsTarget: runtimeOwnsTarget, ActiveGeneration: activeGeneration}
}

func fai519RuntimeOwnsTarget(root string) (bool, error) {
	lock, err := instancelock.AcquireNamed(root, fai519RuntimeLock)
	if err == nil {
		return false, lock.Release()
	}
	if strings.Contains(err.Error(), "another process") {
		return true, nil
	}
	return false, err
}

func fai519PhaseSucceeded(operation transitionoperation.Operation, phase transitionoperation.Phase) bool {
	if phase == "" {
		return false
	}
	for _, result := range operation.PhaseResults {
		if result.Phase == phase && result.Status == transitionoperation.PhaseResultSucceeded {
			return true
		}
	}
	return false
}

func fai519PhaseMutationCount(state fai519EffectState, phase transitionoperation.Phase) int {
	switch phase {
	case transitionoperation.PhaseMigrations:
		return state.MigrationCount
	case transitionoperation.PhaseCandidateStaged:
		return state.StageCount
	case transitionoperation.PhaseCandidateActivated:
		return state.ActivationCount
	case transitionoperation.PhaseCandidateRestarted:
		return state.RestartCount
	case transitionoperation.PhasePostValidated:
		return state.ValidateCount
	default:
		return 0
	}
}

func claimFAI519Occurrence(t *testing.T, ledger *refreshpostgres.RecoveryLedger, scenario, target string, now time.Time, sequence int) recovery.Occurrence {
	t.Helper()
	planned := now.Add(-time.Duration(sequence+1) * time.Minute)
	policySum := sha256.Sum256([]byte("fai519-interruption-policy-v1"))
	input := recovery.EnqueueInput{ScheduleID: "fai519-" + scenario, ScheduleRevision: "fai519-v1", Scenario: scenario, Operation: recovery.OperationUpgrade, PolicyVersion: "fai519/v1", PolicySHA256: hex.EncodeToString(policySum[:]), TargetScope: target, ArtifactIdentity: productionAdmission("candidate", 'b', '2').Release.Image, PlannedAt: planned, StaleAfter: 24 * time.Hour}
	occurrence, created, err := ledger.Enqueue(t.Context(), input, now)
	if err != nil || !created {
		t.Fatalf("enqueue scenario: created=%v err=%v", created, err)
	}
	claimed, ok, err := ledger.ClaimNext(t.Context(), recovery.ClaimInput{WorkerID: "fai519-crashed-worker", Actor: "qualification", Now: now, Lease: 30 * time.Second})
	if err != nil || !ok || claimed.ID != occurrence.ID {
		t.Fatalf("claim scenario: ok=%v occurrence=%s want=%s err=%v", ok, claimed.ID, occurrence.ID, err)
	}
	return claimed
}

func publishFAI519Evidence(t *testing.T, ledger *refreshpostgres.RecoveryLedger, occurrenceID string) {
	t.Helper()
	claimed, ok, err := ledger.ClaimEvidence(t.Context(), "fai519-terminal-publisher", time.Now().UTC(), time.Minute)
	if err != nil || !ok || claimed.ID != occurrenceID {
		t.Fatalf("claim terminal evidence: ok=%v occurrence=%s want=%s err=%v", ok, claimed.ID, occurrenceID, err)
	}
	if err := ledger.PublishEvidence(t.Context(), claimed.ID, claimed.EvidenceFence, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func runFAI519Child(t *testing.T, config fai519Config, expectKilled bool) {
	t.Helper()
	dsn := config.DSN
	if strings.TrimSpace(dsn) == "" {
		t.Fatal("FAI-519 child PostgreSQL DSN is empty")
	}
	config.DSN = ""
	path := filepath.Join(config.Root, "child-config.json")
	writeFAI519JSON(t, path, config)
	command := exec.Command(os.Args[0], "-test.run=^TestFAI519TransitionChild$", "-test.v")
	command.Env = append(os.Environ(), fai519ChildEnv+"=1", fai519ConfigEnv+"="+path, fai519DSNEnv+"="+dsn)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if expectKilled {
		if err == nil {
			t.Fatalf("interruption child was not killed: %s", output.String())
		}
		if exit, ok := err.(*exec.ExitError); !ok || exit.ProcessState.ExitCode() != -1 {
			t.Fatalf("interruption child exit = %v: %s", err, output.String())
		}
		return
	}
	if err != nil {
		t.Fatalf("transition child: %v: %s", err, output.String())
	}
}

func startFAI519Runtime(t *testing.T, root, image, target string) *exec.Cmd {
	t.Helper()
	command, err := startFAI519RuntimeProcess(root, image, target)
	if err != nil {
		t.Fatal(err)
	}
	return command
}

func startFAI519RuntimeProcess(root, image, target string) (*exec.Cmd, error) {
	config := fai519Config{Root: root, CandidateImage: image, TargetDigest: target}
	path := filepath.Join(root, fmt.Sprintf("runtime-config-%d.json", time.Now().UnixNano()))
	if err := writeFAI519JSONFile(path, config); err != nil {
		return nil, err
	}
	if err := os.Remove(filepath.Join(root, "runtime.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	command := exec.Command(os.Args[0], "-test.run=^TestFAI519TargetRuntimeHelper$", "-test.v")
	command.Env = append(os.Environ(), fai519RuntimeEnv+"=1", fai519ConfigEnv+"="+path)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		identity, err := readFAI519RuntimeFile(root)
		if err == nil && identity.PID == command.Process.Pid && identity.Image == image && identity.TargetIdentityDigest == target {
			return command, nil
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			return nil, fmt.Errorf("runtime exited: %s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = command.Process.Kill()
	_ = command.Wait()
	return nil, errors.New("runtime did not publish identity")
}

func stopFAI519Runtime(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Kill()
	_ = command.Wait()
}

func stopFAI519RuntimePID(pid int) error {
	if pid <= 0 || !processAlive(pid) {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func awaitFAI519StoppedTarget(root string) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lock, err := instancelock.AcquireNamed(root, fai519RuntimeLock)
		if err == nil {
			return lock.Release()
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("target did not release exclusive runtime ownership")
}

func processAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func fai519Paths(root string) hostinstall.Paths {
	return hostinstall.Paths{Root: filepath.Join(root, "install"), ConfigDir: filepath.Join(root, "etc"), SystemBin: filepath.Join(root, "bin"), Systemd: filepath.Join(root, "systemd"), Systemctl: "systemctl"}
}

func mutateFAI519State(root string, mutate func(*fai519EffectState) error) error {
	lock, err := instancelock.AcquireNamed(root, ".effects.lock")
	if err != nil {
		return err
	}
	defer lock.Release()
	state, err := readFAI519EffectStateFile(root)
	if err != nil {
		return err
	}
	if err := mutate(&state); err != nil {
		return err
	}
	return writeFAI519JSONFile(filepath.Join(root, "effects.json"), state)
}

func readFAI519EffectStateFile(root string) (fai519EffectState, error) {
	var state fai519EffectState
	contents, err := os.ReadFile(filepath.Join(root, "effects.json"))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(contents, &state)
	return state, err
}

func readFAI519EffectState(t *testing.T, root string) fai519EffectState {
	t.Helper()
	state, err := readFAI519EffectStateFile(root)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func assertFAI519EffectsExactlyOnce(t *testing.T, state fai519EffectState) {
	t.Helper()
	if state.MigrationCount != 1 || state.StageCount != 1 || state.ActivationCount != 1 || state.RestartCount != 1 || state.ValidateCount != 1 || !state.MigrationDone || state.Generation == "" || !state.Activated || !state.Restarted || !state.Validated {
		t.Fatalf("effects were not exactly once after resume: %#v", state)
	}
}

func readFAI519RuntimeFile(root string) (fai519RuntimeIdentity, error) {
	var identity fai519RuntimeIdentity
	contents, err := os.ReadFile(filepath.Join(root, "runtime.json"))
	if err != nil {
		return identity, err
	}
	err = json.Unmarshal(contents, &identity)
	return identity, err
}

func readFAI519Runtime(t *testing.T, root string) fai519RuntimeIdentity {
	t.Helper()
	identity, err := readFAI519RuntimeFile(root)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func readFAI519Config(t *testing.T) fai519Config {
	t.Helper()
	contents, err := os.ReadFile(os.Getenv(fai519ConfigEnv))
	if err != nil {
		t.Fatal(err)
	}
	var config fai519Config
	if err := json.Unmarshal(contents, &config); err != nil {
		t.Fatal(err)
	}
	config.DSN = strings.TrimSpace(os.Getenv(fai519DSNEnv))
	return config
}

func fai519EvidenceRoot(t *testing.T) string {
	t.Helper()
	root := strings.TrimSpace(os.Getenv(fai519EvidenceEnv))
	if root == "" {
		root = t.TempDir()
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func fai519EvidenceReference(t *testing.T, path string) recovery.EvidenceReference {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	return recovery.EvidenceReference{Kind: recovery.EvidenceTransitionQualification, URI: "file://" + filepath.ToSlash(path), SHA256: hex.EncodeToString(sum[:])}
}

func writeFAI519JSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := writeFAI519JSONFile(path, value); err != nil {
		t.Fatal(err)
	}
}

func writeFAI519JSONFile(path string, value any) error {
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if len(contents) > 2<<20 {
		return errors.New("FAI-519 evidence exceeds bounded size")
	}
	if err := securefs.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(path, contents)
}

func assertFAI519EvidenceContainsNoCredentials(t *testing.T, root string, secrets ...string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range secrets {
			secret = strings.TrimSpace(secret)
			if secret != "" && bytes.Contains(contents, []byte(secret)) {
				return fmt.Errorf("credential found in evidence file %s", path)
			}
		}
		if bytes.Contains(bytes.ToLower(contents), []byte(`"dsn"`)) || bytes.Contains(bytes.ToLower(contents), []byte(`"password"`)) {
			return fmt.Errorf("credential field found in evidence file %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
