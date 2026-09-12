package module

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/project/contractprojection"
	"github.com/flidai/leapview/internal/project/contractpublication"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func modulePublication(t *testing.T, version string, optional bool) contractpublication.ContractPublication {
	t.Helper()
	fields := `"id":{"datatype":"Integer"}`
	if optional {
		fields += `,"name":{"datatype":"String","nullable":true}`
	}
	raw := `{"apiVersion":"leapview.dev/v1","kind":"Source","metadata":{"id":"source:orders","name":"orders"},"spec":{"connection":"warehouse","location":{"type":"path","path":"orders.csv","format":"csv"},"schema":{"mode":"strict","fields":{` + fields + `}}}}`
	var source projectcontracts.Source
	if err := json.Unmarshal([]byte(raw), &source); err != nil {
		t.Fatal(err)
	}
	projection, err := contractprojection.ProjectSource(source, contractprojection.Contract{Version: version, Compatibility: "backward"})
	if err != nil {
		t.Fatal(err)
	}
	publication, err := contractpublication.Prepare(contractpublication.ContractPublicationInput{
		InstanceID: "instance:test", Projection: projection,
		Validation: contractpublication.ValidationEvidence{Version: contractpublication.ValidationEvidenceVersion,
			Checks: []contractpublication.ValidationCheck{{Name: "projection", Outcome: contractpublication.ValidationPassed, Reference: "module test"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return publication
}

func moduleSemanticPublication(t *testing.T, version, allowedValues string) contractpublication.ContractPublication {
	t.Helper()
	raw := `{"apiVersion":"leapview.dev/v1","kind":"SemanticModel","metadata":{"id":"semantic:orders","name":"orders"},"spec":{"datasets":{"orders":{"model":"orders","requiredAccessGrants":["region"],"accessFilters":[{"field":"region","userAttribute":"region"}],"metrics":{"orders":{"type":"simple","agg":"count","field":"id","requiredAccessGrants":["region"]}}}},"accessGrants":{"region":{"userAttribute":"region","allowedValues":` + allowedValues + `}},"dimensions":{"region":{"datatype":"String","bindings":{"orders":{"field":"orders.region"}},"requiredAccessGrants":["region"]}}}}`
	var model projectcontracts.SemanticModel
	if err := json.Unmarshal([]byte(raw), &model); err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	references, err := contractprojection.NewReferenceContext(graph)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := contractprojection.ProjectSemanticModel(model, contractprojection.Contract{Version: version, Compatibility: "backward"}, references)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := contractpublication.Prepare(contractpublication.ContractPublicationInput{
		InstanceID: "instance:test", Projection: projection,
		Validation: contractpublication.ValidationEvidence{Version: contractpublication.ValidationEvidenceVersion,
			Checks: []contractpublication.ValidationCheck{{Name: "projection", Outcome: contractpublication.ValidationPassed, Reference: "module test"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return publication
}

func TestPublicationPolicyIdentityAdapterIsDeterministicAndExact(t *testing.T) {
	genesis := modulePublication(t, "1.0.0", false)
	genesisPolicy, err := contractpublication.DeriveGenesisPolicyEvidence(genesis)
	if err != nil {
		t.Fatal(err)
	}
	genesis, err = contractpublication.AttachPolicyEvidence(contractpublication.PolicyContext{BaselineKind: contractpublication.BaselineGenesis}, genesis, genesisPolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	genesisIdentity, err := PublicationPolicyIdentityFromContractPublication(genesis, nil)
	if err != nil {
		t.Fatal(err)
	}
	if genesisIdentity.Policy.BaselineKind != "genesis" || genesisIdentity.Policy.Baseline != (resultidentity.PublicationIdentity{}) {
		t.Fatalf("genesis retained a baseline: %#v", genesisIdentity.Policy)
	}

	baseline := modulePublication(t, "1.0.0", false)
	candidate := modulePublication(t, "1.1.0", true)
	policy, err := contractpublication.DeriveUpdatePolicyEvidence(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err = contractpublication.AttachPolicyEvidence(contractpublication.PolicyContext{BaselineKind: contractpublication.BaselineExisting, Existing: &baseline}, candidate, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := PublicationPolicyIdentityFromContractPublication(candidate, &baseline)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PublicationPolicyIdentityFromContractPublication(candidate, &baseline)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("equivalent evidence mapped differently: %#v %#v", first, second)
	}
	if first.Candidate.Digest != candidate.Digest || first.Policy.Baseline.Digest != baseline.Digest || first.Policy.PolicyEvidenceDigest != policy.EvidenceDigest {
		t.Fatalf("adapter did not retain exact publication/policy identity: %#v", first)
	}
	if first.Policy.ApprovalEvidenceDigest != "" {
		t.Fatal("non-widening publication unexpectedly retained approval evidence")
	}
}

func TestPublicationPolicyIdentityAdapterRequiresExactUpdateBaseline(t *testing.T) {
	baseline := modulePublication(t, "1.0.0", false)
	candidate := modulePublication(t, "1.1.0", true)
	policy, err := contractpublication.DeriveUpdatePolicyEvidence(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err = contractpublication.AttachPolicyEvidence(contractpublication.PolicyContext{BaselineKind: contractpublication.BaselineExisting, Existing: &baseline}, candidate, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	wrong := modulePublication(t, "1.0.0", true)
	if _, err := PublicationPolicyIdentityFromContractPublication(candidate, &wrong); err == nil {
		t.Fatal("candidate was accepted with a different baseline")
	}
	if _, err := PublicationPolicyIdentityFromContractPublication(candidate, nil); err == nil {
		t.Fatal("update was accepted as genesis")
	}
	missingPolicy := candidate.Clone()
	missingPolicy.Validation.PolicyEvidence = nil
	if _, err := PublicationPolicyIdentityFromContractPublication(missingPolicy, &baseline); err == nil {
		t.Fatal("publication without policy evidence was accepted")
	}
}

func TestPublicationPolicyIdentityAdapterBindsWideningApproval(t *testing.T) {
	baseline := moduleSemanticPublication(t, "1.0.0", `["east"]`)
	candidate := moduleSemanticPublication(t, "1.1.0", `["east","west"]`)
	context := contractpublication.PolicyContext{BaselineKind: contractpublication.BaselineExisting, Existing: &baseline}
	policy, err := contractpublication.DeriveUpdatePolicyEvidence(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.RequiresSecurityApproval || policy.Classification.SecurityImpact != "widening" {
		t.Fatalf("fixture did not produce widening classification: %#v", policy.Classification)
	}
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	approval, err := contractpublication.PrepareWideningApproval(policy, "principal:reviewer", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	qualified, err := contractpublication.AttachPolicyEvidence(context, candidate, policy, &approval)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := PublicationPolicyIdentityFromContractPublication(qualified, &baseline)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Policy.ApprovalEvidenceDigest != approval.EvidenceDigest {
		t.Fatalf("approval evidence was not bound: %#v", identity.Policy)
	}
	noApproval := qualified.Clone()
	noApproval.Validation.ApprovalEvidence = nil
	if _, err := PublicationPolicyIdentityFromContractPublication(noApproval, &baseline); !errors.Is(err, contractpublication.ErrApprovalRequired) {
		t.Fatalf("missing widening approval error = %v", err)
	}
	wrongApproval := approval.Clone()
	wrongApproval.PolicyDigest = baseline.Digest
	noApproval = qualified.Clone()
	noApproval.Validation.ApprovalEvidence = &wrongApproval
	if _, err := PublicationPolicyIdentityFromContractPublication(noApproval, &baseline); err == nil {
		t.Fatal("approval bound to a different policy was accepted")
	}
}

func TestPublicationPolicyIdentityAdapterRejectsTamperedAndIndeterminateEvidence(t *testing.T) {
	baseline := modulePublication(t, "1.0.0", false)
	candidate := modulePublication(t, "1.1.0", true)
	policy, err := contractpublication.DeriveUpdatePolicyEvidence(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	qualified, err := contractpublication.AttachPolicyEvidence(contractpublication.PolicyContext{BaselineKind: contractpublication.BaselineExisting, Existing: &baseline}, candidate, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	tampered := qualified.Clone()
	tampered.Validation.PolicyEvidence.EvidenceDigest = baseline.Digest
	if _, err := PublicationPolicyIdentityFromContractPublication(tampered, &baseline); err == nil {
		t.Fatal("tampered policy evidence was accepted")
	}
	tampered = qualified.Clone()
	tampered.CanonicalBytes[len(tampered.CanonicalBytes)-1] ^= 1
	if _, err := PublicationPolicyIdentityFromContractPublication(tampered, &baseline); err == nil {
		t.Fatal("tampered publication bytes were accepted")
	}
	if _, err := PublicationPolicyIdentityFromContractPublication(qualified, nil); err == nil {
		t.Fatal("indeterminate baseline context was accepted")
	}
}

func TestProtectedDependencyRotatesWhenPublicationPolicyIdentityChanges(t *testing.T) {
	identity := resultidentity.SemanticAccessIdentity{
		ProjectID: "project:test", Environment: "prod", InstanceID: "instance:test", ModelID: "semantic:orders",
		Generation: "generation:test", PrincipalID: "principal:test", Profile: semanticvalue.Profile,
		RegistryProfile: semanticvalue.Profile, RegistryRevision: 1, RegistryDigest: moduleDigest('a'),
		ControlProfile: semanticvalue.Profile, ControlRevision: 1, ControlDigest: moduleDigest('b'),
		EffectiveAttributeDigest: moduleDigest('c'), PolicyDigest: moduleDigest('d'), DecisionDigest: moduleDigest('e'),
		PublicationPolicy: resultidentity.PublicationPolicyIdentity{
			Candidate: resultidentity.PublicationIdentity{InstanceID: "instance:test", AuthoredID: "semantic:orders", ResourceKind: "semantic_model", Version: "1.0.0", VersionBaseline: "1.0.0", ProjectionProfile: "leapview.contract/v1", Digest: moduleDigest('f')},
			Policy:    resultidentity.PolicyIdentity{BaselineKind: "genesis", Class: "compatible", Compatibility: "additive", StructuralCompatibility: "additive", SemanticCompatibility: "additive", SecurityImpact: "none", PolicyEvidenceDigest: moduleDigest('0')},
		},
	}
	baseInput := resultidentity.DependencyInput{SemanticModelID: "semantic:orders", SemanticModelDigest: moduleDigest('1'), Relations: []resultidentity.RelationRevision{{RelationID: "model:orders", RevisionDigest: moduleDigest('2')}}, BindingFingerprint: moduleDigest('3'), Execution: resultidentity.ExecutionIdentity{PlannerDigest: moduleDigest('4'), RuntimeDigest: moduleDigest('5'), CapabilityDigest: moduleDigest('6'), SettingsDigest: moduleDigest('7')}, ResultFormat: resultidentity.ResultFormat{Name: "arrow-ipc", Version: 1}, SemanticAccess: &identity}
	base, err := resultidentity.NewDependency(baseInput)
	if err != nil {
		t.Fatal(err)
	}
	changedPublication := identity
	changedPublication.PublicationPolicy.Candidate.Version = "1.1.0"
	changedPublication.PublicationPolicy.Candidate.VersionBaseline = "1.1.0"
	changedPublication.PublicationPolicy.Candidate.Digest = moduleDigest('8')
	changedPublication.PublicationPolicy.Policy.PolicyEvidenceDigest = moduleDigest('9')
	changed, err := resultidentity.NewDependency(resultidentity.DependencyInput{SemanticModelID: "semantic:orders", SemanticModelDigest: moduleDigest('1'), Relations: []resultidentity.RelationRevision{{RelationID: "model:orders", RevisionDigest: moduleDigest('2')}}, BindingFingerprint: moduleDigest('3'), Execution: resultidentity.ExecutionIdentity{PlannerDigest: moduleDigest('4'), RuntimeDigest: moduleDigest('5'), CapabilityDigest: moduleDigest('6'), SettingsDigest: moduleDigest('7')}, ResultFormat: resultidentity.ResultFormat{Name: "arrow-ipc", Version: 1}, SemanticAccess: &changedPublication})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(base.Canonical(), changed.Canonical()) || base.Digest() == changed.Digest() {
		t.Fatal("publication/policy identity change did not rotate protected dependency")
	}
}

func moduleDigest(value byte) string {
	const hex = "0123456789abcdef"
	return "sha256:" + string(bytes.Repeat([]byte{hex[value&0x0f]}, 64))
}
