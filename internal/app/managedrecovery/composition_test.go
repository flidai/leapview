package managedrecovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/refresh/recovery"
)

type managedSetReader struct {
	providerrestore.RecoverySetAuthority
	set recoveryset.RecoverySet
}

func (reader managedSetReader) ReadExact(context.Context, string) (recoveryset.RecoverySet, error) {
	return reader.set, nil
}

type managedOccurrenceReader struct {
	recovery.Repository
	occurrence recovery.Occurrence
}

func (reader managedOccurrenceReader) Occurrence(context.Context, string) (recovery.Occurrence, error) {
	return reader.occurrence, nil
}

func TestNewManagedRejectsForeignPrimarySystemIdentityBeforeProviderEffects(t *testing.T) {
	data, err := os.ReadFile("../../recoveryset/testdata/successor-legacy/recoveryset-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	set, err := recoveryset.ParseRecoverySet(data)
	if err != nil {
		t.Fatal(err)
	}
	set.FrontierDigest, err = set.Digest()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	frontier := PGFrontier{SystemID: "7620001234567890123"}
	artifact := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	config := ManagedConfig{InstanceHome: home, RecoverySetID: set.ID, OccurrenceID: "occurrence", Credentials: ManagedCredentials{RecoverySetID: set.ID, OccurrenceID: "occurrence", TargetID: set.Delivery.TargetID}, Readback: PGNativeReadbackConfig{Frontier: frontier}, PrimaryFence: providerrestore.PrimaryFenceSSHConfig{TargetID: set.Delivery.TargetID, SSH: "/nix/store/pinned/bin/ssh", Primaries: []providerrestore.PrimaryEnrollment{{ClusterIdentity: "postgres-system-id:" + frontier.SystemID, SystemIdentifier: "foreign-system-id"}}}, SecretRoot: filepath.Join(home, "must-not-be-created")}
	config.Artifact.Image = artifact
	authorities := ManagedAuthorities{Sets: managedSetReader{set: set}, Ledger: managedOccurrenceReader{occurrence: recovery.Occurrence{ID: config.OccurrenceID, Operation: recovery.OperationRestore, TargetScope: set.Delivery.TargetID, ArtifactIdentity: artifact}}}
	if _, err := NewManaged(t.Context(), config, authorities); err == nil || !strings.Contains(err.Error(), "primary enrollment differs") {
		t.Fatalf("foreign system identity reached provider setup: %v", err)
	}
	config.PrimaryFence.Primaries[0].SystemIdentifier = frontier.SystemID
	config.PrimaryFence.Primaries[0].ClusterIdentity = "unbound-cluster"
	if _, err := NewManaged(t.Context(), config, authorities); err == nil || !strings.Contains(err.Error(), "primary enrollment differs") {
		t.Fatalf("unbound cluster identity reached provider setup: %v", err)
	}
	if _, err := os.Lstat(config.SecretRoot); !os.IsNotExist(err) {
		t.Fatal("foreign original primary created private provider effects")
	}
	var nilReader *managedOccurrenceReader
	authorities.Ledger = nilReader
	if _, err := NewManaged(t.Context(), config, authorities); err == nil {
		t.Fatal("typed nil authority accepted")
	}
}
