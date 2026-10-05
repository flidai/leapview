package architecture

import "testing"

func TestCredentialDraftPackagesRemainCapabilityOwnedAndNarrow(t *testing.T) {
	tests := []struct {
		path string
		want Layer
	}{
		{path: "internal/credential", want: LayerUseCase},
		{path: "internal/credential/api/gen", want: LayerAdapter},
		{path: "internal/credential/module", want: LayerModule},
		{path: "internal/credential/encryption", want: LayerUseCase},
		{path: "internal/credential/postgres", want: LayerAdapter},
		{path: "internal/credential/postgres/internal/db", want: LayerAdapter},
	}
	for _, test := range tests {
		rule, ok := ClassifyPackage(test.path)
		if !ok || rule.Capability != "credential" || rule.Layer != test.want {
			t.Errorf("%s classification = %#v, %v; want credential %s", test.path, rule, ok, test.want)
		}
	}

	if got := CapabilityDependencies["credential"]; len(got) != 0 {
		t.Fatalf("credential has broad capability dependencies %v; expected exact shared-contract edges", got)
	}
	for _, test := range []struct {
		packagePath string
		wantAllowed bool
	}{
		{packagePath: "internal/access", wantAllowed: true},
		{packagePath: "internal/analytics/connectors", wantAllowed: true},
		{packagePath: "internal/project/graph", wantAllowed: true},
		{packagePath: "internal/access/postgres", wantAllowed: false},
		{packagePath: "internal/analytics/connectionbinding", wantAllowed: false},
		{packagePath: "internal/project/compiler", wantAllowed: false},
	} {
		target, ok := ClassifyPackage(test.packagePath)
		if !ok {
			t.Fatalf("%s is not classified", test.packagePath)
		}
		if IsSharedContractImport("credential", test.packagePath) != test.wantAllowed {
			t.Errorf("credential shared-contract rule for %s = %v, want %v", test.packagePath, IsSharedContractImport("credential", test.packagePath), test.wantAllowed)
		}
		violation := CapabilityImportViolation("internal/credential", PackageRule{Prefix: "internal/credential", Capability: "credential", Layer: LayerUseCase}, test.packagePath, target)
		if (violation == "") != test.wantAllowed {
			t.Errorf("credential -> %s violation = %q, want allowed=%v", test.packagePath, violation, test.wantAllowed)
		}
	}
}
