package architecture

import "testing"

func TestSemanticValueIsAPublishedAccessDependency(t *testing.T) {
	source, sourceOK := ClassifyPackage("internal/access")
	target, targetOK := ClassifyPackage("internal/semanticvalue")
	if !sourceOK || !targetOK {
		t.Fatalf("classify access=%v semanticvalue=%v", sourceOK, targetOK)
	}
	if violation := CapabilityImportViolation("internal/access", source, "internal/semanticvalue", target); violation != "" {
		t.Fatalf("access -> semanticvalue violation = %q, want published contract", violation)
	}
}
