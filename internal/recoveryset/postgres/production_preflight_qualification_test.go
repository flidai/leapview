//go:build fai518qualification

package postgres_test

import (
	"bytes"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	releasetransitionapp "github.com/flidai/leapview/internal/app/releasetransitionpreflightproduction"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/compatibility"
	postgresmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/recoveryset"
	recoverysetpostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	"github.com/flidai/leapview/internal/release/artifactadmission"
	"github.com/flidai/leapview/internal/release/migrationcapability"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestFAI518ProductionPreflightCompositionUsesAuthoritativeV2Evidence(t *testing.T) {
	pool := productionPreflightDB(t)
	releases := releasepostgres.New(pool)
	targets := deploymentpostgres.New(pool)
	recoverySets := recoverysetpostgres.New(pool)
	capabilities, err := releasepostgres.NewMigrationCapabilityAuthority(releases, productionOwnerRegistry())
	if err != nil {
		t.Fatal(err)
	}

	const targetID = "target"
	const targetRevision = int64(1)
	if _, err := targets.CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: targetID, ProjectID: "project", Environment: "production", TargetRevision: targetRevision}); err != nil {
		t.Fatal(err)
	}
	targetDigest, err := (transitionpreflight.TargetIdentity{TargetID: targetID, TargetRevision: targetRevision}).Digest()
	if err != nil {
		t.Fatal(err)
	}
	predecessor := productionAdmission("predecessor", 'a', '1')
	candidate := productionAdmission("candidate", 'b', '2')
	predecessorIdentity := publishProductionAdmission(t, releases, predecessor)
	candidateIdentity := publishProductionAdmission(t, releases, candidate)
	publishProductionCapabilities(t, capabilities, predecessorIdentity.ArtifactAdmissionDigest, targetDigest, false)
	publishProductionCapabilities(t, capabilities, candidateIdentity.ArtifactAdmissionDigest, targetDigest, true)
	publishProductionPolicy(t, releases, predecessorIdentity, candidateIdentity)

	published := productionPublishedFrontier(t, recoverySets, productionRecoverySetFixture(t))
	resolver, err := releasetransitionapp.NewProductionResolver(releasetransitionapp.ProductionDependencies{
		Releases: releases, Targets: targets, MigrationCapabilities: capabilities, RecoverySets: recoverySets,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := transitionpreflight.ResolutionRequest{
		PredecessorRef: predecessor.Release.Image,
		CandidateRef:   candidate.Release.Image,
		TargetRef:      targetID,
		RecoveryFrontier: transitionpreflight.RecoveryFrontierRef{
			SetID: published.ID, Digest: published.FrontierDigest,
		},
	}
	first, err := resolver.ResolveAndEvaluate(t.Context(), request)
	if err != nil {
		t.Fatalf("production preflight: %v", err)
	}
	second, err := resolver.ResolveAndEvaluate(t.Context(), request)
	if err != nil {
		t.Fatalf("production preflight replay: %v", err)
	}
	if first.Evidence.Decision != transitionpreflight.DecisionBinaryRollbackCompatible || first.EvidenceDigest != second.EvidenceDigest || !bytes.Equal(first.CanonicalEvidence, second.CanonicalEvidence) {
		t.Fatalf("production evidence did not replay canonically: first=%#v second=%#v", first, second)
	}
	if _, err := transitionpreflight.ParseEvidence(first.CanonicalEvidence); err != nil {
		t.Fatalf("parse production evidence: %v", err)
	}

	t.Run("wrong artifact pair", func(t *testing.T) {
		invalid := request
		invalid.PredecessorRef, invalid.CandidateRef = request.CandidateRef, request.PredecessorRef
		if _, err := resolver.ResolveAndEvaluate(t.Context(), invalid); err == nil {
			t.Fatal("reversed artifact pair produced preflight evidence")
		}
	})
	t.Run("wrong target", func(t *testing.T) {
		if _, err := targets.CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: "other-target", ProjectID: "other-project", Environment: "production", TargetRevision: 1}); err != nil {
			t.Fatal(err)
		}
		invalid := request
		invalid.TargetRef = "other-target"
		if _, err := resolver.ResolveAndEvaluate(t.Context(), invalid); err == nil {
			t.Fatal("wrong target produced preflight evidence")
		}
	})
	t.Run("missing capability", func(t *testing.T) {
		missing := productionAdmission("missing-capability", 'c', '3')
		missingIdentity := publishProductionAdmission(t, releases, missing)
		publishProductionCapabilities(t, capabilities, missingIdentity.ArtifactAdmissionDigest, targetDigest, true, migrationcapability.SubsystemDuckLake)
		publishProductionPolicy(t, releases, predecessorIdentity, missingIdentity)
		invalid := request
		invalid.CandidateRef = missing.Release.Image
		if _, err := resolver.ResolveAndEvaluate(t.Context(), invalid); err == nil {
			t.Fatal("missing capability produced preflight evidence")
		}
	})
	t.Run("invalid release policy", func(t *testing.T) {
		withoutPolicy := productionAdmission("missing-policy", 'd', '4')
		withoutPolicyIdentity := publishProductionAdmission(t, releases, withoutPolicy)
		publishProductionCapabilities(t, capabilities, withoutPolicyIdentity.ArtifactAdmissionDigest, targetDigest, true)
		invalid := request
		invalid.CandidateRef = withoutPolicy.Release.Image
		if _, err := resolver.ResolveAndEvaluate(t.Context(), invalid); !errors.Is(err, releasepostgres.ErrPolicyNotFound) {
			t.Fatalf("missing policy error = %v, want ErrPolicyNotFound", err)
		}
	})
	t.Run("invalid recovery frontier", func(t *testing.T) {
		prepared := productionRecoverySetFixture(t)
		prepared.ID = "018f3f83-7b2f-7b37-9f9e-000000000900"
		created, err := recoverySets.Create(t.Context(), prepared)
		if err != nil {
			t.Fatal(err)
		}
		invalid := request
		invalid.RecoveryFrontier = transitionpreflight.RecoveryFrontierRef{SetID: created.ID, Digest: created.FrontierDigest}
		if _, err := resolver.ResolveAndEvaluate(t.Context(), invalid); !errors.Is(err, transitionpreflight.ErrStaleFrontier) {
			t.Fatalf("prepared frontier error = %v, want ErrStaleFrontier", err)
		}
	})
	t.Run("conflicting owner evidence", func(t *testing.T) {
		conflict := productionCapabilities(t, candidateIdentity.ArtifactAdmissionDigest, targetDigest, true)[0]
		conflict.Goose.SchemaVersion = "goose/v99"
		conflict.Goose.RunnableSchemaVersions = append(conflict.Goose.RunnableSchemaVersions, "goose/v99")
		evidence, err := migrationcapability.SignOwnerEvidence(conflict, productionOwnerKeyID, productionOwnerPrivateKeys()[conflict.Subsystem])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := capabilities.Publish(t.Context(), evidence); !errors.Is(err, releasepostgres.ErrMigrationCapabilityConflict) {
			t.Fatalf("conflicting capability error = %v", err)
		}
	})
	t.Run("revoked artifact and stale capability", func(t *testing.T) {
		if err := releases.RevokeArtifactAdmission(t.Context(), candidate.Release.Image, "qualification revocation", time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
		if _, err := resolver.ResolveAndEvaluate(t.Context(), request); !errors.Is(err, releasepostgres.ErrArtifactAdmissionRevoked) {
			t.Fatalf("revoked artifact error = %v, want ErrArtifactAdmissionRevoked", err)
		}
	})
}

