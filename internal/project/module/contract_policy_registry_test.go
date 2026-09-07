package module

import (
	"testing"

	"github.com/flidai/leapview/internal/project/contractversion"
	identitymodule "github.com/flidai/leapview/internal/project/identityledger/module"
)

func TestContractPolicyDecisionDetachesRegisteredTypeEvidence(t *testing.T) {
	decision := identitymodule.PolicyDecision{RegistryTypes: &contractversion.SemanticRegistryTypes{
		Revision:    4,
		Definitions: []contractversion.RegisteredSemanticType{{ID: "definition:region", Version: 2}},
	}}
	clone := clonePolicyDecision(decision)
	clone.RegistryTypes.Revision++
	clone.RegistryTypes.Definitions[0].Version++
	if decision.RegistryTypes.Revision != 4 || decision.RegistryTypes.Definitions[0].Version != 2 {
		t.Fatal("planner-facing decision aliases immutable registered type evidence")
	}
	if clonePolicyDecision(identitymodule.PolicyDecision{}).RegistryTypes != nil {
		t.Fatal("historical decision acquired synthetic registered type evidence")
	}
}
