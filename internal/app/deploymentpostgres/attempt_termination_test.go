package deploymentpostgres

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
)

func deterministicAbortInput(t *testing.T, attempt deploymentnative.DeliveryBuildAttempt, ownerID string, errorDigestByte byte) AttemptTerminationInput {
	t.Helper()
	if ownerID == "" {
		ownerID = attempt.OwnerID
	}
	document := nativeBuildTerminationEvidence{
		SchemaVersion: 1, AttemptID: attempt.AttemptID, OwnerID: ownerID, FencingEpoch: attempt.FencingEpoch,
		RequestDigest: attempt.RequestDigest, PlanDigest: attempt.PlanDigest, PhysicalPoolID: attempt.PhysicalPoolID,
		Namespace: attempt.Namespace, SessionIdentity: attempt.SessionIdentity, Phase: NativePhysicalBuildPhaseValidation,
		Classification: NativePhysicalFailureDeterministic, ErrorDigest: admissionDigest(errorDigestByte),
	}
	evidence, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return AttemptTerminationInput{AttemptID: attempt.AttemptID, OwnerID: ownerID, FencingEpoch: attempt.FencingEpoch, Evidence: evidence}
}

func uniqueTerminationFixture(t *testing.T, index int) candidateAdmissionFixture {
	t.Helper()
	fixture := candidateAdmissionFixtureInput(t)
	fixture.Input.Lease.LeaseID = "0198f2c0-7c7a-7f00-8a11-0000000004" + string(rune('0'+index)) + "1"
	fixture.Plan.PlanID = "0198f2c0-7c7a-7f00-8a11-0000000004" + string(rune('0'+index)) + "2"
	fixture.Input.Attempt.PlanID = fixture.Plan.PlanID
	fixture.Input.Attempt.AttemptID = "0198f2c0-7c7a-7f00-8a11-0000000004" + string(rune('0'+index)) + "3"
	fixture.Input.Attempt.CandidateID = "0198f2c0-7c7a-7f00-8a11-0000000004" + string(rune('0'+index)) + "4"
	fixture.Input.Lease.TargetID = "target-attempt-termination-" + string(rune('0'+index))
	fixture.Input.Attempt.PhysicalPoolID = "pool-attempt-termination-" + string(rune('0'+index))
	fixture.Input.Artifact.ServingStateID = "serving-state-attempt-termination-" + string(rune('0'+index))
	fixture.Target.TargetID = fixture.Input.Lease.TargetID
	fixture.Target.ProjectID = "project-attempt-termination-" + string(rune('0'+index))
	fixture.Plan.PlanID = fixture.Input.Attempt.PlanID
	fixture.Plan.TargetID = fixture.Target.TargetID
	fixture.Candidate.CandidateID = fixture.Input.Attempt.CandidateID
	fixture.Candidate.TargetID = fixture.Target.TargetID
	fixture.Candidate.PlanID = fixture.Plan.PlanID
	fixture.Plan = nativePlanFixture(t, fixture.Plan, fixture.Target.ProjectID)
	fixture.Input.Attempt.PlanDigest = fixture.Plan.PlanDigest
	fixture.ExpiresAt = fixture.Input.Lease.ExpiresAt
	return fixture
}

