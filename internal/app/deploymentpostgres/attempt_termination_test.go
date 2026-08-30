package deploymentpostgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
)

type attemptTerminationDuckLakeStub struct {
	inner  *ducklakepostgres.Repository
	fail   error
	tamper bool
}

func (s *attemptTerminationDuckLakeStub) Configured() bool {
	return s != nil && s.inner != nil && s.inner.Configured()
}

func (s *attemptTerminationDuckLakeStub) AbortAttemptTx(ctx context.Context, tx ducklakepostgres.Tx, in ducklakepostgres.TerminateAttemptInput) (ducklakepostgres.AttemptEvidence, error) {
	if s.fail != nil {
		return ducklakepostgres.AttemptEvidence{}, s.fail
	}
	got, err := s.inner.AbortAttemptTx(ctx, tx, in)
	if s.tamper {
		got.RequestDigest = admissionDigest('9')
	}
	return got, err
}

func (s *attemptTerminationDuckLakeStub) MarkAttemptIndeterminateTx(ctx context.Context, tx ducklakepostgres.Tx, in ducklakepostgres.TerminateAttemptInput) (ducklakepostgres.AttemptEvidence, error) {
	if s.fail != nil {
		return ducklakepostgres.AttemptEvidence{}, s.fail
	}
	got, err := s.inner.MarkAttemptIndeterminateTx(ctx, tx, in)
	if s.tamper {
		got.RequestDigest = admissionDigest('9')
	}
	return got, err
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
	fixture.Input.CatalogID = "catalog-attempt-termination-" + string(rune('0'+index))
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
	ducklake := ducklakepostgres.New(p)
	if _, err := NewAttemptTermination(nil, ducklake); err == nil {
		t.Fatal("attempt termination accepted a nil delivery authority")
	}
	if _, err := NewAttemptTermination(delivery, nil); err == nil {
		t.Fatal("attempt termination accepted a nil DuckLake authority")
	}
	termination, err := NewAttemptTermination(delivery, ducklake)
	if err != nil {
		t.Fatal(err)
	}

	aborted := uniqueTerminationFixture(t, 1)
	seedCandidateAdmissionFixture(t, delivery, ducklake, aborted)
	admission, _ := NewCandidateBuildAttemptAdmission(delivery, ducklake)
	if _, err := admission.AdmitCandidateBuildAttempt(t.Context(), aborted.Input); err != nil {
		t.Fatal(err)
	}
	input := AttemptTerminationInput{AttemptID: aborted.Input.Attempt.AttemptID, OwnerID: aborted.Input.Attempt.OwnerID, FencingEpoch: 1, Evidence: []byte(`{"z":2,"a":"terminated"}`)}
	first, err := termination.AbortAttempt(t.Context(), input)
	if err != nil {
		t.Fatalf("abort attempt: %v", err)
	}
	if first.DeliveryAttempt.State != deploymentnative.AttemptAborted || first.DuckLakeAttempt.State != ducklakepostgres.AttemptAborted || !sameTerminationEvidence(first.DeliveryAttempt.TerminationEvidence, []byte(`{"a":"terminated","z":2}`)) || !sameTerminationEvidence(first.DuckLakeAttempt.TerminationEvidence, []byte(`{"a":"terminated","z":2}`)) {
		t.Fatalf("abort result = %#v", first)
	}
	if replay, err := termination.AbortAttempt(t.Context(), input); err != nil || replay.DeliveryAttempt.State != deploymentnative.AttemptAborted || replay.DuckLakeAttempt.State != ducklakepostgres.AttemptAborted {
		t.Fatalf("exact abort replay = %#v, %v", replay, err)
	}
	if _, err := termination.AbortAttempt(t.Context(), AttemptTerminationInput{AttemptID: input.AttemptID, OwnerID: input.OwnerID, FencingEpoch: input.FencingEpoch, Evidence: []byte(`{"reason":"different"}`)}); !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("conflicting abort evidence error = %v", err)
	}
	if _, err := termination.AbortAttempt(t.Context(), AttemptTerminationInput{AttemptID: input.AttemptID, OwnerID: "stale-owner", FencingEpoch: input.FencingEpoch, Evidence: input.Evidence}); !errors.Is(err, deploymentnative.ErrStaleFence) {
		t.Fatalf("stale abort fence error = %v", err)
	}

	indeterminate := uniqueTerminationFixture(t, 2)
	seedCandidateAdmissionFixture(t, delivery, ducklake, indeterminate)
	if _, err := admission.AdmitCandidateBuildAttempt(t.Context(), indeterminate.Input); err != nil {
		t.Fatal(err)
	}
	indeterminateInput := AttemptTerminationInput{AttemptID: indeterminate.Input.Attempt.AttemptID, OwnerID: indeterminate.Input.Attempt.OwnerID, FencingEpoch: 1, Evidence: []byte(`{"session":"unknown"}`)}
	indeterminateFirst, err := termination.MarkAttemptIndeterminate(t.Context(), indeterminateInput)
	if err != nil || indeterminateFirst.DeliveryAttempt.State != deploymentnative.AttemptIndeterminate || indeterminateFirst.DuckLakeAttempt.State != ducklakepostgres.AttemptIndeterminate {
		t.Fatalf("indeterminate result = %#v, %v", indeterminateFirst, err)
	}
	if replay, err := termination.MarkAttemptIndeterminate(t.Context(), indeterminateInput); err != nil || replay.DuckLakeAttempt.State != ducklakepostgres.AttemptIndeterminate {
		t.Fatalf("exact indeterminate replay = %#v, %v", replay, err)
	}

	failure := uniqueTerminationFixture(t, 3)
	seedCandidateAdmissionFixture(t, delivery, ducklake, failure)
	if _, err := admission.AdmitCandidateBuildAttempt(t.Context(), failure.Input); err != nil {
		t.Fatal(err)
	}
	failErr := errors.New("injected second-ledger failure")
	stub := &attemptTerminationDuckLakeStub{inner: ducklake, fail: failErr}
	failing, err := NewAttemptTermination(delivery, stub)
	if err != nil {
		t.Fatal(err)
	}
	failureInput := AttemptTerminationInput{AttemptID: failure.Input.Attempt.AttemptID, OwnerID: failure.Input.Attempt.OwnerID, FencingEpoch: 1, Evidence: []byte(`{"reason":"terminated"}`)}
	if _, err := failing.AbortAttempt(t.Context(), failureInput); !errors.Is(err, failErr) {
		t.Fatalf("injected second-ledger error = %v", err)
	}
	if got, err := delivery.BuildAttempt(t.Context(), failureInput.AttemptID); err != nil || got.State != deploymentnative.AttemptRunning {
		t.Fatalf("delivery was not rolled back after second-ledger failure = %#v, %v", got, err)
	}
	if got, err := ducklake.LoadAttempt(t.Context(), failureInput.AttemptID); err != nil || got.State != ducklakepostgres.AttemptRunning {
		t.Fatalf("DuckLake was not rolled back after second-ledger failure = %#v, %v", got, err)
	}
	stub.fail = nil
	if _, err := failing.AbortAttempt(t.Context(), failureInput); err != nil {
		t.Fatalf("abort after injected failure: %v", err)
	}

	tampered := uniqueTerminationFixture(t, 4)
	seedCandidateAdmissionFixture(t, delivery, ducklake, tampered)
	if _, err := admission.AdmitCandidateBuildAttempt(t.Context(), tampered.Input); err != nil {
		t.Fatal(err)
	}
	stub = &attemptTerminationDuckLakeStub{inner: ducklake, tamper: true}
	tampering, err := NewAttemptTermination(delivery, stub)
	if err != nil {
		t.Fatal(err)
	}
	tamperInput := AttemptTerminationInput{AttemptID: tampered.Input.Attempt.AttemptID, OwnerID: tampered.Input.Attempt.OwnerID, FencingEpoch: 1, Evidence: []byte(`{"reason":"indeterminate"}`)}
	if _, err := tampering.MarkAttemptIndeterminate(t.Context(), tamperInput); !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("tampered second-ledger output error = %v", err)
	}
	if got, err := delivery.BuildAttempt(t.Context(), tamperInput.AttemptID); err != nil || got.State != deploymentnative.AttemptRunning {
		t.Fatalf("delivery was not rolled back after tampered output = %#v, %v", got, err)
	}
	if got, err := ducklake.LoadAttempt(t.Context(), tamperInput.AttemptID); err != nil || got.State != ducklakepostgres.AttemptRunning {
		t.Fatalf("DuckLake was not rolled back after tampered output = %#v, %v", got, err)
	}

	expired := uniqueTerminationFixture(t, 5)
	expired.ExpiresAt = time.Now().UTC().Add(750 * time.Millisecond)
	expired.Input.Lease.ExpiresAt = expired.ExpiresAt
	expired.Input.Attempt.LeaseExpiresAt = expired.ExpiresAt
	seedCandidateAdmissionFixture(t, delivery, ducklake, expired)
	if _, err := admission.AdmitCandidateBuildAttempt(t.Context(), expired.Input); err != nil {
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
	if _, err := termination.AbortAttempt(t.Context(), AttemptTerminationInput{AttemptID: expired.Input.Attempt.AttemptID, OwnerID: expired.Input.Attempt.OwnerID, FencingEpoch: 1, Evidence: []byte(`{"session":"terminated","committed":false}`)}); err != nil {
		t.Fatalf("expired attempt termination: %v", err)
	}
}

