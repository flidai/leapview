package deploymentpostgres

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/google/uuid"
)

func buildIdentityDigest(ch byte) string {
	return "sha256:" + strings.Repeat(string(ch), 64)
}

func nativeBuildIdentityRequest(t *testing.T) deploymentmodule.NativeDeliveryBuildRequest {
	t.Helper()
	plan, err := uuid.Parse("0198f2c0-7c7a-7f00-8a11-000000001001")
	if err != nil {
		t.Fatal(err)
	}
	return deploymentmodule.NativeDeliveryBuildRequest{
		ProjectID: "project_native_build_identity", TargetID: "target-native-build",
		Environment: "prod", PlanID: plan, PrincipalID: "principal-native-build",
		IdempotencyKey: "native-build-key",
	}
}

func nativeBuildIdentityOutcome(t *testing.T, request deploymentmodule.NativeDeliveryBuildRequest) (nativeBuildOutcome, deploymentmodule.NativeOperationAcquireInput) {
	t.Helper()
	requestDigest, err := nativeBuildRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "0198f2c0-7c7a-7f00-8a11-000000001002"
	ids := make(map[string]string, 7)
	for _, role := range []string{"candidate", "attempt", "lease", "generation", "seal", "event", "audit"} {
		ids[role], err = nativeBuildConsequenceID(operationID, role)
		if err != nil {
			t.Fatal(err)
		}
	}
	outcome := nativeBuildOutcome{
		OperationID: operationID, OperationOwnerID: "0198f2c0-7c7a-7f00-8a11-000000001003", PlanID: request.PlanID.String(), CandidateID: ids["candidate"],
		AttemptID: ids["attempt"], LeaseID: ids["lease"], GenerationID: ids["generation"], SealID: ids["seal"],
		EventID: ids["event"], AuditID: ids["audit"], ProjectID: request.ProjectID.String(), TargetID: request.TargetID,
		Environment: request.Environment, ActorID: request.PrincipalID, IdempotencyKey: request.IdempotencyKey,
		RequestDigest: requestDigest, PlanDigest: buildIdentityDigest('a'), SourceDigest: buildIdentityDigest('b'),
		ExecutionDigest: buildIdentityDigest('c'), ServingArtifactID: "artifact-native-build",
		QualificationDigest: buildIdentityDigest('e'), ServingArtifactDigest: buildIdentityDigest('d'), Status: "sealed",
	}
	operationInput := deploymentmodule.NativeOperationAcquireInput{
		Scope: request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: request.IdempotencyKey,
		RequestDigest: requestDigest, OwnerID: "0198f2c0-7c7a-7f00-8a11-000000001003",
	}
	return outcome, operationInput
}

func TestNativeBuildRequestDigestBindsAllRequestIdentity(t *testing.T) {
	request := nativeBuildIdentityRequest(t)
	first, err := nativeBuildRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := nativeBuildRequestDigest(request)
	if err != nil || first != second {
		t.Fatalf("repeated request digest = %q/%q, err %v", first, second, err)
	}
	mutations := []func(*deploymentmodule.NativeDeliveryBuildRequest){
		func(r *deploymentmodule.NativeDeliveryBuildRequest) { r.ProjectID = "another-project" },
		func(r *deploymentmodule.NativeDeliveryBuildRequest) { r.TargetID = "another-target" },
		func(r *deploymentmodule.NativeDeliveryBuildRequest) { r.Environment = "staging" },
		func(r *deploymentmodule.NativeDeliveryBuildRequest) {
			r.PlanID, _ = uuid.Parse("0198f2c0-7c7a-7f00-8a11-000000001004")
		},
		func(r *deploymentmodule.NativeDeliveryBuildRequest) { r.PrincipalID = "another-principal" },
		func(r *deploymentmodule.NativeDeliveryBuildRequest) { r.IdempotencyKey = "another-key" },
	}
	for index, mutate := range mutations {
		changed := request
		mutate(&changed)
		digest, err := nativeBuildRequestDigest(changed)
		if err != nil {
			t.Fatalf("mutation %d rejected: %v", index, err)
		}
		if digest == first {
			t.Fatalf("mutation %d did not change request digest", index)
		}
	}
}

