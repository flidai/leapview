package deployment

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

func deliveryTestDigest(char byte) string {
	return "sha256:" + strings.Repeat(string(char), 64)
}

func TestResolveDeliveryReuseDecisionRequiresExactCandidateOrAggregatesRelations(t *testing.T) {
	plan := &DeliveryPlan{Evidence: DeliveryPlanEvidence{Reuse: []DeliveryReuseDecision{{ResourceID: "model:orders", Reusable: true}}}}
	if _, ok := ResolveDeliveryReuseDecision(plan, "candidate-1"); ok {
		t.Fatal("single relation decision was accepted as candidate evidence")
	}
	plan.Evidence.Reuse = append(plan.Evidence.Reuse, DeliveryReuseDecision{ResourceID: "model:customers", Reusable: false, RetainBase: true})
	decision, ok := ResolveDeliveryReuseDecision(plan, "candidate-1")
	if !ok || decision.Reusable || !decision.RetainBase {
		t.Fatalf("partial relation decisions = %#v, found=%v", decision, ok)
	}
}

func deliveryTestPlan(t *testing.T) DeliveryPlan {
	t.Helper()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	plan, err := NewDeliveryPlan(DeliveryPlan{
		ID: "plan-1", TargetID: "target-1", ProjectID: graph.ResourceID("project-1"), Environment: "prod",
		Operation: DeliveryOperationCodeChange, SourceDigest: deliveryTestDigest('a'),
		BaseGenerationID: "generation-1", BaseTargetRevision: 7,
		Execution: DeliveryExecutionInputs{
			SourceArtifactDigest: deliveryTestDigest('a'), CompilerDigest: deliveryTestDigest('b'),
			ExecutableDigest: deliveryTestDigest('c'), DependencyDigest: deliveryTestDigest('d'),
			ConfigDigest: deliveryTestDigest('e'), BindingDigest: deliveryTestDigest('f'),
			RuntimeDigest: deliveryTestDigest('0'), CapabilityDigest: deliveryTestDigest('1'),
		},
		Provenance: DeliveryProvenance{Repository: "https://example.invalid/repo", SourceRevision: "rev-1", Builder: "ci"},
		Governance: DeliveryGovernance{
			PolicyDigest: deliveryTestDigest('2'), AuthorizationDigest: deliveryTestDigest('3'),
			QualificationDigest: deliveryTestDigest('4'), ExpiresAt: now.Add(time.Hour), RequiresApproval: true, ApprovalPolicyRevision: 1,
		},
		Evidence: DeliveryPlanEvidence{
			ImpactStatement:       "direct model change with downstream dashboard impact",
			PhysicalWorkStatement: "materialize affected semantic relations",
			ReuseStatement:        "reuse unchanged dimensions by execution digest",
			Qualification:         DeliveryQualificationEvidence{Policy: "protected", Steps: []DeliveryQualificationStep{{ID: "contracts", Kind: "contract", Description: "run graph contracts", Required: true, Blocking: true}}},
			StalePolicy:           DeliveryStalePolicy{Mode: "reject"}, Rollback: DeliveryRollbackEvidence{Class: DeliveryRollbackSafe},
		},
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("new plan: %v", err)
	}
	return plan
}

func TestNewDeliveryPlanDefaultsSourceOwnerBeforeDigest(t *testing.T) {
	plan := deliveryTestPlan(t)
	if plan.SourceOwnerID != plan.Provenance.Builder {
		t.Fatalf("source owner = %q, want provenance builder %q", plan.SourceOwnerID, plan.Provenance.Builder)
	}

	replay := plan
	replay.SourceOwnerID = ""
	replay.Digest = ""
	replay, err := NewDeliveryPlan(replay)
	if err != nil {
		t.Fatal(err)
	}
	if replay.SourceOwnerID != plan.SourceOwnerID || replay.Digest != plan.Digest {
		t.Fatalf("defaulted replay identity = owner %q digest %q, want owner %q digest %q", replay.SourceOwnerID, replay.Digest, plan.SourceOwnerID, plan.Digest)
	}
}

func deliveryTestGateEvidence(t *testing.T, plan DeliveryPlan, outcome release.GateOutcome) *release.GateEvidence {
	t.Helper()
	raw := release.GateEvidence{Version: 1, CandidateID: "candidate-1", SourceDigest: plan.SourceDigest, BindingGeneration: deliveryTestDigest('c'), RuntimeVersion: "runtime-1", DuckDBVersion: "duckdb-1", Outcome: outcome, EvaluatedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC), Bounds: release.GateBounds{MaxRows: 10, MaxQueries: 2, MaxMillis: 100}}
	if outcome != release.GateSuccess {
		source := release.GateSourceEvidence{ID: "source-1", Mode: "inferred", SourceDigest: plan.SourceDigest, SchemaOutcome: outcome}
		if outcome == release.GateUnavailable {
			source.SchemaFailure = release.GateObservationFailureUnavailable
		} else if outcome == release.GateTimeout {
			source.SchemaFailure = release.GateObservationFailureTimeout
		}
		raw.Sources = []release.GateSourceEvidence{source}
	}
	evidence, err := raw.Canonical()
	if err != nil {
		t.Fatalf("canonical gate evidence: %v", err)
	}
	return &evidence
}

