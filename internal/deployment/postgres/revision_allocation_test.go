package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
)

func TestPostgresTargetRevisionAllocationReplayRollbackAndConcurrency(t *testing.T) {
	p := deliveryTestDB(t)
	r := New(p)
	ctx := context.Background()
	target := "target_revision_allocator"
	if _, err := r.CreateTarget(ctx, TargetInput{TargetID: target, ProjectID: "project_revision", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	plan := func(id string, digestByte byte) PlanInput {
		rich, document := richPlanDocumentFixture(t, id, target, "project_revision")
		if digestByte != 'a' {
			rich.SourceDigest = testDigest(digestByte)
			rich.Execution.SourceArtifactDigest = rich.SourceDigest
			var err error
			rich, err = deployment.NewDeliveryPlan(rich)
			if err != nil {
				t.Fatal(err)
			}
			document, err = json.Marshal(rich)
			if err != nil {
				t.Fatal(err)
			}
		}
		return PlanInput{PlanID: id, TargetID: target, PlanRevision: 0, PlanDigest: rich.Digest, CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: rich.Execution.ConfigDigest, SecurityDomainFingerprint: testDigest('d'), ArtifactDigest: testDigest('e'), QualificationDigest: rich.Governance.QualificationDigest, PlanDocument: document}
	}
	invalidRevision := plan("0198f2c0-7c7a-7f00-8a11-000000001000", 'a')
	invalidRevision.PlanRevision = 9
	if _, err := r.CreatePlanAllocated(ctx, invalidRevision); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nonzero allocated plan revision = %v", err)
	}
	first, err := r.CreatePlanAllocated(ctx, plan("0198f2c0-7c7a-7f00-8a11-000000001001", 'a'))
	if err != nil || first.PlanRevision != 1 {
		t.Fatalf("first allocated plan = %#v, %v", first, err)
	}
	replay, err := r.CreatePlanAllocated(ctx, plan(first.PlanID, 'a'))
	if err != nil || replay.PlanRevision != first.PlanRevision {
		t.Fatalf("plan replay = %#v, %v", replay, err)
	}
	if _, err := r.CreatePlanAllocated(ctx, plan(first.PlanID, 'f')); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting plan replay = %v", err)
	}
	second, err := r.CreatePlanAllocated(ctx, plan("0198f2c0-7c7a-7f00-8a11-000000001002", 'a'))
	if err != nil || second.PlanRevision != 2 {
		t.Fatalf("second allocated plan = %#v, %v", second, err)
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := r.CreatePlanAllocatedTx(ctx, tx, plan("0198f2c0-7c7a-7f00-8a11-000000001003", 'a'))
	if err != nil || rolled.PlanRevision != 3 {
		_ = tx.Rollback(ctx)
		t.Fatalf("rolled-back allocation = %#v, %v", rolled, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	afterRollback, err := r.CreatePlanAllocated(ctx, plan("0198f2c0-7c7a-7f00-8a11-000000001004", 'a'))
	if err != nil || afterRollback.PlanRevision != 3 {
		t.Fatalf("post-rollback allocation = %#v, %v", afterRollback, err)
	}

	const concurrentPlans = 8
	planRevisions := make(chan int64, concurrentPlans)
	planErrors := make(chan error, concurrentPlans)
	var wg sync.WaitGroup
	for i := 0; i < concurrentPlans; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := "0198f2c0-7c7a-7f00-8a11-00000000110" + string(rune('0'+i))
			created, createErr := r.CreatePlanAllocated(ctx, plan(id, 'a'))
			if createErr != nil {
				planErrors <- createErr
				return
			}
			planRevisions <- created.PlanRevision
		}()
	}
	wg.Wait()
	close(planRevisions)
	close(planErrors)
	for createErr := range planErrors {
		t.Fatal(createErr)
	}
	gotRevisions := make([]int64, 0, concurrentPlans)
	for revision := range planRevisions {
		gotRevisions = append(gotRevisions, revision)
	}
	sort.Slice(gotRevisions, func(i, j int) bool { return gotRevisions[i] < gotRevisions[j] })
	if len(gotRevisions) != concurrentPlans {
		t.Fatalf("allocated plan count = %v", gotRevisions)
	}
	for i, revision := range gotRevisions {
		want := int64(4 + i)
		if revision != want {
			t.Fatalf("allocated plan revisions = %v, want contiguous from 4", gotRevisions)
		}
	}

	explicitTarget := "target_explicit_plan_counter"
	if _, err := r.CreateTarget(ctx, TargetInput{TargetID: explicitTarget, ProjectID: "project_explicit", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	explicitPlan := richPlanInputFixture(t, "0198f2c0-7c7a-7f00-8a11-000000001500", explicitTarget, "project_explicit")
	explicitPlan.PlanRevision = 7
	if _, err := r.CreatePlan(ctx, explicitPlan); err != nil {
		t.Fatal(err)
	}
	allocatedInput := richPlanInputFixture(t, "0198f2c0-7c7a-7f00-8a11-000000001501", explicitTarget, "project_explicit")
	allocatedInput.PlanRevision = 0
	allocatedPlan, err := r.CreatePlanAllocated(ctx, allocatedInput)
	if err != nil || allocatedPlan.PlanRevision != 8 {
		t.Fatalf("post-explicit plan allocation = %#v, %v", allocatedPlan, err)
	}
}

func TestPostgresFreshTargetPlanAllocationAndCandidateConcurrency(t *testing.T) {
	p := deliveryTestDB(t)
	r := New(p)
	ctx := context.Background()
	target := "target_fresh_allocator"
	planID := "0198f2c0-7c7a-7f00-8a11-000000002001"
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, plan, err := r.CreateTargetAndPlanAllocatedTx(ctx, tx, TargetInput{TargetID: target, ProjectID: "project_fresh", Environment: "prod"}, richPlanInputFixture(t, planID, target, "project_fresh"))
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if plan.PlanRevision != 1 {
		_ = tx.Rollback(ctx)
		t.Fatalf("fresh target first plan revision = %d", plan.PlanRevision)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	const concurrentCandidates = 8
	invalidCandidate := CandidateInput{CandidateID: "0198f2c0-7c7a-7f00-8a11-000000002000", TargetID: target, PlanID: planID, ArtifactDigest: testDigest('e'), CandidateRevision: 9}
	if _, err := r.CreateCandidateAllocated(ctx, invalidCandidate); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nonzero allocated candidate revision = %v", err)
	}
	candidateRevisions := make(chan int64, concurrentCandidates)
	candidateErrors := make(chan error, concurrentCandidates)
	var wg sync.WaitGroup
	for i := 0; i < concurrentCandidates; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := "0198f2c0-7c7a-7f00-8a11-00000000210" + string(rune('0'+i))
			candidate, createErr := r.CreateCandidateAllocated(ctx, CandidateInput{CandidateID: id, TargetID: target, PlanID: planID, ArtifactDigest: testDigest('e')})
			if createErr != nil {
				candidateErrors <- createErr
				return
			}
			candidateRevisions <- candidate.CandidateRevision
		}()
	}
	wg.Wait()
	close(candidateRevisions)
	close(candidateErrors)
	for createErr := range candidateErrors {
		t.Fatal(createErr)
	}
	gotRevisions := make([]int64, 0, concurrentCandidates)
	for revision := range candidateRevisions {
		gotRevisions = append(gotRevisions, revision)
	}
	sort.Slice(gotRevisions, func(i, j int) bool { return gotRevisions[i] < gotRevisions[j] })
	for i, revision := range gotRevisions {
		if revision != int64(i+1) {
			t.Fatalf("allocated candidate revisions = %v, want contiguous from 1", gotRevisions)
		}
	}

	explicitTarget := "target_explicit_candidate_counter"
	explicitPlanID := "0198f2c0-7c7a-7f00-8a11-000000002500"
	if _, err := r.CreateTarget(ctx, TargetInput{TargetID: explicitTarget, ProjectID: "project_explicit_candidate", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreatePlan(ctx, func() PlanInput {
		p := richPlanInputFixture(t, explicitPlanID, explicitTarget, "project_explicit_candidate")
		p.PlanRevision = 1
		return p
	}()); err != nil {
		t.Fatal(err)
	}
	explicitCandidate := CandidateInput{CandidateID: "0198f2c0-7c7a-7f00-8a11-000000002501", TargetID: explicitTarget, PlanID: explicitPlanID, CandidateRevision: 7, ArtifactDigest: testDigest('e')}
	if _, err := r.CreateCandidate(ctx, explicitCandidate); err != nil {
		t.Fatal(err)
	}
	allocatedCandidate, err := r.CreateCandidateAllocated(ctx, CandidateInput{CandidateID: "0198f2c0-7c7a-7f00-8a11-000000002502", TargetID: explicitTarget, PlanID: explicitPlanID, ArtifactDigest: explicitCandidate.ArtifactDigest})
	if err != nil || allocatedCandidate.CandidateRevision != 8 {
		t.Fatalf("post-explicit candidate allocation = %#v, %v", allocatedCandidate, err)
	}
}

func TestPostgresGenerationRevisionAllocationReplayRollbackAndConcurrency(t *testing.T) {
	p := deliveryTestDB(t)
	r := New(p)
	ctx := context.Background()
	target := "target_generation_allocator"
	planID := "0198f2c0-7c7a-7f00-8a11-000000003001"
	candidateID := "0198f2c0-7c7a-7f00-8a11-000000003002"
	attemptID := "0198f2c0-7c7a-7f00-8a11-000000003003"
	sealID := "0198f2c0-7c7a-7f00-8a11-000000003004"
	planDigest, artifactDigest := testDigest('a'), testDigest('e')
	if _, err := r.CreateTarget(ctx, TargetInput{TargetID: target, ProjectID: "project_generation", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	planInput := richPlanInputFixture(t, planID, target, "project_generation")
	planDigest, artifactDigest = planInput.PlanDigest, planInput.ArtifactDigest
	if _, err := r.CreatePlanAllocated(ctx, planInput); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateCandidateAllocated(ctx, CandidateInput{CandidateID: candidateID, TargetID: target, PlanID: planID, ArtifactDigest: artifactDigest}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BeginBuildAttempt(ctx, BuildAttemptInput{AttemptID: attemptID, PlanID: planID, CandidateID: candidateID, OwnerID: "builder-generation", PhysicalPoolID: "pool-generation", FencingEpoch: 1, RequestDigest: testDigest('f'), PlanDigest: planDigest, Namespace: "candidate/generation", SessionIdentity: "session-generation", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BindBuildArtifact(ctx, BuildArtifactBindingInput{AttemptID: attemptID, ServingArtifactID: "artifact-generation", ServingArtifactDigest: artifactDigest, ServingStateID: "generation-test", OwnerID: "builder-generation", FencingEpoch: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitBuildAttempt(ctx, CommitAttemptInput{AttemptID: attemptID, OwnerID: "builder-generation", FencingEpoch: 1, SnapshotID: 42, CommitMarker: testCommitMarker(attemptID, "pool-generation", testDigest('f'), planDigest)}); err != nil {
		t.Fatal(err)
	}
	seal := SnapshotSealInput{SealID: sealID, AttemptID: attemptID, CandidateID: candidateID, PhysicalPoolID: "pool-generation", TenantDomain: "tenant-generation", Region: "us-east", EncryptionDomain: "enc-generation", ObjectNamespace: "objects/generation", CatalogDatabase: "ducklake", CatalogID: "catalog-generation", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000003005", CatalogVersion: 1, DuckLakeSnapshotID: 42, RelationNamespace: "candidate/generation", RelationManifestDigest: testDigest('1'), ClosureDigest: testDigest('8'), ObjectRoot: "objects/generation/42", ObjectRootDigest: testDigest('6'), ArtifactRoot: "artifacts/" + artifactDigest, ArtifactRootDigest: testDigest('7'), CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), RequestDigest: testDigest('f'), PlanDigest: planDigest, CompatibilityDigest: testDigest('2'), ServingArtifactID: "artifact-generation", ServingArtifactDigest: artifactDigest, DuckDBVersion: "1", RuntimeVersion: "runtime-v1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1", QualificationEvidence: []byte(`{"checks":["schema"]}`)}
	if _, err := r.CreateSnapshotSeal(ctx, seal); err != nil {
		t.Fatal(err)
	}
	if _, err := r.QualifyCandidate(ctx, candidateID, sealID, testDigest('3')); err != nil {
		t.Fatal(err)
	}
	generation := func(id string, digestValue string) GenerationInput {
		return GenerationInput{GenerationID: id, TargetID: target, CandidateID: candidateID, SnapshotSealID: sealID, PlanID: planID, PlanDigest: planDigest, ArtifactRoot: seal.ArtifactRoot, ArtifactRootDigest: seal.ArtifactRootDigest, ServingArtifactDigest: digestValue, CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d')}
	}
	invalidRevision := generation("0198f2c0-7c7a-7f00-8a11-000000003010", artifactDigest)
	invalidRevision.GenerationRevision = 9
	if _, err := r.CreateGenerationAllocated(ctx, invalidRevision); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nonzero allocated generation revision = %v", err)
	}
	first, err := r.CreateGenerationAllocated(ctx, generation("0198f2c0-7c7a-7f00-8a11-000000003011", artifactDigest))
	if err != nil || first.GenerationRevision != 1 {
		t.Fatalf("first allocated generation = %#v, %v", first, err)
	}
	replay, err := r.CreateGenerationAllocated(ctx, generation(first.GenerationID, artifactDigest))
	if err != nil || replay.GenerationRevision != first.GenerationRevision {
		t.Fatalf("generation replay = %#v, %v", replay, err)
	}
	if _, err := r.CreateGenerationAllocated(ctx, generation(first.GenerationID, testDigest('f'))); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting generation replay = %v", err)
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := r.CreateGenerationAllocatedTx(ctx, tx, generation("0198f2c0-7c7a-7f00-8a11-000000003012", artifactDigest))
	if err != nil || rolled.GenerationRevision != 2 {
		_ = tx.Rollback(ctx)
		t.Fatalf("rolled-back generation = %#v, %v", rolled, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	afterRollback, err := r.CreateGenerationAllocated(ctx, generation("0198f2c0-7c7a-7f00-8a11-000000003013", artifactDigest))
	if err != nil || afterRollback.GenerationRevision != 2 {
		t.Fatalf("post-rollback generation = %#v, %v", afterRollback, err)
	}
	explicit, err := r.CreateGeneration(ctx, GenerationInput{GenerationID: "0198f2c0-7c7a-7f00-8a11-000000003014", TargetID: target, CandidateID: candidateID, SnapshotSealID: sealID, PlanID: planID, PlanDigest: planDigest, ArtifactRoot: seal.ArtifactRoot, ArtifactRootDigest: seal.ArtifactRootDigest, ServingArtifactDigest: artifactDigest, CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), GenerationRevision: 50})
	if err != nil || explicit.GenerationRevision != 50 {
		t.Fatalf("explicit generation = %#v, %v", explicit, err)
	}
	afterExplicit, err := r.CreateGenerationAllocated(ctx, generation("0198f2c0-7c7a-7f00-8a11-000000003015", artifactDigest))
	if err != nil || afterExplicit.GenerationRevision != 51 {
		t.Fatalf("post-explicit generation = %#v, %v", afterExplicit, err)
	}

	const concurrentGenerations = 2
	revisions := make(chan int64, concurrentGenerations)
	createErrors := make(chan error, concurrentGenerations)
	var wg sync.WaitGroup
	for i := 0; i < concurrentGenerations; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := "0198f2c0-7c7a-7f00-8a11-00000000302" + string(rune('0'+i))
			created, createErr := r.CreateGenerationAllocated(ctx, generation(id, artifactDigest))
			if createErr != nil {
				createErrors <- createErr
				return
			}
			revisions <- created.GenerationRevision
		}()
	}
	wg.Wait()
	close(revisions)
	close(createErrors)
	for createErr := range createErrors {
		t.Fatal(createErr)
	}
	got := make([]int64, 0, concurrentGenerations)
	for revision := range revisions {
		got = append(got, revision)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if len(got) != concurrentGenerations || got[0] != 52 || got[1] != 53 {
		t.Fatalf("concurrent generation revisions = %v", got)
	}
}
