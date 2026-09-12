package transitionpreflight

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/platform/compatibility"
)

func TestEvaluateDecisionMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Input)
		want   Decision
		reason ReasonCode
	}{
		{name: "all persistent domains backward compatible", want: DecisionBinaryRollbackCompatible},
		{name: "control incompatibility requires provider recovery", mutate: func(input *Input) {
			input.Control.Compatibility = CompatibilityIncompatible
		}, want: DecisionProviderRecoveryRequired, reason: ReasonControlIncompatible},
		{name: "river schema incompatibility requires provider recovery", mutate: func(input *Input) {
			input.River.SchemaCompatibility = CompatibilityIncompatible
		}, want: DecisionProviderRecoveryRequired, reason: ReasonRiverIncompatible},
		{name: "river job history incompatibility requires provider recovery", mutate: func(input *Input) {
			input.River.JobHistoryCompatibility = CompatibilityIncompatible
		}, want: DecisionProviderRecoveryRequired, reason: ReasonRiverIncompatible},
		{name: "ducklake incompatibility requires provider recovery", mutate: func(input *Input) {
			input.DuckLake.Compatibility = CompatibilityIncompatible
		}, want: DecisionProviderRecoveryRequired, reason: ReasonDuckLakeIncompatible},
		{name: "legacy predecessor is unsupported", mutate: func(input *Input) {
			input.Predecessor.ArchitectureMarker = "sqlite"
		}, want: DecisionUnsupported, reason: ReasonPredecessorNotPostgreSQL},
		{name: "missing recovery frontier is unsupported", mutate: func(input *Input) {
			input.Control.Compatibility = CompatibilityIncompatible
			input.RecoveryFrontier = nil
		}, want: DecisionUnsupported, reason: ReasonMissingRecoveryFrontier},
		{name: "missing recovery frontier rejects binary decision too", mutate: func(input *Input) {
			input.RecoveryFrontier = nil
		}, want: DecisionUnsupported, reason: ReasonMissingRecoveryFrontier},
		{name: "missing migration ownership is unsupported", mutate: func(input *Input) {
			input.MigrationOwnership = MigrationOwnership{}
		}, want: DecisionUnsupported, reason: ReasonMissingMigrationOwnership},
		{name: "wrong Goose ownership is unsupported", mutate: func(input *Input) {
			input.MigrationOwnership.GooseControlSchemaOwner = OwnerRiver
		}, want: DecisionUnsupported, reason: ReasonGooseOwnershipMismatch},
		{name: "unknown compatibility is unsupported", mutate: func(input *Input) {
			input.DuckLake.Compatibility = CompatibilityUnknown
		}, want: DecisionUnsupported, reason: ReasonUnknownDuckLakeCompatibility},
		{name: "conflicting policy metadata is unsupported", mutate: func(input *Input) {
			input.Control.Compatibility = CompatibilityIncompatible
			input.ReleasePolicy.Rules[0].Decision = DecisionBinaryRollbackCompatible
		}, want: DecisionUnsupported, reason: ReasonPolicyMismatch},
		{name: "ambiguous matching policy is unsupported", mutate: func(input *Input) {
			input.ReleasePolicy.Rules = append(input.ReleasePolicy.Rules, input.ReleasePolicy.Rules[0])
		}, want: DecisionUnsupported, reason: ReasonMultiplePolicyRules},
		{name: "forward-only policy is unsupported", mutate: func(input *Input) {
			input.ReleasePolicy.Rules[0].RollbackToArtifactDigest = input.ReleasePolicy.Rules[0].CandidateArtifactDigest
		}, want: DecisionUnsupported, reason: ReasonPolicyMismatch},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := testInput()
			if test.mutate != nil {
				test.mutate(&input)
			}
			if test.want == DecisionProviderRecoveryRequired {
				input.ReleasePolicy.Rules[0].Decision = DecisionProviderRecoveryRequired
				input.ReleasePolicy.Digest, _ = input.ReleasePolicy.ContentDigest()
			}
			evidence, err := Evaluate(input)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if evidence.Decision != test.want {
				t.Fatalf("decision = %q, want %q; evidence = %#v", evidence.Decision, test.want, evidence)
			}
			if test.reason != "" && !containsReason(evidence.ReasonCodes, test.reason) {
				t.Fatalf("reason codes = %v, want %q", evidence.ReasonCodes, test.reason)
			}
			if !sort.SliceIsSorted(evidence.ReasonCodes, func(i, j int) bool { return evidence.ReasonCodes[i] < evidence.ReasonCodes[j] }) {
				t.Fatalf("reason codes are not sorted: %v", evidence.ReasonCodes)
			}
		})
	}
}