func TestDeliveryPlanSeparatesExecutionFromProvenance(t *testing.T) {
	plan := deliveryTestPlan(t)
	changed := plan
	changed.Provenance.SourceRevision = "rev-2"
	changed.ProvenanceDigest = ""
	changed.Digest = ""
	changed, err := NewDeliveryPlan(changed)
	if err != nil {
		t.Fatalf("rebuild provenance variant: %v", err)
	}
	if changed.ExecutionDigest != plan.ExecutionDigest {
		t.Fatalf("provenance changed execution identity: %s != %s", changed.ExecutionDigest, plan.ExecutionDigest)
	}
	if changed.Digest == plan.Digest || changed.ProvenanceDigest == plan.ProvenanceDigest {
		t.Fatal("plan identity did not include provenance evidence")
	}
	executionChanged := plan
	executionChanged.Execution.BindingDigest = deliveryTestDigest('9')
	executionChanged.ExecutionDigest, _ = executionChanged.Execution.ExecutionDigest()
	executionChanged.ProvenanceDigest = ""
	executionChanged.GovernanceDigest = ""
	executionChanged.Digest = ""
	executionChanged, err = NewDeliveryPlan(executionChanged)
	if err != nil {
		t.Fatalf("rebuild execution variant: %v", err)
	}
	if executionChanged.ExecutionDigest == plan.ExecutionDigest {
		t.Fatal("result-affecting binding change preserved execution identity")
	}
}

