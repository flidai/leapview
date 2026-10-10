//go:build leapview_static_iceberg

package extension

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
)

func TestCompiledIcebergBindsSelectedWrapperAndAWSLibraries(t *testing.T) {
	policy, err := os.ReadFile("../../nix/iceberg-source-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"linux_amd64", "linux_arm64"} {
		builtin, ok := CompiledBuiltin("iceberg", platform)
		if !ok || builtin.SourceRevision != "757264559e745be697e9306e144e8889eb1dc024" || builtin.NativeDependencyLockSHA256 != fmt.Sprintf("%x", sha256.Sum256(policy)) {
			t.Fatalf("compiled Iceberg policy missing or changed for %s", platform)
		}
		arch := "amd64"
		if platform == "linux_arm64" {
			arch = "arm64"
		}
		identity := Identity{Builtin: true, Name: "iceberg", Platform: platform, GOOS: "linux", GOARCH: arch, DuckDBVersion: builtin.DuckDBVersion, ExtensionVersion: builtin.SourceRevision, Digest: builtin.Digest(), SupportProfile: "test"}
		if err := VerifyBuiltinDescriptor(identity, builtin.Bytes(), builtin.Provenance(), "package:compiled-engine"); err != nil {
			t.Fatal(err)
		}
		changed := builtin
		changed.NativeDependencyLockSHA256 = "substituted AWS selection"
		if VerifyBuiltinDescriptor(identity, changed.Bytes(), changed.Provenance(), "package:compiled-engine") == nil {
			t.Fatal("substituted Iceberg native policy accepted")
		}
	}
}
