package postgres

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/release/migrationcapability"
)

func TestMigrationCapabilityAuthorityPublishesExactArtifactCapabilities(t *testing.T) {
	repository := New(testDB(t))
	authority := migrationCapabilityAuthority(t, repository)
	predecessor := artifactAdmission("predecessor", "a", "1")
	candidate := artifactAdmission("candidate", "b", "2")
	predecessorIdentity, err := repository.PublishArtifactAdmission(t.Context(), predecessor)
	if err != nil {
		t.Fatal(err)
	}
	candidateIdentity, err := repository.PublishArtifactAdmission(t.Context(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	target := digest("f")

	predecessorCapabilities := migrationCapabilities(t, predecessorIdentity.ArtifactAdmissionDigest, target, "15")
	candidateCapabilities := migrationCapabilities(t, candidateIdentity.ArtifactAdmissionDigest, target, "16")
	for _, capability := range predecessorCapabilities {
		published, err := authority.Publish(t.Context(), signedMigrationCapability(t, capability))
		if err != nil {
			t.Fatalf("publish predecessor %s: %v", capability.Subsystem, err)
		}
		replayed, err := authority.Publish(t.Context(), signedMigrationCapability(t, capability))
		if err != nil {
			t.Fatalf("replay predecessor %s: %v", capability.Subsystem, err)
		}
		resolved, err := authority.ResolveMigrationCapability(t.Context(), capability.ArtifactAdmissionDigest, target, capability.Subsystem)
		if err != nil {
			t.Fatalf("resolve predecessor %s: %v", capability.Subsystem, err)
		}
		if !reflect.DeepEqual(published, replayed) || !reflect.DeepEqual(published, resolved) {
			t.Fatalf("%s publication/replay/readback differ", capability.Subsystem)
		}
	}

	if _, err := authority.ResolveMigrationCapability(t.Context(), candidateIdentity.ArtifactAdmissionDigest, target, migrationcapability.SubsystemGoose); !errors.Is(err, ErrMigrationCapabilityNotFound) {
		t.Fatalf("predecessor capability reused for candidate: %v", err)
	}
	for _, capability := range candidateCapabilities {
		if _, err := authority.Publish(t.Context(), signedMigrationCapability(t, capability)); err != nil {
			t.Fatalf("publish candidate %s: %v", capability.Subsystem, err)
		}
	}
	predecessorDigest, _ := predecessorCapabilities[0].Digest()
	candidateDigest, _ := candidateCapabilities[0].Digest()
	if predecessorDigest == candidateDigest {
		t.Fatal("different artifact bindings reused one capability digest")
	}
}

func TestMigrationCapabilityAuthorityFailsClosed(t *testing.T) {
	pool := testDB(t)
	repository := New(pool)
	authority := migrationCapabilityAuthority(t, repository)
	identity, err := repository.PublishArtifactAdmission(t.Context(), artifactAdmission("candidate", "a", "1"))
	if err != nil {
		t.Fatal(err)
	}
	target := digest("b")
	capability := migrationCapabilities(t, identity.ArtifactAdmissionDigest, target, "16")[0]

	unknownArtifact := capability
	unknownArtifact.ArtifactAdmissionDigest = digest("9")
	if _, err := authority.Publish(t.Context(), signedMigrationCapability(t, unknownArtifact)); !errors.Is(err, ErrMigrationCapabilityArtifact) {
		t.Fatalf("unknown artifact error = %v", err)
	}
	wrongTarget := signedMigrationCapability(t, capability)
	wrongTarget.TargetIdentityDigest = "prod-target"
	if _, err := authority.Publish(t.Context(), wrongTarget); !errors.Is(err, ErrMigrationCapabilityOwnerEvidence) {
		t.Fatalf("wrong target error = %v", err)
	}
	if _, err := authority.Publish(t.Context(), signedMigrationCapability(t, capability)); err != nil {
		t.Fatal(err)
	}
	conflicting := capability
	conflicting.Goose = &migrationcapability.GooseCapability{
		SchemaVersion: "goose/v17", RunnableSchemaVersions: []string{"goose/v16", "goose/v17"}, MigrationGraphDigest: digest("8"),
	}
	if _, err := authority.Publish(t.Context(), signedMigrationCapability(t, conflicting)); !errors.Is(err, ErrMigrationCapabilityConflict) {
		t.Fatalf("conflicting publication error = %v", err)
	}
	if _, err := authority.ResolveMigrationCapability(t.Context(), identity.ArtifactAdmissionDigest, digest("c"), capability.Subsystem); !errors.Is(err, ErrMigrationCapabilityNotFound) {
		t.Fatalf("wrong target lookup error = %v", err)
	}
	if _, err := authority.ResolveMigrationCapability(t.Context(), digest("c"), target, capability.Subsystem); !errors.Is(err, ErrMigrationCapabilityNotFound) {
		t.Fatalf("wrong artifact lookup error = %v", err)
	}

	if err := repository.RevokeArtifactAdmission(t.Context(), artifactAdmission("candidate", "a", "1").Release.Image, "security decision revoked", time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ResolveMigrationCapability(t.Context(), identity.ArtifactAdmissionDigest, target, capability.Subsystem); !errors.Is(err, ErrMigrationCapabilityArtifact) || !errors.Is(err, ErrArtifactAdmissionRevoked) {
		t.Fatalf("revoked artifact capability error = %v", err)
	}
}

func TestMigrationCapabilityAuthorityRejectsCorruptStoredBytes(t *testing.T) {
	pool := testDB(t)
	repository := New(pool)
	authority := migrationCapabilityAuthority(t, repository)
	identity, err := repository.PublishArtifactAdmission(t.Context(), artifactAdmission("candidate", "a", "1"))
	if err != nil {
		t.Fatal(err)
	}
	target := digest("b")
	capability := migrationCapabilities(t, identity.ArtifactAdmissionDigest, target, "16")[0]
	originalDigest, _ := capability.Digest()
	evidence := signedMigrationCapability(t, capability)
	evidenceDigest, _ := evidence.Digest()
	evidenceBytes, _ := evidence.CanonicalJSON()
	mutated := capability
	mutated.Goose = &migrationcapability.GooseCapability{SchemaVersion: "goose/v17", RunnableSchemaVersions: []string{"goose/v17"}, MigrationGraphDigest: digest("7")}
	mutatedBytes, _ := mutated.CanonicalJSON()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO release.migration_capability
		(artifact_admission_digest, target_identity_digest, subsystem, owner_identity,
			 owner_contract_version, capability_version, capability_digest, capability_bytes,
			 owner_evidence_version, owner_evidence_digest, owner_evidence_bytes)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, capability.ArtifactAdmissionDigest, target, capability.Subsystem,
		capability.Owner.Identity, capability.Owner.ContractVersion, capability.Version, originalDigest, mutatedBytes,
		evidence.Version, evidenceDigest, evidenceBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ResolveMigrationCapability(t.Context(), capability.ArtifactAdmissionDigest, target, capability.Subsystem); !errors.Is(err, ErrMigrationCapabilityDigestMismatch) {
		t.Fatalf("corrupt read error = %v", err)
	}
}

func TestMigrationCapabilityAuthorityRejectsCorruptStoredOwnerEvidence(t *testing.T) {
	pool := testDB(t)
	repository := New(pool)
	authority := migrationCapabilityAuthority(t, repository)
	identity, err := repository.PublishArtifactAdmission(t.Context(), artifactAdmission("candidate", "a", "1"))
	if err != nil {
		t.Fatal(err)
	}
	capability := migrationCapabilities(t, identity.ArtifactAdmissionDigest, digest("b"), "16")[0]
	capabilityDigest, _ := capability.Digest()
	capabilityBytes, _ := capability.CanonicalJSON()
	evidence := signedMigrationCapability(t, capability)
	evidence.Proof.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	evidenceDigest, _ := evidence.Digest()
	evidenceBytes, _ := evidence.CanonicalJSON()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO release.migration_capability
		(artifact_admission_digest, target_identity_digest, subsystem, owner_identity,
		 owner_contract_version, capability_version, capability_digest, capability_bytes,
		 owner_evidence_version, owner_evidence_digest, owner_evidence_bytes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, capability.ArtifactAdmissionDigest, capability.TargetIdentityDigest, capability.Subsystem,
		capability.Owner.Identity, capability.Owner.ContractVersion, capability.Version, capabilityDigest, capabilityBytes,
		evidence.Version, evidenceDigest, evidenceBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ResolveMigrationCapability(t.Context(), capability.ArtifactAdmissionDigest, capability.TargetIdentityDigest, capability.Subsystem); !errors.Is(err, ErrMigrationCapabilityOwnerEvidence) {
		t.Fatalf("corrupt owner evidence error = %v", err)
	}
}

func TestMigrationCapabilityAuthorityIsImmutableAndConcurrent(t *testing.T) {
	pool := testDB(t)
	repository := New(pool)
	authority := migrationCapabilityAuthority(t, repository)
	identity, err := repository.PublishArtifactAdmission(t.Context(), artifactAdmission("candidate", "a", "1"))
	if err != nil {
		t.Fatal(err)
	}
	capability := migrationCapabilities(t, identity.ArtifactAdmissionDigest, digest("b"), "16")[0]
	evidence := signedMigrationCapability(t, capability)

	var wait sync.WaitGroup
	errorsSeen := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, publishErr := authority.Publish(t.Context(), evidence)
			errorsSeen <- publishErr
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for publishErr := range errorsSeen {
		if publishErr != nil {
			t.Fatalf("identical concurrent publication: %v", publishErr)
		}
	}
	for name, statement := range map[string]string{
		"update":   `UPDATE release.migration_capability SET capability_digest = '` + digest("9") + `'`,
		"delete":   `DELETE FROM release.migration_capability`,
		"truncate": `TRUNCATE release.migration_capability`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := pool.Exec(t.Context(), statement); err == nil {
				t.Fatalf("%s unexpectedly mutated migration capability", name)
			}
		})
	}
}

func TestMigrationCapabilityAuthorityRejectsUnauthenticatedOwnerEvidence(t *testing.T) {
	repository := New(testDB(t))
	authority := migrationCapabilityAuthority(t, repository)
	identity, err := repository.PublishArtifactAdmission(t.Context(), artifactAdmission("candidate", "a", "1"))
	if err != nil {
		t.Fatal(err)
	}
	capability := migrationCapabilities(t, identity.ArtifactAdmissionDigest, digest("b"), "16")[0]
	valid := signedMigrationCapability(t, capability)

	unsigned := valid
	unsigned.Proof.Signature = ""
	if _, err := authority.Publish(t.Context(), unsigned); !errors.Is(err, ErrMigrationCapabilityOwnerEvidence) {
		t.Fatalf("unsigned evidence error = %v", err)
	}
	wrongKey, err := migrationcapability.SignOwnerEvidence(capability, "qualification-key", ed25519.NewKeyFromSeed(bytes.Repeat([]byte{99}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Publish(t.Context(), wrongKey); !errors.Is(err, ErrMigrationCapabilityOwnerEvidence) {
		t.Fatalf("untrusted owner proof error = %v", err)
	}
	wrongArtifact := valid
	wrongArtifact.ArtifactAdmissionDigest = digest("c")
	if _, err := authority.Publish(t.Context(), wrongArtifact); !errors.Is(err, ErrMigrationCapabilityOwnerEvidence) {
		t.Fatalf("substituted artifact error = %v", err)
	}
	wrongTarget := valid
	wrongTarget.TargetIdentityDigest = digest("d")
	if _, err := authority.Publish(t.Context(), wrongTarget); !errors.Is(err, ErrMigrationCapabilityOwnerEvidence) {
		t.Fatalf("substituted target error = %v", err)
	}
	wrongOwner := valid
	wrongOwner.Owner.Identity = migrationcapability.DuckLakeOwnerIdentity
	if _, err := authority.Publish(t.Context(), wrongOwner); !errors.Is(err, ErrMigrationCapabilityOwnerEvidence) {
		t.Fatalf("substituted owner error = %v", err)
	}
}

func TestMigrationCapabilityAuthorityFreezesTrustedOwnerKeys(t *testing.T) {
	repository := New(testDB(t))
	registry := migrationCapabilityOwnerRegistry()
	authority, err := NewMigrationCapabilityAuthority(repository, registry)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := repository.PublishArtifactAdmission(t.Context(), artifactAdmission("candidate", "a", "1"))
	if err != nil {
		t.Fatal(err)
	}
	capability := migrationCapabilities(t, identity.ArtifactAdmissionDigest, digest("b"), "16")[0]
	attackerKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{99}, ed25519.SeedSize))
	registry.Keys[0].PublicKey = base64.StdEncoding.EncodeToString(attackerKey.Public().(ed25519.PublicKey))
	forged, err := migrationcapability.SignOwnerEvidence(capability, "qualification-key", attackerKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Publish(t.Context(), forged); !errors.Is(err, ErrMigrationCapabilityOwnerEvidence) {
		t.Fatalf("publication with post-composition registry mutation error = %v", err)
	}
}

func TestMigrationCapabilityPublicationIsFencedAgainstRevocation(t *testing.T) {
	t.Run("publication wins before revocation", func(t *testing.T) {
		pool := testDB(t)
		repository := New(pool)
		authority := migrationCapabilityAuthority(t, repository)
		admission := artifactAdmission("candidate", "a", "1")
		identity, err := repository.PublishArtifactAdmission(t.Context(), admission)
		if err != nil {
			t.Fatal(err)
		}
		capability := migrationCapabilities(t, identity.ArtifactAdmissionDigest, digest("b"), "16")[0]
		evidence := signedMigrationCapability(t, capability)

		publishTx, err := pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer publishTx.Rollback(t.Context())
		if _, err := publishTx.Exec(t.Context(), `SELECT artifact_reference FROM release.oci_artifact_admission WHERE admission_digest=$1 FOR UPDATE`, identity.ArtifactAdmissionDigest); err != nil {
			t.Fatal(err)
		}
		revocationStarted := make(chan struct{})
		revocationDone := make(chan error, 1)
		go func() {
			close(revocationStarted)
			revocationDone <- repository.RevokeArtifactAdmission(t.Context(), admission.Release.Image, "security decision revoked", time.Date(2026, 9, 14, 13, 0, 0, 0, time.UTC))
		}()
		<-revocationStarted
		if _, err := publishMigrationCapability(t.Context(), publishTx, evidence, migrationCapabilityOwnerRegistry()); err != nil {
			t.Fatal(err)
		}
		if err := publishTx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := <-revocationDone; err != nil {
			t.Fatal(err)
		}
		if _, err := authority.ResolveMigrationCapability(t.Context(), identity.ArtifactAdmissionDigest, capability.TargetIdentityDigest, capability.Subsystem); !errors.Is(err, ErrArtifactAdmissionRevoked) {
			t.Fatalf("post-revocation resolution error = %v", err)
		}
		var count int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM release.migration_capability WHERE artifact_admission_digest=$1`, identity.ArtifactAdmissionDigest).Scan(&count); err != nil || count != 1 {
			t.Fatalf("immutable pre-revocation capability count = %d, error = %v", count, err)
		}
	})

	t.Run("revocation wins before publication", func(t *testing.T) {
		pool := testDB(t)
		repository := New(pool)
		authority := migrationCapabilityAuthority(t, repository)
		admission := artifactAdmission("candidate", "c", "2")
		identity, err := repository.PublishArtifactAdmission(t.Context(), admission)
		if err != nil {
			t.Fatal(err)
		}
		capability := migrationCapabilities(t, identity.ArtifactAdmissionDigest, digest("d"), "16")[0]
		evidence := signedMigrationCapability(t, capability)

		revokeTx, err := pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer revokeTx.Rollback(t.Context())
		if _, err := revokeTx.Exec(t.Context(), `SELECT artifact_reference FROM release.oci_artifact_admission WHERE admission_digest=$1 FOR UPDATE`, identity.ArtifactAdmissionDigest); err != nil {
			t.Fatal(err)
		}
		if _, err := revokeTx.Exec(t.Context(), `INSERT INTO release.oci_artifact_admission_revocation (artifact_reference, admission_digest, revoked_at, reason) VALUES ($1,$2,$3,$4)`, admission.Release.Image, identity.ArtifactAdmissionDigest, time.Date(2026, 9, 14, 13, 5, 0, 0, time.UTC), "security decision revoked"); err != nil {
			t.Fatal(err)
		}
		publicationStarted := make(chan struct{})
		publicationDone := make(chan error, 1)
		go func() {
			close(publicationStarted)
			_, publishErr := authority.Publish(t.Context(), evidence)
			publicationDone <- publishErr
		}()
		<-publicationStarted
		if err := revokeTx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := <-publicationDone; !errors.Is(err, ErrMigrationCapabilityArtifact) || !errors.Is(err, ErrArtifactAdmissionRevoked) {
			t.Fatalf("publication after revocation error = %v", err)
		}
		var count int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM release.migration_capability WHERE artifact_admission_digest=$1`, identity.ArtifactAdmissionDigest).Scan(&count); err != nil || count != 0 {
			t.Fatalf("post-revocation capability count = %d, error = %v", count, err)
		}
	})
}

func migrationCapabilities(t *testing.T, artifactDigest, targetDigest, revision string) []migrationcapability.Capability {
	t.Helper()
	tuple := physicalpool.Compatibility{
		DuckDBRuntime: "duckdb:1.5.4", DuckLakeExtension: "ducklake:0.3.0", CatalogFormat: "ducklake-catalog:v1",
		StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1",
	}
	tupleDigest, err := tuple.Digest()
	if err != nil {
		t.Fatal(err)
	}
	base := migrationcapability.Capability{Version: migrationcapability.Version, ArtifactAdmissionDigest: artifactDigest, TargetIdentityDigest: targetDigest}
	goose := base
	goose.Subsystem = migrationcapability.SubsystemGoose
	goose.Owner = migrationcapability.Owner{Identity: migrationcapability.GooseOwnerIdentity, ContractVersion: migrationcapability.GooseOwnerContractVersion}
	goose.Goose = &migrationcapability.GooseCapability{SchemaVersion: "goose/v" + revision, RunnableSchemaVersions: capabilityVersions("goose/v15", "goose/v"+revision), MigrationGraphDigest: digest("1")}
	river := base
	river.Subsystem = migrationcapability.SubsystemRiverJobs
	river.Owner = migrationcapability.Owner{Identity: migrationcapability.RiverJobsOwnerIdentity, ContractVersion: migrationcapability.RiverJobsOwnerContractVersion}
	river.RiverJobs = &migrationcapability.RiverJobsCapability{
		SchemaVersion: "river/v6", RunnableSchemaVersions: []string{"river/v6"}, JobHistoryVersion: "jobs/v" + revision,
		RunnableJobHistoryVersions: capabilityVersions("jobs/v15", "jobs/v"+revision), MigrationGraphDigest: digest("2"),
	}
	duckLake := base
	duckLake.Subsystem = migrationcapability.SubsystemDuckLake
	duckLake.Owner = migrationcapability.Owner{Identity: migrationcapability.DuckLakeOwnerIdentity, ContractVersion: migrationcapability.DuckLakeOwnerContractVersion}
	duckLake.DuckLake = &migrationcapability.DuckLakeCapability{
		Compatibility: tuple, CatalogSchemaVersion: "ducklake-catalog/v" + revision,
		RunnableCatalogSchemaVersions: capabilityVersions("ducklake-catalog/v15", "ducklake-catalog/v"+revision), MigrationGraphDigest: digest("3"),
	}
	pool := base
	pool.Subsystem = migrationcapability.SubsystemPhysicalPool
	pool.Owner = migrationcapability.Owner{Identity: migrationcapability.PhysicalPoolOwnerIdentity, ContractVersion: migrationcapability.PhysicalPoolContractVersion}
	pool.PhysicalPool = &migrationcapability.PhysicalPoolCapability{Compatibility: tuple, CompatibleTupleDigests: []string{tupleDigest}}
	return []migrationcapability.Capability{goose, river, duckLake, pool}
}

func capabilityVersions(values ...string) []string {
	if len(values) == 2 && values[0] == values[1] {
		return values[:1]
	}
	return values
}

func migrationCapabilityAuthority(t *testing.T, repository *Repository) *MigrationCapabilityAuthority {
	t.Helper()
	authority, err := NewMigrationCapabilityAuthority(repository, migrationCapabilityOwnerRegistry())
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func signedMigrationCapability(t *testing.T, capability migrationcapability.Capability) migrationcapability.OwnerEvidence {
	t.Helper()
	key, ok := migrationCapabilityOwnerKeys()[capability.Subsystem]
	if !ok {
		t.Fatalf("missing test owner key for %s", capability.Subsystem)
	}
	evidence, err := migrationcapability.SignOwnerEvidence(capability, "qualification-key", key)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func migrationCapabilityOwnerRegistry() migrationcapability.OwnerRegistry {
	keys := migrationCapabilityOwnerKeys()
	owners := map[migrationcapability.Subsystem]migrationcapability.Owner{
		migrationcapability.SubsystemGoose:        {Identity: migrationcapability.GooseOwnerIdentity, ContractVersion: migrationcapability.GooseOwnerContractVersion},
		migrationcapability.SubsystemRiverJobs:    {Identity: migrationcapability.RiverJobsOwnerIdentity, ContractVersion: migrationcapability.RiverJobsOwnerContractVersion},
		migrationcapability.SubsystemDuckLake:     {Identity: migrationcapability.DuckLakeOwnerIdentity, ContractVersion: migrationcapability.DuckLakeOwnerContractVersion},
		migrationcapability.SubsystemPhysicalPool: {Identity: migrationcapability.PhysicalPoolOwnerIdentity, ContractVersion: migrationcapability.PhysicalPoolContractVersion},
	}
	registry := migrationcapability.OwnerRegistry{Version: migrationcapability.OwnerRegistryVersion}
	for _, subsystem := range []migrationcapability.Subsystem{
		migrationcapability.SubsystemGoose,
		migrationcapability.SubsystemRiverJobs,
		migrationcapability.SubsystemDuckLake,
		migrationcapability.SubsystemPhysicalPool,
	} {
		registry.Keys = append(registry.Keys, migrationcapability.OwnerKey{
			Owner: owners[subsystem], KeyID: "qualification-key", Algorithm: migrationcapability.OwnerProofAlgorithm,
			PublicKey: base64.StdEncoding.EncodeToString(keys[subsystem].Public().(ed25519.PublicKey)),
		})
	}
	return registry
}

func migrationCapabilityOwnerKeys() map[migrationcapability.Subsystem]ed25519.PrivateKey {
	return map[migrationcapability.Subsystem]ed25519.PrivateKey{
		migrationcapability.SubsystemGoose:        ed25519.NewKeyFromSeed(bytes.Repeat([]byte{31}, ed25519.SeedSize)),
		migrationcapability.SubsystemRiverJobs:    ed25519.NewKeyFromSeed(bytes.Repeat([]byte{32}, ed25519.SeedSize)),
		migrationcapability.SubsystemDuckLake:     ed25519.NewKeyFromSeed(bytes.Repeat([]byte{33}, ed25519.SeedSize)),
		migrationcapability.SubsystemPhysicalPool: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{34}, ed25519.SeedSize)),
	}
}