func TestDeliveryExecutionDigestChangesForEveryResultAffectingInput(t *testing.T) {
	base := DeliveryExecutionInputs{
		SourceArtifactDigest: deliveryTestDigest('a'), CompilerDigest: deliveryTestDigest('b'),
		ExecutableDigest: deliveryTestDigest('c'), DependencyDigest: deliveryTestDigest('d'),
		ConfigDigest: deliveryTestDigest('e'), BindingDigest: deliveryTestDigest('f'),
		RuntimeDigest: deliveryTestDigest('0'), CapabilityDigest: deliveryTestDigest('1'),
		DataInputs: []DeliveryDataInput{
			{ID: "bounded-orders", Mode: DeliveryDataBounded, Bound: "2026-08-17T00:00:00Z"},
			{ID: "observed-events", Mode: DeliveryDataObserved},
			{ID: "pinned-customers", Mode: DeliveryDataPinned, Revision: "revision-1"},
		},
	}
	want, err := base.ExecutionDigest()
	if err != nil {
		t.Fatalf("base execution digest: %v", err)
	}
	mutations := map[string]func(*DeliveryExecutionInputs){
		"source artifact": func(inputs *DeliveryExecutionInputs) { inputs.SourceArtifactDigest = deliveryTestDigest('9') },
		"compiler":        func(inputs *DeliveryExecutionInputs) { inputs.CompilerDigest = deliveryTestDigest('9') },
		"executable":      func(inputs *DeliveryExecutionInputs) { inputs.ExecutableDigest = deliveryTestDigest('9') },
		"dependency":      func(inputs *DeliveryExecutionInputs) { inputs.DependencyDigest = deliveryTestDigest('9') },
		"config":          func(inputs *DeliveryExecutionInputs) { inputs.ConfigDigest = deliveryTestDigest('9') },
		"binding":         func(inputs *DeliveryExecutionInputs) { inputs.BindingDigest = deliveryTestDigest('9') },
		"runtime":         func(inputs *DeliveryExecutionInputs) { inputs.RuntimeDigest = deliveryTestDigest('9') },
		"capability":      func(inputs *DeliveryExecutionInputs) { inputs.CapabilityDigest = deliveryTestDigest('9') },
		"pinned revision": func(inputs *DeliveryExecutionInputs) {
			inputs.DataInputs[2].Revision = "revision-2"
		},
		"bounded interval": func(inputs *DeliveryExecutionInputs) {
			inputs.DataInputs[0].Bound = "2026-08-18T00:00:00Z"
		},
		"observed declaration": func(inputs *DeliveryExecutionInputs) {
			inputs.DataInputs[1].Explanation = "connector equivalence token changed"
		},
	}
	for name, mutate := range mutations {
		changed := base
		changed.DataInputs = append([]DeliveryDataInput(nil), base.DataInputs...)
		mutate(&changed)
		got, err := changed.ExecutionDigest()
		if err != nil {
			t.Fatalf("%s execution digest: %v", name, err)
		}
		if got == want {
			t.Errorf("%s mutation preserved execution digest %s", name, got)
		}
	}
}

func TestDeliveryGovernanceAndCredentialRotationPreserveExecutionReuseIdentity(t *testing.T) {
	plan := deliveryTestPlan(t)
	governance := plan
	governance.Governance.RequiresApproval = !governance.Governance.RequiresApproval
	governance.Governance.AuthorizationDigest = deliveryTestDigest('9')
	governance.GovernanceDigest, _ = canonicalJSONDigest(governance.Governance)
	governance.Digest = ""
	governance, err := NewDeliveryPlan(governance)
	if err != nil {
		t.Fatalf("governance-only plan: %v", err)
	}
	if governance.ExecutionDigest != plan.ExecutionDigest {
		t.Fatalf("governance-only change altered execution identity: %s != %s", governance.ExecutionDigest, plan.ExecutionDigest)
	}
	if governance.Digest == plan.Digest {
		t.Fatal("governance-only change did not produce a distinct plan identity")
	}

	// Secret values are deliberately absent from DeliveryExecutionInputs. A
	// provider rotation therefore changes neither the endpoint/binding digest
	// nor the physical reuse key when all binding semantics remain unchanged.
	rotatedSecret := plan
	rotatedSecret.Provenance.Builder = "credential-provider:version-2"
	rotatedSecret.ProvenanceDigest = ""
	rotatedSecret.Digest = ""
	rotatedSecret, err = NewDeliveryPlan(rotatedSecret)
	if err != nil {
		t.Fatalf("secret-rotation provenance variant: %v", err)
	}
	if rotatedSecret.ExecutionDigest != plan.ExecutionDigest {
		t.Fatalf("secret rotation altered execution identity: %s != %s", rotatedSecret.ExecutionDigest, plan.ExecutionDigest)
	}
}

