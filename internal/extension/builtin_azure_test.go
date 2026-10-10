//go:build leapview_static_azure

package extension

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
)

func TestCompiledAzureBindsSelectedSDKAndNativeLibraries(t *testing.T) {
	policy, err := os.ReadFile("../../nix/azure-source-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"linux_amd64", "linux_arm64"} {
		builtin, ok := CompiledBuiltin("azure", platform)
		if !ok || builtin.SourceRevision != "563589b2f24290a4dcdd4247eaedf2b544f9dbcd" || builtin.NativeDependencyLockSHA256 != fmt.Sprintf("%x", sha256.Sum256(policy)) {
			t.Fatalf("compiled Azure policy missing or changed for %s", platform)
		}
		arch := "amd64"
		if platform == "linux_arm64" {
			arch = "arm64"
		}
		identity := Identity{Builtin: true, Name: "azure", Platform: platform, GOOS: "linux", GOARCH: arch, DuckDBVersion: builtin.DuckDBVersion, ExtensionVersion: builtin.SourceRevision, Digest: builtin.Digest(), SupportProfile: "test"}
		if err := VerifyBuiltinDescriptor(identity, builtin.Bytes(), builtin.Provenance(), "package:compiled-engine"); err != nil {
			t.Fatal(err)
		}
		changed := builtin
		changed.NativeDependencyLockSHA256 = "substituted SDK selection"
		if VerifyBuiltinDescriptor(identity, changed.Bytes(), changed.Provenance(), "package:compiled-engine") == nil {
			t.Fatal("substituted Azure native policy accepted")
		}
	}
}