func TestAttemptTerminationPostgresAtomicOutcomesReplayAndRollback(t *testing.T) {
	p := candidateAdmissionDB(t)
	delivery := deploymentnative.New(p)
	if _, err := NewAttemptTermination(nil); err == nil {
		t.Fatal("attempt termination accepted a nil delivery authority")
	}
	termination, err := NewAttemptTermination(delivery)
	if err != nil {
		t.Fatal(err)
	}

	aborted := uniqueTerminationFixture(t, 1)
	seedCandidateAdmissionFixture(t, delivery, aborted)
	admission, _ := NewCandidateBuildAttemptAdmission(delivery, candidatePhysicalAdmissionStub{})
	abortedAdmission, err := admission.AdmitCandidateBuildAttempt(t.Context(), aborted.Input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := termination.AbortAttempt(t.Context(), AttemptTerminationInput{AttemptID: abortedAdmission.Attempt.AttemptID, OwnerID: abortedAdmission.Attempt.OwnerID, FencingEpoch: 1, Evidence: []byte(`{"reason":"untyped"}`)}); !errors.Is(err, deploymentnative.ErrInvalid) {
		t.Fatalf("untyped abort evidence error = %v", err)
	}
	if got, err := delivery.BuildAttempt(t.Context(), abortedAdmission.Attempt.AttemptID); err != nil || got.State != deploymentnative.AttemptRunning {
		t.Fatalf("invalid abort changed delivery attempt = %#v, %v", got, err)
	}
	input := deterministicAbortInput(t, abortedAdmission.Attempt, "", '7')
	first, err := termination.AbortAttempt(t.Context(), input)
	if err != nil {
		t.Fatalf("abort attempt: %v", err)
	}
	if first.DeliveryAttempt.State != deploymentnative.AttemptAborted || !sameTerminationEvidence(first.DeliveryAttempt.TerminationEvidence, input.Evidence) {
		t.Fatalf("abort result = %#v", first)
	}
	if replay, err := termination.AbortAttempt(t.Context(), input); err != nil || replay.DeliveryAttempt.State != deploymentnative.AttemptAborted {
		t.Fatalf("exact abort replay = %#v, %v", replay, err)
	}
	if _, err := termination.AbortAttempt(t.Context(), deterministicAbortInput(t, abortedAdmission.Attempt, "", '8')); !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("conflicting abort evidence error = %v", err)
	}
	if _, err := termination.AbortAttempt(t.Context(), deterministicAbortInput(t, abortedAdmission.Attempt, "stale-owner", '7')); !errors.Is(err, deploymentnative.ErrStaleFence) {
		t.Fatalf("stale abort fence error = %v", err)
	}

	indeterminate := uniqueTerminationFixture(t, 2)
	seedCandidateAdmissionFixture(t, delivery, indeterminate)
	if _, err := admission.AdmitCandidateBuildAttempt(t.Context(), indeterminate.Input); err != nil {
		t.Fatal(err)
	}
	indeterminateInput := AttemptTerminationInput{AttemptID: indeterminate.Input.Attempt.AttemptID, OwnerID: indeterminate.Input.Attempt.OwnerID, FencingEpoch: 1, Evidence: []byte(`{"session":"unknown"}`)}
	indeterminateFirst, err := termination.MarkAttemptIndeterminate(t.Context(), indeterminateInput)
	if err != nil || indeterminateFirst.DeliveryAttempt.State != deploymentnative.AttemptIndeterminate {
		t.Fatalf("indeterminate result = %#v, %v", indeterminateFirst, err)
	}
	if replay, err := termination.MarkAttemptIndeterminate(t.Context(), indeterminateInput); err != nil {
		t.Fatalf("exact indeterminate replay = %#v, %v", replay, err)
	}

	failure := uniqueTerminationFixture(t, 3)
	seedCandidateAdmissionFixture(t, delivery, failure)
	failureAdmission, err := admission.AdmitCandidateBuildAttempt(t.Context(), failure.Input)
	if err != nil {
		t.Fatal(err)
	}
	failureInput := deterministicAbortInput(t, failureAdmission.Attempt, "", '7')
	if _, err := termination.AbortAttempt(t.Context(), failureInput); err != nil {
		t.Fatalf("abort after injected failure: %v", err)
	}

	tampered := uniqueTerminationFixture(t, 4)
	seedCandidateAdmissionFixture(t, delivery, tampered)
	if _, err := admission.AdmitCandidateBuildAttempt(t.Context(), tampered.Input); err != nil {
		t.Fatal(err)
	}
	tamperInput := AttemptTerminationInput{AttemptID: tampered.Input.Attempt.AttemptID, OwnerID: tampered.Input.Attempt.OwnerID, FencingEpoch: 1, Evidence: []byte(`{"reason":"indeterminate"}`)}
	if _, err := termination.MarkAttemptIndeterminate(t.Context(), tamperInput); err != nil {
		t.Fatalf("indeterminate termination = %v", err)
	}

	expired := uniqueTerminationFixture(t, 5)
	expired.ExpiresAt = time.Now().UTC().Add(750 * time.Millisecond)
	expired.Input.Lease.ExpiresAt = expired.ExpiresAt
	expired.Input.Attempt.LeaseExpiresAt = expired.ExpiresAt
	seedCandidateAdmissionFixture(t, delivery, expired)
	expiredAdmission, err := admission.AdmitCandidateBuildAttempt(t.Context(), expired.Input)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(expired.ExpiresAt) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if time.Now().Before(expired.ExpiresAt) {
		t.Fatal("test did not observe the immutable attempt lease expiry")
	}
	marker, err := (catalogartifact.CommitMarker{
		SchemaVersion: catalogartifact.CommitMarkerSchemaVersion, DeliveryID: "delivery-expired", GenerationID: "generation-expired",
		AttemptID: expired.Input.Attempt.AttemptID, LeaseEpoch: 1, RequestDigest: expired.Input.Attempt.RequestDigest,
		PlanDigest: expired.Input.Attempt.PlanDigest, Project: "project-expired", Environment: "prod", PhysicalPoolID: expired.Input.Attempt.PhysicalPoolID,
	}).CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CommitBuildAttempt(t.Context(), deploymentnative.CommitAttemptInput{AttemptID: expired.Input.Attempt.AttemptID, OwnerID: expired.Input.Attempt.OwnerID, FencingEpoch: 1, SnapshotID: 1, CommitMarker: []byte(marker)}); !errors.Is(err, deploymentnative.ErrLeaseExpired) {
		t.Fatalf("expired commit error = %v, want lease-expired", err)
	}
	if _, err := termination.AbortAttempt(t.Context(), deterministicAbortInput(t, expiredAdmission.Attempt, "", '7')); err != nil {
		t.Fatalf("expired attempt termination: %v", err)
	}
}

