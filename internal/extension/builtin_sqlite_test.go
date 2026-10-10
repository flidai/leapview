package extension

import "testing"

func TestCompiledSQLiteDescriptorBindsOfficialAmalgamation(t *testing.T) {
	for _, platform := range []string{"linux_amd64", "linux_arm64"} {
		builtin, ok := CompiledBuiltin("sqlite", platform)
		if !staticSQLiteEnabled {
			if ok {
				t.Fatal("normal build accepted compiled SQLite")
			}
			continue
		}
		if !ok || builtin.SourceRevision != "494e9feed54c20b6bbfb665baf26864bc7e3b517" || builtin.SQLiteSourceID != "2026-07-24 19:02:57 bf7c7f30031888f4e796e429ab3978879485813aaca6f641c7b33e4e09459bcc" {
			t.Fatalf("missing pinned SQLite descriptor: %#v", builtin)
		}
		arch := "amd64"
		if platform == "linux_arm64" {
			arch = "arm64"
		}
		identity := Identity{Builtin: true, Name: "sqlite", Platform: platform, GOOS: "linux", GOARCH: arch, DuckDBVersion: builtin.DuckDBVersion, ExtensionVersion: builtin.SourceRevision, Digest: builtin.Digest(), SupportProfile: "test"}
		if err := VerifyBuiltinDescriptor(identity, builtin.Bytes(), builtin.Provenance(), "package:compiled-engine"); err != nil {
			t.Fatal(err)
		}
		changed := builtin
		changed.SourceArchiveSHA256 = "different-source"
		if VerifyBuiltinDescriptor(identity, changed.Bytes(), builtin.Provenance(), "package:compiled-engine") == nil {
			t.Fatal("substituted amalgamation accepted")
		}
		changed = builtin
		changed.SQLiteSourceID = "different-upstream"
		if VerifyBuiltinDescriptor(identity, changed.Bytes(), builtin.Provenance(), "package:compiled-engine") == nil {
			t.Fatal("substituted source identity accepted")
		}
	}
}
