package deployment

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogseal"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

type lifecycleTarget struct{ state DeliveryTarget }

func lifecycleGateEvidence(t *testing.T, candidateID, sourceDigest string, now time.Time) *release.GateEvidence {
	t.Helper()
	evidence, err := (release.GateEvidence{Version: 1, CandidateID: candidateID, SourceDigest: sourceDigest, BindingGeneration: release.BindingFingerprint(nil), RuntimeVersion: "runtime:test", DuckDBVersion: "duckdb:test", Outcome: release.GateSuccess, EvaluatedAt: now, Bounds: release.GateBounds{MaxRows: 100, MaxQueries: 10, MaxMillis: 1000}}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	return &evidence
}

func (t lifecycleTarget) ResolveDeliveryTarget(context.Context, string) (DeliveryTarget, error) {
	return t.state, nil
}

type lifecycleStore struct{ created int }

func (s *lifecycleStore) CreatePlan(_ context.Context, p DeliveryPlan) (DeliveryPlan, error) {
	s.created++
	return p, nil
}
func (s *lifecycleStore) PlanByID(context.Context, string) (DeliveryPlan, error) {
	return DeliveryPlan{}, errors.New("unused")
}
func (*lifecycleStore) CreateWriterLeaseAndBuildAttempt(context.Context, DeliveryWriterLease, DeliveryBuildAttempt) (DeliveryWriterLease, DeliveryBuildAttempt, error) {
	panic("writer path reached")
}
func (*lifecycleStore) DeliveryBuildAttemptByID(context.Context, string) (DeliveryBuildAttempt, error) {
	panic("writer path reached")
}
func (*lifecycleStore) TransitionBuildAttempt(context.Context, string, int64, DeliveryBuildAttemptStatus, time.Time) (DeliveryBuildAttempt, error) {
	panic("writer path reached")
}
func (*lifecycleStore) MarkBuildFailed(context.Context, string, int64, string, time.Time) (DeliveryBuildAttempt, error) {
	panic("writer path reached")
}

func lifecycleDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func TestDeliveryLifecyclePreviewIsReadOnly(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	store := &lifecycleStore{}
	lifecycle := &DeliveryLifecycle{Targets: lifecycleTarget{state: DeliveryTarget{TargetID: "target", ProjectID: "project", Environment: "prod", TargetRevision: 3}}, Store: store, Now: func() time.Time { return now }}
	d := lifecycleDigest
	plan, err := lifecycle.Preview(t.Context(), DeliveryPlanRequest{ID: "plan-preview", TargetID: "target", ProjectID: "project", Environment: "prod", Operation: DeliveryOperationCodeChange, SourceDigest: d('a'), Execution: DeliveryExecutionInputs{SourceArtifactDigest: d('a'), CompilerDigest: d('b'), ExecutableDigest: d('c'), DependencyDigest: d('d'), ConfigDigest: d('e'), BindingDigest: d('f'), RuntimeDigest: d('0'), CapabilityDigest: d('1')}, Provenance: DeliveryProvenance{Builder: "test"}, Governance: DeliveryGovernance{PolicyDigest: d('2'), AuthorizationDigest: d('3'), QualificationDigest: d('4'), ExpiresAt: now.Add(time.Hour), ObservedInputsAllowed: true}, Evidence: DeliveryPlanEvidence{ImpactStatement: "no graph impact", PhysicalWorkStatement: "no physical work", ReuseStatement: "no reuse", Qualification: DeliveryQualificationEvidence{Policy: "protected", Steps: []DeliveryQualificationStep{{ID: "contracts", Kind: "contract", Description: "run contracts", Required: true, Blocking: true}}}, StalePolicy: DeliveryStalePolicy{Mode: "reject"}, Rollback: DeliveryRollbackEvidence{Class: DeliveryRollbackSafe}}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.BaseTargetRevision != 3 || plan.BaseGenerationID != "" {
		t.Fatalf("plan base = %q/%d", plan.BaseGenerationID, plan.BaseTargetRevision)
	}
	if store.created != 0 {
		t.Fatalf("preview persisted %d plans", store.created)
	}
}

type lifecycleSequencedTarget struct {
	states []DeliveryTarget
	reads  int
}

func (t *lifecycleSequencedTarget) ResolveDeliveryTarget(context.Context, string) (DeliveryTarget, error) {
	if len(t.states) == 0 {
		return DeliveryTarget{}, errors.New("no target state")
	}
	index := t.reads
	if index >= len(t.states) {
		index = len(t.states) - 1
	}
	t.reads++
	return t.states[index], nil
}

func TestDeliveryLifecycleRejectsStaleBeforePhysicalWork(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	plan := testLifecycleBuildPlan(t, now)
	store := &lifecycleBuildStore{plan: plan}
	target := &lifecycleSequencedTarget{states: []DeliveryTarget{
		{TargetID: "target", ProjectID: "project", Environment: "prod", TargetRevision: plan.BaseTargetRevision},
		{TargetID: "target", ProjectID: "project", Environment: "prod", TargetRevision: plan.BaseTargetRevision + 1},
	}}
	physicalCalls, artifactCalls := 0, 0
	lifecycle := &DeliveryLifecycle{Targets: target, Store: store, Now: func() time.Time { return now }}
	_, err := lifecycle.Build(t.Context(), DeliveryBuildRequest{
		PlanID: plan.ID, AttemptID: "attempt-stale-before-work", WriterLeaseID: "writer-stale-before-work", CandidateID: "candidate-stale-before-work", SealID: "seal-stale-before-work",
		ServingArtifactID: "artifact-stale-before-work", ServingArtifactDigest: lifecycleDigest('e'), ServingStateID: "state-stale-before-work", PhysicalPoolID: "pool-stale-before-work", OwnerID: "owner", Epoch: 1, LeaseLifetime: time.Hour, CreatedAt: now,
		PrepareArtifacts: func(context.Context, DeliveryBuildInput) (DeliveryArtifactIdentity, error) {
			artifactCalls++
			return DeliveryArtifactIdentity{ServingArtifactID: "artifact-stale-before-work", ServingArtifactDigest: lifecycleDigest('e'), ServingStateID: "state-stale-before-work"}, nil
		},
		Runner: func(context.Context, DeliveryBuildInput) (DeliveryBuildOutput, error) {
			physicalCalls++
			return DeliveryBuildOutput{}, errors.New("physical work must not start")
		},
	})
	if !errors.Is(err, ErrDeliveryStale) {
		t.Fatalf("stale build error = %v, want ErrDeliveryStale", err)
	}
	if target.reads != 2 || artifactCalls != 0 || physicalCalls != 0 {
		t.Fatalf("target reads=%d artifact calls=%d physical calls=%d", target.reads, artifactCalls, physicalCalls)
	}
	if !store.failed || !store.leaseReleased {
		t.Fatalf("stale attempt terminality = failed:%v released:%v", store.failed, store.leaseReleased)
	}
}

type lifecycleBuildStore struct {
	plan            DeliveryPlan
	attempt         DeliveryBuildAttempt
	preexisting     DeliveryBuildAttempt
	lease           DeliveryWriterLease
	failTransition  DeliveryBuildAttemptStatus
	failed          bool
	leaseReleased   bool
	transitionCount int
	failedGate      *release.GateEvidence
}

func (s *lifecycleBuildStore) CreatePlan(context.Context, DeliveryPlan) (DeliveryPlan, error) {
	return s.plan, nil
}
func (s *lifecycleBuildStore) PlanByID(context.Context, string) (DeliveryPlan, error) {
	return s.plan, nil
}
func (s *lifecycleBuildStore) CreateWriterLeaseAndBuildAttempt(_ context.Context, lease DeliveryWriterLease, attempt DeliveryBuildAttempt) (DeliveryWriterLease, DeliveryBuildAttempt, error) {
	s.lease = lease
	if s.preexisting.ID != "" {
		s.attempt = s.preexisting
		return lease, s.preexisting, nil
	}
	s.attempt = attempt
	return lease, attempt, nil
}
func (s *lifecycleBuildStore) DeliveryBuildAttemptByID(context.Context, string) (DeliveryBuildAttempt, error) {
	return s.attempt, nil
}
func (s *lifecycleBuildStore) TransitionBuildAttempt(_ context.Context, _ string, expected int64, next DeliveryBuildAttemptStatus, now time.Time) (DeliveryBuildAttempt, error) {
	s.transitionCount++
	if s.failTransition == next {
		return DeliveryBuildAttempt{}, fmt.Errorf("injected %s transition failure", next)
	}
	if s.attempt.Revision != expected {
		return DeliveryBuildAttempt{}, ErrDeliveryConflict
	}
	updated, err := s.attempt.Transition(next, now)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	s.attempt = updated
	return updated, nil
}
func (s *lifecycleBuildStore) MarkBuildFailed(_ context.Context, _ string, expected int64, code string, now time.Time) (DeliveryBuildAttempt, error) {
	if s.attempt.Revision != expected {
		return DeliveryBuildAttempt{}, ErrDeliveryConflict
	}
	updated, err := s.attempt.MarkFailed(code, now)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	s.failed, s.attempt = true, updated
	return updated, nil
}
func (s *lifecycleBuildStore) MarkBuildFailedAndReleaseLease(_ context.Context, _ string, expected int64, lease DeliveryWriterLease, code string, now time.Time) (DeliveryBuildAttempt, error) {
	updated, err := s.MarkBuildFailed(context.Background(), s.attempt.ID, expected, code, now)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if lease.ID != s.lease.ID {
		return DeliveryBuildAttempt{}, ErrDeliveryConflict
	}
	s.leaseReleased = true
	return updated, nil
}
func (s *lifecycleBuildStore) BindDeliveryBuildSnapshot(_ context.Context, id string, expected, snapshotID int64, now time.Time) (DeliveryBuildAttempt, error) {
	if id != s.attempt.ID || expected != s.attempt.Revision || snapshotID <= 0 {
		return DeliveryBuildAttempt{}, ErrDeliveryConflict
	}
	if s.attempt.QualifiedSnapshotID != 0 && s.attempt.QualifiedSnapshotID != snapshotID {
		return DeliveryBuildAttempt{}, ErrDeliveryConflict
	}
	if s.attempt.QualifiedSnapshotID == 0 && s.attempt.Status != DeliveryBuildValidating {
		return DeliveryBuildAttempt{}, ErrDeliveryConflict
	}
	if s.attempt.QualifiedSnapshotID == 0 {
		s.attempt.QualifiedSnapshotID = snapshotID
		s.attempt.Revision++
		s.attempt.UpdatedAt = now
	}
	return s.attempt, nil
}

func TestDeliveryLifecycleRunnerBindsSnapshotAfterValidatingTransition(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	plan := testLifecycleBuildPlan(t, now)
	store := &lifecycleBuildStore{plan: plan}
	file, err := os.CreateTemp(t.TempDir(), "detached-*.ducklake")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("runner-catalog"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	objects := &lifecycleObjectStore{}
	seals := &lifecycleSealRepository{onDone: func(identity catalogseal.SealIdentity) {
		store.attempt.Status = DeliveryBuildSealed
		store.attempt.SealID = identity.SealID
		store.attempt.CandidateID = identity.Candidate.ID
		store.attempt.TerminalAt = now
		store.attempt.Revision++
		store.attempt.UpdatedAt = now
	}}
	lifecycle := &DeliveryLifecycle{Targets: lifecycleTarget{state: DeliveryTarget{TargetID: "target", ProjectID: "project", Environment: "prod"}}, Store: store, Now: func() time.Time { return now }}
	result, err := lifecycle.Build(t.Context(), DeliveryBuildRequest{
		PlanID: plan.ID, AttemptID: "attempt-runner-snapshot", WriterLeaseID: "writer-runner-snapshot", CandidateID: "candidate-runner-snapshot", SealID: "seal-runner-snapshot",
		ServingArtifactID: "artifact-runner-snapshot", ServingArtifactDigest: lifecycleDigest('e'), ServingStateID: "state-runner-snapshot", PhysicalPoolID: "pool-build", OwnerID: "owner-build", Epoch: 1, LeaseLifetime: time.Hour, CreatedAt: now,
		Runner: func(context.Context, DeliveryBuildInput) (DeliveryBuildOutput, error) {
			return DeliveryBuildOutput{Catalog: catalogseal.FileCatalog{Path: file.Name()}, SnapshotID: 42, QualificationDigest: lifecycleDigest('4'), ClosureDigest: lifecycleDigest('5'), CompatibilityDigest: lifecycleDigest('6'), GateEvidence: lifecycleGateEvidence(t, "candidate-runner-snapshot", plan.SourceDigest, now), ResolvedInputs: DeliveryResolvedBuildInputs{PolicyDigest: plan.Governance.PolicyDigest}, ObjectStore: objects, SealRepository: seals, RemoteVerifier: lifecycleRemoteVerifier{}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SnapshotID != 42 || result.Attempt.QualifiedSnapshotID != 42 {
		t.Fatalf("runner snapshot result = %#v", result)
	}
}
func (s *lifecycleBuildStore) RecordFailedBuildGateEvidence(_ context.Context, _ string, evidence *release.GateEvidence) error {
	if evidence == nil {
		return ErrDeliveryInvalid
	}
	copy := *evidence
	s.failedGate = &copy
	return nil
}
func (s *lifecycleBuildStore) TransitionWriterLease(_ context.Context, id string, expected, next DeliveryLeaseStatus, now time.Time) (DeliveryWriterLease, error) {
	if id != s.lease.ID || s.lease.Status != expected || next != DeliveryLeaseReleased {
		return DeliveryWriterLease{}, ErrDeliveryConflict
	}
	updated, err := s.lease.Release(now)
	if err != nil {
		return DeliveryWriterLease{}, err
	}
	s.lease = updated
	s.leaseReleased = true
	return updated, nil
}

type lifecycleClosablePhase struct{ closed *bool }

func (h lifecycleClosablePhase) Close() error {
	*h.closed = true
	return nil
}

type lifecycleFaultPhase struct {
	stage  string
	closed *bool
}

type lifecycleGateFailurePhase struct {
	evidence *release.GateEvidence
}

type lifecycleCanonicalGatePhase struct {
	evidence *release.GateEvidence
}

func (lifecycleGateFailurePhase) Construct(context.Context, DeliveryBuildInput) (any, error) {
	return lifecycleGateFailurePhase{}, nil
}
func (lifecycleGateFailurePhase) Normalize(context.Context, DeliveryBuildInput, any) error {
	return nil
}
func (p lifecycleGateFailurePhase) Qualify(context.Context, DeliveryBuildInput, any) (DeliveryBuildOutput, error) {
	return DeliveryBuildOutput{GateEvidence: p.evidence}, errors.New("candidate gate blocked")
}
func (lifecycleGateFailurePhase) Close() error { return nil }

func (p lifecycleCanonicalGatePhase) Construct(context.Context, DeliveryBuildInput) (any, error) {
	return p, nil
}
func (lifecycleCanonicalGatePhase) Normalize(context.Context, DeliveryBuildInput, any) error {
	return nil
}
func (p lifecycleCanonicalGatePhase) Qualify(context.Context, DeliveryBuildInput, any) (DeliveryBuildOutput, error) {
	// Injected canonical evidence must still be rejected by the delivery
	// boundary; this phase intentionally returns no evaluator error.
	return DeliveryBuildOutput{GateEvidence: p.evidence}, nil
}
func (lifecycleCanonicalGatePhase) Close() error { return nil }

type lifecycleSuccessPhase struct {
	path   string
	closed *bool
	output DeliveryBuildOutput
}

func (r lifecycleSuccessPhase) Construct(context.Context, DeliveryBuildInput) (any, error) {
	return r, nil
}
func (r lifecycleSuccessPhase) Normalize(context.Context, DeliveryBuildInput, any) error { return nil }
func (r lifecycleSuccessPhase) Qualify(context.Context, DeliveryBuildInput, any) (DeliveryBuildOutput, error) {
	return r.output, nil
}
func (r lifecycleSuccessPhase) Close() error {
	if r.closed != nil {
		*r.closed = true
	}
	return nil
}

type lifecycleObjectStore struct {
	body     []byte
	metadata catalogseal.ObjectMetadata
}

func (s *lifecycleObjectStore) Create(_ context.Context, _ string, reader io.Reader, metadata catalogseal.ObjectMetadata) error {
	body, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.body, s.metadata = body, metadata
	return nil
}
func (s *lifecycleObjectStore) Open(_ context.Context, _ string) (catalogseal.Object, error) {
	return catalogseal.Object{Body: io.NopCloser(bytes.NewReader(s.body)), Size: int64(len(s.body)), Metadata: s.metadata}, nil
}

type lifecycleSealRepository struct {
	record catalogseal.SealRecord
	onDone func(catalogseal.SealIdentity)
}

func (s *lifecycleSealRepository) Lookup(context.Context, string) (catalogseal.SealRecord, error) {
	if s.record.Identity.SealID == "" {
		return catalogseal.SealRecord{}, catalogseal.ErrSealNotFound
	}
	return s.record, nil
}
func (s *lifecycleSealRepository) Prepare(_ context.Context, identity catalogseal.SealIdentity) (catalogseal.SealRecord, error) {
	s.record = catalogseal.SealRecord{Identity: identity, Status: catalogseal.SealPreparing}
	return s.record, nil
}
func (s *lifecycleSealRepository) MarkUploaded(context.Context, string) (catalogseal.SealRecord, error) {
	s.record.Status = catalogseal.SealUploaded
	return s.record, nil
}
func (s *lifecycleSealRepository) CompleteVerified(_ context.Context, _ catalogseal.CompleteInput) (catalogseal.Completion, error) {
	s.record.Status = catalogseal.SealVerified
	if s.onDone != nil {
		s.onDone(s.record.Identity)
	}
	return catalogseal.Completion{Seal: s.record, CandidateID: s.record.Identity.Candidate.ID, LeaseReleased: true}, nil
}

func (r lifecycleFaultPhase) Construct(context.Context, DeliveryBuildInput) (any, error) {
	if r.stage == "construct" {
		return nil, errors.New("injected construct failure")
	}
	return lifecycleClosablePhase{closed: r.closed}, nil
}
func (r lifecycleFaultPhase) Normalize(context.Context, DeliveryBuildInput, any) error {
	if r.stage == "normalize" {
		return errors.New("injected normalize failure")
	}
	return nil
}
func (r lifecycleFaultPhase) Qualify(context.Context, DeliveryBuildInput, any) (DeliveryBuildOutput, error) {
	if r.stage == "qualify" {
		return DeliveryBuildOutput{}, errors.New("injected qualify failure")
	}
	if r.stage == "incomplete" {
		return DeliveryBuildOutput{}, nil
	}
	return DeliveryBuildOutput{}, errors.New("qualification should not be reached")
}

func testLifecycleBuildPlan(t *testing.T, now time.Time) DeliveryPlan {
	t.Helper()
	d := lifecycleDigest
	projectID, err := projectgraph.NewResourceID("project")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewDeliveryPlan(DeliveryPlan{ID: "plan-build", TargetID: "target", ProjectID: projectID, Environment: "prod", Operation: DeliveryOperationCodeChange, SourceDigest: d('a'), Execution: DeliveryExecutionInputs{SourceArtifactDigest: d('a'), CompilerDigest: d('b'), ExecutableDigest: d('c'), DependencyDigest: d('d'), ConfigDigest: d('e'), BindingDigest: d('f'), RuntimeDigest: d('0'), CapabilityDigest: d('1')}, Provenance: DeliveryProvenance{Builder: "test"}, Governance: DeliveryGovernance{PolicyDigest: d('2'), AuthorizationDigest: d('3'), QualificationDigest: d('4'), ExpiresAt: now.Add(time.Hour)}, Evidence: DeliveryPlanEvidence{ImpactStatement: "test impact", PhysicalWorkStatement: "test work", ReuseStatement: "test reuse", Qualification: DeliveryQualificationEvidence{Policy: "test policy", Steps: []DeliveryQualificationStep{{ID: "contracts", Kind: "contract", Description: "run contracts", Required: true, Blocking: true}}}, StalePolicy: DeliveryStalePolicy{Mode: "reject"}, Rollback: DeliveryRollbackEvidence{Class: DeliveryRollbackSafe}}, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestDeliveryLifecycleAllowRetainedBaseRequiresExactSealedIdentity(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	base := testLifecycleBuildPlan(t, now)
	base.BaseGenerationID, base.BaseTargetRevision = "generation-base", 1
	base.Evidence.StalePolicy = DeliveryStalePolicy{Mode: "allow_retained_base", AllowRetainedBase: true, Description: "qualify against retained sealed base"}
	base.ExecutionDigest, _ = base.Execution.ExecutionDigest()
	base.GovernanceDigest, _ = canonicalJSONDigest(base.Governance)
	base.EvidenceDigest, _ = base.Evidence.Digest()
	base.Digest = ""
	base, err := NewDeliveryPlan(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name          string
		catalogDigest string
		poolID        string
		wantStale     bool
	}{
		{name: "exact retained base", catalogDigest: lifecycleDigest('6'), poolID: "pool-build"},
		{name: "missing retained base", wantStale: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &lifecycleBuildStore{plan: base}
			target := &lifecycleSequencedTarget{states: []DeliveryTarget{
				{TargetID: "target", ProjectID: "project", Environment: "prod", ActiveGenerationID: "generation-base", TargetRevision: 1},
				{TargetID: "target", ProjectID: "project", Environment: "prod", ActiveGenerationID: "generation-new", TargetRevision: 2},
			}}
			constructed := false
			runner := lifecycleConstructProbe{called: &constructed}
			lifecycle := &DeliveryLifecycle{Targets: target, Store: store, Now: func() time.Time { return now }}
			_, buildErr := lifecycle.Build(t.Context(), DeliveryBuildRequest{PlanID: base.ID, AttemptID: "attempt-retained-base", WriterLeaseID: "writer-retained-base", CandidateID: "candidate-retained-base", SealID: "seal-retained-base", ServingArtifactID: "artifact-retained-base", ServingArtifactDigest: lifecycleDigest('e'), ServingStateID: "state-retained-base", PhysicalPoolID: "pool-build", OwnerID: "owner-build", Epoch: 1, LeaseLifetime: time.Hour, CreatedAt: now, BaseCatalogDigest: test.catalogDigest, BasePhysicalPoolID: test.poolID, PhasedRunner: runner})
			if test.wantStale {
				if buildErr == nil || constructed {
					t.Fatalf("missing retained base err=%v constructed=%v", buildErr, constructed)
				}
				return
			}
			if buildErr == nil || errors.Is(buildErr, ErrDeliveryStale) || !strings.Contains(buildErr.Error(), "injected construct failure") {
				t.Fatalf("exact retained base err=%v constructed=%v", buildErr, constructed)
			}
		})
	}
}

type lifecycleConstructProbe struct{ called *bool }

func (r lifecycleConstructProbe) Construct(context.Context, DeliveryBuildInput) (any, error) {
	*r.called = true
	return nil, errors.New("injected construct failure")
}
func (lifecycleConstructProbe) Normalize(context.Context, DeliveryBuildInput, any) error {
	return errors.New("normalize should not run")
}
func (lifecycleConstructProbe) Qualify(context.Context, DeliveryBuildInput, any) (DeliveryBuildOutput, error) {
	return DeliveryBuildOutput{}, errors.New("qualify should not run")
}

func TestDeliveryLifecycleClosesPhasedCatalogOnEveryPreSealFailure(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name          string
		phaseFailure  string
		transitionErr DeliveryBuildAttemptStatus
	}{
		{name: "construct", phaseFailure: "construct"},
		{name: "normalizing transition", transitionErr: DeliveryBuildNormalizing},
		{name: "normalize", phaseFailure: "normalize"},
		{name: "validating transition", transitionErr: DeliveryBuildValidating},
		{name: "qualify", phaseFailure: "qualify"},
		{name: "qualification incomplete", phaseFailure: "incomplete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := testLifecycleBuildPlan(t, now)
			store := &lifecycleBuildStore{plan: plan, failTransition: test.transitionErr}
			closed := false
			runner := lifecycleFaultPhase{stage: test.phaseFailure, closed: &closed}
			lifecycle := &DeliveryLifecycle{Targets: lifecycleTarget{state: DeliveryTarget{TargetID: "target", ProjectID: "project", Environment: "prod"}}, Store: store, Now: func() time.Time { return now }}
			_, err := lifecycle.Build(t.Context(), DeliveryBuildRequest{PlanID: plan.ID, AttemptID: "attempt-build", WriterLeaseID: "writer-build", CandidateID: "candidate-build", SealID: "seal-build", ServingArtifactID: "artifact-build", ServingArtifactDigest: lifecycleDigest('e'), ServingStateID: "state-build", PhysicalPoolID: "pool-build", OwnerID: "owner-build", Epoch: 1, LeaseLifetime: time.Hour, CreatedAt: now, PhasedRunner: runner})
			if err == nil {
				t.Fatal("fault-injected build unexpectedly succeeded")
			}
			if test.name != "construct" && !closed {
				t.Fatal("working catalog was not closed after pre-seal failure")
			}
			if !store.failed || !store.leaseReleased {
				t.Fatalf("failure terminality = failed:%v leaseReleased:%v", store.failed, store.leaseReleased)
			}
		})
	}
}

func TestDeliveryLifecycleReconcilesDurablePhasesAfterRestart(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	plan := testLifecycleBuildPlan(t, now)
	for _, phase := range []DeliveryBuildAttemptStatus{DeliveryBuildNormalizing, DeliveryBuildValidating, DeliveryBuildSealing} {
		t.Run(string(phase), func(t *testing.T) {
			store := &lifecycleBuildStore{plan: plan, preexisting: DeliveryBuildAttempt{ID: "attempt-restart", PlanID: plan.ID, PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: "pool-build", WriterLeaseID: "writer-build", Status: phase, Revision: 2, CreatedAt: now, UpdatedAt: now}}
			closed := false
			runner := lifecycleFaultPhase{stage: "incomplete", closed: &closed}
			lifecycle := &DeliveryLifecycle{Targets: lifecycleTarget{state: DeliveryTarget{TargetID: "target", ProjectID: "project", Environment: "prod"}}, Store: store, Now: func() time.Time { return now }}
			_, err := lifecycle.Build(t.Context(), DeliveryBuildRequest{PlanID: plan.ID, AttemptID: "attempt-restart", WriterLeaseID: "writer-build", CandidateID: "candidate-build", SealID: "seal-build", ServingArtifactID: "artifact-build", ServingArtifactDigest: lifecycleDigest('e'), ServingStateID: "state-build", PhysicalPoolID: "pool-build", OwnerID: "owner-build", Epoch: 1, LeaseLifetime: time.Hour, CreatedAt: now, PhasedRunner: runner})
			if err == nil || strings.Contains(err.Error(), "build attempt is") {
				t.Fatalf("phase %s was not reconciled: %v", phase, err)
			}
			if !closed || !store.failed || !store.leaseReleased {
				t.Fatalf("phase %s cleanup/failure = closed:%v failed:%v released:%v", phase, closed, store.failed, store.leaseReleased)
			}
		})
	}
}

func TestDeliveryLifecycleReconcilesPhasesToSeal(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	plan := testLifecycleBuildPlan(t, now)
	for _, phase := range []DeliveryBuildAttemptStatus{DeliveryBuildNormalizing, DeliveryBuildValidating, DeliveryBuildSealing} {
		t.Run(string(phase), func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "detached-*.ducklake")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString("restartable-catalog"); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			qualifiedSnapshotID := int64(0)
			if phase == DeliveryBuildSealing {
				qualifiedSnapshotID = 42
			}
			store := &lifecycleBuildStore{plan: plan, preexisting: DeliveryBuildAttempt{ID: "attempt-restart-success", PlanID: plan.ID, PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: "pool-build", WriterLeaseID: "writer-build", Status: phase, Revision: 2, QualifiedSnapshotID: qualifiedSnapshotID, CreatedAt: now, UpdatedAt: now}}
			objects := &lifecycleObjectStore{}
			seals := &lifecycleSealRepository{onDone: func(identity catalogseal.SealIdentity) {
				if store.attempt.QualifiedSnapshotID != 42 {
					t.Errorf("catalog sealed before snapshot binding: got %d, want 42", store.attempt.QualifiedSnapshotID)
				}
				store.attempt.Status = DeliveryBuildSealed
				store.attempt.SealID = identity.SealID
				store.attempt.CandidateID = identity.Candidate.ID
				store.attempt.TerminalAt = now
				store.attempt.Revision++
				store.attempt.UpdatedAt = now
			}}
			closed := false
			runner := lifecycleSuccessPhase{closed: &closed, output: DeliveryBuildOutput{Catalog: catalogseal.FileCatalog{Path: file.Name()}, SnapshotID: 42, QualificationDigest: lifecycleDigest('4'), ClosureDigest: lifecycleDigest('5'), CompatibilityDigest: lifecycleDigest('6'), GateEvidence: lifecycleGateEvidence(t, "candidate-restart-success", plan.SourceDigest, now), ResolvedInputs: DeliveryResolvedBuildInputs{PolicyDigest: plan.Governance.PolicyDigest}, ObjectStore: objects, SealRepository: seals, RemoteVerifier: lifecycleRemoteVerifier{}}}
			lifecycle := &DeliveryLifecycle{Targets: lifecycleTarget{state: DeliveryTarget{TargetID: "target", ProjectID: "project", Environment: "prod"}}, Store: store, Now: func() time.Time { return now }}
			result, err := lifecycle.Build(t.Context(), DeliveryBuildRequest{PlanID: plan.ID, AttemptID: "attempt-restart-success", WriterLeaseID: "writer-build", CandidateID: "candidate-restart-success", SealID: "seal-restart-success", ServingArtifactID: "artifact-build", ServingArtifactDigest: lifecycleDigest('e'), ServingStateID: "state-build", PhysicalPoolID: "pool-build", OwnerID: "owner-build", Epoch: 1, LeaseLifetime: time.Hour, CreatedAt: now, PhasedRunner: runner})
			if err != nil {
				t.Fatal(err)
			}
			if result.Attempt.Status != DeliveryBuildSealed || result.SnapshotID != 42 || store.attempt.QualifiedSnapshotID != 42 || store.failed || store.leaseReleased || (phase != DeliveryBuildSealing && store.transitionCount == 0) {
				t.Fatalf("phase %s result=%#v failed=%v released=%v transitions=%d", phase, result.Attempt, store.failed, store.leaseReleased, store.transitionCount)
			}
		})
	}
}

func TestDeliveryLifecycleFailedGateEvidenceDoesNotChangeActiveGeneration(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	plan := testLifecycleBuildPlan(t, now)
	plan.BaseGenerationID, plan.BaseTargetRevision = "generation-active", 0
	plan.ExecutionDigest, _ = plan.Execution.ExecutionDigest()
	plan.GovernanceDigest, _ = canonicalJSONDigest(plan.Governance)
	plan.EvidenceDigest, _ = plan.Evidence.Digest()
	plan.Digest = ""
	plan, err := NewDeliveryPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	activeTarget := &lifecycleSequencedTarget{states: []DeliveryTarget{{TargetID: "target", ProjectID: "project", Environment: "prod", ActiveGenerationID: "generation-active", TargetRevision: 0}}}
	store := &lifecycleBuildStore{plan: plan}
	evidence, err := (release.GateEvidence{Version: 1, CandidateID: "candidate-gate-failed", SourceDigest: plan.SourceDigest, BindingGeneration: release.BindingFingerprint(nil), RuntimeVersion: "runtime:test", DuckDBVersion: "duckdb:test", Outcome: release.GateUnavailable, EvaluatedAt: now, Bounds: release.GateBounds{MaxRows: 100, MaxQueries: 10, MaxMillis: 100}, Sources: []release.GateSourceEvidence{{ID: "source-1", Mode: "inferred", SourceDigest: plan.SourceDigest, SchemaOutcome: release.GateUnavailable}}}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &DeliveryLifecycle{Targets: activeTarget, Store: store, Now: func() time.Time { return now }}
	_, err = lifecycle.Build(t.Context(), DeliveryBuildRequest{PlanID: plan.ID, AttemptID: "attempt-gate-failed", WriterLeaseID: "writer-gate-failed", CandidateID: "candidate-gate-failed", SealID: "seal-gate-failed", ServingArtifactID: "artifact-gate-failed", ServingArtifactDigest: lifecycleDigest('e'), ServingStateID: "state-gate-failed", PhysicalPoolID: "pool-build", BaseCatalogDigest: lifecycleDigest('6'), BasePhysicalPoolID: "pool-build", OwnerID: "owner-build", Epoch: 1, LeaseLifetime: time.Hour, CreatedAt: now, PhasedRunner: lifecycleGateFailurePhase{evidence: &evidence}})
	if err == nil || store.failedGate == nil {
		t.Fatalf("failed gate build err=%v evidence=%#v", err, store.failedGate)
	}
	if store.failedGate.Digest != evidence.Digest || store.attempt.Status != DeliveryBuildFailed || store.leaseReleased == false {
		t.Fatalf("failed gate terminal evidence=%#v attempt=%#v released=%v", store.failedGate, store.attempt, store.leaseReleased)
	}
	if activeTarget.states[0].ActiveGenerationID != "generation-active" || activeTarget.states[0].TargetRevision != 0 {
		t.Fatalf("active generation changed after failed gate: %#v", activeTarget.states[0])
	}
}

func TestDeliveryLifecycleRejectsInjectedCanonicalFailedEvidence(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	plan := testLifecycleBuildPlan(t, now)
	plan.BaseGenerationID, plan.BaseTargetRevision = "generation-active", 0
	plan.ExecutionDigest, _ = plan.Execution.ExecutionDigest()
	plan.GovernanceDigest, _ = canonicalJSONDigest(plan.Governance)
	plan.EvidenceDigest, _ = plan.Evidence.Digest()
	plan.Digest = ""
	plan, err := NewDeliveryPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	activeTarget := &lifecycleSequencedTarget{states: []DeliveryTarget{{TargetID: "target", ProjectID: "project", Environment: "prod", ActiveGenerationID: "generation-active", TargetRevision: 0}}}
	for _, outcome := range []release.GateOutcome{release.GateUnavailable, release.GateTimeout} {
		t.Run(string(outcome), func(t *testing.T) {
			store := &lifecycleBuildStore{plan: plan}
			failure := release.GateObservationFailureUnavailable
			if outcome == release.GateTimeout {
				failure = release.GateObservationFailureTimeout
			}
			evidence, err := (release.GateEvidence{Version: 1, CandidateID: "candidate-injected-" + string(outcome), SourceDigest: plan.SourceDigest, BindingGeneration: release.BindingFingerprint(nil), RuntimeVersion: "runtime:test", DuckDBVersion: "duckdb:test", Outcome: outcome, EvaluatedAt: now, Bounds: release.GateBounds{MaxRows: 100, MaxQueries: 10, MaxMillis: 100}, Sources: []release.GateSourceEvidence{{ID: "source-1", Mode: "inferred", SourceDigest: plan.SourceDigest, SchemaOutcome: outcome, SchemaFailure: failure}}}).Canonical()
			if err != nil {
				t.Fatal(err)
			}
			lifecycle := &DeliveryLifecycle{Targets: activeTarget, Store: store, Now: func() time.Time { return now }}
			_, err = lifecycle.Build(t.Context(), DeliveryBuildRequest{PlanID: plan.ID, AttemptID: "attempt-injected-" + string(outcome), WriterLeaseID: "writer-injected-" + string(outcome), CandidateID: evidence.CandidateID, SealID: "seal-injected-" + string(outcome), ServingArtifactID: "artifact-injected-" + string(outcome), ServingArtifactDigest: lifecycleDigest('e'), ServingStateID: "state-injected-" + string(outcome), PhysicalPoolID: "pool-build", BaseCatalogDigest: lifecycleDigest('6'), BasePhysicalPoolID: "pool-build", OwnerID: "owner-build", Epoch: 1, LeaseLifetime: time.Hour, CreatedAt: now, PhasedRunner: lifecycleCanonicalGatePhase{evidence: &evidence}})
			if err == nil || store.failedGate == nil || store.attempt.Status != DeliveryBuildFailed {
				t.Fatalf("injected %s build err=%v failedGate=%#v attempt=%#v", outcome, err, store.failedGate, store.attempt)
			}
			if activeTarget.states[0].ActiveGenerationID != "generation-active" || activeTarget.states[0].TargetRevision != 0 {
				t.Fatalf("active generation changed after injected %s: %#v", outcome, activeTarget.states[0])
			}
		})
	}
}

type lifecycleRemoteVerifier struct{}

func (lifecycleRemoteVerifier) Verify(context.Context, catalogseal.RemoteVerification) error {
	return nil
}
