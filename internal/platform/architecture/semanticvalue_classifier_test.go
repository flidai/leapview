package architecture

import "testing"

func TestContractClassifierReusesSemanticValueAuthority(t *testing.T) {
	source, sourceOK := ClassifyPackage("internal/project/contractversion")
	target, targetOK := ClassifyPackage("internal/semanticvalue")
	if !sourceOK || !targetOK || target.Layer != LayerContract {
		t.Fatal("classifier or shared semantic value contract is not classified")
	}
	if violation := CapabilityImportViolation("internal/project/contractversion", source, "internal/semanticvalue", target); violation != "" {
		t.Fatalf("classifier must use the existing semantic value authority: %s", violation)
	}
}
