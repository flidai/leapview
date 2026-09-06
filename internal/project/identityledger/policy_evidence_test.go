package identityledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/project/contractprojection"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	"github.com/flidai/leapview/internal/project/contractversion"
)

func TestPolicyEvidenceRoundTripIsDeterministic(t *testing.T) {
	baseline := policyTestSourcePublication(t, "instance-policy", "1.0.0", false)
	candidate := policyTestSourcePublication(t, "instance-policy", "1.1.0", true)
	result, err := contractversion.ValidateVersionTransition(baseline.CanonicalBytes, candidate.CanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	context := PolicyContext{
		BaselineKind:              PolicyBaselineExisting,
		Baseline:                  policyTestPublicationIdentity(baseline),
		ExpectedLifecycleSequence: 7,
	}
	first, err := NewPolicyEvidence(context, &baseline, candidate, 7, "bundle-policy", result)
	if err != nil {
		t.Fatal(err)
	}
	if first.EvidenceDigest == "" || !strings.HasPrefix(first.EvidenceDigest, "sha256:") {
		t.Fatalf("evidence digest = %q", first.EvidenceDigest)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip PolicyEvidence
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if err := roundTrip.Validate(); err != nil {
		t.Fatalf("round-tripped evidence is invalid: %v", err)
	}
	if !reflect.DeepEqual(first, roundTrip) {
		t.Fatalf("round trip changed evidence:\nfirst=%#v\nroundTrip=%#v", first, roundTrip)
	}

	baselineAgain := policyTestSourcePublication(t, "instance-policy", "1.0.0", false)
	candidateAgain := policyTestSourcePublication(t, "instance-policy", "1.1.0", true)
	resultAgain, err := contractversion.ValidateVersionTransition(baselineAgain.CanonicalBytes, candidateAgain.CanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPolicyEvidence(context, &baselineAgain, candidateAgain, 7, "bundle-policy", resultAgain)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if !bytes.Equal(firstJSON, secondJSON) || first.EvidenceDigest != second.EvidenceDigest {
		t.Fatalf("equivalent evidence is not deterministic:\nfirst=%s\nsecond=%s", firstJSON, secondJSON)
	}
}

func TestPolicyEvidenceRejectsMissingDigest(t *testing.T) {
	evidence := policyTestExistingEvidence(t)
	evidence.EvidenceDigest = ""
	if err := evidence.Validate(); !errors.Is(err, ErrPolicyEvidenceInvalid) {
		t.Fatalf("missing digest error = %v", err)
	}
}

func TestPolicyEvidenceRejectsTamperedApprovalState(t *testing.T) {
	baseline := policyTestSemanticPublication(t, "instance-policy", "1.0.0", "region", []string{"emea"})
	candidate := policyTestSemanticPublication(t, "instance-policy", "1.1.0", "region", []string{"emea", "amer"})
	result, err := contractversion.ValidateVersionTransition(baseline.CanonicalBytes, candidate.CanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RequiresSecurityApproval {
		t.Fatalf("security widening did not require approval: %#v", result)
	}
	evidence, err := NewPolicyEvidence(PolicyContext{
		BaselineKind:              PolicyBaselineExisting,
		Baseline:                  policyTestPublicationIdentity(baseline),
		ExpectedLifecycleSequence: 1,
	}, &baseline, candidate, 1, "bundle-policy", result)
	if err != nil {
		t.Fatal(err)
	}
	evidence.ApprovalState = PolicyApprovalNotRequired
	if err := evidence.Validate(); err == nil || !strings.Contains(err.Error(), "approval state") {
		t.Fatalf("tampered approval state error = %v", err)
	}
}

func TestNewPolicyEvidenceRejectsFalseButInternallyValidClassification(t *testing.T) {
	baseline := policyTestSemanticPublication(t, "instance-policy", "1.0.0", "region", []string{"emea"})
	candidate := policyTestSemanticPublication(t, "instance-policy", "1.1.0", "region", []string{"emea", "amer"})
	falseResult, err := contractversion.Classify(candidate.CanonicalBytes, candidate.CanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := falseResult.ValidatePublication(); err != nil {
		t.Fatalf("same-document result should be internally valid: %v", err)
	}
	if len(falseResult.Changes) != 0 || falseResult.RequiresSecurityApproval {
		t.Fatalf("same-document result unexpectedly describes a change: %#v", falseResult)
	}
	_, err = NewPolicyEvidence(PolicyContext{
		BaselineKind:              PolicyBaselineExisting,
		Baseline:                  policyTestPublicationIdentity(baseline),
		ExpectedLifecycleSequence: 1,
	}, &baseline, candidate, 1, "bundle-policy", falseResult)
	if !errors.Is(err, ErrPolicyEvidenceConflict) {
		t.Fatalf("false classifier result was accepted: %v", err)
	}
}

func TestPolicyEvidenceRejectsIndeterminateOrMissingClassifierDimension(t *testing.T) {
	baseline := policyTestSemanticPublication(t, "instance-policy", "1.0.0", "region", []string{"emea"})
	candidate := policyTestSemanticPublication(t, "instance-policy", "2.0.0", "country", []string{"emea"})
	result, err := contractversion.Classify(baseline.CanonicalBytes, candidate.CanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	if result.SecurityImpact != contractversion.SecurityIndeterminate {
		t.Fatalf("unknown security dimension was not indeterminate: %#v", result)
	}
	context := PolicyContext{
		BaselineKind:              PolicyBaselineExisting,
		Baseline:                  policyTestPublicationIdentity(baseline),
		ExpectedLifecycleSequence: 1,
	}
	if _, err := NewPolicyEvidence(context, &baseline, candidate, 1, "bundle-policy", result); !errors.Is(err, contractversion.ErrIndeterminate) {
		t.Fatalf("indeterminate classifier result error = %v", err)
	}

	validBaseline := policyTestSourcePublication(t, "instance-policy", "1.0.0", false)
	validCandidate := policyTestSourcePublication(t, "instance-policy", "1.1.0", true)
	validResult, err := contractversion.ValidateVersionTransition(validBaseline.CanonicalBytes, validCandidate.CanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	validResult.StructuralCompatibility = ""
	if _, err := NewPolicyEvidence(PolicyContext{
		BaselineKind:              PolicyBaselineExisting,
		Baseline:                  policyTestPublicationIdentity(validBaseline),
		ExpectedLifecycleSequence: 1,
	}, &validBaseline, validCandidate, 1, "bundle-policy", validResult); !errors.Is(err, ErrPolicyEvidenceInvalid) || !errors.Is(err, contractversion.ErrInvalidResult) {
		t.Fatalf("missing classifier dimension error = %v", err)
	}
}

func TestPolicyEvidenceSameAndDifferentBaselinesHaveDistinctIdentity(t *testing.T) {
	baseline := policyTestSourcePublication(t, "instance-policy", "1.0.0", false)
	candidate := policyTestSourcePublication(t, "instance-policy", "1.1.0", true)
	result, err := contractversion.ValidateVersionTransition(baseline.CanonicalBytes, candidate.CanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	context := PolicyContext{BaselineKind: PolicyBaselineExisting, Baseline: policyTestPublicationIdentity(baseline), ExpectedLifecycleSequence: 1}
	first, err := NewPolicyEvidence(context, &baseline, candidate, 1, "bundle-policy", result)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPolicyEvidence(context, &baseline, candidate, 1, "bundle-policy", result)
	if err != nil {
		t.Fatal(err)
	}
	if first.EvidenceDigest != second.EvidenceDigest {
		t.Fatalf("same baseline changed digest: %q != %q", first.EvidenceDigest, second.EvidenceDigest)
	}

	differentBaseline := policyTestSourcePublication(t, "instance-policy", "1.0.1", false)
	differentResult, err := contractversion.ValidateVersionTransition(differentBaseline.CanonicalBytes, candidate.CanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	different, err := NewPolicyEvidence(PolicyContext{
		BaselineKind:              PolicyBaselineExisting,
		Baseline:                  policyTestPublicationIdentity(differentBaseline),
		ExpectedLifecycleSequence: 1,
	}, &differentBaseline, candidate, 1, "bundle-policy", differentResult)
	if err != nil {
		t.Fatal(err)
	}
	if first.EvidenceDigest == different.EvidenceDigest || EqualPolicyPublicationIdentity(first.Baseline, different.Baseline) {
		t.Fatalf("different baseline was not retained: first=%#v different=%#v", first.Baseline, different.Baseline)
	}
}

func TestPolicyEvidenceRejectsUnexpectedLifecycleSequence(t *testing.T) {
	baseline := policyTestSourcePublication(t, "instance-policy", "1.0.0", false)
	candidate := policyTestSourcePublication(t, "instance-policy", "1.1.0", true)
	result, err := contractversion.ValidateVersionTransition(baseline.CanonicalBytes, candidate.CanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewPolicyEvidence(PolicyContext{
		BaselineKind:              PolicyBaselineExisting,
		Baseline:                  policyTestPublicationIdentity(baseline),
		ExpectedLifecycleSequence: 2,
	}, &baseline, candidate, 1, "bundle-policy", result)
	if !errors.Is(err, ErrPolicyEvidenceConflict) {
		t.Fatalf("unexpected lifecycle sequence error = %v", err)
	}
}

func TestPolicyEvidenceRejectsMismatchedResourceIdentity(t *testing.T) {
	baseline := policyTestSourcePublication(t, "instance-policy", "1.0.0", false)
	candidate := policyTestSourcePublication(t, "instance-policy", "1.1.0", true)
	result, err := contractversion.ValidateVersionTransition(baseline.CanonicalBytes, candidate.CanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	wrongBaseline := policyTestPublicationIdentity(baseline)
	wrongBaseline.AuthoredID = "source:other"
	_, err = NewPolicyEvidence(PolicyContext{
		BaselineKind:              PolicyBaselineExisting,
		Baseline:                  wrongBaseline,
		ExpectedLifecycleSequence: 1,
	}, &baseline, candidate, 1, "bundle-policy", result)
	if !errors.Is(err, ErrPolicyEvidenceInvalid) {
		t.Fatalf("mismatched resource identity error = %v", err)
	}
}

func TestPolicyEvidenceRequiresExplicitGenesis(t *testing.T) {
	candidate := policyTestSourcePublication(t, "instance-policy", "1.0.0", true)
	result, err := contractversion.ClassifyInitial(candidate.CanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	genesis, err := NewPolicyEvidence(PolicyContext{
		BaselineKind:              PolicyBaselineGenesis,
		ExpectedLifecycleSequence: 1,
	}, nil, candidate, 1, "bundle-policy", result)
	if err != nil {
		t.Fatal(err)
	}
	if genesis.BaselineKind != PolicyBaselineGenesis || genesis.Baseline != (PolicyPublicationIdentity{}) {
		t.Fatalf("genesis retained a baseline: %#v", genesis)
	}

	if _, err := NewPolicyEvidence(PolicyContext{ExpectedLifecycleSequence: 1}, nil, candidate, 1, "bundle-policy", result); !errors.Is(err, ErrPolicyEvidenceInvalid) {
		t.Fatalf("implicit genesis was accepted: %v", err)
	}
	identity := policyTestPublicationIdentity(candidate)
	if _, err := NewPolicyEvidence(PolicyContext{
		BaselineKind:              PolicyBaselineGenesis,
		Baseline:                  identity,
		ExpectedLifecycleSequence: 1,
	}, nil, candidate, 1, "bundle-policy", result); !errors.Is(err, ErrPolicyEvidenceInvalid) {
		t.Fatalf("genesis with a baseline was accepted: %v", err)
	}
	if _, err := NewPolicyEvidence(PolicyContext{
		BaselineKind:              PolicyBaselineExisting,
		ExpectedLifecycleSequence: 1,
	}, nil, candidate, 1, "bundle-policy", result); !errors.Is(err, ErrPolicyEvidenceInvalid) {
		t.Fatalf("existing publication without a baseline was accepted: %v", err)
	}
}

func TestLegacyV1PublicationIsNotPolicyQualified(t *testing.T) {
	publication := policyTestSourcePublication(t, "instance-policy", "1.0.0", false)
	if publication.Validation.PolicyEvidence != nil {
		t.Fatal("legacy publication unexpectedly contains policy evidence")
	}
	if err := publication.Validation.Validate(); err != nil {
		t.Fatalf("legacy validation evidence is not readable: %v", err)
	}
	if _, err := publication.PolicyDecision(); !errors.Is(err, ErrPolicyEvidenceInvalid) {
		t.Fatalf("legacy v1 publication was qualified: %v", err)
	}
}

func TestPolicyDecisionRejectsTransplantedPublication(t *testing.T) {
	publication := policyTestExistingPublication(t)
	transplanted := publication
	transplanted.InstanceID = "instance-other"
	if _, err := transplanted.PolicyDecision(); !errors.Is(err, ErrPolicyEvidenceInvalid) {
		t.Fatalf("transplanted publication was accepted: %v", err)
	}
}

func TestPolicyDecisionIsDetachedFromPublicationEvidence(t *testing.T) {
	publication := policyTestExistingPublication(t)
	first, err := publication.PolicyDecision()
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Classification.Changes) == 0 || len(first.AffectedResources) == 0 || len(first.ChangedDimensions) == 0 {
		t.Fatalf("fixture lacks detachable decision data: %#v", first)
	}
	first.Classification.Changes[0].Path = "tampered"
	first.AffectedResources[0].AuthoredID = "source:tampered"
	first.ChangedDimensions[0] = contractversion.DomainSecurity
	second, err := publication.PolicyDecision()
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("decision mutation escaped publication boundary:\nwant=%s\ngot=%s", want, got)
	}
}

func TestPolicyEvidenceRejectsChangedCanonicalEnvelope(t *testing.T) {
	baseline := policyTestSourcePublication(t, "instance-policy", "1.0.0", false)
	candidate := policyTestSourcePublication(t, "instance-policy", "1.1.0", true)
	result, err := contractversion.ValidateVersionTransition(baseline.CanonicalBytes, candidate.CanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	changed := candidate
	changed.CanonicalBytes = bytes.Replace(changed.CanonicalBytes, []byte(`"version":"1.1.0"`), []byte(`"version":"1.1.1"`), 1)
	if bytes.Equal(changed.CanonicalBytes, candidate.CanonicalBytes) {
		t.Fatalf("test failed to change canonical envelope: %s", changed.CanonicalBytes)
	}
	_, err = NewPolicyEvidence(PolicyContext{
		BaselineKind:              PolicyBaselineExisting,
		Baseline:                  policyTestPublicationIdentity(baseline),
		ExpectedLifecycleSequence: 1,
	}, &baseline, changed, 1, "bundle-policy", result)
	if !errors.Is(err, ErrPolicyEvidenceInvalid) {
		t.Fatalf("changed canonical envelope was accepted: %v", err)
	}
}

func TestPolicyEvidenceAffectedResourcesRemainDirectOnlyAfterDigestRecompute(t *testing.T) {
	base := policyTestExistingEvidence(t)
	for _, test := range []struct {
		name      string
		resources []PolicyAffectedResource
	}{
		{name: "missing", resources: nil},
		{name: "extra", resources: append(append([]PolicyAffectedResource(nil), base.AffectedResources...), PolicyAffectedResource{
			InstanceID: base.Candidate.InstanceID, AuthoredID: "source:other", ResourceKind: base.Candidate.ResourceKind, Scope: PolicyAffectedResourceScope,
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := base
			evidence.AffectedResources = test.resources
			digest, err := evidence.computeDigest()
			if err != nil {
				t.Fatal(err)
			}
			evidence.EvidenceDigest = digest
			if err := evidence.Validate(); !errors.Is(err, ErrPolicyEvidenceInvalid) {
				t.Fatalf("%s affected-resource evidence was accepted: %v", test.name, err)
			}
		})
	}
}

func TestPolicyDecisionRepeatsForNoContentChangeVersion(t *testing.T) {
	baseline := policyTestSourcePublication(t, "instance-policy", "1.0.0", false)
	candidate := policyTestSourcePublication(t, "instance-policy", "1.1.0", false)
	result, err := contractversion.ValidateVersionTransition(baseline.CanonicalBytes, candidate.CanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 0 {
		t.Fatalf("version-only transition unexpectedly changed content: %#v", result)
	}
	context := PolicyContext{
		BaselineKind:              PolicyBaselineExisting,
		Baseline:                  policyTestPublicationIdentity(baseline),
		ExpectedLifecycleSequence: 1,
	}
	evidence, err := NewPolicyEvidence(context, &baseline, candidate, 1, "bundle-policy", result)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Validation.PolicyEvidence = &evidence
	first, err := candidate.PolicyDecision()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Classification.Changes) != 0 || len(first.ChangedDimensions) != 0 {
		t.Fatalf("empty content change was not preserved: %#v", first)
	}
	want, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.AffectedResources) == 0 {
		t.Fatal("no-content decision lost direct resource")
	}
	first.AffectedResources[0].Scope = "tampered"
	second, err := candidate.PolicyDecision()
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("repeated no-content decision changed:\nwant=%s\ngot=%s", want, got)
	}
}

func policyTestExistingEvidence(t *testing.T) PolicyEvidence {
	t.Helper()
	publication := policyTestExistingPublication(t)
	return *publication.Validation.PolicyEvidence
}

func policyTestExistingPublication(t *testing.T) ContractPublication {
	t.Helper()
	baseline := policyTestSourcePublication(t, "instance-policy", "1.0.0", false)
	candidate := policyTestSourcePublication(t, "instance-policy", "1.1.0", true)
	result, err := contractversion.ValidateVersionTransition(baseline.CanonicalBytes, candidate.CanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := NewPolicyEvidence(PolicyContext{
		BaselineKind:              PolicyBaselineExisting,
		Baseline:                  policyTestPublicationIdentity(baseline),
		ExpectedLifecycleSequence: 1,
	}, &baseline, candidate, 1, "bundle-policy", result)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Validation.PolicyEvidence = &evidence
	return candidate
}

func policyTestPublicationIdentity(publication ContractPublication) *PolicyPublicationIdentity {
	return &PolicyPublicationIdentity{
		InstanceID: publication.InstanceID, AuthoredID: publication.AuthoredID,
		ResourceKind: publication.ResourceKind, Version: publication.Version,
		VersionBaseline: publication.VersionBaseline, ProjectionProfile: publication.ProjectionProfile,
		Digest: publication.Digest,
	}
}

func policyTestSourcePublication(t *testing.T, instanceID, version string, note bool) ContractPublication {
	t.Helper()
	document := map[string]any{
		"apiVersion": "leapview.dev/v1",
		"kind":       "Source",
		"metadata":   map[string]any{"id": "source:orders", "name": "orders"},
		"spec": map[string]any{
			"connection": "connection:warehouse",
			"location":   map[string]any{"type": "path", "path": "/private/orders.csv", "format": "csv"},
			"schema": map[string]any{
				"mode": "strict",
				"fields": map[string]any{
					"order_id": map[string]any{"datatype": "String", "nullable": false},
				},
			},
		},
	}
	if note {
		document["spec"].(map[string]any)["schema"].(map[string]any)["fields"].(map[string]any)["note"] = map[string]any{"datatype": "String", "nullable": true}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var source projectcontracts.Source
	if err := json.Unmarshal(encoded, &source); err != nil {
		t.Fatal(err)
	}
	projection, err := contractprojection.ProjectSource(source, contractprojection.Contract{Version: version, Compatibility: "backward"})
	if err != nil {
		t.Fatal(err)
	}
	publication, err := PrepareContractPublication(ContractPublicationInput{
		InstanceID: instanceID,
		Projection: projection,
		Validation: policyTestValidationEvidence(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return publication
}

func policyTestSemanticPublication(t *testing.T, instanceID, version, userAttribute string, allowed []string) ContractPublication {
	t.Helper()
	values := make([]any, 0, len(allowed))
	for _, value := range allowed {
		values = append(values, value)
	}
	document := map[string]any{
		"apiVersion": "leapview.dev/v1",
		"kind":       "SemanticModel",
		"metadata":   map[string]any{"id": "semantic-model:sales", "name": "sales"},
		"spec": map[string]any{
			"datasets": map[string]any{
				"orders": map[string]any{"model": "orders", "requiredAccessGrants": []string{"region_access"}},
			},
			"accessGrants": map[string]any{
				"region_access": map[string]any{"userAttribute": userAttribute, "allowedValues": values},
			},
			"dimensions": map[string]any{},
			"filters":    map[string]any{},
			"metrics": map[string]any{
				"order_count": map[string]any{
					"type": "aggregate", "dataset": "orders", "aggregation": "count",
					"input": map[string]any{"field": "orders.order_id"},
				},
			},
		},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var semantic projectcontracts.SemanticModel
	if err := json.Unmarshal(encoded, &semantic); err != nil {
		t.Fatal(err)
	}
	projection, err := contractprojection.ProjectSemanticModel(semantic, contractprojection.Contract{Version: version, Compatibility: "backward"})
	if err != nil {
		t.Fatal(err)
	}
	publication, err := PrepareContractPublication(ContractPublicationInput{
		InstanceID: instanceID,
		Projection: projection,
		Validation: policyTestValidationEvidence(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return publication
}

func policyTestValidationEvidence() ValidationEvidence {
	return ValidationEvidence{Version: 1, Checks: []ValidationCheck{
		{Name: "projection", Outcome: ValidationPassed, Reference: "policy evidence fixture"},
	}}
}