func TestEvaluateRejectsStructurallyMalformedInput(t *testing.T) {
	t.Parallel()

	input := testInput()
	input.SchemaVersion = 0
	if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing schema version error = %v, want ErrInvalidInput", err)
	}

	input = testInput()
	input.Candidate.Release.Image = "ghcr.io/flidai/leapview:latest"
	if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid OCI image error = %v, want ErrInvalidInput", err)
	}

	input = testInput()
	input.Candidate.Release.Image = input.Predecessor.Release.Image
	if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("same artifact error = %v, want ErrInvalidInput", err)
	}

	input = testInput()
	input.RecoveryFrontier = &RecoveryFrontierRef{SetID: "not-a-uuid", Digest: "sha256:" + strings.Repeat("a", 64)}
	if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid frontier error = %v, want ErrInvalidInput", err)
	}

	input = testInput()
	input.Candidate.Release.ReleaseID = string([]byte{0xff})
	if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid UTF-8 error = %v, want ErrInvalidInput", err)
	}
}

func TestEvaluateIsOrderIndependentAndDigestChangesOnMutation(t *testing.T) {
	t.Parallel()

	first := testInput()
	second := testInput()
	predecessorDigest, err := first.Predecessor.Digest()
	if err != nil {
		t.Fatal(err)
	}
	candidateDigest, err := first.Candidate.Digest()
	if err != nil {
		t.Fatal(err)
	}
	first.ReleasePolicy.Rules = []ReleasePolicyRule{
		first.ReleasePolicy.Rules[0],
		{PredecessorArtifactDigest: candidateDigest, CandidateArtifactDigest: predecessorDigest, RollbackFromArtifactDigest: predecessorDigest, RollbackToArtifactDigest: candidateDigest, Decision: DecisionProviderRecoveryRequired},
	}
	first.ReleasePolicy.Digest, _ = first.ReleasePolicy.ContentDigest()
	second.ReleasePolicy.Rules = []ReleasePolicyRule{
		{PredecessorArtifactDigest: candidateDigest, CandidateArtifactDigest: predecessorDigest, RollbackFromArtifactDigest: predecessorDigest, RollbackToArtifactDigest: candidateDigest, Decision: DecisionProviderRecoveryRequired},
		first.ReleasePolicy.Rules[0],
	}
	second.ReleasePolicy.Digest, _ = second.ReleasePolicy.ContentDigest()
	left, err := Evaluate(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := Evaluate(second)
	if err != nil {
		t.Fatal(err)
	}
	leftJSON, err := left.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	rightJSON, err := right.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	leftDigest, err := left.Digest()
	if err != nil {
		t.Fatal(err)
	}
	rightDigest, err := right.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(leftJSON, rightJSON) || leftDigest != rightDigest {
		t.Fatalf("reordered policy changed canonical identity:\n%s\n%s\n%s\n%s", leftJSON, rightJSON, leftDigest, rightDigest)
	}

	mutatedInput := testInput()
	mutatedInput.Control.CandidateSchemaVersion = "control/v2"
	mutated, err := Evaluate(mutatedInput)
	if err != nil {
		t.Fatal(err)
	}
	mutatedDigest, err := mutated.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if mutatedDigest == leftDigest {
		t.Fatal("mutating evidence did not change digest")
	}
}

func TestPhaseIdentitiesAreFixedOrderedAndDeterministic(t *testing.T) {
	t.Parallel()

	first, err := Evaluate(testInput())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Evaluate(testInput())
	if err != nil {
		t.Fatal(err)
	}
	wantNames := PhaseNames()
	if len(first.PhaseIdentities) != len(wantNames) {
		t.Fatalf("phase count = %d, want %d", len(first.PhaseIdentities), len(wantNames))
	}
	for i, phase := range first.PhaseIdentities {
		if phase.Phase != wantNames[i] {
			t.Fatalf("phase %d = %q, want %q", i, phase.Phase, wantNames[i])
		}
		if phase.Digest != second.PhaseIdentities[i].Digest || !strings.HasPrefix(phase.Digest, "sha256:") {
			t.Fatalf("phase %q is not deterministic SHA-256 identity: %#v / %#v", phase.Phase, phase, second.PhaseIdentities[i])
		}
	}
}

func TestEvidenceValidationRecomputesDecisionReasonsAndPhaseIdentities(t *testing.T) {
	t.Parallel()

	evidence, err := Evaluate(testInput())
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*Evidence){
		func(value *Evidence) { value.Decision = DecisionUnsupported },
		func(value *Evidence) { value.ReasonCodes = append(value.ReasonCodes, ReasonPolicyMismatch) },
		func(value *Evidence) { value.PhaseIdentities[0].Digest = "sha256:" + strings.Repeat("f", 64) },
		func(value *Evidence) { value.ReleasePolicy.Digest = "sha256:" + strings.Repeat("f", 64) },
	}
	for i, mutate := range mutations {
		mutated := evidence
		mutate(&mutated)
		if err := mutated.Validate(); err == nil {
			t.Fatalf("mutation %d was accepted", i)
		}
	}
}