func TestNativeBuildConsequenceIDsAreDeterministicUUIDv7AndRoleBound(t *testing.T) {
	operationID := "0198f2c0-7c7a-7f00-8a11-000000001005"
	seen := map[string]string{}
	for _, role := range []string{"candidate", "attempt", "lease", "generation", "seal", "event", "audit"} {
		first, err := nativeBuildConsequenceID(operationID, role)
		if err != nil {
			t.Fatalf("role %s: %v", role, err)
		}
		second, _ := nativeBuildConsequenceID(operationID, role)
		if first != second {
			t.Fatalf("role %s is not deterministic", role)
		}
		parsed, err := uuid.Parse(first)
		if err != nil || parsed.String() != first || parsed.Version() != 7 || parsed.Variant() != uuid.RFC4122 {
			t.Fatalf("role %s produced noncanonical UUIDv7 %q", role, first)
		}
		if previous, duplicate := seen[first]; duplicate {
			t.Fatalf("roles %s and %s collided at %s", previous, role, first)
		}
		seen[first] = role
	}
	if _, err := nativeBuildConsequenceID(operationID, "unknown"); err == nil {
		t.Fatal("unknown consequence role was accepted")
	}
	if _, err := nativeBuildConsequenceID("0198f2c0-7c7a-8f00-8a11-000000001005", "event"); err == nil {
		t.Fatal("non-v7 operation identity was accepted")
	}
	if _, err := nativeBuildConsequenceID("0198f2c0-7c7a-7f00-0a11-000000001005", "event"); err == nil {
		t.Fatal("non-RFC4122 operation identity was accepted")
	}
}

func TestNativeBuildRequestRejectsUnsafeOrUnboundedText(t *testing.T) {
	request := nativeBuildIdentityRequest(t)
	for name, mutate := range map[string]func(*deploymentmodule.NativeDeliveryBuildRequest){
		"target control":      func(r *deploymentmodule.NativeDeliveryBuildRequest) { r.TargetID = "target\nother" },
		"environment control": func(r *deploymentmodule.NativeDeliveryBuildRequest) { r.Environment = "prod\x00" },
		"principal invalid":   func(r *deploymentmodule.NativeDeliveryBuildRequest) { r.PrincipalID = string([]byte{0xff}) },
		"idempotency bound":   func(r *deploymentmodule.NativeDeliveryBuildRequest) { r.IdempotencyKey = strings.Repeat("k", 513) },
		"uuid variant": func(r *deploymentmodule.NativeDeliveryBuildRequest) {
			r.PlanID, _ = uuid.Parse("0198f2c0-7c7a-7f00-0a11-000000001001")
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := request
			mutate(&changed)
			if _, err := nativeBuildRequestDigest(changed); !errors.Is(err, deployment.ErrDeliveryInvalid) {
				t.Fatalf("unsafe request error = %v", err)
			}
		})
	}
}

func TestNativeBuildOutcomeEncodeDecodeBindsTerminalEvidence(t *testing.T) {
	request := nativeBuildIdentityRequest(t)
	outcome, operationInput := nativeBuildIdentityOutcome(t, request)
	raw, err := encodeNativeBuildOutcome(outcome, request, operationInput)
	if err != nil {
		t.Fatalf("valid outcome rejected: %v", err)
	}
	decoded, err := decodeNativeBuildOutcome(raw, request, operationInput)
	if err != nil || decoded != outcome {
		t.Fatalf("decoded outcome = %#v, err %v", decoded, err)
	}

	operationInput.OwnerID = "another-ephemeral-owner"
	if _, err := decodeNativeBuildOutcome(raw, request, operationInput); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("tampered operation owner accepted: %v", err)
	}
	operationInput.OwnerID = outcome.OperationOwnerID

	badStatus := outcome
	badStatus.Status = "failed"
	if _, err := encodeNativeBuildOutcome(badStatus, request, operationInput); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("non-terminal-success status error = %v", err)
	}
	badDigest := outcome
	badDigest.RequestDigest = buildIdentityDigest('e')
	if _, err := decodeNativeBuildOutcome(mustJSON(t, badDigest), request, operationInput); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("tampered request digest error = %v", err)
	}
	badOwner := outcome
	badOwner.OperationOwnerID = "0198f2c0-7c7a-7f00-8a11-000000001004"
	if _, err := encodeNativeBuildOutcome(badOwner, request, operationInput); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("tampered operation owner outcome accepted: %v", err)
	}
}

