package deployment

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogseal"
)

type destinationQualificationPhase struct {
	output    DeliveryBuildOutput
	called    *bool
	devDigest string
}

func (p destinationQualificationPhase) Construct(context.Context, DeliveryBuildInput) (any, error) {
	return p, nil
}

func (p destinationQualificationPhase) Normalize(context.Context, DeliveryBuildInput, any) error {
	return nil
}

func (p destinationQualificationPhase) Qualify(context.Context, DeliveryBuildInput, any) (DeliveryBuildOutput, error) {
	*p.called = true
	return p.output, nil
}

func (destinationQualificationPhase) Close() error { return nil }

// TestDestinationBuildRunsQualificationAfterDevelopmentEvidence proves that
// a plan carrying a development qualification digest still executes the
// destination-owned phased Qualify callback and seals its resulting digest.
func TestDestinationBuildRunsQualificationAfterDevelopmentEvidence(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	plan := testLifecycleBuildPlan(t, now)
	devDigest := plan.Governance.QualificationDigest
	file, err := os.CreateTemp(t.TempDir(), "destination-qualification-*.ducklake")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("destination-qualified-catalog"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	objects := &lifecycleObjectStore{}
	store := &lifecycleBuildStore{plan: plan}
	seals := &lifecycleSealRepository{onDone: func(identity catalogseal.SealIdentity) {
		store.attempt.Status = DeliveryBuildSealed
		store.attempt.SealID = identity.SealID
		store.attempt.CandidateID = identity.Candidate.ID
		store.attempt.TerminalAt = now
		store.attempt.Revision++
		store.attempt.UpdatedAt = now
	}}
	called := false
	destinationDigest := lifecycleDigest('9')
	runner := destinationQualificationPhase{
		called:    &called,
		devDigest: devDigest,
		output: DeliveryBuildOutput{
			Catalog: catalogseal.FileCatalog{Path: file.Name()}, QualificationDigest: destinationDigest,
			ClosureDigest: lifecycleDigest('5'), CompatibilityDigest: lifecycleDigest('6'),
			ResolvedInputs: DeliveryResolvedBuildInputs{PolicyDigest: plan.Governance.PolicyDigest},
			ObjectStore:    objects, SealRepository: seals, RemoteVerifier: lifecycleRemoteVerifier{},
		},
	}
	lifecycle := &DeliveryLifecycle{Targets: lifecycleTarget{state: DeliveryTarget{TargetID: "target", ProjectID: "project", Environment: "prod"}}, Store: store, Now: func() time.Time { return now }}
	result, err := lifecycle.Build(t.Context(), DeliveryBuildRequest{
		PlanID: plan.ID, AttemptID: "attempt-destination-qualification", WriterLeaseID: "writer-destination-qualification",
		CandidateID: "candidate-destination-qualification", SealID: "seal-destination-qualification",
		ServingArtifactID: "artifact-destination-qualification", ServingArtifactDigest: lifecycleDigest('e'), ServingStateID: "state-destination-qualification",
		PhysicalPoolID: "pool-build", OwnerID: "owner-build", Epoch: 1, LeaseLifetime: time.Hour, CreatedAt: now, PhasedRunner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("destination qualification callback did not run")
	}
	if result.Completion.Seal.Identity.Qualification.Digest != destinationDigest {
		t.Fatalf("sealed qualification digest = %q, want destination digest %q", result.Completion.Seal.Identity.Qualification.Digest, destinationDigest)
	}
	if result.Completion.Seal.Identity.Qualification.Digest == devDigest {
		t.Fatalf("destination reused development qualification digest %q", devDigest)
	}
}