func TestTargetBindingAndPolicyBindCompleteArtifacts(t *testing.T) {
	t.Parallel()

	input := testInput()
	input.Control.TargetIdentityDigest = "sha256:" + strings.Repeat("0", 64)
	evidence, err := Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Decision != DecisionUnsupported || !containsReason(evidence.ReasonCodes, ReasonTargetIdentityMismatch) {
		t.Fatalf("cross-target projection was accepted: decision=%q reasons=%v", evidence.Decision, evidence.ReasonCodes)
	}

	input = testInput()
	input.Candidate.Release.Version = "1.1.1"
	evidence, err = Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Decision != DecisionUnsupported || !containsReason(evidence.ReasonCodes, ReasonPolicyMismatch) {
		t.Fatalf("artifact metadata substitution was accepted: decision=%q reasons=%v", evidence.Decision, evidence.ReasonCodes)
	}

	input = testInput()
	input.ReleasePolicy.Digest = "sha256:" + strings.Repeat("0", 64)
	evidence, err = Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Decision != DecisionUnsupported || !containsReason(evidence.ReasonCodes, ReasonReleasePolicyDigestMismatch) {
		t.Fatalf("policy digest mismatch was accepted: decision=%q reasons=%v", evidence.Decision, evidence.ReasonCodes)
	}
}

func TestDuckLakeVerdictBindsBothExactTuples(t *testing.T) {
	t.Parallel()

	baseline, err := Evaluate(testInput())
	if err != nil {
		t.Fatal(err)
	}
	changedInput := testInput()
	changedInput.DuckLake.Candidate.DuckDBRuntime = "duckdb:1.6.0"
	changed, err := Evaluate(changedInput)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Decision != DecisionBinaryRollbackCompatible {
		t.Fatalf("owner-qualified cross-version tuple decision = %q", changed.Decision)
	}
	baselineDigest, err := baseline.Digest()
	if err != nil {
		t.Fatal(err)
	}
	changedDigest, err := changed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if changedDigest == baselineDigest {
		t.Fatal("DuckLake tuple change was not bound into evidence")
	}
}

func TestParseEvidenceIsStrictAndRevalidates(t *testing.T) {
	t.Parallel()

	evidence, err := Evaluate(testInput())
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := evidence.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseEvidence(canonical); err != nil {
		t.Fatalf("canonical evidence did not parse: %v", err)
	}
	text := string(canonical)
	unknown := strings.TrimSuffix(text, "}") + ",\"unknown\":1}"
	if _, err := ParseEvidence([]byte(unknown)); err == nil {
		t.Fatal("unknown evidence field was accepted")
	}
	duplicate := strings.Replace(text, "{\"schemaVersion\":1,", "{\"schemaVersion\":1,\"schemaVersion\":1,", 1)
	if _, err := ParseEvidence([]byte(duplicate)); err == nil {
		t.Fatal("duplicate evidence field was accepted")
	}
	if _, err := ParseEvidence(append(append([]byte{}, canonical...), []byte(" {}")...)); err == nil {
		t.Fatal("trailing evidence value was accepted")
	}
}

