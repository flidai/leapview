package extension

import (
	"errors"
	"testing"
)

func TestBuiltinRegistryIsClosedAndDescriptorAuthenticated(t *testing.T) {
	for _, name := range []string{"httpfs", "LANCE", "lance;INSTALL httpfs", ""} {
		if _, ok := CompiledBuiltin(name, "linux_amd64"); ok {
			t.Fatalf("unexpected compiled extension %q", name)
		}
	}
	for _, platform := range []string{"linux-amd64", "linux_amd64_musl", "osx_arm64", ""} {
		if _, ok := CompiledBuiltin("lance", platform); ok {
			t.Fatalf("unexpected compiled platform %q", platform)
		}
	}
	builtin, enabled := CompiledBuiltin("lance", "linux_amd64")
	if !enabled {
		if !errors.Is(ValidateBuiltinIdentity(Identity{Builtin: true, Name: "lance", Platform: "linux_amd64"}), ErrExtensionIntegrity) {
			t.Fatal("normal build accepted manifest-selected builtin")
		}
		return
	}
	identity := Identity{Builtin: true, Name: "lance", DuckDBVersion: builtin.DuckDBVersion, ExtensionVersion: builtin.SourceRevision, GOOS: "linux", GOARCH: "amd64", Platform: builtin.Platform, Digest: builtin.Digest(), SupportProfile: "test"}
	if err := VerifyBuiltinDescriptor(identity, builtin.Bytes(), builtin.Provenance(), "package:compiled-engine"); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Identity){
		func(i *Identity) { i.Builtin = false },
		func(i *Identity) { i.Name = "httpfs" },
		func(i *Identity) { i.GOARCH = "arm64" },
		func(i *Identity) { i.DuckDBVersion = "v1.5.6" },
		func(i *Identity) {
			i.Digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		},
		func(i *Identity) { i.ExtensionVersion = "different-source" },
	} {
		changed := identity
		mutate(&changed)
		if VerifyBuiltinDescriptor(changed, builtin.Bytes(), builtin.Provenance(), "package:compiled-engine") == nil {
			t.Fatalf("forged builtin identity accepted: %#v", changed)
		}
	}
	if VerifyBuiltinDescriptor(identity, append(builtin.Bytes(), ' '), builtin.Provenance(), "package:compiled-engine") == nil || VerifyBuiltinDescriptor(identity, builtin.Bytes(), "attest:duckdb-core-pinned", "sig:duckdb-official") == nil {
		t.Fatal("altered descriptor or invented official provenance accepted")
	}
	fileIdentity := identity
	fileIdentity.Builtin = false
	a, _ := identity.Canonical()
	b, _ := fileIdentity.Canonical()
	if a == b {
		t.Fatal("builtin and file identities collide")
	}
}