func TestAttemptTerminationTxComposesAndCallerControlsRollback(t *testing.T) {
	p := candidateAdmissionDB(t)
	delivery := deploymentnative.New(p)
	admission, err := NewCandidateBuildAttemptAdmission(delivery, candidatePhysicalAdmissionStub{})
	if err != nil {
		t.Fatal(err)
	}
	termination, err := NewAttemptTermination(delivery)
	if err != nil {
		t.Fatal(err)
	}

	aborted := uniqueTerminationFixture(t, 6)
	seedCandidateAdmissionFixture(t, delivery, aborted)
	abortedAdmission, err := admission.AdmitCandidateBuildAttempt(t.Context(), aborted.Input)
	if err != nil {
		t.Fatal(err)
	}
	abortInput := deterministicAbortInput(t, abortedAdmission.Attempt, "", '7')
	tx, err := delivery.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := termination.AbortAttemptTx(t.Context(), tx, abortInput)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("abort attempt in caller transaction: %v", err)
	}
	if result.DeliveryAttempt.State != deploymentnative.AttemptAborted {
		_ = tx.Rollback(t.Context())
		t.Fatalf("abort result = %#v", result)
	}
	adjacent := deploymentnative.TargetInput{TargetID: "target-attempt-termination-adjacent", ProjectID: aborted.Target.ProjectID, Environment: "staging"}
	if _, err := delivery.CreateTargetTx(t.Context(), tx, adjacent); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("adjacent mutation after caller-owned abort: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit composed abort transaction: %v", err)
	}
	if _, err := delivery.Target(t.Context(), adjacent.TargetID); err != nil {
		t.Fatalf("adjacent mutation was not committed with abort: %v", err)
	}
	if got, err := delivery.BuildAttempt(t.Context(), abortInput.AttemptID); err != nil || got.State != deploymentnative.AttemptAborted {
		t.Fatalf("aborted delivery attempt = %#v, %v", got, err)
	}

	indeterminate := uniqueTerminationFixture(t, 7)
	seedCandidateAdmissionFixture(t, delivery, indeterminate)
	if _, err := admission.AdmitCandidateBuildAttempt(t.Context(), indeterminate.Input); err != nil {
		t.Fatal(err)
	}
	indeterminateInput := AttemptTerminationInput{AttemptID: indeterminate.Input.Attempt.AttemptID, OwnerID: indeterminate.Input.Attempt.OwnerID, FencingEpoch: 1, Evidence: []byte(`{"reason":"caller-rollback"}`)}
	tx, err = delivery.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := termination.MarkAttemptIndeterminateTx(t.Context(), tx, indeterminateInput); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("mark indeterminate in caller transaction: %v", err)
	}
	rolledBackAdjacent := deploymentnative.TargetInput{TargetID: "target-attempt-termination-rollback-adjacent", ProjectID: indeterminate.Target.ProjectID, Environment: "staging"}
	if _, err := delivery.CreateTargetTx(t.Context(), tx, rolledBackAdjacent); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("adjacent mutation before caller rollback: %v", err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}
	if _, err := delivery.Target(t.Context(), rolledBackAdjacent.TargetID); !errors.Is(err, deploymentnative.ErrNotFound) {
		t.Fatalf("caller rollback retained adjacent target, err=%v", err)
	}
	if got, err := delivery.BuildAttempt(t.Context(), indeterminateInput.AttemptID); err != nil || got.State != deploymentnative.AttemptRunning {
		t.Fatalf("rolled-back delivery attempt = %#v, %v", got, err)
	}
}

