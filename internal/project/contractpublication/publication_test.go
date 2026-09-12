package contractpublication

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/project/contractprojection"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func testSource(t *testing.T, version string, optional bool) contractprojection.Source {
	t.Helper()
	var source projectcontracts.Source
	fields := `"id":{"datatype":"Integer"}`
	if optional {
		fields += `,"name":{"datatype":"String","nullable":true}`
	}
	raw := `{"apiVersion":"leapview.dev/v1","kind":"Source","metadata":{"id":"source:orders","name":"orders"},"spec":{"connection":"warehouse","location":{"type":"path","path":"orders.csv","format":"csv"},"schema":{"mode":"strict","fields":{` + fields + `}}}}`
	if err := json.Unmarshal([]byte(raw), &source); err != nil {
		t.Fatal(err)
	}
	projection, err := contractprojection.ProjectSource(source, contractprojection.Contract{Version: version, Compatibility: "backward"})
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

func testValidation() ValidationEvidence {
	return ValidationEvidence{Version: ValidationEvidenceVersion, Checks: []ValidationCheck{
		{Name: "z-check", Outcome: ValidationWarning, Reference: "z"},
		{Name: "a-check", Outcome: ValidationPassed, Reference: "a"},
	}}
}

func testPublication(t *testing.T, version string, optional bool) ContractPublication {
	t.Helper()
	publication, err := Prepare(ContractPublicationInput{
		InstanceID: "instance:test", Projection: testSource(t, version, optional), Validation: testValidation(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return publication
}

func TestPrepareUsesFAI620CanonicalBytesAndDigest(t *testing.T) {
	projection := testSource(t, "1.0.0", false)
	publication, err := Prepare(ContractPublicationInput{InstanceID: "instance:test", Projection: projection, Validation: testValidation()})
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, err := contractprojection.CanonicalBytes(projection)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := contractprojection.DigestSourcePublication(wantBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(publication.CanonicalBytes, wantBytes) || publication.Digest != wantDigest {
		t.Fatalf("publication did not retain FAI-620 identity: digest=%q want=%q", publication.Digest, wantDigest)
	}
	if publication.VersionBaseline != "1.0.0" || publication.ResourceKind != projectgraph.KindSource || publication.ProjectionProfile != contractprojection.Profile {
		t.Fatalf("derived identity = %#v", publication.Identity())
	}
	if publication.Validation.Checks[0].Name != "a-check" || publication.Validation.Checks[1].Name != "z-check" {
		t.Fatalf("validation checks were not normalized: %#v", publication.Validation.Checks)
	}
	copyBytes := publication.Canonical()
	copyBytes[0] ^= 0xff
	if bytes.Equal(copyBytes, publication.CanonicalBytes) {
		t.Fatal("canonical bytes were not defensively copied")
	}
	for _, evidence := range []ValidationEvidence{{Version: 1, Checks: testValidation().Checks, PolicyEvidence: &PolicyEvidence{}}, {Version: 1, Checks: testValidation().Checks, ApprovalEvidence: &WideningApprovalEvidence{}}} {
		if _, err := Prepare(ContractPublicationInput{InstanceID: "instance:test", Projection: projection, Validation: evidence}); err == nil {
			t.Fatal("accepted caller-supplied server-derived evidence")
		}
	}
}

func TestGenesisAndUpdatePolicyEvidenceAreDistinctAndDeterministic(t *testing.T) {
	genesis := testPublication(t, "1.0.0", false)
	candidate := testPublication(t, "1.1.0", true)
	first, err := DeriveGenesisPolicyEvidence(genesis)
	if err != nil {
		t.Fatal(err)
	}
	if first.BaselineKind != BaselineGenesis || first.Baseline != (PublicationIdentity{}) {
		t.Fatalf("genesis evidence = %#v", first)
	}
	update, err := DeriveUpdatePolicyEvidence(genesis, candidate)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := DeriveUpdatePolicyEvidence(genesis, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if update.BaselineKind != BaselineExisting || !EqualPolicyEvidence(update, repeated) || update.EvidenceDigest == "" {
		t.Fatalf("update evidence is not deterministic: %#v %#v", update, repeated)
	}
	if _, err := DerivePolicyEvidence(PolicyContext{}, candidate); err == nil {
		t.Fatal("missing baseline kind was inferred")
	}
	if _, err := DerivePolicyEvidence(PolicyContext{BaselineKind: BaselineGenesis, Existing: &genesis}, candidate); err == nil {
		t.Fatal("genesis accepted an existing baseline")
	}
	if err := ValidatePolicyEvidenceAgainst(PolicyContext{BaselineKind: BaselineExisting, Existing: &genesis}, candidate, update); err != nil {
		t.Fatalf("exact policy evidence did not replay: %v", err)
	}
}

func TestPublicationAndPolicyTamperingFailsClosed(t *testing.T) {
	baseline := testPublication(t, "1.0.0", false)
	candidate := testPublication(t, "1.1.0", true)
	policy, err := DeriveUpdatePolicyEvidence(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	mutated := candidate.Clone()
	mutated.CanonicalBytes[len(mutated.CanonicalBytes)-1] ^= 1
	if err := mutated.Validate(); err == nil {
		t.Fatal("tampered canonical bytes were accepted")
	}
	mutatedPolicy := policy.Clone()
	mutatedPolicy.Candidate.Digest = baseline.Digest
	if err := mutatedPolicy.Validate(); err == nil {
		t.Fatal("tampered candidate identity was accepted")
	}
	if err := ValidatePolicyEvidenceAgainst(PolicyContext{BaselineKind: BaselineExisting, Existing: &candidate}, candidate, policy); err == nil {
		t.Fatal("baseline substitution was accepted")
	}
	if !EqualContractPublicationContent(candidate, candidate.Clone()) {
		t.Fatal("replayed content was not equal")
	}
	withTimestamp := candidate.Clone()
	withTimestamp.PublishedAt = time.Unix(42, 0).UTC()
	if !EqualContractPublicationContent(candidate, withTimestamp) {
		t.Fatal("publication timestamp changed content equality")
	}
	wrongProfile := candidate.Identity()
	wrongProfile.ProjectionProfile = "leapview.contract/v2"
	if err := wrongProfile.Validate(); err == nil {
		t.Fatal("unsupported publication profile was accepted")
	}

	alternateBaseline := testPublication(t, "1.0.0", true)
	if _, err := AttachPolicyEvidence(PolicyContext{BaselineKind: BaselineExisting, Existing: &alternateBaseline}, candidate, policy, nil); err == nil {
		t.Fatal("policy evidence was attached against a substituted baseline")
	}
}

func TestValidationEvidenceRejectsMissingDuplicateAndFailedChecks(t *testing.T) {
	for name, evidence := range map[string]ValidationEvidence{
		"missing checks": {Version: ValidationEvidenceVersion},
		"duplicate checks": {Version: ValidationEvidenceVersion, Checks: []ValidationCheck{
			{Name: "projection", Outcome: ValidationPassed, Reference: "one"},
			{Name: "projection", Outcome: ValidationPassed, Reference: "two"},
		}},
		"failed check": {Version: ValidationEvidenceVersion, Checks: []ValidationCheck{
			{Name: "projection", Outcome: ValidationOutcome("failed"), Reference: "test"},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeValidationEvidence(evidence); err == nil {
				t.Fatal("invalid validation evidence was accepted")
			}
		})
	}
}

func semanticPublication(t *testing.T, version, allowed string) ContractPublication {
	t.Helper()
	var input projectcontracts.SemanticModel
	raw := `{"apiVersion":"leapview.dev/v1","kind":"SemanticModel","metadata":{"id":"semantic:orders","name":"orders_semantic"},"spec":{"datasets":{"orders":{"model":"orders_model","requiredAccessGrants":["region"],"accessFilters":[{"field":"region","userAttribute":"region"}],"metrics":{"orders":{"type":"simple","agg":"count","field":"id","requiredAccessGrants":["region"]}}}},"accessGrants":{"region":{"userAttribute":"region","allowedValues":` + allowed + `}},"dimensions":{"region":{"datatype":"String","bindings":{"orders":{"field":"orders.region"}},"requiredAccessGrants":["region"]}}}}`
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "model:orders", Name: "orders_model", Kind: projectgraph.KindModel}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	context, err := contractprojection.NewReferenceContext(graph)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := contractprojection.ProjectSemanticModel(input, contractprojection.Contract{Version: version, Compatibility: "backward"}, context)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := Prepare(ContractPublicationInput{InstanceID: "instance:test", Projection: projection, Validation: testValidation()})
	if err != nil {
		t.Fatal(err)
	}
	return publication
}

func TestSecurityWideningRequiresExactUnexpiredApproval(t *testing.T) {
	baseline := semanticPublication(t, "1.0.0", `["east"]`)
	candidate := semanticPublication(t, "1.1.0", `["east","west"]`)
	policy, err := DeriveUpdatePolicyEvidence(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.RequiresSecurityApproval || policy.Classification.SecurityImpact != "widening" {
		t.Fatalf("policy did not require widening approval: %#v", policy.Classification)
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	context := PolicyContext{BaselineKind: BaselineExisting, Existing: &baseline}
	if err := ValidateAdmission(context, candidate, policy, nil, now); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("missing approval error = %v", err)
	}
	approval, err := PrepareWideningApproval(policy, "principal:reviewer", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAdmission(context, candidate, policy, &approval, now); err != nil {
		t.Fatalf("valid approval rejected: %v", err)
	}
	if _, err := AttachPolicyEvidence(context, candidate, policy, nil); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("attach without required approval error = %v", err)
	}
	qualified, err := AttachPolicyEvidence(context, candidate, policy, &approval)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateQualifiedPublication(context, qualified, now); err != nil {
		t.Fatalf("qualified publication rejected: %v", err)
	}
	if err := ValidateHistoricalPublication(context, qualified); err != nil {
		t.Fatalf("historical qualified publication rejected: %v", err)
	}
	tamperedQualified := qualified.Clone()
	tamperedQualified.Validation.PolicyEvidence.Candidate.Digest = baseline.Digest
	if err := ValidateHistoricalPublication(context, tamperedQualified); err == nil {
		t.Fatal("nested policy candidate substitution was accepted")
	}
	if err := approval.Validate(); err != nil {
		t.Fatalf("expired historical evidence became unverifiable: %v", err)
	}
	if err := ValidateHistoricalPublication(context, qualified); err != nil {
		t.Fatalf("historical expired evidence became unverifiable: %v", err)
	}
	if err := ValidateQualifiedPublication(context, qualified, now.Add(2*time.Hour)); !errors.Is(err, ErrApprovalExpired) {
		t.Fatalf("qualified expired approval error = %v", err)
	}
	if err := ValidateAdmission(context, candidate, policy, &approval, now.Add(2*time.Hour)); !errors.Is(err, ErrApprovalExpired) {
		t.Fatalf("expired approval error = %v", err)
	}
	wrong := approval.Clone()
	wrong.PolicyDigest = baseline.Digest
	if err := ValidateAdmission(context, candidate, policy, &wrong, now); err == nil {
		t.Fatal("unrelated approval was accepted")
	}
	nonWideningCandidate := semanticPublication(t, "1.1.0", `["east"]`)
	nonWidening, err := DeriveUpdatePolicyEvidence(baseline, nonWideningCandidate)
	if err != nil {
		t.Fatal(err)
	}
	if nonWidening.RequiresSecurityApproval {
		t.Fatal("optional field update unexpectedly required security approval")
	}
	if err := ValidateAdmission(PolicyContext{BaselineKind: BaselineExisting, Existing: &baseline}, nonWideningCandidate, nonWidening, &approval, now); !errors.Is(err, ErrApprovalUnexpected) {
		t.Fatalf("unrelated approval on non-widening update error = %v", err)
	}
}
