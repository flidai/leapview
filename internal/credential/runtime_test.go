package credential

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestRuntimeCredentialConsumesExactVersionAndClearsFields(t *testing.T) {
	f := newValidationFixture(t, map[string]string{"password": "runtime-secret"})
	runtime, err := NewRuntimeCredentials(f.repository, f.keys, f.scopes)
	if err != nil {
		t.Fatal(err)
	}
	var retained map[string]string
	err = runtime.UseVersion(t.Context(), f.resource, f.version.Metadata.Binding.VersionID, func(fields map[string]string) error {
		if fields["password"] != "runtime-secret" {
			t.Fatal("wrong saved credential")
		}
		retained = fields
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(retained) != 0 {
		t.Fatal("plaintext field map retained")
	}
	for _, b := range f.keys.lastPlaintext {
		if b != 0 {
			t.Fatal("decrypted bytes retained")
		}
	}
	if err := runtime.UseVersion(t.Context(), f.resource, uuid.NewString(), func(map[string]string) error { t.Fatal("missing version reached runtime"); return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown version=%v", err)
	}
	f.scopes.scope.Destination = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := runtime.UseVersion(t.Context(), f.resource, f.version.Metadata.Binding.VersionID, func(map[string]string) error { t.Fatal("changed destination received old secret"); return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("destination drift=%v", err)
	}
	if f.keys.decryptCalls != 1 {
		t.Fatal("scope or version mismatch decrypted a secret")
	}
}

func TestRuntimeCredentialProviderPanicCannotDiscloseSecret(t *testing.T) {
	f := newValidationFixture(t, map[string]string{"password": "panic-secret"})
	runtime, err := NewRuntimeCredentials(f.repository, f.keys, f.scopes)
	if err != nil {
		t.Fatal(err)
	}
	var retained map[string]string
	defer func() {
		value := recover()
		if value != ErrUnavailable {
			t.Errorf("provider panic crossed the credential boundary unsanitized")
		}
		if len(retained) != 0 {
			t.Error("provider panic retained plaintext fields")
		}
	}()
	_ = runtime.UseVersion(t.Context(), f.resource, f.version.Metadata.Binding.VersionID, func(fields map[string]string) error { retained = fields; panic(fields["password"]) })
}
