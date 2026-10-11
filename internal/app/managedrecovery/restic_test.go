package managedrecovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/recoveryset"
)

func resticFixture(t *testing.T) (*Restic, providerrestore.ObjectRequest) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("acknowledged"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := CaptureFiles(source)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	password := filepath.Join(root, "password")
	if err := os.WriteFile(password, []byte("private-runtime-password"), 0600); err != nil {
		t.Fatal(err)
	}
	point := recoveryset.ObjectRoot{Kind: recoveryset.ObjectRootDuckLake, URI: source, VersionID: strings.Repeat("a", 64), ProviderRecoveryFrontier: "restic:" + strings.Repeat("a", 64), Digest: digestBytes([]byte(source))}
	restorer, err := NewRestic(ResticConfig{TargetID: "target", RecoverySetID: "set", Root: point, Restic: "/nix/store/pinned/bin/restic", Repository: filepath.Join(root, "repository"), PasswordFile: password, Destination: filepath.Join(root, "restored"), Manifest: manifest, ManifestDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	return restorer, providerrestore.ObjectRequest{TargetID: "target", RecoverySetID: "set", Root: point, IdempotencyKey: "exact-operation"}
}

func TestResticRestoreChecksContentBeforeExposureAndReplaysWithoutEffect(t *testing.T) {
	restorer, request := resticFixture(t)
	calls := 0
	restorer.execute = func(_ context.Context, program string, args []string, inherited *os.File) error {
		calls++
		if program != restorer.config.Restic || inherited == nil || args[6] != request.Root.VersionID+":"+restorer.source {
			t.Fatalf("unexpected exact snapshot command: %v", args)
		}
		if _, err := os.Stat(restorer.config.Destination); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("destination exposed before verification")
		}
		return os.WriteFile(filepath.Join(args[8], "data"), []byte("acknowledged"), 0600)
	}
	result, err := restorer.RestoreObject(t.Context(), request)
	if err != nil || result.ObservedVersionID != request.Root.VersionID {
		t.Fatalf("restore: %v", err)
	}
	if _, err := restorer.RestoreObject(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("completed restore was replayed")
	}
	other := request
	other.IdempotencyKey = "another-operation"
	if _, err := restorer.RestoreObject(t.Context(), other); err == nil {
		t.Fatal("destination adopted by another operation")
	}
	if err := os.WriteFile(filepath.Join(restorer.config.Destination, "data"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := restorer.RestoreObject(t.Context(), request); err == nil {
		t.Fatal("mutated restored content accepted on replay")
	}
}

func TestResticRestoreRejectsForeignFrontierAndCorruptBytes(t *testing.T) {
	restorer, request := resticFixture(t)
	calls := 0
	restorer.execute = func(_ context.Context, _ string, args []string, _ *os.File) error {
		calls++
		return os.WriteFile(filepath.Join(args[8], "data"), []byte("wrong"), 0600)
	}
	wrong := request
	wrong.Root.VersionID = strings.Repeat("b", 64)
	if _, err := restorer.RestoreObject(t.Context(), wrong); err == nil || calls != 0 {
		t.Fatal("foreign snapshot reached provider")
	}
	if _, err := restorer.RestoreObject(t.Context(), request); err == nil {
		t.Fatal("corrupt backup accepted")
	}
	if _, err := os.Stat(restorer.config.Destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("corrupt tree exposed")
	}
}
