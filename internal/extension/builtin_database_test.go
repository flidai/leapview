//go:build leapview_static_database

package extension

import (
	"encoding/json"
	"testing"
)

func TestCompiledDatabaseBindsSelectedAuthenticationDependencies(t *testing.T) {
	for _, platform := range []string{"linux_amd64", "linux_arm64"} {
		for _, name := range []string{"postgres", "mysql"} {
			b, ok := CompiledBuiltin(name, platform)
			if !ok || len(b.SourceRevision) != 40 || len(b.NativeDependencyLockSHA256) != 64 {
				t.Fatalf("missing compiled %s dependency policy for %s", name, platform)
			}
			arch := "amd64"
			if platform == "linux_arm64" {
				arch = "arm64"
			}
			id := Identity{Builtin: true, Name: name, Platform: platform, GOOS: "linux", GOARCH: arch, DuckDBVersion: b.DuckDBVersion, ExtensionVersion: b.SourceRevision, Digest: b.Digest(), SupportProfile: "test"}
			if err := VerifyBuiltinDescriptor(id, b.Bytes(), b.Provenance(), "package:compiled-engine"); err != nil {
				t.Fatal(err)
			}
			changed := b
			changed.NativeDependencyLockSHA256 = "substituted authentication profile"
			payload, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if VerifyBuiltinDescriptor(id, append(payload, '\n'), b.Provenance(), "package:compiled-engine") == nil {
				t.Fatal("substituted authentication dependency policy accepted")
			}
		}
	}
}