func TestCanonicalGoldenVector(t *testing.T) {
	t.Parallel()

	evidence, err := Evaluate(testInput())
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := evidence.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := evidence.Digest()
	if err != nil {
		t.Fatal(err)
	}
	fixtureBytes, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		EvidenceDigest string   `json:"evidenceDigest"`
		PolicyDigest   string   `json:"policyDigest"`
		PhaseDigests   []string `json:"phaseDigests"`
	}
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(canonical) == 0 || digest != fixture.EvidenceDigest || evidence.ReleasePolicy.Digest != fixture.PolicyDigest {
		t.Fatalf("golden mismatch:\ncanonical = %s\nevidence digest = %s\npolicy digest = %s\nfixture = %#v", canonical, digest, evidence.ReleasePolicy.Digest, fixture)
	}
	if len(fixture.PhaseDigests) != len(evidence.PhaseIdentities) {
		t.Fatalf("golden phase count = %d, want %d", len(fixture.PhaseDigests), len(evidence.PhaseIdentities))
	}
	for i, phase := range evidence.PhaseIdentities {
		if phase.Digest != fixture.PhaseDigests[i] {
			t.Fatalf("golden phase %q digest = %s, want %s", phase.Phase, phase.Digest, fixture.PhaseDigests[i])
		}
	}
}

func testInput() Input {
	predecessorImage := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	candidateImage := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	tuples := physicalpool.Compatibility{
		DuckDBRuntime:         "duckdb:1.5.4",
		DuckLakeExtension:     "ducklake:0.3.0",
		CatalogFormat:         "ducklake-catalog:v1",
		StorageImplementation: "s3",
		ObjectNamingContract:  "uuidv7:v1",
	}
	predecessor := ArtifactIdentity{Release: compatibility.ReleaseIdentity{
		ReleaseID: "v1.0.0", Version: "1.0.0", SourceRevision: strings.Repeat("1", 40),
		Image: predecessorImage, Distribution: "public", Platform: "linux/amd64",
	}, ArchitectureMarker: ArchitecturePostgreSQL, ArtifactAdmissionDigest: "sha256:" + strings.Repeat("d", 64)}
	candidate := ArtifactIdentity{Release: compatibility.ReleaseIdentity{
		ReleaseID: "v1.1.0", Version: "1.1.0", SourceRevision: strings.Repeat("2", 40),
		Image: candidateImage, Distribution: "public", Platform: "linux/amd64",
	}, ArchitectureMarker: ArchitecturePostgreSQL, ArtifactAdmissionDigest: "sha256:" + strings.Repeat("e", 64)}
	predecessorDigest, _ := predecessor.Digest()
	candidateDigest, _ := candidate.Digest()
	targetDigest := "sha256:" + strings.Repeat("f", 64)
	policy := ReleasePolicy{Version: "release-policy/v1", Rules: []ReleasePolicyRule{{PredecessorArtifactDigest: predecessorDigest, CandidateArtifactDigest: candidateDigest, RollbackFromArtifactDigest: candidateDigest, RollbackToArtifactDigest: predecessorDigest, Decision: DecisionBinaryRollbackCompatible}}}
	policy.Digest, _ = policy.ContentDigest()
	return Input{
		SchemaVersion: 1, TargetIdentityDigest: targetDigest,
		Predecessor: predecessor, Candidate: candidate,
		MigrationOwnership: MigrationOwnership{
			GooseControlSchemaOwner: OwnerLeapView, RiverOperationalSchemaOwner: OwnerRiver, RiverJobHistoryOwner: OwnerLeapView,
		},
		Control:          PostgreSQLControlProjection{Compatibility: CompatibilityBackwardCompatible, PredecessorSchemaVersion: "goose/v1", CandidateSchemaVersion: "goose/v2", TargetIdentityDigest: targetDigest},
		River:            RiverJobProjection{SchemaCompatibility: CompatibilityBackwardCompatible, JobHistoryCompatibility: CompatibilityBackwardCompatible, ExistingSchemaVersion: "river/v1", RequiredSchemaVersion: "river/v2", ExistingJobHistoryVersion: "jobs/v1", RequiredJobHistoryVersion: "jobs/v2", TargetIdentityDigest: targetDigest},
		DuckLake:         DuckLakeProjection{Compatibility: CompatibilityBackwardCompatible, Predecessor: tuples, Candidate: tuples, TargetIdentityDigest: targetDigest},
		RecoveryFrontier: &RecoveryFrontierRef{SetID: "018f3f83-7b2f-7b37-9f9e-000000000010", Digest: "sha256:" + strings.Repeat("c", 64), TargetIdentityDigest: targetDigest},
		ReleasePolicy:    policy,
	}
}

func containsReason(reasons []ReasonCode, want ReasonCode) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}