func TestDeliveryReusePolicyRequiresExactPhysicalIdentity(t *testing.T) {
	base := DeliveryReuseInput{
		ResourceID: "orders", ExecutionDigest: deliveryTestDigest('a'), BaseExecutionDigest: deliveryTestDigest('a'),
		CatalogDigest: deliveryTestDigest('b'), BaseCatalogDigest: deliveryTestDigest('b'), PhysicalPoolID: "pool-1", BasePhysicalPoolID: "pool-1",
		CompatibilityDigest: deliveryTestDigest('c'), BaseCompatibilityDigest: deliveryTestDigest('c'), Deterministic: true,
	}
	decision, err := EvaluateDeliveryReuse(base)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Reusable || decision.ReuseKeyDigest == "" {
		t.Fatalf("exact reuse decision = %#v, want reusable key", decision)
	}
	for name, mutate := range map[string]func(*DeliveryReuseInput){
		"execution":     func(input *DeliveryReuseInput) { input.ExecutionDigest = deliveryTestDigest('d') },
		"catalog":       func(input *DeliveryReuseInput) { input.CatalogDigest = deliveryTestDigest('e') },
		"pool":          func(input *DeliveryReuseInput) { input.PhysicalPoolID = "pool-2" },
		"compatibility": func(input *DeliveryReuseInput) { input.CompatibilityDigest = deliveryTestDigest('f') },
	} {
		changed := base
		mutate(&changed)
		decision, err := EvaluateDeliveryReuse(changed)
		if err != nil {
			t.Fatalf("%s reuse decision: %v", name, err)
		}
		if decision.Reusable || decision.ReuseKeyDigest != "" {
			t.Errorf("%s mismatch reused physical identity: %#v", name, decision)
		}
	}
}

func TestDeliveryReuseRelationContextFailsClosedWhenIncomplete(t *testing.T) {
	base := DeliveryReuseInput{
		ResourceID: "orders", RelationScoped: true,
		ExecutionDigest: deliveryTestDigest('a'), BaseExecutionDigest: deliveryTestDigest('a'),
		CatalogDigest: deliveryTestDigest('b'), BaseCatalogDigest: deliveryTestDigest('b'), PhysicalPoolID: "pool-1", BasePhysicalPoolID: "pool-1",
		CompatibilityDigest: deliveryTestDigest('c'), BaseCompatibilityDigest: deliveryTestDigest('c'), Deterministic: true,
	}
	if _, err := EvaluateDeliveryReuse(base); err == nil {
		t.Fatal("relation-scoped reuse accepted missing context identities")
	}
	base.ContextDigest = deliveryTestDigest('d')
	if _, err := EvaluateDeliveryReuse(base); err == nil {
		t.Fatal("relation-scoped reuse accepted asymmetric context identities")
	}
	base.RelationScoped = false
	base.ContextDigest = ""
	base.BaseContextDigest = ""
	if decision, err := EvaluateDeliveryReuse(base); err != nil || !decision.Reusable {
		t.Fatalf("legacy candidate-level context omission = %#v, err=%v", decision, err)
	}
}

func TestDeliveryReusePolicyDisablesUndeclaredNondeterminism(t *testing.T) {
	base := DeliveryReuseInput{
		ResourceID: "events", ExecutionDigest: deliveryTestDigest('a'), BaseExecutionDigest: deliveryTestDigest('a'),
		CatalogDigest: deliveryTestDigest('b'), BaseCatalogDigest: deliveryTestDigest('b'), PhysicalPoolID: "pool-1", BasePhysicalPoolID: "pool-1",
		CompatibilityDigest: deliveryTestDigest('c'), BaseCompatibilityDigest: deliveryTestDigest('c'), Deterministic: false,
	}
	decision, err := EvaluateDeliveryReuse(base)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Reusable || !strings.Contains(decision.Reason, "nondeterminism") {
		t.Fatalf("undeclared nondeterminism decision = %#v", decision)
	}
	base.Deterministic = true
	base.Observed = true
	decision, err = EvaluateDeliveryReuse(base)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Reusable || !strings.Contains(decision.Reason, "equivalence token") {
		t.Fatalf("un-tokenized observed decision = %#v", decision)
	}
	base.EquivalenceToken = "connector-token-1"
	decision, err = EvaluateDeliveryReuse(base)
	if err != nil || !decision.Reusable {
		t.Fatalf("stable observed token decision = %#v, err=%v", decision, err)
	}
}

