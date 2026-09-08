package architecture

import "testing"

func TestRecoveryObservationUsesOneWayInventoryContract(t *testing.T) {
	if !CapabilityDependencies["recoveryset"]["manageddata"] || CapabilityDependencies["manageddata"]["recoveryset"] {
		t.Fatal("recovery must consume managed inventory without a reverse dependency")
	}
	source, _ := ClassifyPackage("internal/recoveryset/postgres")
	contract, _ := ClassifyPackage("internal/manageddata")
	if violation := CapabilityImportViolation("internal/recoveryset/postgres", source, "internal/manageddata", contract); violation != "" {
		t.Fatal(violation)
	}
	adapter, _ := ClassifyPackage("internal/manageddata/postgres")
	if violation := CapabilityImportViolation("internal/recoveryset/postgres", source, "internal/manageddata/postgres", adapter); violation == "" {
		t.Fatal("recovery must not bypass managed-data contract ownership")
	}
}