func TestNormalizeAttemptTerminationInputCanonicalBounds(t *testing.T) {
	base := AttemptTerminationInput{AttemptID: "0198f2c0-7c7a-7f00-8a11-000000000403", OwnerID: "builder", FencingEpoch: 1, Evidence: []byte(`{"b":1,"a":2}`)}
	got, canonical, err := normalizeAttemptTerminationInput(base)
	if err != nil || string(canonical) != `{"a":2,"b":1}` || string(got.Evidence) != string(canonical) {
		t.Fatalf("normalized termination input = %#v, %q, %v", got, canonical, err)
	}
	large := []byte(`{"nonce":900719925474099312345678901234567890}`)
	_, canonical, err = normalizeAttemptTerminationInput(AttemptTerminationInput{AttemptID: base.AttemptID, OwnerID: base.OwnerID, FencingEpoch: 1, Evidence: large})
	if err != nil || string(canonical) != string(large) {
		t.Fatalf("large integer evidence was not preserved: %q, %v", canonical, err)
	}
	for name, input := range map[string]AttemptTerminationInput{
		"missing evidence":   {AttemptID: base.AttemptID, OwnerID: base.OwnerID, FencingEpoch: 1},
		"array evidence":     {AttemptID: base.AttemptID, OwnerID: base.OwnerID, FencingEpoch: 1, Evidence: []byte(`[]`)},
		"oversized evidence": {AttemptID: base.AttemptID, OwnerID: base.OwnerID, FencingEpoch: 1, Evidence: []byte(`{"value":"` + strings.Repeat("x", 32760) + `"}`)},
	} {
		if _, _, err := normalizeAttemptTerminationInput(input); !errors.Is(err, deploymentnative.ErrInvalid) {
			t.Errorf("%s error = %v", name, err)
		}
	}
}

func TestAttemptReconciliationExpiredLeaseExactCommitAndReplay(t *testing.T) {
	p := candidateAdmissionDB(t)
	delivery := deploymentnative.New(p)
	admission, err := NewCandidateBuildAttemptAdmission(delivery, candidatePhysicalAdmissionStub{})
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := NewAttemptTermination(delivery)
	if err != nil {
		t.Fatal(err)
	}
	fixture := uniqueTerminationFixture(t, 8)
	fixture.ExpiresAt = time.Now().UTC().Add(750 * time.Millisecond)
	fixture.Input.Lease.ExpiresAt = fixture.ExpiresAt
	fixture.Input.Attempt.LeaseExpiresAt = fixture.ExpiresAt
	seedCandidateAdmissionFixture(t, delivery, fixture)
	if _, err := admission.AdmitCandidateBuildAttempt(t.Context(), fixture.Input); err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(fixture.ExpiresAt) {
		time.Sleep(10 * time.Millisecond)
	}
	marker, err := (catalogartifact.CommitMarker{SchemaVersion: catalogartifact.CommitMarkerSchemaVersion, DeliveryID: "delivery-reconcile", GenerationID: "generation-reconcile", AttemptID: fixture.Input.Attempt.AttemptID, LeaseEpoch: 1, RequestDigest: fixture.Input.Attempt.RequestDigest, PlanDigest: fixture.Input.Attempt.PlanDigest, Project: "project-reconcile", Environment: "prod", PhysicalPoolID: fixture.Input.Attempt.PhysicalPoolID}).CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	in := AttemptReconciliationInput{AttemptID: fixture.Input.Attempt.AttemptID, OwnerID: fixture.Input.Attempt.OwnerID, FencingEpoch: 1, PhysicalPoolID: fixture.Input.Attempt.PhysicalPoolID, SnapshotID: 11, CommitMarker: []byte(marker), State: deploymentnative.AttemptCommitted}
	if _, err := delivery.CommitBuildAttempt(t.Context(), deploymentnative.CommitAttemptInput{AttemptID: in.AttemptID, OwnerID: in.OwnerID, FencingEpoch: 1, SnapshotID: in.SnapshotID, CommitMarker: in.CommitMarker}); !errors.Is(err, deploymentnative.ErrLeaseExpired) {
		t.Fatalf("normal expired commit error = %v, want lease expiry", err)
	}
	if _, err := recovery.MarkAttemptIndeterminate(t.Context(), AttemptTerminationInput{AttemptID: fixture.Input.Attempt.AttemptID, OwnerID: fixture.Input.Attempt.OwnerID, FencingEpoch: 1, Evidence: []byte(`{"reason":"ack-lost"}`)}); err != nil {
		t.Fatal(err)
	}
	first, err := recovery.ReconcileAttempt(t.Context(), in)
	if err != nil || first.DeliveryAttempt.State != deploymentnative.AttemptCommitted {
		t.Fatalf("expired recovery commit = %#v, %v", first, err)
	}
	if replay, err := recovery.ReconcileAttempt(t.Context(), in); err != nil || replay.DeliveryAttempt.State != deploymentnative.AttemptCommitted {
		t.Fatalf("exact recovery replay = %#v, %v", replay, err)
	}
	changed := in
	changed.SnapshotID++
	if _, err := recovery.ReconcileAttempt(t.Context(), changed); !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("mismatched recovery replay = %v, want conflict", err)
	}
}