func TestRestatementPlanRetainsBoundedIntervalsScopeStrategyAndIdempotency(t *testing.T) {
	plan := deliveryTestPlan(t)
	plan.Operation = DeliveryOperationRestatement
	plan.Execution.DataInputs = []DeliveryDataInput{
		{ID: "orders", Mode: DeliveryDataBounded, Bound: "2026-08-17T00:00:00Z"},
		{ID: "customer-dimension", Mode: DeliveryDataPinned, Revision: "revision-42"},
	}
	plan.Evidence.Restatement = &DeliveryRestatementEvidence{
		RequestedStart: "2026-08-01T00:00:00Z", RequestedEnd: "2026-08-07T00:00:00Z",
		EffectiveStart: "2026-08-02T00:00:00Z", EffectiveEnd: "2026-08-07T00:00:00Z",
		DownstreamScope: []string{"model.orders", "dashboard.sales"}, Strategy: "replace-window",
		IdempotencyKey: "restatement-42", WideningExplanation: "source watermark starts one day later",
		Estimate: &DeliveryEstimate{Work: "orders", LowerBound: 1, UpperBound: 2, Expected: 1, Unit: "relation", Basis: "bounded interval", Confidence: "high"},
	}
	plan.ExecutionDigest, _ = plan.Execution.ExecutionDigest()
	plan.EvidenceDigest, plan.Digest = "", ""
	first, err := NewDeliveryPlan(plan)
	if err != nil {
		t.Fatalf("restatement plan: %v", err)
	}
	if first.Evidence.Restatement == nil || first.Evidence.Restatement.EffectiveStart != "2026-08-02T00:00:00Z" || first.Evidence.Restatement.IdempotencyKey != "restatement-42" {
		t.Fatalf("restatement evidence was not retained: %#v", first.Evidence.Restatement)
	}
	changed := first
	changed.Evidence.Restatement = &DeliveryRestatementEvidence{
		RequestedStart: "2026-08-01T00:00:00Z", RequestedEnd: "2026-08-08T00:00:00Z",
		EffectiveStart: "2026-08-02T00:00:00Z", EffectiveEnd: "2026-08-08T00:00:00Z",
		DownstreamScope: []string{"model.orders", "dashboard.sales"}, Strategy: "replace-window",
		IdempotencyKey: "restatement-42", WideningExplanation: "source watermark starts one day later",
		Estimate: first.Evidence.Restatement.Estimate,
	}
	changed.EvidenceDigest, changed.Digest = "", ""
	changed, err = NewDeliveryPlan(changed)
	if err != nil {
		t.Fatalf("changed restatement plan: %v", err)
	}
	if changed.EvidenceDigest == first.EvidenceDigest || changed.Digest == first.Digest {
		t.Fatal("restatement interval change did not alter immutable evidence identity")
	}
}

func TestDeliveryPlanEvidenceIsReconstructableNonSecretJSON(t *testing.T) {
	plan := deliveryTestPlan(t)
	for name, value := range map[string]any{
		"provenance": plan.Provenance,
		"governance": plan.Governance,
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s evidence: %v", name, err)
		}
		var object map[string]any
		if err := json.Unmarshal(encoded, &object); err != nil {
			t.Fatalf("unmarshal %s evidence: %v", name, err)
		}
		if len(object) == 0 {
			t.Fatalf("%s evidence encoded as an empty object", name)
		}
		for key := range object {
			if strings.Contains(strings.ToLower(key), "secret") || strings.Contains(strings.ToLower(key), "credential") || strings.Contains(strings.ToLower(key), "token") {
				t.Fatalf("%s evidence contains secret-bearing field %q", name, key)
			}
		}
	}
}