func TestDecodeNativeBuildOutcomeRejectsUnknownOversizedAndNoncanonicalData(t *testing.T) {
	request := nativeBuildIdentityRequest(t)
	outcome, operationInput := nativeBuildIdentityOutcome(t, request)
	raw, err := encodeNativeBuildOutcome(outcome, request, operationInput)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["unknown"] = true
	unknown, _ := json.Marshal(object)
	if _, err := decodeNativeBuildOutcome(unknown, request, operationInput); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("unknown outcome field error = %v", err)
	}
	if _, err := decodeNativeBuildOutcome(json.RawMessage(strings.Repeat("x", maxNativeBuildOutcomeBytes+1)), request, operationInput); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("oversized outcome error = %v", err)
	}
	var tampered nativeBuildOutcome
	if err := json.Unmarshal(raw, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.EventID = strings.ToUpper(tampered.EventID)
	if _, err := decodeNativeBuildOutcome(mustJSON(t, tampered), request, operationInput); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("noncanonical event identity error = %v", err)
	}
	if _, err := encodeNativeBuildOutcome(nativeBuildOutcome{}, request, operationInput); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("empty outcome error = %v", err)
	}
}

func TestNativeBuildOutcomeRootsSuccessorIdentityAndSupportsDepthTwo(t *testing.T) {
	request := nativeBuildIdentityRequest(t)
	outcome, operationInput := nativeBuildIdentityOutcome(t, request)
	rootAttempt, err := nativeBuildConsequenceID(outcome.OperationID, "attempt")
	if err != nil {
		t.Fatal(err)
	}
	firstAttempt, err := nativeBuildSuccessorID(rootAttempt, "attempt")
	if err != nil {
		t.Fatal(err)
	}
	firstLease, err := nativeBuildSuccessorID(rootAttempt, "lease")
	if err != nil {
		t.Fatal(err)
	}
	validFirst := outcome
	validFirst.AttemptID, validFirst.LeaseID = firstAttempt, firstLease
	validFirst.AttemptIdentity = "native-build-successor/" + firstAttempt
	validFirst.PredecessorAttemptID = rootAttempt
	validFirst.SuccessorDepth = 1
	if _, err := encodeNativeBuildOutcome(validFirst, request, operationInput); err != nil {
		t.Fatalf("depth-one successor outcome rejected: %v", err)
	}
	secondAttempt, err := nativeBuildSuccessorID(firstAttempt, "attempt")
	if err != nil {
		t.Fatal(err)
	}
	secondLease, err := nativeBuildSuccessorID(firstAttempt, "lease")
	if err != nil {
		t.Fatal(err)
	}
	valid := outcome
	valid.AttemptID, valid.LeaseID = secondAttempt, secondLease
	valid.AttemptIdentity = "native-build-successor/" + secondAttempt
	valid.PredecessorAttemptID = firstAttempt
	valid.SuccessorDepth = 2
	if _, err := encodeNativeBuildOutcome(valid, request, operationInput); err != nil {
		t.Fatalf("depth-two successor outcome rejected: %v", err)
	}
	// A UUIDv7 pair derived from an unrelated seed is not accepted merely
	// because the pair is internally consistent; it must be rooted at the
	// operation's deterministic attempt chain.
	arbitraryPredecessor := "0198f2c0-7c7a-7f00-8a11-000000009999"
	arbitraryAttempt, err := nativeBuildSuccessorID(arbitraryPredecessor, "attempt")
	if err != nil {
		t.Fatal(err)
	}
	arbitraryLease, err := nativeBuildSuccessorID(arbitraryPredecessor, "lease")
	if err != nil {
		t.Fatal(err)
	}
	forged := outcome
	forged.AttemptID, forged.LeaseID = arbitraryAttempt, arbitraryLease
	forged.AttemptIdentity = "native-build-successor/" + arbitraryAttempt
	forged.PredecessorAttemptID = arbitraryPredecessor
	forged.SuccessorDepth = 1
	if _, err := encodeNativeBuildOutcome(forged, request, operationInput); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("unrooted successor outcome error = %v", err)
	}
}

