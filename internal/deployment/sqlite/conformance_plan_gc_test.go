package sqlite

// This suite is intentionally composition-level evidence.  It drives the
// production plan/build lifecycle, sealed publication coordinator, SQLite
// root/lease fences, and physical-pool collector for each supported policy.
// The only test adapters below provide detached catalog bytes and an
// in-memory object store; lifecycle state remains the production repository.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogseal"
	"github.com/flidai/leapview/internal/deployment"
	deploymentgc "github.com/flidai/leapview/internal/deployment/gc"
	"github.com/flidai/leapview/internal/deployment/sealedcontrol"
	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

type conformancePolicy struct {
	name                  string
	environment           string
	targetID              string
	projectID             string
	inputMode             deployment.DeliveryDataInputMode
	observedInputsAllowed bool
	qualificationPolicy   string
	approvalRequired      bool
	poolID                string
}

var conformancePolicies = []conformancePolicy{
	{name: "local-evaluation", environment: "local-evaluation", targetID: "target-conformance-local", projectID: "project-conformance-local", inputMode: deployment.DeliveryDataObserved, observedInputsAllowed: true, qualificationPolicy: "local-evaluation", poolID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	{name: "private-pull-request", environment: "private-pull-request", targetID: "target-conformance-pr", projectID: "project-conformance-pr", inputMode: deployment.DeliveryDataBounded, observedInputsAllowed: true, qualificationPolicy: "private-pull-request", poolID: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	{name: "protected-production", environment: "protected-production", targetID: "target-conformance-prod", projectID: "project-conformance-prod", inputMode: deployment.DeliveryDataPinned, qualificationPolicy: "protected-production", approvalRequired: true, poolID: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
}

// TestPlanToGCLifecycleConformance exercises the same production lifecycle
// under local evaluation, private/PR, and protected-production policy.  Policy
// differences are plan evidence and approval configuration; no alternate
// lifecycle implementation is used.
//
// This is representative composition evidence: the maintained lifecycle,
// catalog-seal, publication-recovery, and fencing suites enumerate the
// individual crash boundaries and same-base concurrency races.  Keeping those
// focused suites separate makes this matrix assert that the boundaries still
// compose through one persisted plan-to-GC path.
func TestPlanToGCLifecycleConformance(t *testing.T) {
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	for _, policy := range conformancePolicies {
		policy := policy
		t.Run(policy.name, func(t *testing.T) {
			ctx := t.Context()
			store, repo := openConformanceRepository(t, policy, now)
			pool := policy.poolID
			insertDeliveryPool(t, store, pool)

			clock := now
			repo.WithDeliveryClock(func() time.Time { return clock }).WithCatalogSealClock(func() time.Time { return clock })
			lifecycle := &deployment.DeliveryLifecycle{Targets: repo, Store: repo, Now: func() time.Time { return clock }}
			objects := &conformanceObjects{objects: map[string]conformanceObject{}}

			plan1 := conformancePlan(t, policy, "plan-conformance-1", now, 'a')
			planned1, err := lifecycle.Plan(ctx, deployment.DeliveryPlanRequest{
				ID: plan1.ID, ActorID: "actor-conformance", TargetID: policy.targetID, ProjectID: policy.projectID, Environment: policy.environment,
				Operation: plan1.Operation, SourceDigest: plan1.SourceDigest, Execution: plan1.Execution, Provenance: plan1.Provenance,
				Governance: plan1.Governance, Evidence: plan1.Evidence, CreatedAt: plan1.CreatedAt, Persist: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !planned1.Persisted || planned1.Plan.Digest != plan1.Digest {
				t.Fatalf("plan result persisted=%v digest=%q want %q", planned1.Persisted, planned1.Plan.Digest, plan1.Digest)
			}
			if policy.approvalRequired && planned1.Plan.Provenance.AttestationDigest == "" {
				t.Fatal("protected-production plan omitted trusted source attestation binding")
			}

			clock = now.Add(time.Minute)
			objects.ambiguous = true // lost catalog-upload acknowledgement
			build1, candidate1 := conformanceBuild(t, ctx, lifecycle, repo, objects, planned1.Plan, "conformance-one", "catalog-conformance-one", now.Add(time.Minute), policy.inputMode, "")
			if build1.Attempt.Status != deployment.DeliveryBuildSealed || !build1.Completion.LeaseReleased || candidate1.Status != deployment.DeliveryCandidateReady {
				t.Fatalf("first build attempt=%s leaseReleased=%v candidate=%s", build1.Attempt.Status, build1.Completion.LeaseReleased, candidate1.Status)
			}
			// A process restart sees the sealed attempt and reconstructs the exact
			// completion without calling the physical runner a second time.
			restartedLifecycle := &deployment.DeliveryLifecycle{Targets: repo, Store: repo, Now: func() time.Time { return clock }}
			restarted := conformancePhase{t: t, failIfRun: true}
			restartRequest := conformanceBuildRequest(planned1.Plan, "conformance-one", "catalog-conformance-one", now.Add(time.Minute), &restarted)
			restartRequest.PhysicalPoolID = policy.poolID
			retry, err := restartedLifecycle.Build(ctx, restartRequest)
			if err != nil {
				t.Fatalf("sealed build restart reconciliation: %v", err)
			}
			if retry.Completion.CandidateID != build1.Completion.CandidateID || restarted.constructed {
				t.Fatalf("restart completion=%#v runnerConstructed=%v", retry.Completion, restarted.constructed)
			}

			verified1 := verifiedSealFromCompletion(build1.Completion)
			publication1 := conformancePublication(planned1.Plan, candidate1, "publication-conformance-one", "generation-conformance-one", now.Add(2*time.Minute))
			generation1 := conformanceGeneration(planned1.Plan, candidate1, publication1.GenerationID, publication1.CreatedAt)
			approved := !policy.approvalRequired
			clock = now.Add(2 * time.Minute)
			expectedSeal := verified1
			coordinator := conformanceCoordinator(repo, &expectedSeal, &approved, policy.approvalRequired, &clock)
			// The protected path persists a pending publication before approval;
			// once trusted approval is supplied it uses the same exact request.
			if policy.approvalRequired {
				if _, err := coordinator.PublishWithActivation(ctx, sealedcontrol.PublishRequest{Publication: publication1, Generation: generation1, Seal: verified1, ActorID: "actor-conformance"}, nil); !errors.Is(err, deployment.ErrApprovalRequired) {
					t.Fatalf("unapproved protected publication error=%v, want approval required", err)
				}
				pending, err := repo.DeliveryPublicationByID(ctx, publication1.ID)
				if err != nil || pending.Status != deployment.DeliveryPublicationPending {
					t.Fatalf("protected pending publication=%#v err=%v", pending, err)
				}
				approved = true
			}
			// Model a lost activation acknowledgement on the first publication;
			// SQLite reconciliation must return the committed durable result.
			// The repository hook models a crash after the durable transaction.
			setConformanceLostCommitAck(repo)
			published1, err := coordinator.PublishWithActivation(ctx, sealedcontrol.PublishRequest{Publication: publication1, Generation: generation1, Seal: verified1, ActorID: "actor-conformance"}, nil)
			if err != nil {
				t.Fatalf("exact publication with lost acknowledgement: %v", err)
			}
			if published1.Status != deployment.DeliveryPublicationCommitted || published1.ResultTargetRevision != 1 {
				t.Fatalf("publication=%#v", published1)
			}
			repo.hooks.CommitPublication = nil
			retryPublication, err := coordinator.Publish(ctx, sealedcontrol.PublishRequest{Publication: publication1, Generation: generation1, Seal: verified1, ActorID: "actor-conformance"})
			if err != nil || retryPublication != published1 {
				t.Fatalf("publication retry=%#v err=%v", retryPublication, err)
			}

			// Build and publish a second exact candidate. The first generation is
			// then rollback-selectable without rebuilding its catalog.
			clock = now.Add(3 * time.Minute)
			planned2, err := lifecycle.Plan(ctx, deployment.DeliveryPlanRequest{
				ID: "plan-conformance-2", ActorID: "actor-conformance", TargetID: policy.targetID, ProjectID: policy.projectID, Environment: policy.environment,
				Operation: deployment.DeliveryOperationCodeChange, SourceDigest: deployment.CanonicalDeliveryDigest([]byte("source-b")),
				Execution: conformanceExecution('b', policy.inputMode), Provenance: conformanceProvenance(policy), Governance: conformanceGovernance(policy, now), Evidence: conformanceEvidence(policy), CreatedAt: clock, Persist: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			clock = now.Add(4 * time.Minute)
			build2, candidate2 := conformanceBuild(t, ctx, lifecycle, repo, objects, planned2.Plan, "conformance-two", "catalog-conformance-two", clock, policy.inputMode, candidate1.CatalogDigest)
			verified2 := verifiedSealFromCompletion(build2.Completion)
			expectedSeal = verified2
			publication2 := conformancePublication(planned2.Plan, candidate2, "publication-conformance-two", "generation-conformance-two", now.Add(5*time.Minute))
			generation2 := conformanceGeneration(planned2.Plan, candidate2, publication2.GenerationID, publication2.CreatedAt)
			clock = now.Add(5 * time.Minute)
			published2, err := coordinator.Publish(ctx, sealedcontrol.PublishRequest{Publication: publication2, Generation: generation2, Seal: verified2, ActorID: "actor-conformance"})
			if err != nil || published2.Status != deployment.DeliveryPublicationCommitted {
				t.Fatalf("second publication=%#v err=%v", published2, err)
			}

			// Hold a generation reader across publication, rollback, retirement,
			// and GC. A candidate-bound lease is released so candidate retirement
			// can proceed while the generation lease remains the sole root.
			reader := deployment.DeliveryQueryLease{ID: "reader-generation-two", HolderID: "reader-conformance", GenerationID: generation2.ID, CatalogDigest: candidate2.CatalogDigest, PhysicalPoolID: pool, CreatedAt: now.Add(5 * time.Minute), ExpiresAt: now.Add(4 * time.Hour)}
			if _, _, err := repo.AcquireQueryLeaseAgainstRoot(ctx, reader); err != nil {
				t.Fatal(err)
			}
			candidateReader := reader
			candidateReader.ID, candidateReader.GenerationID, candidateReader.CandidateID = "reader-candidate-two", "", candidate2.ID
			if _, _, err := repo.AcquireQueryLeaseAgainstRoot(ctx, candidateReader); err != nil {
				t.Fatal(err)
			}

			clock = now.Add(6 * time.Minute)
			expectedSeal = verified1
			rollbackRequest := sealedcontrol.RollbackRequest{Request: deployment.RollbackRequest{
				ID: "rollback-conformance-one", ActorID: "actor-conformance", RequestDigest: deployment.CanonicalDeliveryDigest([]byte("rollback-conformance-one")), TargetID: policy.targetID, ProjectID: projectgraph.ResourceID(policy.projectID), Environment: policy.environment,
				GenerationID: generation1.ID, CandidateID: candidate1.ID, ExpectedBaseGenerationID: generation2.ID, ExpectedTargetRevision: 2, VerifiedSeal: verified1, CreatedAt: clock,
			}, ActorID: "actor-conformance"}
			rolledBack, err := coordinator.Rollback(ctx, rollbackRequest)
			if err != nil || rolledBack.GenerationID != generation1.ID || rolledBack.TargetRevision != 3 {
				t.Fatalf("rollback=%#v err=%v", rolledBack, err)
			}
			if _, err := repo.ReleaseQueryLease(ctx, candidateReader.ID, now.Add(7*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.RetireDeliveryCandidate(ctx, candidate2.ID, now.Add(8*time.Minute)); err != nil {
				t.Fatalf("retire candidate while generation reader is active: %v", err)
			}
			// Keep one independent root lease so the first destructive delete can
			// release it synchronously.  Query-lease release is a real root
			// mutation (and therefore advances the pool fence revision), providing
			// deterministic evidence that the collector stops before its next
			// batch when roots change during deletion.
			rootRaceLease := deployment.DeliveryQueryLease{ID: "reader-root-race", HolderID: "reader-root-race", GenerationID: generation1.ID, CatalogDigest: candidate1.CatalogDigest, PhysicalPoolID: pool, CreatedAt: now.Add(8 * time.Minute), ExpiresAt: now.Add(4 * time.Hour)}
			if _, _, err := repo.AcquireQueryLeaseAgainstRoot(ctx, rootRaceLease); err != nil {
				t.Fatal(err)
			}

			data1, data2 := "data/conformance-one.parquet", "data/conformance-two.parquet"
			objects.add(data1, []byte("data-one"), deployment.CanonicalDeliveryDigest([]byte("data-one")), now.Add(-2*time.Hour))
			objects.add(data2, []byte("data-two"), deployment.CanonicalDeliveryDigest([]byte("data-two")), now.Add(-2*time.Hour))
			objects.add("orphan/conformance-a", []byte("orphan-a"), deployment.CanonicalDeliveryDigest([]byte("orphan-a")), now.Add(-2*time.Hour))
			objects.add("orphan/conformance-b", []byte("orphan-b"), deployment.CanonicalDeliveryDigest([]byte("orphan-b")), now.Add(-2*time.Hour))
			inspector := conformanceInspector{candidate1.CatalogObjectKey: {CatalogKey: candidate1.CatalogObjectKey, CatalogDigest: candidate1.CatalogDigest, DataFiles: []string{data1}}, candidate2.CatalogObjectKey: {CatalogKey: candidate2.CatalogObjectKey, CatalogDigest: candidate2.CatalogDigest, DataFiles: []string{data2}}}

			// The first destructive batch changes the durable root revision. The
			// collector must fence before the next batch rather than sweeping a
			// stale root set.
			var rootRaceErr error
			objects.onDelete = func() {
				_, rootRaceErr = repo.ReleaseQueryLease(ctx, rootRaceLease.ID, now.Add(3*time.Hour))
			}
			clock = now.Add(3 * time.Hour)
			firstGC, err := (deploymentgc.Collector{Control: repo, Store: &conformanceGCStore{objects}, Inspector: inspector, Quarantiner: repo, Config: deploymentgc.Config{PhysicalPoolID: pool, HolderID: "gc-conformance", CycleID: "gc-conformance-race", Now: func() time.Time { return clock }, BatchSize: 1, OrphanGrace: time.Minute, ReaderGrace: time.Hour}}).Run(ctx)
			if rootRaceErr != nil || !errors.Is(err, deploymentgc.ErrGCStale) || firstGC.Deleted != 1 {
				t.Fatalf("stale-root GC result=%#v err=%v rootRaceErr=%v", firstGC, err, rootRaceErr)
			}
			if !objects.has(candidate2.CatalogObjectKey) {
				t.Fatal("GC removed catalog protected by the long generation lease")
			}

			// Once the reader lease expires and its grace period drains, the
			// retired generation and its closure become sweepable. The active
			// generation remains rooted.
			if _, err := repo.ExpireQueryLease(ctx, reader.ID, reader.ExpiresAt); err != nil {
				t.Fatal(err)
			}
			clock = now.Add(6 * time.Hour)
			objects.onDelete = nil
			finalGC, err := (deploymentgc.Collector{Control: repo, Store: &conformanceGCStore{objects}, Inspector: inspector, Quarantiner: repo, Config: deploymentgc.Config{PhysicalPoolID: pool, HolderID: "gc-conformance-final", CycleID: "gc-conformance-final", Now: func() time.Time { return clock }, OrphanGrace: time.Minute, ReaderGrace: time.Hour}}).Run(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if finalGC.Deleted != 3 || objects.has(candidate2.CatalogObjectKey) || !objects.has(candidate1.CatalogObjectKey) {
				t.Fatalf("final GC result=%#v deleted=%v active=%v retired=%v", finalGC, objects.deleted, objects.has(candidate1.CatalogObjectKey), objects.has(candidate2.CatalogObjectKey))
			}

			// Rollback outside the reviewed retention window is rejected by the
			// same durable generation contract (the successful rollback above is
			// the exact in-window selection).
			generation2After, err := repo.DeliveryGenerationByID(ctx, generation2.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := generation2After.Rollback(now.Add(24 * time.Hour)); !errors.Is(err, deployment.ErrDeliveryStale) {
				t.Fatalf("outside-window rollback error=%v, want ErrDeliveryStale", err)
			}
		})
	}
}

func openConformanceRepository(t *testing.T, policy conformancePolicy, now time.Time) (*platform.Store, *Repository) {
	t.Helper()
	store, err := platform.Open(t.Context(), t.TempDir()+"/conformance.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO delivery_target_revisions (target_id,project_id,environment,target_revision,created_at,updated_at) VALUES (?,?,?,?,?,?)`, policy.targetID, policy.projectID, policy.environment, 0, deliveryTime(now), deliveryTime(now)); err != nil {
		t.Fatal(err)
	}
	return store, NewRepositoryWithHooks(store.SQLDB(), ActivationHooks{})
}

func conformancePlan(t *testing.T, policy conformancePolicy, id string, now time.Time, source byte) deployment.DeliveryPlan {
	t.Helper()
	plan, err := deployment.NewDeliveryPlan(deployment.DeliveryPlan{ID: id, ActorID: "actor-conformance", TargetID: policy.targetID, ProjectID: projectgraph.ResourceID(policy.projectID), Environment: policy.environment, Operation: deployment.DeliveryOperationCodeChange, SourceDigest: deployment.CanonicalDeliveryDigest([]byte("source-" + string(source))), Execution: conformanceExecution(source, policy.inputMode), Provenance: conformanceProvenance(policy), Governance: conformanceGovernance(policy, now), Evidence: conformanceEvidence(policy), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func conformanceExecution(source byte, mode deployment.DeliveryDataInputMode) deployment.DeliveryExecutionInputs {
	sourceDigest := deployment.CanonicalDeliveryDigest([]byte("source-" + string(source)))
	input := deployment.DeliveryDataInput{ID: "orders", Mode: mode}
	switch mode {
	case deployment.DeliveryDataPinned:
		input.Revision = "revision-2026-08-18"
	case deployment.DeliveryDataBounded:
		input.Bound = "watermark-2026-08-18"
	}
	return deployment.DeliveryExecutionInputs{SourceArtifactDigest: sourceDigest, CompilerDigest: repoDeliveryDigest('b'), ExecutableDigest: repoDeliveryDigest('c'), DependencyDigest: repoDeliveryDigest('d'), ConfigDigest: repoDeliveryDigest('e'), BindingDigest: repoDeliveryDigest('f'), RuntimeDigest: repoDeliveryDigest('0'), CapabilityDigest: repoDeliveryDigest('1'), DataInputs: []deployment.DeliveryDataInput{input}}
}

func conformanceProvenance(policy conformancePolicy) deployment.DeliveryProvenance {
	builder := "developer"
	if policy.approvalRequired {
		builder = "trusted-builder"
	}
	provenance := deployment.DeliveryProvenance{Builder: builder, Repository: "conformance", SourceRevision: "commit-conformance"}
	if policy.approvalRequired {
		provenance.AttestationDigest = repoDeliveryDigest('5')
	}
	return provenance
}

func conformanceGovernance(policy conformancePolicy, now time.Time) deployment.DeliveryGovernance {
	return deployment.DeliveryGovernance{PolicyDigest: repoDeliveryDigest('2'), AuthorizationDigest: repoDeliveryDigest('3'), QualificationDigest: repoDeliveryDigest('4'), ExpiresAt: now.Add(4 * time.Hour), ObservedInputsAllowed: policy.observedInputsAllowed}
}

func conformanceEvidence(policy conformancePolicy) deployment.DeliveryPlanEvidence {
	return deployment.DeliveryPlanEvidence{ImpactStatement: "conformance impact", PhysicalWorkStatement: "conformance private build", ReuseStatement: "conformance exact reuse", Qualification: deployment.DeliveryQualificationEvidence{Policy: policy.qualificationPolicy, Steps: []deployment.DeliveryQualificationStep{{ID: "contracts", Kind: "contract", Description: "run lifecycle contracts", Required: true, Blocking: true}}}, StalePolicy: deployment.DeliveryStalePolicy{Mode: "reject"}, Rollback: deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryRollbackSafe, RetentionWindow: "1h"}}
}

func conformanceBuildRequest(plan deployment.DeliveryPlan, attemptID, candidateID string, created time.Time, runner deployment.DeliveryBuildPhasedRunner) deployment.DeliveryBuildRequest {
	return deployment.DeliveryBuildRequest{PlanID: plan.ID, IdempotencyKey: "idempotency-" + attemptID, AttemptID: attemptID, WriterLeaseID: "writer-" + attemptID, CandidateID: candidateID, SealID: "seal-" + attemptID, ServingArtifactID: "artifact-" + attemptID, ServingArtifactDigest: repoDeliveryDigest('7'), ServingStateID: "state-" + attemptID, PhysicalPoolID: conformancePoolID(plan.TargetID), OwnerID: "builder-conformance", Epoch: 1, LeaseLifetime: 4 * time.Hour, CreatedAt: created, PhasedRunner: runner}
}

func conformancePoolID(targetID string) string {
	switch targetID {
	case "target-conformance-local":
		return "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	case "target-conformance-pr":
		return "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	case "target-conformance-prod":
		return "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	default:
		return "pool-conformance-unknown"
	}
}

func conformanceBuild(t *testing.T, ctx context.Context, lifecycle *deployment.DeliveryLifecycle, repo *Repository, objects *conformanceObjects, plan deployment.DeliveryPlan, attemptID, candidateID string, created time.Time, mode deployment.DeliveryDataInputMode, baseCatalogDigest string) (deployment.DeliveryBuildResult, deployment.DeliveryCandidate) {
	t.Helper()
	path := t.TempDir() + "/catalog.ducklake"
	if err := os.WriteFile(path, []byte("catalog-"+candidateID), 0o600); err != nil {
		t.Fatal(err)
	}
	evidence, err := (release.GateEvidence{Version: 1, CandidateID: candidateID, SourceDigest: plan.SourceDigest, BindingGeneration: release.BindingFingerprint(nil), RuntimeVersion: "runtime:test", DuckDBVersion: "duckdb:test", Outcome: release.GateSuccess, EvaluatedAt: created.UTC(), Bounds: release.GateBounds{MaxRows: 100, MaxQueries: 10, MaxMillis: 1000}}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	output := deployment.DeliveryBuildOutput{Catalog: catalogseal.FileCatalog{Path: path}, QualificationDigest: repoDeliveryDigest('8'), ClosureDigest: repoDeliveryDigest('9'), CompatibilityDigest: repoDeliveryDigest('a'), GateEvidence: &evidence, ResolvedInputs: conformanceResolvedInputs(plan, mode), ObjectStore: &conformanceCatalogStore{objects: objects, ambiguous: objects.ambiguous, createdAt: created}, SealRepository: repo, RemoteVerifier: conformanceVerifier{objects: objects}}
	runner := &conformancePhase{t: t, output: output}
	request := conformanceBuildRequest(plan, attemptID, candidateID, created, runner)
	// The pool identity is derived from the target and must match the pool row;
	// keeping it explicit catches accidental cross-target reuse in this suite.
	request.PhysicalPoolID = conformancePoolID(plan.TargetID)
	request.BaseCatalogDigest = baseCatalogDigest
	if baseCatalogDigest != "" {
		request.BasePhysicalPoolID = request.PhysicalPoolID
	}
	result, err := lifecycle.Build(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := repo.DeliveryCandidateByID(ctx, candidateID)
	if err != nil {
		t.Fatal(err)
	}
	return result, candidate
}

func conformanceResolvedInputs(plan deployment.DeliveryPlan, mode deployment.DeliveryDataInputMode) deployment.DeliveryResolvedBuildInputs {
	binding := release.BindingFingerprint(nil)
	evidence, err := (release.GateEvidence{Version: 1, CandidateID: "candidate-conformance", SourceDigest: plan.SourceDigest, BindingGeneration: binding, RuntimeVersion: "runtime:test", DuckDBVersion: "duckdb:test", Outcome: release.GateSuccess, EvaluatedAt: time.Date(2026, 8, 18, 15, 0, 0, 0, time.UTC), Bounds: release.GateBounds{MaxRows: 100, MaxQueries: 10, MaxMillis: 1000}}).Canonical()
	if err != nil {
		panic(err)
	}
	resolved := deployment.DeliveryResolvedBuildInputs{PolicyDigest: plan.Governance.PolicyDigest, GateEvidence: &evidence}
	for _, declaration := range plan.Execution.DataInputs {
		input := deployment.DeliveryResolvedDataInput{ID: declaration.ID, Mode: declaration.Mode, PlannedRevision: declaration.Revision, PlannedBound: declaration.Bound, Explanation: "conformance resolved input"}
		switch mode {
		case deployment.DeliveryDataPinned:
			input.ActualRevision = declaration.Revision
		case deployment.DeliveryDataBounded:
			input.ActualBound = declaration.Bound
		case deployment.DeliveryDataObserved:
			input.ObservationDigest = repoDeliveryDigest('6')
		}
		resolved.Inputs = append(resolved.Inputs, input)
	}
	return resolved
}

func conformancePublication(plan deployment.DeliveryPlan, candidate deployment.DeliveryCandidate, id, generationID string, created time.Time) deployment.DeliveryPublication {
	return deployment.DeliveryPublication{ID: id, ActorID: "actor-conformance", RequestDigest: deployment.CanonicalDeliveryDigest([]byte(id)), TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment, PlanID: plan.ID, PlanDigest: plan.Digest, CandidateID: candidate.ID, GenerationID: generationID, ExpectedBaseGenerationID: plan.BaseGenerationID, ExpectedTargetRevision: plan.BaseTargetRevision, Status: deployment.DeliveryPublicationPending, CreatedAt: created}
}

func conformanceGeneration(plan deployment.DeliveryPlan, candidate deployment.DeliveryCandidate, id string, created time.Time) deployment.DeliveryGeneration {
	return deployment.DeliveryGeneration{ID: id, CandidateID: candidate.ID, PlanID: plan.ID, PlanDigest: plan.Digest, TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment, CatalogDigest: candidate.CatalogDigest, CatalogObjectKey: candidate.CatalogObjectKey, PhysicalPoolID: candidate.PhysicalPoolID, ServingArtifactID: candidate.ServingArtifactID, ServingArtifactDigest: candidate.ServingArtifactDigest, ServingStateID: candidate.ServingStateID, CompatibilityDigest: candidate.CompatibilityDigest, RollbackClass: deployment.DeliveryRollbackSafe, RollbackUntil: created.Add(time.Hour), Status: deployment.DeliveryGenerationPrepared, CreatedAt: created}
}

func verifiedSealFromCompletion(completion catalogseal.Completion) deployment.VerifiedSeal {
	i := completion.Seal.Identity
	return deployment.VerifiedSeal{SealID: i.SealID, CatalogDigest: i.CatalogDigest, CatalogObjectKey: i.ObjectKey, ObjectSize: i.ObjectSize, PhysicalPoolID: i.Pool.ID, CompatibilityDigest: i.Pool.CompatibilityDigest, ClosureDigest: i.Closure.Digest, QualificationDigest: i.Qualification.Digest, ServingArtifactID: i.Candidate.ServingArtifactID, ServingArtifactDigest: i.Candidate.ServingArtifactDigest}
}

func conformanceCoordinator(repo *Repository, seal *deployment.VerifiedSeal, approved *bool, requireApproval bool, clock *time.Time) *sealedcontrol.Coordinator {
	return &sealedcontrol.Coordinator{Publications: repo, Rollbacks: repo, VerifySeal: func(_ context.Context, binding sealedcontrol.SealBinding) error {
		if !reflect.DeepEqual(binding.Seal, *seal) {
			return errors.New("verified seal identity drift")
		}
		return nil
	}, Authorize: func(context.Context, sealedcontrol.SealBinding) error { return nil }, ApprovalVerifier: func(context.Context, sealedcontrol.SealBinding, deployment.PublicationIntent) error {
		if requireApproval && !*approved {
			return deployment.ErrApprovalRequired
		}
		return nil
	}, Now: func() time.Time { return *clock }}
}

// setConformanceLostCommitAck installs the repository's test-only commit hook
// without exposing SQL transaction details in the scenario itself.
func setConformanceLostCommitAck(repo *Repository) {
	repo.hooks.CommitPublication = func(_ context.Context, tx *sql.Tx) error {
		_ = tx.Commit()
		return errors.New("lost activation acknowledgement")
	}
}

type conformancePhase struct {
	t           *testing.T
	output      deployment.DeliveryBuildOutput
	failIfRun   bool
	constructed bool
}

func (p *conformancePhase) Construct(context.Context, deployment.DeliveryBuildInput) (any, error) {
	p.constructed = true
	if p.failIfRun {
		p.t.Fatal("sealed restart invoked physical runner")
	}
	return p, nil
}
func (*conformancePhase) Normalize(context.Context, deployment.DeliveryBuildInput, any) error {
	return nil
}
func (p *conformancePhase) Qualify(context.Context, deployment.DeliveryBuildInput, any) (deployment.DeliveryBuildOutput, error) {
	return p.output, nil
}
func (*conformancePhase) Close() error { return nil }

type conformanceObject struct {
	body      []byte
	digest    string
	version   string
	createdAt time.Time
	metadata  map[string]string
}

type conformanceObjects struct {
	mu        sync.Mutex
	objects   map[string]conformanceObject
	deleted   []string
	onDelete  func()
	ambiguous bool
}

func (s *conformanceObjects) add(key string, body []byte, digest string, createdAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = conformanceObject{body: append([]byte(nil), body...), digest: digest, version: "v1", createdAt: createdAt, metadata: map[string]string{catalogseal.MetadataDigest: digest, catalogseal.MetadataSize: fmt.Sprint(len(body))}}
}
func (s *conformanceObjects) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[key]
	return ok
}

type conformanceCatalogStore struct {
	objects   *conformanceObjects
	ambiguous bool
	createdAt time.Time
}

func (s *conformanceCatalogStore) Create(_ context.Context, key string, reader io.Reader, metadata catalogseal.ObjectMetadata) error {
	body, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.objects.mu.Lock()
	defer s.objects.mu.Unlock()
	if _, exists := s.objects.objects[key]; exists {
		return catalogseal.ErrObjectExists
	}
	digest := metadata[catalogseal.MetadataDigest]
	s.objects.objects[key] = conformanceObject{body: append([]byte(nil), body...), digest: digest, version: "v1", createdAt: s.createdAt.UTC(), metadata: metadata}
	if s.ambiguous || s.objects.ambiguous {
		s.objects.ambiguous = false
		return catalogseal.ErrObjectAmbiguous
	}
	return nil
}
func (s *conformanceCatalogStore) Open(_ context.Context, key string) (catalogseal.Object, error) {
	s.objects.mu.Lock()
	defer s.objects.mu.Unlock()
	o, ok := s.objects.objects[key]
	if !ok {
		return catalogseal.Object{}, os.ErrNotExist
	}
	return catalogseal.Object{Body: io.NopCloser(bytes.NewReader(o.body)), Size: int64(len(o.body)), Metadata: o.metadata}, nil
}

type conformanceVerifier struct{ objects *conformanceObjects }

func (v conformanceVerifier) Verify(ctx context.Context, remote catalogseal.RemoteVerification) error {
	o, err := remote.Open(ctx)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(o.Body)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	if got := "sha256:" + fmt.Sprintf("%x", sum[:]); got != remote.Identity.CatalogDigest || int64(len(body)) != remote.Identity.ObjectSize {
		return fmt.Errorf("remote catalog identity mismatch")
	}
	return nil
}

type conformanceInspector map[string]deploymentgc.CatalogReachability

func (i conformanceInspector) Inspect(_ context.Context, root deployment.DeliveryRoot) (deploymentgc.CatalogReachability, error) {
	reach, ok := i[root.ObjectKey]
	if !ok {
		return deploymentgc.CatalogReachability{}, errors.New("unknown catalog root")
	}
	return reach, nil
}

type conformanceGCStore struct{ objects *conformanceObjects }

func (s *conformanceGCStore) Open(_ context.Context, key string) (deploymentgc.CatalogObject, error) {
	s.objects.mu.Lock()
	defer s.objects.mu.Unlock()
	o, ok := s.objects.objects[key]
	if !ok {
		return deploymentgc.CatalogObject{}, os.ErrNotExist
	}
	return deploymentgc.CatalogObject{Body: io.NopCloser(bytes.NewReader(o.body)), Size: int64(len(o.body)), Metadata: o.metadata}, nil
}
func (s *conformanceGCStore) ListPoolObjects(context.Context, string) ([]deploymentgc.Object, error) {
	s.objects.mu.Lock()
	defer s.objects.mu.Unlock()
	result := make([]deploymentgc.Object, 0, len(s.objects.objects))
	for key, o := range s.objects.objects {
		result = append(result, deploymentgc.Object{Key: key, Digest: o.digest, Version: o.version, CreatedAt: o.createdAt, LastModified: o.createdAt})
	}
	return result, nil
}
func (s *conformanceGCStore) DeleteConditional(_ context.Context, req deploymentgc.DeleteRequest) (deploymentgc.DeleteResponse, error) {
	s.objects.mu.Lock()
	o, ok := s.objects.objects[req.Key]
	if !ok {
		s.objects.mu.Unlock()
		return deploymentgc.DeleteResponse{NotFound: true}, nil
	}
	if o.digest != req.Digest || (req.Version != "" && req.Version != o.version) {
		s.objects.mu.Unlock()
		return deploymentgc.DeleteResponse{}, errors.New("conditional delete identity changed")
	}
	delete(s.objects.objects, req.Key)
	s.objects.deleted = append(s.objects.deleted, req.Key)
	fn := s.objects.onDelete
	s.objects.onDelete = nil
	s.objects.mu.Unlock()
	if fn != nil {
		fn()
	}
	return deploymentgc.DeleteResponse{Deleted: true}, nil
}
func (s *conformanceGCStore) Stat(_ context.Context, _ string, key string) (deploymentgc.Object, error) {
	s.objects.mu.Lock()
	defer s.objects.mu.Unlock()
	o, ok := s.objects.objects[key]
	if !ok {
		return deploymentgc.Object{}, os.ErrNotExist
	}
	return deploymentgc.Object{Key: key, Digest: o.digest, Version: o.version, CreatedAt: o.createdAt, LastModified: o.createdAt}, nil
}