func TestDeliveryPlanRequiresExplicitEvidenceAndCanonicalizesOrder(t *testing.T) {
	plan := deliveryTestPlan(t)
	incomplete := plan
	incomplete.Evidence.Qualification.Steps = nil
	incomplete.EvidenceDigest, incomplete.Digest = "", ""
	if _, err := NewDeliveryPlan(incomplete); !errors.Is(err, ErrDeliveryInvalid) {
		t.Fatalf("incomplete qualification evidence err=%v", err)
	}
	left, right := plan, plan
	left.Evidence.GraphImpact.DirectlyModified = []DeliveryImpactResource{{ID: "model-b", Kind: "model", Change: "modified"}, {ID: "model-a", Kind: "model", Change: "modified"}}
	right.Evidence.GraphImpact.DirectlyModified = []DeliveryImpactResource{{ID: "model-a", Kind: "model", Change: "modified"}, {ID: "model-b", Kind: "model", Change: "modified"}}
	left.EvidenceDigest, left.Digest = "", ""
	right.EvidenceDigest, right.Digest = "", ""
	left, err := NewDeliveryPlan(left)
	if err != nil {
		t.Fatal(err)
	}
	right, err = NewDeliveryPlan(right)
	if err != nil {
		t.Fatal(err)
	}
	if left.EvidenceDigest != right.EvidenceDigest || left.Digest != right.Digest {
		t.Fatalf("equivalent evidence digests differ: %s/%s vs %s/%s", left.EvidenceDigest, left.Digest, right.EvidenceDigest, right.Digest)
	}
}

