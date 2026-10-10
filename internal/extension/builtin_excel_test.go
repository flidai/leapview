//go:build leapview_static_excel

package extension

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
)

func TestCompiledExcelBindsSelectedParserAndZipLibraries(t *testing.T) {
	policy, err := os.ReadFile("../../nix/excel-source-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"linux_amd64", "linux_arm64"} {
		builtin, ok := CompiledBuiltin("excel", platform)
		if !ok || builtin.SourceRevision != "f4c72b5ef04a03b3a78a95b5a2ee94ba93e3178d" || builtin.NativeDependencyLockSHA256 != fmt.Sprintf("%x", sha256.Sum256(policy)) {
			t.Fatalf("compiled Excel policy missing or changed for %s", platform)
		}
		arch := "amd64"
		if platform == "linux_arm64" {
			arch = "arm64"
		}
		identity := Identity{Builtin: true, Name: "excel", Platform: platform, GOOS: "linux", GOARCH: arch, DuckDBVersion: builtin.DuckDBVersion, ExtensionVersion: builtin.SourceRevision, Digest: builtin.Digest(), SupportProfile: "test"}
		if err := VerifyBuiltinDescriptor(identity, builtin.Bytes(), builtin.Provenance(), "package:compiled-engine"); err != nil {
			t.Fatal(err)
		}
		changed := builtin
		changed.NativeDependencyLockSHA256 = "substituted codec selection"
		if VerifyBuiltinDescriptor(identity, changed.Bytes(), changed.Provenance(), "package:compiled-engine") == nil {
			t.Fatal("substituted Excel native policy accepted")
		}
	}
}
