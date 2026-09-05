package deploymentpostgres

import (
	"errors"
	"testing"
	"time"

	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	lineagepostgres "github.com/flidai/leapview/internal/lineage/postgres"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingnative "github.com/flidai/leapview/internal/servingstate/postgres"
)

// Delivery owns retention-root transitions while serving_state owns reader
// leases. A retiring root must reject new exact-generation readers and cannot
// expire until existing DB-clock-live leases have drained.
func TestDeliveryRetentionRootRetirementDrainsExactReaderLeases(t *testing.T) {
	p := generationAdmissionDB(t)
	delivery := deploymentnative.New(p)
	serving := servingnative.New(p)
	input := validGenerationAdmissionInput(t)
	seedGenerationAdmission(t, delivery, input)
	capability, err := NewGenerationAdmission(delivery, serving, lineagepostgres.New(p), candidatePhysicalAdmissionStub{}, &testManagedDataBindingAdmission{}, releasepostgres.New(p))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capability.CompleteBuildAndAdmit(t.Context(), input); err != nil {
		t.Fatalf("admit generation: %v", err)
	}
	reader, err := serving.CreateQuerySnapshotLease(t.Context(), servingstate.SnapshotLeaseInput{
		ServingStateID: servingstate.ID(input.Generation.GenerationID), DuckLakeSnapshotID: input.Commit.SnapshotID,
		OwnerID: "retention-reader", ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("create exact reader lease: %v", err)
	}
	// Candidate retirement is deadline-gated until activation selects its
	// generation. Move the governance deadline into the past after admitting
	// this reader so the test can exercise the drain race independently.
	if _, err := p.Exec(t.Context(), `UPDATE delivery.delivery_retention_root SET expires_at=clock_timestamp()-interval '1 second' WHERE root_id=$1::uuid`, input.Generation.CandidateID); err != nil {
		t.Fatal(err)
	}

	tx, err := delivery.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	retiring, err := delivery.RetireRetentionRootTx(t.Context(), tx, input.Generation.CandidateID)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("retire root: %v", err)
	}
	if retiring.State != "retiring" {
		_ = tx.Rollback(t.Context())
		t.Fatalf("retired root state = %q", retiring.State)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := serving.CreateQuerySnapshotLease(t.Context(), servingstate.SnapshotLeaseInput{
		ServingStateID: servingstate.ID(input.Generation.GenerationID), DuckLakeSnapshotID: input.Commit.SnapshotID,
		OwnerID: "late-reader", ExpiresAt: time.Now().UTC().Add(time.Minute),
	}); err == nil {
		t.Fatal("reader lease admitted after root retirement")
	}
	if _, err := delivery.ExpireRetentionRoot(t.Context(), input.Generation.CandidateID); !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("expiry with active exact reader lease = %v, want ErrConflict", err)
	}
	if err := serving.ReleaseQuerySnapshotLease(t.Context(), reader); err != nil {
		t.Fatalf("release reader lease: %v", err)
	}
	// Candidate governance expiry is the authoritative DB-clock deadline;
	// move it into the past to model maintenance after that deadline.
	if _, err := p.Exec(t.Context(), `UPDATE delivery.delivery_retention_root SET expires_at=clock_timestamp()-interval '1 second' WHERE root_id=$1::uuid`, input.Generation.CandidateID); err != nil {
		t.Fatal(err)
	}
	expired, err := delivery.ExpireRetentionRoot(t.Context(), input.Generation.CandidateID)
	if err != nil || expired.State != "expired" {
		t.Fatalf("expiry after reader drain = %#v, err=%v", expired, err)
	}
}

