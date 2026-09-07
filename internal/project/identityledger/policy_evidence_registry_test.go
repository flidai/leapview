package identityledger

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/project/contractversion"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func policyRegistryFixture() contractversion.SemanticRegistryTypes {
	return contractversion.SemanticRegistryTypes{
		InstanceID: "instance-policy", ProjectID: "project:policy", ControlRevision: 3,
		Profile: semanticvalue.Profile, Revision: 4, Digest: "sha256:" + strings.Repeat("a", 64),
		Definitions: []contractversion.RegisteredSemanticType{{
			ID: "definition:region", Name: "region", Type: semanticvalue.TypeString,
			Shape: "scalar", Version: 2, Enabled: true,
		}},
	}
}

func policyRegistryReference(types contractversion.SemanticRegistryTypes) *PolicyRegistryReference {
	return &PolicyRegistryReference{
		InstanceID: types.InstanceID, ProjectID: "project:policy", ControlRevision: types.ControlRevision,
		Profile: types.Profile, Revision: types.Revision, Digest: types.Digest,
	}
}

func TestRegisteredPolicyEvidencePreservesTypesApprovalAndReplay(t *testing.T) {
	base := policyTestSemanticPublication(t, "instance-policy", "1.0.0", "region", []string{"emea"})
	next := policyTestSemanticPublication(t, "instance-policy", "1.1.0", "region", []string{"emea", "amer"})
	types := policyRegistryFixture()
	context := PolicyContext{BaselineKind: PolicyBaselineExisting, Baseline: policyTestPublicationIdentity(base), ExpectedLifecycleSequence: 7, ExpectedRegistry: policyRegistryReference(types)}
	evidence, err := DerivePolicyEvidence(context, &base, next, 7, "bundle-policy", types)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Version != RegistryPolicyEvidenceVersion || evidence.RegistryTypes == nil || !reflect.DeepEqual(*evidence.RegistryTypes, types) || evidence.ApprovalState != PolicyApprovalRequired {
		t.Fatalf("typed evidence lost identity or security dimensions: %+v", evidence)
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	var restored PolicyEvidence
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.Validate(); err != nil || !reflect.DeepEqual(evidence, restored) || !EqualPolicyContextEvidence(context, &restored) {
		t.Fatalf("retained replay differs: %v", err)
	}
	changed := *context.ExpectedRegistry
	changed.Revision++
	context.ExpectedRegistry = &changed
	if EqualPolicyContextEvidence(context, &restored) {
		t.Fatal("conflicting retry registry revision accepted")
	}
	// A caller cannot mutate the evidence through its original type slice.
	types.Definitions[0].Version++
	if evidence.RegistryTypes.Definitions[0].Version != 2 {
		t.Fatal("derived evidence aliases classifier inputs")
	}
	decision, err := evidence.Decision()
	if err != nil {
		t.Fatal(err)
	}
	decision.RegistryTypes.Definitions[0].Version++
	if evidence.RegistryTypes.Definitions[0].Version != 2 {
		t.Fatal("decision aliases retained evidence")
	}
	normalized, err := normalizePolicyEvidence(evidence)
	if err != nil {
		t.Fatal(err)
	}
	normalized.RegistryTypes.Definitions[0].Version++
	if evidence.RegistryTypes.Definitions[0].Version != 2 {
		t.Fatal("normalized publication aliases retained evidence")
	}
}

func TestRegisteredPolicyEvidenceRejectsMissingStaleAndTransplantedTypes(t *testing.T) {
	base := policyTestSemanticPublication(t, "instance-policy", "1.0.0", "region", []string{"emea"})
	next := policyTestSemanticPublication(t, "instance-policy", "1.1.0", "region", []string{"emea", "amer"})
	for _, name := range []string{"missing-types", "missing-reference", "stale-control", "stale-registry", "cross-instance", "cross-project", "wrong-type", "missing-definition", "extra-definition"} {
		t.Run(name, func(t *testing.T) {
			types := policyRegistryFixture()
			context := PolicyContext{BaselineKind: PolicyBaselineExisting, Baseline: policyTestPublicationIdentity(base), ExpectedLifecycleSequence: 7, ExpectedRegistry: policyRegistryReference(types)}
			inputs := []contractversion.SemanticRegistryTypes{types}
			switch name {
			case "missing-types":
				inputs = nil
			case "missing-reference":
				context.ExpectedRegistry = nil
			case "stale-control":
				inputs[0].ControlRevision++
			case "stale-registry":
				inputs[0].Revision++
			case "cross-instance":
				inputs[0].InstanceID = "instance-other"
			case "cross-project":
				inputs[0].ProjectID = "project:other"
			case "wrong-type":
				inputs[0].Definitions[0].Type = semanticvalue.TypeBoolean
			case "missing-definition":
				inputs[0].Definitions = nil
			case "extra-definition":
				inputs[0].Definitions = append([]contractversion.RegisteredSemanticType{{ID: "definition:department", Name: "department", Type: semanticvalue.TypeString, Shape: "scalar", Version: 1, Enabled: true}}, inputs[0].Definitions...)
			}
			if _, err := DerivePolicyEvidence(context, &base, next, 7, "bundle-policy", inputs...); err == nil {
				t.Fatalf("invalid %s accepted", name)
			}
		})
	}
}

func TestRegisteredPolicyEvidenceVersionAndDigestCannotBeDowngraded(t *testing.T) {
	base := policyTestSemanticPublication(t, "instance-policy", "1.0.0", "region", []string{"emea"})
	next := policyTestSemanticPublication(t, "instance-policy", "1.1.0", "region", []string{"emea", "amer"})
	types := policyRegistryFixture()
	context := PolicyContext{BaselineKind: PolicyBaselineExisting, Baseline: policyTestPublicationIdentity(base), ExpectedLifecycleSequence: 1, ExpectedRegistry: policyRegistryReference(types)}
	evidence, err := DerivePolicyEvidence(context, &base, next, 1, "bundle-policy", types)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"v2-with-types", "v3-without-types", "changed-definition", "changed-registry"} {
		t.Run(name, func(t *testing.T) {
			changed, err := normalizePolicyEvidence(evidence)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "v2-with-types":
				changed.Version = PolicyEvidenceVersion
			case "v3-without-types":
				changed.RegistryTypes = nil
			case "changed-definition":
				changed.RegistryTypes.Definitions[0].Version++
			case "changed-registry":
				changed.RegistryTypes.Digest = "sha256:" + strings.Repeat("b", 64)
			}
			if err := changed.Validate(); !errors.Is(err, ErrPolicyEvidenceInvalid) {
				t.Fatalf("tampered %s accepted: %v", name, err)
			}
		})
	}
}

