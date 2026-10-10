//go:build leapview_static_avro

package extension

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
)

func TestCompiledAvroBindsSelectedForkAndCodecLibraries(t *testing.T) {
	policy, err := os.ReadFile("../../nix/avro-source-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"linux_amd64", "linux_arm64"} {
		builtin, ok := CompiledBuiltin("avro", platform)
		if !ok || builtin.SourceRevision != "f9d590297485f0318f480372c70bdd852826e258" || builtin.NativeDependencyLockSHA256 != fmt.Sprintf("%x", sha256.Sum256(policy)) {
			t.Fatalf("compiled Avro policy missing or changed for %s", platform)
		}
		arch := "amd64"
		if platform == "linux_arm64" {
			arch = "arm64"
		}
		identity := Identity{Builtin: true, Name: "avro", Platform: platform, GOOS: "linux", GOARCH: arch, DuckDBVersion: builtin.DuckDBVersion, ExtensionVersion: builtin.SourceRevision, Digest: builtin.Digest(), SupportProfile: "test"}
		if err := VerifyBuiltinDescriptor(identity, builtin.Bytes(), builtin.Provenance(), "package:compiled-engine"); err != nil {
			t.Fatal(err)
		}
		changed := builtin
		changed.NativeDependencyLockSHA256 = "substituted codec selection"
		if VerifyBuiltinDescriptor(identity, changed.Bytes(), changed.Provenance(), "package:compiled-engine") == nil {
			t.Fatal("substituted Avro native policy accepted")
		}
	}
}
