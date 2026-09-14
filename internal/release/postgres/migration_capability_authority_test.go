package postgres

import (
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
		published, err := repository.PublishMigrationCapability(t.Context(), capability)
		if err != nil {
			t.Fatalf("publish predecessor %s: %v", capability.Subsystem, err)
		}
		replayed, err := repository.PublishMigrationCapability(t.Context(), capability)
		if err != nil {
			t.Fatalf("replay predecessor %s: %v", capability.Subsystem, err)
		}
		resolved, err := repository.ResolveMigrationCapability(t.Context(), capability.ArtifactAdmissionDigest, target, capability.Subsystem)
		if err != nil {
			t.Fatalf("resolve predecessor %s: %v", capability.Subsystem, err)
		}
		if !reflect.DeepEqual(published, replayed) || !reflect.DeepEqual(published, resolved) {
			t.Fatalf("%s publication/replay/readback differ", capability.Subsystem)
		}
	}

	if _, err := repository.ResolveMigrationCapability(t.Context(), candidateIdentity.ArtifactAdmissionDigest, target, migrationcapability.SubsystemGoose); !errors.Is(err, ErrMigrationCapabilityNotFound) {
		t.Fatalf("predecessor capability reused for candidate: %v", err)
	}
	for _, capability := range candidateCapabilities {
		if _, err := repository.PublishMigrationCapability(t.Context(), capability); err != nil {
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
	identity, err := repository.PublishArtifactAdmission(t.Context(), artifactAdmission("candidate", "a", "1"))
	if err != nil {
		t.Fatal(err)
	}
	target := digest("b")
	capability := migrationCapabilities(t, identity.ArtifactAdmissionDigest, target, "16")[0]

	unknownArtifact := capability
	unknownArtifact.ArtifactAdmissionDigest = digest("9")
	if _, err := repository.PublishMigrationCapability(t.Context(), unknownArtifact); !errors.Is(err, ErrMigrationCapabilityArtifact) {
		t.Fatalf("unknown artifact error = %v", err)
	}
	wrongTarget := capability
	wrongTarget.TargetIdentityDigest = "prod-target"
	if _, err := repository.PublishMigrationCapability(t.Context(), wrongTarget); !errors.Is(err, ErrMigrationCapabilityInvalid) {
		t.Fatalf("wrong target error = %v", err)
	}
	if _, err := repository.PublishMigrationCapability(t.Context(), capability); err != nil {
		t.Fatal(err)
	}
	conflicting := capability
	conflicting.Goose = &migrationcapability.GooseCapability{
		SchemaVersion: "goose/v17", RunnableSchemaVersions: []string{"goose/v16", "goose/v17"}, MigrationGraphDigest: digest("8"),
	}
	if _, err := repository.PublishMigrationCapability(t.Context(), conflicting); !errors.Is(err, ErrMigrationCapabilityConflict) {
		t.Fatalf("conflicting publication error = %v", err)
	}
	if _, err := repository.ResolveMigrationCapability(t.Context(), identity.ArtifactAdmissionDigest, digest("c"), capability.Subsystem); !errors.Is(err, ErrMigrationCapabilityNotFound) {
		t.Fatalf("wrong target lookup error = %v", err)
	}
	if _, err := repository.ResolveMigrationCapability(t.Context(), digest("c"), target, capability.Subsystem); !errors.Is(err, ErrMigrationCapabilityNotFound) {
		t.Fatalf("wrong artifact lookup error = %v", err)
	}

	if err := repository.RevokeArtifactAdmission(t.Context(), artifactAdmission("candidate", "a", "1").Release.Image, "security decision revoked", time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ResolveMigrationCapability(t.Context(), identity.ArtifactAdmissionDigest, target, capability.Subsystem); !errors.Is(err, ErrMigrationCapabilityArtifact) || !errors.Is(err, ErrArtifactAdmissionRevoked) {
		t.Fatalf("revoked artifact capability error = %v", err)
	}
}

func TestMigrationCapabilityAuthorityRejectsCorruptStoredBytes(t *testing.T) {
	pool := testDB(t)
	repository := New(pool)
	identity, err := repository.PublishArtifactAdmission(t.Context(), artifactAdmission("candidate", "a", "1"))
	if err != nil {
		t.Fatal(err)
	}
	target := digest("b")
	capability := migrationCapabilities(t, identity.ArtifactAdmissionDigest, target, "16")[0]
	originalDigest, _ := capability.Digest()
	mutated := capability
	mutated.Goose = &migrationcapability.GooseCapability{SchemaVersion: "goose/v17", RunnableSchemaVersions: []string{"goose/v17"}, MigrationGraphDigest: digest("7")}
	mutatedBytes, _ := mutated.CanonicalJSON()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO release.migration_capability
		(artifact_admission_digest, target_identity_digest, subsystem, owner_identity,
		 owner_contract_version, capability_version, capability_digest, capability_bytes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, capability.ArtifactAdmissionDigest, target, capability.Subsystem,
		capability.Owner.Identity, capability.Owner.ContractVersion, capability.Version, originalDigest, mutatedBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ResolveMigrationCapability(t.Context(), capability.ArtifactAdmissionDigest, target, capability.Subsystem); !errors.Is(err, ErrMigrationCapabilityDigestMismatch) {
		t.Fatalf("corrupt read error = %v", err)
	}
}

func TestMigrationCapabilityAuthorityIsImmutableAndConcurrent(t *testing.T) {
	pool := testDB(t)
	repository := New(pool)
	identity, err := repository.PublishArtifactAdmission(t.Context(), artifactAdmission("candidate", "a", "1"))
	if err != nil {
		t.Fatal(err)
	}
	capability := migrationCapabilities(t, identity.ArtifactAdmissionDigest, digest("b"), "16")[0]

	var wait sync.WaitGroup
	errorsSeen := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, publishErr := repository.PublishMigrationCapability(t.Context(), capability)
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