func TestAttemptTerminationTxComposesAndCallerControlsRollback(t *testing.T) {
	p := candidateAdmissionDB(t)
	delivery := deploymentnative.New(p)
	ducklake := ducklakepostgres.New(p)
	admission, err := NewCandidateBuildAttemptAdmission(delivery, ducklake)
	if err != nil {
		t.Fatal(err)
	}
	termination, err := NewAttemptTermination(delivery, ducklake)
	if err != nil {
		t.Fatal(err)
	}

	aborted := uniqueTerminationFixture(t, 6)
	seedCandidateAdmissionFixture(t, delivery, ducklake, aborted)
	if _, err := admission.AdmitCandidateBuildAttempt(t.Context(), aborted.Input); err != nil {
		t.Fatal(err)
	}
	abortInput := AttemptTerminationInput{AttemptID: aborted.Input.Attempt.AttemptID, OwnerID: aborted.Input.Attempt.OwnerID, FencingEpoch: 1, Evidence: []byte(`{"reason":"caller-owned"}`)}
	tx, err := delivery.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := termination.AbortAttemptTx(t.Context(), tx, abortInput)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("abort attempt in caller transaction: %v", err)
	}
	if result.DeliveryAttempt.State != deploymentnative.AttemptAborted || result.DuckLakeAttempt.State != ducklakepostgres.AttemptAborted {
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
	seedCandidateAdmissionFixture(t, delivery, ducklake, indeterminate)
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
	if got, err := ducklake.LoadAttempt(t.Context(), indeterminateInput.AttemptID); err != nil || got.State != ducklakepostgres.AttemptRunning {
		t.Fatalf("rolled-back DuckLake attempt = %#v, %v", got, err)
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