func TestReplayFailedNativeBuildValidatesExactSanitizedEvidence(t *testing.T) {
	attemptID := "0198f2c0-7c7a-7f00-8a11-000000001020"
	requestDigest := buildIdentityDigest('a')
	errorDigest := buildIdentityDigest('f')
	evidence := nativeBuildTerminationEvidence{
		SchemaVersion: 1, AttemptID: attemptID, OwnerID: "principal-native-build", FencingEpoch: 2,
		RequestDigest: requestDigest, PlanDigest: buildIdentityDigest('b'), PhysicalPoolID: "pool-native",
		Namespace: "candidate/native", SessionIdentity: "native-session", Phase: NativePhysicalBuildPhaseValidation,
		Classification: NativePhysicalFailureDeterministic, ErrorDigest: errorDigest,
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	operation := deploymentmodule.NativeOperationRecord{State: deploymentmodule.NativeOperationStateFailed, AttemptID: attemptID, Outcome: raw}
	err = replayFailedNativeBuild(operation, requestDigest)
	if !errors.Is(err, deployment.ErrDeliveryConflict) || !strings.Contains(err.Error(), errorDigest) {
		t.Fatalf("failed replay error = %v, want terminal conflict correlation", err)
	}
	evidence.RequestDigest = buildIdentityDigest('c')
	operation.Outcome, _ = json.Marshal(evidence)
	if err := replayFailedNativeBuild(operation, requestDigest); !errors.Is(err, deployment.ErrDeliveryConflict) || strings.Contains(err.Error(), errorDigest) {
		t.Fatalf("tampered failed replay error = %v", err)
	}
}

func TestReplayFailedNativeBuildValidatesPreflightEvidence(t *testing.T) {
	requestDigest := buildIdentityDigest('a')
	errorDigest := buildIdentityDigest('f')
	evidence := nativeBuildPreflightFailureEvidence{
		SchemaVersion: 1, RequestDigest: requestDigest, PlanDigest: buildIdentityDigest('b'),
		Phase: NativePhysicalBuildPhaseValidation, Classification: NativePhysicalFailureDeterministic,
		ErrorDigest: errorDigest,
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	operation := deploymentmodule.NativeOperationRecord{State: deploymentmodule.NativeOperationStateFailed, Outcome: raw}
	if err := replayFailedNativeBuild(operation, requestDigest); !errors.Is(err, deployment.ErrDeliveryConflict) || !strings.Contains(err.Error(), errorDigest) {
		t.Fatalf("preflight replay error = %v, want terminal conflict correlation", err)
	}
	evidence.RequestDigest = buildIdentityDigest('c')
	operation.Outcome, _ = json.Marshal(evidence)
	if err := replayFailedNativeBuild(operation, requestDigest); !errors.Is(err, deployment.ErrDeliveryConflict) || strings.Contains(err.Error(), errorDigest) {
		t.Fatalf("tampered preflight replay error = %v", err)
	}
}

func mustJSON(t *testing.T, value nativeBuildOutcome) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