func TestDeliveryRetentionMaintenanceRetiresDueCandidatesAndExpiresReadyRoots(t *testing.T) {
	p := generationAdmissionDB(t)
	delivery := deploymentnative.New(p)
	input := validGenerationAdmissionInput(t)
	seedGenerationAdmission(t, delivery, input)
	capability, err := NewGenerationAdmission(delivery, servingnative.New(p), lineagepostgres.New(p), candidatePhysicalAdmissionStub{}, &testManagedDataBindingAdmission{}, releasepostgres.New(p))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capability.CompleteBuildAndAdmit(t.Context(), input); err != nil {
		t.Fatalf("admit generation: %v", err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE delivery.delivery_retention_root SET expires_at=clock_timestamp()-interval '1 second' WHERE root_id=$1::uuid`, input.Generation.CandidateID); err != nil {
		t.Fatal(err)
	}

	drain := deploymentnative.NewMaintenance(p)
	result, err := drain.Drain(t.Context(), input.Seal.PhysicalPoolID, input.Seal.CatalogID, 0, 10)
	if err != nil {
		t.Fatalf("drain due candidate root: %v", err)
	}
	if result.Retired != 1 || result.Expired != 1 {
		t.Fatalf("drain result = %#v, want one retired and expired root", result)
	}
	var state string
	var retiredAt, expiredAt *time.Time
	if err := p.QueryRow(t.Context(), `SELECT state,retired_at,expired_at FROM delivery.delivery_retention_root WHERE root_id=$1::uuid`, input.Generation.CandidateID).Scan(&state, &retiredAt, &expiredAt); err != nil {
		t.Fatal(err)
	}
	if state != "expired" || retiredAt == nil || expiredAt == nil {
		t.Fatalf("drained candidate root = state %q retired %v expired %v", state, retiredAt, expiredAt)
	}
	replay, err := drain.Drain(t.Context(), input.Seal.PhysicalPoolID, input.Seal.CatalogID, 0, 10)
	if err != nil || replay.Retired != 0 || replay.Expired != 0 {
		t.Fatalf("drain replay = %#v, %v", replay, err)
	}
}

func TestActiveGenerationRetiringRootRequiresFreshLiveReplacementBeforeExpiry(t *testing.T) {
	p := generationAdmissionDB(t)
	delivery := deploymentnative.New(p)
	input := validGenerationAdmissionInput(t)
	seedGenerationAdmission(t, delivery, input)
	capability, err := NewGenerationAdmission(delivery, servingnative.New(p), lineagepostgres.New(p), candidatePhysicalAdmissionStub{}, &testManagedDataBindingAdmission{}, releasepostgres.New(p))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capability.CompleteBuildAndAdmit(t.Context(), input); err != nil {
		t.Fatalf("admit generation: %v", err)
	}

	rootOne := "a1a1a1a1-a1a1-41a1-81a1-a1a1a1a1a1a1"
	rootTwo := "a2a2a2a2-a2a2-42a2-82a2-a2a2a2a2a2a2"
	publication := "a3a3a3a3-a3a3-43a3-83a3-a3a3a3a3a3a3"
	if _, err := delivery.CreateTarget(t.Context(), deploymentnative.TargetInput{TargetID: "target-retention-forged", ProjectID: "project-retention-forged", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `INSERT INTO delivery.delivery_retention_root(root_id,target_id,candidate_id,generation_id,snapshot_seal_id,root_kind,state) VALUES('a4a4a4a4-a4a4-44a4-84a4-a4a4a4a4a4a4','target-retention-forged',$1::uuid,$2::uuid,$3::uuid,'generation','live')`, input.Generation.CandidateID, input.Generation.GenerationID, input.Seal.SealID); err == nil {
		t.Fatal("generation retention root accepted a cross-target canonical tuple")
	}
	rootInput := deploymentnative.DeliveryRetentionRoot{RootID: rootOne, TargetID: input.Generation.TargetID, CandidateID: input.Generation.CandidateID, GenerationID: input.Generation.GenerationID, SnapshotSealID: input.Seal.SealID, RootKind: "generation", State: "live"}
	if _, err := delivery.CreateRetentionRoot(t.Context(), rootInput); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.RetireRetentionRoot(t.Context(), rootOne); err != nil {
		t.Fatalf("retire historical generation root before pointer selection: %v", err)
	}
	if _, err := p.Exec(t.Context(), `
		INSERT INTO delivery.delivery_publication(publication_id,target_id,generation_id,candidate_id,snapshot_seal_id,expected_target_revision,result_target_revision,actor_id,state,request_digest,committed_at)
		VALUES($1::uuid,$2,$3::uuid,$4::uuid,$5::uuid,1,2,'retention-test','committed',$6,clock_timestamp())`, publication, input.Generation.TargetID, input.Generation.GenerationID, input.Generation.CandidateID, input.Seal.SealID, admissionDigest('4')); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `INSERT INTO delivery.delivery_active_pointer(target_id,generation_id,publication_id) VALUES($1,$2::uuid,$3::uuid)`, input.Generation.TargetID, input.Generation.GenerationID, publication); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.ExpireRetentionRoot(t.Context(), rootOne); !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("active generation root expired without replacement: %v", err)
	}
	rootInput.RootID = rootTwo
	if _, err := delivery.CreateRetentionRoot(t.Context(), rootInput); err != nil {
		t.Fatal(err)
	}
	expired, err := delivery.ExpireRetentionRoot(t.Context(), rootOne)
	if err != nil || expired.State != "expired" {
		t.Fatalf("retiring historical root with live replacement = %#v, %v", expired, err)
	}
}
