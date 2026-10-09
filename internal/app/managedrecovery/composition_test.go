package managedrecovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	request := ManagedEnrollmentRequest{InstanceHome: home, RecoverySetID: set.ID, FrontierDigest: set.FrontierDigest, RetentionRootID: "018f3f83-7b2f-7b37-9f9e-000000000099", SourceSystemID: frontier.SystemID, AuthoritySystemID: "2", ArtifactIdentity: artifact, Actor: "operator", PlannedAt: time.Now().UTC().Truncate(time.Microsecond)}
	request.ExpiresAt = request.PlannedAt.Add(time.Hour)
	canonical, err := set.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := managedEnrollmentPolicy(request, canonical)
	if err != nil {
		t.Fatal(err)
	}
	intent := managedEnrollmentIntent(request, set, policy)
	id, err := recovery.OccurrenceID(intent)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := recovery.ScheduleRevisionForInput(intent)
	if err != nil {
		t.Fatal(err)
	}
	config.OccurrenceID = id
	config.Credentials.OccurrenceID = id
	config.Enrollment = ManagedEnrollmentReceipt{SchemaVersion: 1, Request: request, CanonicalSetSHA256: digestBytes(canonical), OccurrenceID: id, PolicySHA256: policy, Status: "prepared"}
	authorities := ManagedAuthorities{AuthoritySystemIdentifier: "2", Sets: managedSetReader{set: set}, Ledger: managedOccurrenceReader{occurrence: recovery.Occurrence{ID: config.OccurrenceID, Operation: recovery.OperationRestore, TargetScope: set.Delivery.TargetID, ArtifactIdentity: artifact, PolicySHA256: policy, ScheduleID: intent.ScheduleID, ScheduleRevision: revision, Scenario: intent.Scenario, PolicyVersion: intent.PolicyVersion, PlannedAt: request.PlannedAt, ExpiresAt: request.ExpiresAt}}}
	for name, mutate := range map[string]func(*ManagedConfig, *ManagedAuthorities){
		"missing receipt": func(c *ManagedConfig, _ *ManagedAuthorities) { c.Enrollment = ManagedEnrollmentReceipt{} },
		"receipt home": func(c *ManagedConfig, _ *ManagedAuthorities) {
			c.Enrollment.Request.InstanceHome = filepath.Join(home, "foreign-home")
		},
		"receipt frontier": func(c *ManagedConfig, _ *ManagedAuthorities) {
			c.Enrollment.Request.FrontierDigest = digestBytes([]byte("foreign-frontier"))
		},
		"receipt actor":    func(c *ManagedConfig, _ *ManagedAuthorities) { c.Enrollment.Request.Actor = "foreign-operator" },
		"opened authority": func(_ *ManagedConfig, a *ManagedAuthorities) { a.AuthoritySystemIdentifier = "3" },
	} {
		t.Run(name, func(t *testing.T) {
			copyConfig, copyAuthority := config, authorities
			mutate(&copyConfig, &copyAuthority)
			if _, err := NewManaged(t.Context(), copyConfig, copyAuthority); err == nil || strings.Contains(err.Error(), "primary enrollment differs") {
				t.Fatalf("foreign enrollment reached provider boundary: %v", err)
			}
		})
	}
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
