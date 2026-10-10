//go:build leapview_static_ducklake

package extension

import (
	"encoding/json"
	"testing"
)

func TestCompiledDuckLakeBindsNativeDependencySelection(t *testing.T) {
	for _, platform := range []string{"linux_amd64", "linux_arm64"} {
		builtin, ok := CompiledBuiltin("ducklake", platform)
		if !ok || builtin.SourceRevision != "d318a545571d7d46eb751fa2aa5f6f4389285d3c" {
			t.Fatal("missing source-built DuckLake registration")
		}
		var descriptor map[string]any
		if err := json.Unmarshal(builtin.Bytes(), &descriptor); err != nil {
			t.Fatal(err)
		}
		lock, _ := descriptor["nativeDependencyLockSHA256"].(string)
		if len(lock) != 64 {
			t.Fatal("DuckLake descriptor does not bind the selected CRoaring source/build/patch policy")
		}
		arch := "amd64"
		if platform == "linux_arm64" {
			arch = "arm64"
		}
		identity := Identity{Builtin: true, Name: "ducklake", Platform: platform, GOOS: "linux", GOARCH: arch, DuckDBVersion: builtin.DuckDBVersion, ExtensionVersion: builtin.SourceRevision, Digest: builtin.Digest(), SupportProfile: "test"}
		if err := VerifyBuiltinDescriptor(identity, builtin.Bytes(), builtin.Provenance(), "package:compiled-engine"); err != nil {
			t.Fatal(err)
		}
		descriptor["nativeDependencyLockSHA256"] = "different-native-dependency"
		changed, _ := json.Marshal(descriptor)
		if VerifyBuiltinDescriptor(identity, append(changed, '\n'), builtin.Provenance(), "package:compiled-engine") == nil {
			t.Fatal("substituted CRoaring selection accepted")
		}
	}
}