func TestProductionPreflightDependenciesExposeNoProjectionInjection(t *testing.T) {
	if _, err := releasetransitionapp.NewProductionResolver(releasetransitionapp.ProductionDependencies{}); !errors.Is(err, releasetransitionapp.ErrProductionAuthorityUnavailable) {
		t.Fatalf("empty production dependencies error = %v", err)
	}
}

func productionPreflightDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	harness := postgrestest.Start(t)
	ownerRole := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migratorRole := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Password: "production-preflight", Login: true})
	harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime"})
	harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance"})
	harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	harness.GrantRole(t, ownerRole, migratorRole)
	database := harness.NewDatabase(t, "production_preflight")
	harness.GrantDatabase(t, database.Name, ownerRole, "CREATE")
	harness.GrantDatabase(t, database.Name, migratorRole, "CONNECT", "CREATE")
	pool, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), `ALTER DATABASE `+database.Name+` OWNER TO leapview_control_owner; REVOKE ALL ON SCHEMA public FROM PUBLIC; GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	control, err := sql.Open("pgx", database.URL(migratorRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	river, err := pgxpool.New(t.Context(), database.URL(migratorRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(river.Close)
	if err := postgresmigrations.ApplyRiverAndGoose(t.Context(), river, control, nil); err != nil {
		t.Fatalf("apply control schema: %v", err)
	}
	return pool
}

func publishProductionAdmission(t *testing.T, repository *releasepostgres.Repository, admission artifactadmission.Admission) transitionpreflight.ArtifactIdentity {
	t.Helper()
	identity, err := repository.PublishArtifactAdmission(t.Context(), admission)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func publishProductionPolicy(t *testing.T, repository *releasepostgres.Repository, predecessor, candidate transitionpreflight.ArtifactIdentity) {
	t.Helper()
	predecessorDigest, err := predecessor.Digest()
	if err != nil {
		t.Fatal(err)
	}
	candidateDigest, err := candidate.Digest()
	if err != nil {
		t.Fatal(err)
	}
	policy := transitionpreflight.ReleasePolicy{Version: transitionpreflight.ReleasePolicyVersion, Rules: []transitionpreflight.ReleasePolicyRule{{
		PredecessorArtifactDigest: predecessorDigest, CandidateArtifactDigest: candidateDigest,
		RollbackFromArtifactDigest: candidateDigest, RollbackToArtifactDigest: predecessorDigest,
		Decision: transitionpreflight.DecisionBinaryRollbackCompatible,
	}}}
	policy.Digest, err = policy.ContentDigest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PublishReleasePolicy(t.Context(), predecessor, candidate, policy); err != nil {
		t.Fatal(err)
	}
}

func publishProductionCapabilities(t *testing.T, authority *releasepostgres.MigrationCapabilityAuthority, artifactDigest, targetDigest string, candidate bool, skip ...migrationcapability.Subsystem) {
	t.Helper()
	for _, capability := range productionCapabilities(t, artifactDigest, targetDigest, candidate) {
		if len(skip) == 1 && capability.Subsystem == skip[0] {
			continue
		}
		evidence, err := migrationcapability.SignOwnerEvidence(capability, productionOwnerKeyID, productionOwnerPrivateKeys()[capability.Subsystem])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := authority.Publish(t.Context(), evidence); err != nil {
			t.Fatal(err)
		}
	}
}

func productionCapabilities(t *testing.T, artifactDigest, targetDigest string, candidate bool) []migrationcapability.Capability {
	t.Helper()
	predecessorTuple, candidateTuple := productionCapabilityTuples()
	predecessorTupleDigest, err := predecessorTuple.Digest()
	if err != nil {
		t.Fatal(err)
	}
	candidateTupleDigest, err := candidateTuple.Digest()
	if err != nil {
		t.Fatal(err)
	}
	gooseVersion, riverVersion, jobsVersion, catalogVersion, tuple := "goose/v15", "river/v5", "jobs/v15", "ducklake-catalog/v1", predecessorTuple
	if candidate {
		gooseVersion, riverVersion, jobsVersion, catalogVersion, tuple = "goose/v16", "river/v6", "jobs/v16", "ducklake-catalog/v2", candidateTuple
	}
	base := migrationcapability.Capability{Version: migrationcapability.Version, ArtifactAdmissionDigest: artifactDigest, TargetIdentityDigest: targetDigest}
	goose := base
	goose.Subsystem = migrationcapability.SubsystemGoose
	goose.Owner = migrationcapability.Owner{Identity: migrationcapability.GooseOwnerIdentity, ContractVersion: migrationcapability.GooseOwnerContractVersion}
	goose.Goose = &migrationcapability.GooseCapability{SchemaVersion: gooseVersion, RunnableSchemaVersions: []string{"goose/v15", "goose/v16"}, MigrationGraphDigest: productionDigest('1')}
	river := base
	river.Subsystem = migrationcapability.SubsystemRiverJobs
	river.Owner = migrationcapability.Owner{Identity: migrationcapability.RiverJobsOwnerIdentity, ContractVersion: migrationcapability.RiverJobsOwnerContractVersion}
	river.RiverJobs = &migrationcapability.RiverJobsCapability{SchemaVersion: riverVersion, RunnableSchemaVersions: []string{"river/v5", "river/v6"}, JobHistoryVersion: jobsVersion, RunnableJobHistoryVersions: []string{"jobs/v15", "jobs/v16"}, MigrationGraphDigest: productionDigest('2')}
	duckLake := base
	duckLake.Subsystem = migrationcapability.SubsystemDuckLake
	duckLake.Owner = migrationcapability.Owner{Identity: migrationcapability.DuckLakeOwnerIdentity, ContractVersion: migrationcapability.DuckLakeOwnerContractVersion}
	duckLake.DuckLake = &migrationcapability.DuckLakeCapability{Compatibility: tuple, CatalogSchemaVersion: catalogVersion, RunnableCatalogSchemaVersions: []string{"ducklake-catalog/v1", "ducklake-catalog/v2"}, MigrationGraphDigest: productionDigest('3')}
	physicalPool := base
	physicalPool.Subsystem = migrationcapability.SubsystemPhysicalPool
	physicalPool.Owner = migrationcapability.Owner{Identity: migrationcapability.PhysicalPoolOwnerIdentity, ContractVersion: migrationcapability.PhysicalPoolContractVersion}
	physicalPool.PhysicalPool = &migrationcapability.PhysicalPoolCapability{Compatibility: tuple, CompatibleTupleDigests: []string{predecessorTupleDigest, candidateTupleDigest}}
	return []migrationcapability.Capability{goose, river, duckLake, physicalPool}
}

func productionCapabilityTuples() (physicalpool.Compatibility, physicalpool.Compatibility) {
	predecessor := physicalpool.Compatibility{DuckDBRuntime: "duckdb:1.5.3", DuckLakeExtension: "ducklake:0.2.0", CatalogFormat: "ducklake:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	candidate := predecessor
	candidate.DuckDBRuntime = "duckdb:1.5.4"
	candidate.DuckLakeExtension = "ducklake:0.3.0"
	return predecessor, candidate
}

const productionOwnerKeyID = "qualification-key"

func productionOwnerRegistry() migrationcapability.OwnerRegistry {
	keys := productionOwnerPrivateKeys()
	owners := map[migrationcapability.Subsystem]migrationcapability.Owner{
		migrationcapability.SubsystemGoose:        {Identity: migrationcapability.GooseOwnerIdentity, ContractVersion: migrationcapability.GooseOwnerContractVersion},
		migrationcapability.SubsystemRiverJobs:    {Identity: migrationcapability.RiverJobsOwnerIdentity, ContractVersion: migrationcapability.RiverJobsOwnerContractVersion},
		migrationcapability.SubsystemDuckLake:     {Identity: migrationcapability.DuckLakeOwnerIdentity, ContractVersion: migrationcapability.DuckLakeOwnerContractVersion},
		migrationcapability.SubsystemPhysicalPool: {Identity: migrationcapability.PhysicalPoolOwnerIdentity, ContractVersion: migrationcapability.PhysicalPoolContractVersion},
	}
	registry := migrationcapability.OwnerRegistry{Version: migrationcapability.OwnerRegistryVersion}
	for _, subsystem := range []migrationcapability.Subsystem{migrationcapability.SubsystemGoose, migrationcapability.SubsystemRiverJobs, migrationcapability.SubsystemDuckLake, migrationcapability.SubsystemPhysicalPool} {
		registry.Keys = append(registry.Keys, migrationcapability.OwnerKey{Owner: owners[subsystem], KeyID: productionOwnerKeyID, Algorithm: migrationcapability.OwnerProofAlgorithm, PublicKey: base64.StdEncoding.EncodeToString(keys[subsystem].Public().(ed25519.PublicKey))})
	}
	return registry
}

func productionOwnerPrivateKeys() map[migrationcapability.Subsystem]ed25519.PrivateKey {
	return map[migrationcapability.Subsystem]ed25519.PrivateKey{
		migrationcapability.SubsystemGoose:        ed25519.NewKeyFromSeed(bytes.Repeat([]byte{41}, ed25519.SeedSize)),
		migrationcapability.SubsystemRiverJobs:    ed25519.NewKeyFromSeed(bytes.Repeat([]byte{42}, ed25519.SeedSize)),
		migrationcapability.SubsystemDuckLake:     ed25519.NewKeyFromSeed(bytes.Repeat([]byte{43}, ed25519.SeedSize)),
		migrationcapability.SubsystemPhysicalPool: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{44}, ed25519.SeedSize)),
	}
}

func productionAdmission(id string, image, revision byte) artifactadmission.Admission {
	revisionText := strings.Repeat(string(revision), 40)
	return artifactadmission.Admission{
		Version:            artifactadmission.AdmissionVersion,
		Release:            compatibility.ReleaseIdentity{ReleaseID: id, Version: "1.0." + string(revision), SourceRevision: revisionText, Image: "ghcr.io/flidai/leapview@" + productionDigest(image), Distribution: "distroless", Platform: "linux/amd64"},
		ArchitectureMarker: transitionpreflight.ArchitecturePostgreSQL,
		Repository:         "ghcr.io/flidai/leapview",
		OCIDigest:          productionDigest(image),
		Decision:           artifactadmission.DecisionAdmitted,
		Provenance:         artifactadmission.ProvenanceResult{Reference: productionDigest('c'), Repository: artifactadmission.SourceRepository, Workflow: "flidai/leapview/.github/workflows/release.yml", SourceRevision: revisionText},
		SBOM:               artifactadmission.SBOMResult{Reference: productionDigest('d'), PredicateType: artifactadmission.SBOMPredicateSPDX, Producer: artifactadmission.SBOMProducerBuildx},
		SecurityPolicy:     artifactadmission.SecurityPolicyResult{Version: artifactadmission.SecurityPolicyVersion, Reference: productionDigest('e'), Scanner: artifactadmission.SecurityScannerTrivy},
		AdmittedAt:         time.Date(2026, 9, 15, 10, 0, int(revision-'0'), 0, time.UTC),
	}
}

func productionDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func productionPublishedFrontier(t *testing.T, repository *recoverysetpostgres.Repository, set recoveryset.RecoverySet) recoveryset.RecoverySet {
	t.Helper()
	created, err := repository.Create(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC)
	attempt := recoveryset.ValidationAttempt{
		AttemptID: "018f3f83-7b2f-7b37-9f9e-000000000910", SetID: created.ID,
		OwnerID: "qualification-validator", FenceEpoch: created.FenceEpoch, AuditIdentity: created.AuditIdentity,
		Status: recoveryset.ValidationRunning, StartedAt: started,
	}
	if _, err := repository.BeginValidation(t.Context(), attempt); err != nil {
		t.Fatal(err)
	}
	envelope, err := recoveryset.NewValidationEvidenceEnvelope(created, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := recoveryset.NewValidationResult(envelope, started.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordValidationResult(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	attempt.Status, attempt.ResultDigest, attempt.CompletedAt = recoveryset.ValidationPassed, result.ResultDigest, started.Add(2*time.Second)
	if err := repository.CompleteValidation(t.Context(), attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Publish(t.Context(), created.ID, "qualification-publisher", created.FenceEpoch, attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	published, err := repository.ReadExact(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	return published
}

func productionRecoverySetFixture(t *testing.T) recoveryset.RecoverySet {
	t.Helper()
	compatibilityTuple := recoveryset.CompatibilityTuple{
		DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "ducklake:v1",
		StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1",
	}
	compatibilityDigest, err := compatibilityTuple.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return recoveryset.RecoverySet{
		ID: "018f3f83-7b2f-7b37-9f9e-000000000100", SchemaVersion: recoveryset.SchemaVersion,
		ClusterPoints: []recoveryset.ClusterRecoveryPoint{
			{DatabaseRole: recoveryset.DatabaseControl, ClusterIdentity: "cluster", DatabaseIdentity: "control", RecoveryIdentity: "lsn:0/1"},
			{DatabaseRole: recoveryset.DatabaseDuckLake, ClusterIdentity: "cluster", DatabaseIdentity: "ducklake", RecoveryIdentity: "lsn:0/1"},
		},
		Delivery: recoveryset.DeliveryPointer{TargetID: "target", GenerationID: "018f3f83-7b2f-7b37-9f9e-000000000101", PublicationID: "018f3f83-7b2f-7b37-9f9e-000000000102", TargetRevision: 1},
		Serving: recoveryset.SnapshotSeal{
			SealID: "018f3f83-7b2f-7b37-9f9e-000000000103", PhysicalPoolID: "pool", TenantDomain: "tenant", Region: "region", EncryptionDomain: "enc",
			ObjectNamespace: "objects/target", CatalogDatabase: "ducklake", CatalogID: "catalog", CatalogUUID: "catalog-uuid", CatalogVersion: 1, DuckLakeSnapshotID: 1,
			RelationManifestDigest: productionDigest('a'), RelationNamespace: "candidate", ClosureDigest: productionDigest('b'), ObjectRoot: "objects/target", ObjectRootDigest: productionDigest('c'),
			ArtifactRoot: "artifacts/target", ArtifactRootDigest: productionDigest('d'), ServingArtifactID: "artifact", ServingArtifactDigest: productionDigest('e'),
			CompiledGraphDigest: productionDigest('f'), CompiledConfigDigest: productionDigest('0'), SecurityDomainFingerprint: productionDigest('1'), RequestDigest: productionDigest('2'), PlanDigest: productionDigest('3'), CompatibilityDigest: compatibilityDigest,
			DuckDBVersion: "1", RuntimeVersion: "1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1",
		},
		Catalog: recoveryset.CatalogCommit{CatalogID: "catalog", CatalogDatabase: "ducklake", CatalogUUID: "catalog-uuid", CatalogVersion: 1, SnapshotID: 1},
		ObjectRoots: []recoveryset.ObjectRoot{
			{Kind: recoveryset.ObjectRootDuckLake, URI: "objects/target", VersionID: "1", Digest: productionDigest('c')},
			{Kind: recoveryset.ObjectRootServingArtifact, URI: "artifacts/target", VersionID: "1", Digest: productionDigest('d')},
		},
		Compatibility: compatibilityTuple, FenceEpoch: 2, AuditIdentity: "audit", Status: recoveryset.StatusPrepared, CreatedBy: "operator", CreatedAt: time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC),
	}
}
