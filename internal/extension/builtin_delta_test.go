//go:build leapview_static_delta

package extension

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
)

func TestCompiledDeltaBindsSelectedKernelAndCargoGraph(t *testing.T) {
	policy, err := os.ReadFile("../../nix/delta-source-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"linux_amd64", "linux_arm64"} {
		builtin, ok := CompiledBuiltin("delta", platform)
		if !ok || builtin.SourceRevision != "45c40878601b54b4188b09e08732fe0d576ad222" || builtin.NativeDependencyLockSHA256 != fmt.Sprintf("%x", sha256.Sum256(policy)) {
			t.Fatalf("compiled Delta policy missing or changed for %s", platform)
		}
		arch := "amd64"
		if platform == "linux_arm64" {
			arch = "arm64"
		}
		identity := Identity{Builtin: true, Name: "delta", Platform: platform, GOOS: "linux", GOARCH: arch, DuckDBVersion: builtin.DuckDBVersion, ExtensionVersion: builtin.SourceRevision, Digest: builtin.Digest(), SupportProfile: "test"}
		if err := VerifyBuiltinDescriptor(identity, builtin.Bytes(), builtin.Provenance(), "package:compiled-engine"); err != nil {
			t.Fatal(err)
		}
		changed := builtin
		changed.NativeDependencyLockSHA256 = "substituted Rust feature selection"
		if VerifyBuiltinDescriptor(identity, changed.Bytes(), changed.Provenance(), "package:compiled-engine") == nil {
			t.Fatal("substituted Delta native policy accepted")
		}
	}
}