func TestAttemptReconciliationMissingMarkerTerminatedAbort(t *testing.T) {
	p := candidateAdmissionDB(t)
	delivery := deploymentnative.New(p)
	admission, err := NewCandidateBuildAttemptAdmission(delivery, candidatePhysicalAdmissionStub{})
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := NewAttemptTermination(delivery)
	if err != nil {
		t.Fatal(err)
	}
	fixture := uniqueTerminationFixture(t, 9)
	seedCandidateAdmissionFixture(t, delivery, fixture)
	if _, err := admission.AdmitCandidateBuildAttempt(t.Context(), fixture.Input); err != nil {
		t.Fatal(err)
	}
	if _, err := recovery.MarkAttemptIndeterminate(t.Context(), AttemptTerminationInput{AttemptID: fixture.Input.Attempt.AttemptID, OwnerID: fixture.Input.Attempt.OwnerID, FencingEpoch: 1, Evidence: []byte(`{"reason":"unknown"}`)}); err != nil {
		t.Fatal(err)
	}
	evidence := []byte(`{"schema_version":1,"attempt_id":"` + fixture.Input.Attempt.AttemptID + `","owner_id":"` + fixture.Input.Attempt.OwnerID + `","fencing_epoch":1,"session_identity":"` + fixture.Input.Attempt.SessionIdentity + `","session_terminated":true}`)
	in := AttemptReconciliationInput{AttemptID: fixture.Input.Attempt.AttemptID, OwnerID: fixture.Input.Attempt.OwnerID, FencingEpoch: 1, SessionIdentity: fixture.Input.Attempt.SessionIdentity, TerminationEvidence: evidence, SessionTerminated: true, State: deploymentnative.AttemptAborted}
	first, err := recovery.ReconcileAttempt(t.Context(), in)
	if err != nil || first.DeliveryAttempt.State != deploymentnative.AttemptAborted {
		t.Fatalf("missing-marker terminated abort = %#v, %v", first, err)
	}
	if replay, err := recovery.ReconcileAttempt(t.Context(), in); err != nil || replay.DeliveryAttempt.State != deploymentnative.AttemptAborted {
		t.Fatalf("exact aborted recovery replay = %#v, %v", replay, err)
	}
	bad := in
	bad.TerminationEvidence = []byte(`{"schema_version":1,"attempt_id":"` + fixture.Input.Attempt.AttemptID + `","owner_id":"wrong","fencing_epoch":1,"session_identity":"` + fixture.Input.Attempt.SessionIdentity + `","session_terminated":true}`)
	if _, err := recovery.ReconcileAttempt(t.Context(), bad); !errors.Is(err, deploymentnative.ErrInvalid) && !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("mismatched termination evidence = %v", err)
	}
}