func TestPolicyEvidenceReplayDoesNotSynthesizeHistoricalRegistryContext(t *testing.T) {
	legacy := policyTestExistingEvidence(t)
	context := PolicyContext{BaselineKind: legacy.BaselineKind, Baseline: &legacy.Baseline, ExpectedLifecycleSequence: legacy.LifecycleSequence}
	if !EqualPolicyContextEvidence(context, &legacy) {
		t.Fatal("unchanged historical context is not replayable")
	}
	context.ExpectedRegistry = policyRegistryReference(policyRegistryFixture())
	if EqualPolicyContextEvidence(context, &legacy) {
		t.Fatal("historical evidence acquired caller-supplied registry context")
	}
	context.ExpectedRegistry = nil
	context.ExpectedLifecycleSequence++
	if EqualPolicyContextEvidence(context, &legacy) {
		t.Fatal("stale lifecycle retry accepted")
	}
}

func TestRegisteredPolicyEvidencePreservesRetainedTypeIdentity(t *testing.T) {
	older := policyTestSemanticPublication(t, "instance-policy", "0.9.0", "region", []string{"emea"})
	base := policyTestSemanticPublication(t, "instance-policy", "1.0.0", "region", []string{"emea"})
	next := policyTestSemanticPublication(t, "instance-policy", "1.1.0", "region", []string{"emea", "amer"})
	types := policyRegistryFixture()
	baseContext := PolicyContext{BaselineKind: PolicyBaselineExisting, Baseline: policyTestPublicationIdentity(older), ExpectedLifecycleSequence: 1, ExpectedRegistry: policyRegistryReference(types)}
	retained, err := DerivePolicyEvidence(baseContext, &older, base, 1, "bundle-policy", types)
	if err != nil {
		t.Fatal(err)
	}
	base.Validation.PolicyEvidence = &retained
	for _, name := range []string{"metadata-advance", "new-id", "new-shape", "version-regression", "registry-regression"} {
		t.Run(name, func(t *testing.T) {
			current := types.Clone()
			switch name {
			case "metadata-advance":
				current.Revision++
				current.Definitions[0].Version++
			case "new-id":
				current.Definitions[0].ID = "definition:replacement"
			case "new-shape":
				current.Definitions[0].Shape = "list"
			case "version-regression":
				current.Definitions[0].Version--
			case "registry-regression":
				current.Revision--
			}
			context := PolicyContext{BaselineKind: PolicyBaselineExisting, Baseline: policyTestPublicationIdentity(base), ExpectedLifecycleSequence: 1, ExpectedRegistry: policyRegistryReference(current)}
			_, err := DerivePolicyEvidence(context, &base, next, 1, "bundle-policy", current)
			if name == "metadata-advance" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrPolicyEvidenceConflict) {
				t.Fatalf("retained %s accepted: %v", name, err)
			}
		})
	}
}