func TestResolvedBuildInputsRequireObservedEvidenceDigest(t *testing.T) {
	d := deliveryTestDigest
	plan := deliveryTestPlan(t)
	gates := deliveryTestGateEvidence(t, plan, release.GateSuccess)
	pinned, err := NewDeliveryResolvedBuildInputs(DeliveryResolvedBuildInputs{GateEvidence: gates, Inputs: []DeliveryResolvedDataInput{{ID: "orders", Mode: DeliveryDataPinned, PlannedRevision: "rev-1", ActualRevision: "rev-1", Explanation: "read immutable revision"}}})
	if err != nil || pinned.EvidenceDigest == "" {
		t.Fatalf("pinned evidence=%#v err=%v", pinned, err)
	}
	bounded, err := NewDeliveryResolvedBuildInputs(DeliveryResolvedBuildInputs{GateEvidence: gates, Inputs: []DeliveryResolvedDataInput{{ID: "orders", Mode: DeliveryDataBounded, PlannedBound: "2026-08-17T00:00:00Z", ActualBound: "2026-08-17T00:00:00Z", Explanation: "enforced upper watermark"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDeliveryResolvedBuildInputs(DeliveryResolvedBuildInputs{GateEvidence: gates, Inputs: []DeliveryResolvedDataInput{{ID: "live", Mode: DeliveryDataObserved, Explanation: "observed"}}}); !errors.Is(err, ErrDeliveryInvalid) {
		t.Fatalf("missing observed digest err=%v", err)
	}
	if _, err := NewDeliveryResolvedBuildInputs(DeliveryResolvedBuildInputs{GateEvidence: gates, Inputs: []DeliveryResolvedDataInput{{ID: "live", Mode: DeliveryDataObserved, ObservationDigest: d('a'), Explanation: "observed"}}}); err != nil {
		t.Fatal(err)
	}
	_ = bounded
}

func TestResolvedBuildInputsPersistAndRejectTamperedGateEvidence(t *testing.T) {
	plan := deliveryTestPlan(t)
	evidence, err := (release.GateEvidence{Version: 1, CandidateID: "candidate-1", SourceDigest: plan.SourceDigest, BindingGeneration: "sha256:" + strings.Repeat("c", 64), RuntimeVersion: "runtime-1", DuckDBVersion: "duckdb-1", Outcome: release.GateSuccess, EvaluatedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC), Bounds: release.GateBounds{MaxRows: 10, MaxQueries: 2, MaxMillis: 100}}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ValidateDeliveryResolvedBuildInputs(plan, DeliveryResolvedBuildInputs{PolicyDigest: plan.Governance.PolicyDigest, GateEvidence: &evidence})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded DeliveryResolvedBuildInputs
	if err := json.Unmarshal(encoded, &reloaded); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateDeliveryResolvedBuildInputs(plan, reloaded); err != nil {
		t.Fatalf("reloaded evidence rejected: %v", err)
	}
	reloaded.GateEvidence.Digest = deliveryTestDigest('f')
	if _, err := ValidateDeliveryResolvedBuildInputs(plan, reloaded); !errors.Is(err, ErrDeliveryConflict) {
		t.Fatalf("tampered gate evidence error = %v, want conflict", err)
	}
}

func TestResolvedBuildInputsRejectFailedGateEvidence(t *testing.T) {
	plan := deliveryTestPlan(t)
	for _, outcome := range []release.GateOutcome{release.GateBlocking, release.GateUnavailable, release.GateEmpty, release.GateTimeout} {
		evidence := deliveryTestGateEvidence(t, plan, outcome)
		if _, err := NewDeliveryResolvedBuildInputs(DeliveryResolvedBuildInputs{PolicyDigest: plan.Governance.PolicyDigest, GateEvidence: evidence}); !errors.Is(err, ErrDeliveryConflict) {
			t.Errorf("constructor outcome %q error=%v, want conflict", outcome, err)
		}
		if _, err := ValidateDeliveryResolvedBuildInputs(plan, DeliveryResolvedBuildInputs{PolicyDigest: plan.Governance.PolicyDigest, GateEvidence: evidence}); !errors.Is(err, ErrDeliveryConflict) {
			t.Errorf("validator outcome %q error=%v, want conflict", outcome, err)
		}
	}
}

func TestResolvedBoundedInputRejectsChangedWatermark(t *testing.T) {
	plan := deliveryTestPlan(t)
	gates := deliveryTestGateEvidence(t, plan, release.GateSuccess)
	plan.Execution.DataInputs = []DeliveryDataInput{{ID: "orders", Mode: DeliveryDataBounded, Bound: "2026-08-17T00:00:00Z"}}
	valid, err := ValidateDeliveryResolvedBuildInputs(plan, DeliveryResolvedBuildInputs{
		PolicyDigest: plan.Governance.PolicyDigest,
		GateEvidence: gates,
		Inputs:       []DeliveryResolvedDataInput{{ID: "orders", Mode: DeliveryDataBounded, PlannedBound: "2026-08-17T00:00:00Z", ActualBound: "2026-08-17T00:00:00Z", Explanation: "enforced upper watermark"}},
	})
	if err != nil || valid.EvidenceDigest == "" {
		t.Fatalf("valid bounded resolution = %#v, err=%v", valid, err)
	}
	_, err = ValidateDeliveryResolvedBuildInputs(plan, DeliveryResolvedBuildInputs{
		PolicyDigest: plan.Governance.PolicyDigest,
		GateEvidence: gates,
		Inputs:       []DeliveryResolvedDataInput{{ID: "orders", Mode: DeliveryDataBounded, PlannedBound: "2026-08-17T00:00:00Z", ActualBound: "2026-08-18T00:00:00Z", Explanation: "watermark widened"}},
	})
	if !errors.Is(err, ErrDeliveryInvalid) {
		t.Fatalf("changed bounded watermark error = %v, want invalid", err)
	}
}

func TestResolvedBuildInputsBindExactlyToPlanDeclarations(t *testing.T) {
	plan := deliveryTestPlan(t)
	gates := deliveryTestGateEvidence(t, plan, release.GateSuccess)
	plan.Execution.DataInputs = []DeliveryDataInput{{ID: "orders", Mode: DeliveryDataPinned, Revision: "rev-1"}, {ID: "events", Mode: DeliveryDataObserved}}
	valid, err := ValidateDeliveryResolvedBuildInputs(plan, DeliveryResolvedBuildInputs{PolicyDigest: plan.Governance.PolicyDigest, GateEvidence: gates, Inputs: []DeliveryResolvedDataInput{
		{ID: "orders", Mode: DeliveryDataPinned, PlannedRevision: "rev-1", ActualRevision: "rev-1", Explanation: "immutable revision"},
		{ID: "events", Mode: DeliveryDataObserved, ObservationDigest: deliveryTestDigest('9'), Explanation: "build observation"},
	}})
	if err != nil || valid.EvidenceDigest == "" {
		t.Fatalf("valid resolved plan evidence=%#v err=%v", valid, err)
	}
	missing := valid
	missing.Inputs = missing.Inputs[:1]
	missing.EvidenceDigest = ""
	if _, err := ValidateDeliveryResolvedBuildInputs(plan, missing); !errors.Is(err, ErrDeliveryConflict) {
		t.Fatalf("missing planned input err=%v, want conflict", err)
	}
	mismatch := DeliveryResolvedBuildInputs{PolicyDigest: plan.Governance.PolicyDigest, GateEvidence: gates, Inputs: []DeliveryResolvedDataInput{
		{ID: "orders", Mode: DeliveryDataBounded, PlannedBound: "watermark", ActualBound: "watermark", Explanation: "wrong mode"},
		{ID: "events", Mode: DeliveryDataObserved, ObservationDigest: deliveryTestDigest('9'), Explanation: "build observation"},
	}}
	if _, err := ValidateDeliveryResolvedBuildInputs(plan, mismatch); !errors.Is(err, ErrDeliveryConflict) {
		t.Fatalf("mode mismatch err=%v, want conflict", err)
	}
	policyMismatch := DeliveryResolvedBuildInputs{PolicyDigest: deliveryTestDigest('8'), GateEvidence: gates}
	if _, err := ValidateDeliveryResolvedBuildInputs(plan, policyMismatch); !errors.Is(err, ErrDeliveryConflict) {
		t.Fatalf("policy mismatch err=%v, want conflict", err)
	}
	emptyPlan := deliveryTestPlan(t)
	empty, err := ValidateDeliveryResolvedBuildInputs(emptyPlan, DeliveryResolvedBuildInputs{PolicyDigest: emptyPlan.Governance.PolicyDigest, GateEvidence: deliveryTestGateEvidence(t, emptyPlan, release.GateSuccess)})
	if err != nil || empty.EvidenceDigest == "" {
		t.Fatalf("empty plan evidence=%#v err=%v, want canonical digest", empty, err)
	}
}

func TestDeliveryCanonicalTimeAndCatalogKeyValidation(t *testing.T) {
	if err := validateDeliveryTime("created", time.Date(2026, 8, 17, 12, 0, 0, 0, time.FixedZone("UTC+1", 3600)), true); err == nil {
		t.Fatal("non-UTC timestamp unexpectedly accepted")
	}
	for _, key := range []string{"../catalog.db", "/catalog.db", "catalog\\db", "catalogs//seal"} {
		if err := validateCatalogObjectKey("catalog", key); err == nil {
			t.Fatalf("unsafe object key %q unexpectedly accepted", key)
		}
	}
}

func TestDeliveryGCFencesAreIdempotent(t *testing.T) {
	plan := deliveryTestPlan(t)
	now := plan.CreatedAt
	cycle, err := NewDeliveryGCCycle(DeliveryGCCycle{ID: "gc-1", PhysicalPoolID: "pool-1", Epoch: 1, RootRevision: 4, CreatedAt: now})
	if err != nil {
		t.Fatalf("new GC cycle: %v", err)
	}
	cycle, _ = cycle.Mark(deliveryTestDigest('a'))
	cycle, _ = cycle.BeginDelete()
	cycle, err = cycle.Complete(now.Add(3 * time.Minute))
	if err != nil {
		t.Fatalf("complete GC cycle: %v", err)
	}
	if _, err := cycle.Abort("late", now.Add(4*time.Minute)); !errors.Is(err, ErrDeliveryConflict) {
		t.Fatalf("abort completed cycle error = %v, want conflict", err)
	}
}
