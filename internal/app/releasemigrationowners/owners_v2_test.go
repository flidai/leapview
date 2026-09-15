package releasemigrationowners

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/compatibility"
	postgresmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/release/artifactadmission"
	"github.com/flidai/leapview/internal/release/migrationcapability"
	"github.com/flidai/leapview/internal/release/migrationcompatibility"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const qualificationOwnerKeyID = "qualification-key"

func TestConcreteOwnersResolveAuthoritativeCapabilities(t *testing.T) {
	fixture := newOwnerFixture(t)
	goose, river, duckLake, pool := fixture.resolveAll(t, fixture.selection)

	evidence, err := migrationcompatibility.NewEvidenceV2(goose, river, duckLake, pool)
	if err != nil {
		t.Fatalf("assemble owner evidence: %v", err)
	}
	if evidence.OverallCompatibility != transitionpreflight.CompatibilityBackwardCompatible {
		t.Fatalf("compatibility = %q, want backward compatible", evidence.OverallCompatibility)
	}
	if evidence.Binding != goose.Binding || evidence.Binding != river.Binding || evidence.Binding != duckLake.Binding || evidence.Binding != pool.Binding {
		t.Fatal("concrete owners did not converge on the authority-resolved binding")
	}
	if duckLake.State.PredecessorCatalogSchemaVersion != "ducklake-catalog/v1" || duckLake.State.CandidateCatalogSchemaVersion != "ducklake-catalog/v2" {
		t.Fatalf("DuckLake schemas = %q -> %q, want artifact-owned v1 -> v2", duckLake.State.PredecessorCatalogSchemaVersion, duckLake.State.CandidateCatalogSchemaVersion)
	}
	canonical, err := evidence.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := migrationcompatibility.ParseEvidenceV2(canonical)
	if err != nil || parsed != evidence {
		t.Fatalf("owner evidence round trip = %#v, %v", parsed, err)
	}
	digestBefore, err := evidence.Digest()
	if err != nil {
		t.Fatal(err)
	}
	secondGoose, secondRiver, secondDuckLake, secondPool := fixture.resolveAll(t, fixture.selection)
	secondEvidence, err := migrationcompatibility.NewEvidenceV2(secondGoose, secondRiver, secondDuckLake, secondPool)
	if err != nil {
		t.Fatal(err)
	}
	digestAfter, err := secondEvidence.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if digestAfter != digestBefore {
		t.Fatalf("v2 digest changed across equivalent authority reads: %s != %s", digestAfter, digestBefore)
	}
}

func TestAuthorityV2ComposesOnlyConcreteOwnerEvidence(t *testing.T) {
	fixture := newOwnerFixture(t)
	authority, err := NewAuthorityV2(fixture.releases, fixture.targets, fixture.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	first, err := authority.Resolve(t.Context(), fixture.selection)
	if err != nil {
		t.Fatal(err)
	}
	second, err := authority.Resolve(t.Context(), fixture.selection)
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, err := first.Digest()
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := second.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("authoritative v2 digest changed: %s != %s", firstDigest, secondDigest)
	}
	if _, err := NewAuthorityV2(nil, fixture.targets, fixture.capabilities); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("nil artifact authority error = %v", err)
	}
}

func TestConcreteOwnersCannotReuseCapabilitiesForAnotherArtifactPair(t *testing.T) {
	fixture := newOwnerFixture(t)
	secondPredecessor := admission("second-predecessor", '8', '3')
	secondCandidate := admission("second-candidate", '9', '4')
	if _, err := fixture.releases.PublishArtifactAdmission(t.Context(), secondPredecessor); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.releases.PublishArtifactAdmission(t.Context(), secondCandidate); err != nil {
		t.Fatal(err)
	}
	selection := fixture.selection
	selection.PredecessorArtifactReference = secondPredecessor.Release.Image
	selection.CandidateArtifactReference = secondCandidate.Release.Image
	fixture.requireAllOwnersReject(t, selection, ErrOwnerState)
}

func TestConcreteOwnersRejectMissingCapability(t *testing.T) {
	fixture := newOwnerFixture(t)
	missingCandidate := admission("missing-ducklake-candidate", '7', '5')
	identity, err := fixture.releases.PublishArtifactAdmission(t.Context(), missingCandidate)
	if err != nil {
		t.Fatal(err)
	}
	fixture.publishArtifactCapabilities(t, identity.ArtifactAdmissionDigest, true, migrationcapability.SubsystemDuckLake)
	selection := fixture.selection
	selection.CandidateArtifactReference = missingCandidate.Release.Image
	if _, err := fixture.duckLake.Resolve(t.Context(), selection); !errors.Is(err, ErrOwnerState) {
		t.Fatalf("missing DuckLake capability error = %v, want owner state", err)
	}
}

func TestConcreteOwnersRejectOtherTargetCapabilities(t *testing.T) {
	fixture := newOwnerFixture(t)
	const otherTarget = "target-other"
	if _, err := fixture.targets.CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: otherTarget, ProjectID: "project", Environment: "staging", TargetRevision: 1}); err != nil {
		t.Fatal(err)
	}
	selection := fixture.selection
	selection.TargetID = otherTarget
	fixture.requireAllOwnersReject(t, selection, ErrOwnerState)
}

func TestConcreteOwnersRejectRevokedCapabilityArtifact(t *testing.T) {
	fixture := newOwnerFixture(t)
	if err := fixture.releases.RevokeArtifactAdmission(t.Context(), fixture.candidate.Release.Image, "qualification revocation", time.Date(2026, 9, 15, 1, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	fixture.requireAllOwnersReject(t, fixture.selection, ErrOwnerState)
}

func TestConcreteOwnerDetectsCapabilityRevocationDuringResolution(t *testing.T) {
	fixture := newOwnerFixture(t)
	resolved, err := fixture.goose.binding.resolve(t.Context(), fixture.selection)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := fixture.goose.binding.resolveCapabilities(t.Context(), resolved, migrationcapability.SubsystemGoose)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.releases.RevokeArtifactAdmission(t.Context(), fixture.candidate.Release.Image, "qualification revocation", time.Date(2026, 9, 15, 1, 1, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.goose.binding.confirm(t.Context(), fixture.selection, resolved, migrationcapability.SubsystemGoose, pair); !errors.Is(err, ErrOwnerStale) {
		t.Fatalf("stale capability error = %v, want owner stale", err)
	}
}

func TestConcreteOwnersRejectCallerProjectionFields(t *testing.T) {
	typeOfSelection := reflect.TypeFor[SelectionV2]()
	for i := 0; i < typeOfSelection.NumField(); i++ {
		name := typeOfSelection.Field(i).Name
		if strings.Contains(name, "Digest") || strings.Contains(name, "Compatibility") || strings.Contains(name, "Evidence") || strings.Contains(name, "Binding") || strings.Contains(name, "State") {
			t.Fatalf("production selection exposes caller projection field %q", name)
		}
	}
}

func TestOwnerConstructorsRequireConcreteCapabilityAuthority(t *testing.T) {
	if _, err := NewGooseOwnerV2(nil, nil, nil); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("Goose constructor error = %v", err)
	}
	if _, err := NewRiverJobsOwnerV2(nil, nil, nil); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("River constructor error = %v", err)
	}
	if _, err := NewDuckLakeOwnerV2(nil, nil, nil); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("DuckLake constructor error = %v", err)
	}
	if _, err := NewPhysicalPoolOwnerV2(nil, nil, nil); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("PhysicalPool constructor error = %v", err)
	}
}

type ownerFixture struct {
	selection    SelectionV2
	predecessor  artifactadmission.Admission
	candidate    artifactadmission.Admission
	releases     *releasepostgres.Repository
	targets      *deploymentpostgres.Repository
	capabilities *releasepostgres.MigrationCapabilityAuthority
	goose        *GooseOwnerV2
	river        *RiverJobsOwnerV2
	duckLake     *DuckLakeOwnerV2
	physicalPool *PhysicalPoolOwnerV2
}

func newOwnerFixture(t *testing.T) ownerFixture {
	t.Helper()
	harness := postgrestest.Start(t)
	ownerRole := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migratorRole := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Password: "owner-evidence", Login: true})
	harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime"})
	harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance"})
	harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	harness.GrantRole(t, ownerRole, migratorRole)
	database := harness.NewDatabase(t, "migration_owner_v2_"+shortName(t.Name()))
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

	releases := releasepostgres.New(pool)
	predecessor := admission("predecessor", 'a', '1')
	candidate := admission("candidate", 'b', '2')
	predecessorIdentity, err := releases.PublishArtifactAdmission(t.Context(), predecessor)
	if err != nil {
		t.Fatal(err)
	}
	candidateIdentity, err := releases.PublishArtifactAdmission(t.Context(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	targets := deploymentpostgres.New(pool)
	const targetID = "target-production"
	const targetRevision = int64(7)
	if _, err := targets.CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: targetID, ProjectID: "project", Environment: "production", TargetRevision: targetRevision}); err != nil {
		t.Fatal(err)
	}
	targetDigest, err := (migrationcompatibility.DeploymentTargetIdentityV2{TargetID: targetID, TargetRevision: targetRevision}).Digest()
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := releasepostgres.NewMigrationCapabilityAuthority(releases, qualificationOwnerRegistry())
	if err != nil {
		t.Fatal(err)
	}
	fixture := ownerFixture{
		selection:    SelectionV2{PredecessorArtifactReference: predecessor.Release.Image, CandidateArtifactReference: candidate.Release.Image, TargetID: targetID},
		predecessor:  predecessor,
		candidate:    candidate,
		releases:     releases,
		targets:      targets,
		capabilities: capabilities,
	}
	fixture.publishArtifactCapabilitiesForTarget(t, predecessorIdentity.ArtifactAdmissionDigest, targetDigest, false)
	fixture.publishArtifactCapabilitiesForTarget(t, candidateIdentity.ArtifactAdmissionDigest, targetDigest, true)
	fixture.goose, err = NewGooseOwnerV2(releases, targets, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	fixture.river, err = NewRiverJobsOwnerV2(releases, targets, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	fixture.duckLake, err = NewDuckLakeOwnerV2(releases, targets, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	fixture.physicalPool, err = NewPhysicalPoolOwnerV2(releases, targets, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture ownerFixture) resolveAll(t *testing.T, selection SelectionV2) (migrationcompatibility.GooseOwnerEvidenceV2, migrationcompatibility.RiverJobsOwnerEvidenceV2, migrationcompatibility.DuckLakeOwnerEvidenceV2, migrationcompatibility.PhysicalPoolOwnerEvidenceV2) {
	t.Helper()
	goose, err := fixture.goose.Resolve(t.Context(), selection)
	if err != nil {
		t.Fatalf("resolve Goose owner: %v", err)
	}
	river, err := fixture.river.Resolve(t.Context(), selection)
	if err != nil {
		t.Fatalf("resolve River/jobs owner: %v", err)
	}
	duckLake, err := fixture.duckLake.Resolve(t.Context(), selection)
	if err != nil {
		t.Fatalf("resolve DuckLake owner: %v", err)
	}
	pool, err := fixture.physicalPool.Resolve(t.Context(), selection)
	if err != nil {
		t.Fatalf("resolve PhysicalPool owner: %v", err)
	}
	return goose, river, duckLake, pool
}

func (fixture ownerFixture) requireAllOwnersReject(t *testing.T, selection SelectionV2, want error) {
	t.Helper()
	for name, resolve := range map[string]func() error{
		"Goose":        func() error { _, err := fixture.goose.Resolve(t.Context(), selection); return err },
		"River/jobs":   func() error { _, err := fixture.river.Resolve(t.Context(), selection); return err },
		"DuckLake":     func() error { _, err := fixture.duckLake.Resolve(t.Context(), selection); return err },
		"PhysicalPool": func() error { _, err := fixture.physicalPool.Resolve(t.Context(), selection); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := resolve(); !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
		})
	}
}

func (fixture ownerFixture) publishArtifactCapabilities(t *testing.T, artifactDigest string, candidate bool, skip ...migrationcapability.Subsystem) {
	t.Helper()
	targetDigest, err := (migrationcompatibility.DeploymentTargetIdentityV2{TargetID: fixture.selection.TargetID, TargetRevision: 7}).Digest()
	if err != nil {
		t.Fatal(err)
	}
	fixture.publishArtifactCapabilitiesForTarget(t, artifactDigest, targetDigest, candidate, skip...)
}

func (fixture ownerFixture) publishArtifactCapabilitiesForTarget(t *testing.T, artifactDigest, targetDigest string, candidate bool, skip ...migrationcapability.Subsystem) {
	t.Helper()
	for _, capability := range artifactCapabilities(t, artifactDigest, targetDigest, candidate) {
		if len(skip) == 1 && capability.Subsystem == skip[0] {
			continue
		}
		evidence, err := migrationcapability.SignOwnerEvidence(capability, qualificationOwnerKeyID, qualificationOwnerPrivateKeys()[capability.Subsystem])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.capabilities.Publish(t.Context(), evidence); err != nil {
			t.Fatalf("publish %s capability: %v", capability.Subsystem, err)
		}
	}
}

func artifactCapabilities(t *testing.T, artifactDigest, targetDigest string, candidate bool) []migrationcapability.Capability {
	t.Helper()
	predecessorTuple, candidateTuple := capabilityTuples()
	predecessorTupleDigest, err := predecessorTuple.Digest()
	if err != nil {
		t.Fatal(err)
	}
	candidateTupleDigest, err := candidateTuple.Digest()
	if err != nil {
		t.Fatal(err)
	}
	schema, riverSchema, jobHistory, catalogSchema, tuple := "goose/v15", "river/v5", "jobs/v15", "ducklake-catalog/v1", predecessorTuple
	if candidate {
		schema, riverSchema, jobHistory, catalogSchema, tuple = "goose/v16", "river/v6", "jobs/v16", "ducklake-catalog/v2", candidateTuple
	}
	base := migrationcapability.Capability{Version: migrationcapability.Version, ArtifactAdmissionDigest: artifactDigest, TargetIdentityDigest: targetDigest}
	goose := base
	goose.Subsystem = migrationcapability.SubsystemGoose
	goose.Owner = migrationcapability.Owner{Identity: migrationcapability.GooseOwnerIdentity, ContractVersion: migrationcapability.GooseOwnerContractVersion}
	goose.Goose = &migrationcapability.GooseCapability{SchemaVersion: schema, RunnableSchemaVersions: []string{"goose/v15", "goose/v16"}, MigrationGraphDigest: digest('1')}
	river := base
	river.Subsystem = migrationcapability.SubsystemRiverJobs
	river.Owner = migrationcapability.Owner{Identity: migrationcapability.RiverJobsOwnerIdentity, ContractVersion: migrationcapability.RiverJobsOwnerContractVersion}
	river.RiverJobs = &migrationcapability.RiverJobsCapability{
		SchemaVersion: riverSchema, RunnableSchemaVersions: []string{"river/v5", "river/v6"},
		JobHistoryVersion: jobHistory, RunnableJobHistoryVersions: []string{"jobs/v15", "jobs/v16"}, MigrationGraphDigest: digest('2'),
	}
	duckLake := base
	duckLake.Subsystem = migrationcapability.SubsystemDuckLake
	duckLake.Owner = migrationcapability.Owner{Identity: migrationcapability.DuckLakeOwnerIdentity, ContractVersion: migrationcapability.DuckLakeOwnerContractVersion}
	duckLake.DuckLake = &migrationcapability.DuckLakeCapability{
		Compatibility: tuple, CatalogSchemaVersion: catalogSchema,
		RunnableCatalogSchemaVersions: []string{"ducklake-catalog/v1", "ducklake-catalog/v2"}, MigrationGraphDigest: digest('3'),
	}
	pool := base
	pool.Subsystem = migrationcapability.SubsystemPhysicalPool
	pool.Owner = migrationcapability.Owner{Identity: migrationcapability.PhysicalPoolOwnerIdentity, ContractVersion: migrationcapability.PhysicalPoolContractVersion}
	pool.PhysicalPool = &migrationcapability.PhysicalPoolCapability{Compatibility: tuple, CompatibleTupleDigests: []string{predecessorTupleDigest, candidateTupleDigest}}
	return []migrationcapability.Capability{goose, river, duckLake, pool}
}

func capabilityTuples() (physicalpool.Compatibility, physicalpool.Compatibility) {
	predecessor := physicalpool.Compatibility{
		DuckDBRuntime: "duckdb:1.5.3", DuckLakeExtension: "ducklake:0.2.0", CatalogFormat: "ducklake:v1",
		StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1",
	}
	candidate := predecessor
	candidate.DuckDBRuntime = "duckdb:1.5.4"
	candidate.DuckLakeExtension = "ducklake:0.3.0"
	return predecessor, candidate
}

func qualificationOwnerRegistry() migrationcapability.OwnerRegistry {
	keys := qualificationOwnerPrivateKeys()
	owners := map[migrationcapability.Subsystem]migrationcapability.Owner{
		migrationcapability.SubsystemGoose:        {Identity: migrationcapability.GooseOwnerIdentity, ContractVersion: migrationcapability.GooseOwnerContractVersion},
		migrationcapability.SubsystemRiverJobs:    {Identity: migrationcapability.RiverJobsOwnerIdentity, ContractVersion: migrationcapability.RiverJobsOwnerContractVersion},
		migrationcapability.SubsystemDuckLake:     {Identity: migrationcapability.DuckLakeOwnerIdentity, ContractVersion: migrationcapability.DuckLakeOwnerContractVersion},
		migrationcapability.SubsystemPhysicalPool: {Identity: migrationcapability.PhysicalPoolOwnerIdentity, ContractVersion: migrationcapability.PhysicalPoolContractVersion},
	}
	registry := migrationcapability.OwnerRegistry{Version: migrationcapability.OwnerRegistryVersion}
	for _, subsystem := range []migrationcapability.Subsystem{migrationcapability.SubsystemGoose, migrationcapability.SubsystemRiverJobs, migrationcapability.SubsystemDuckLake, migrationcapability.SubsystemPhysicalPool} {
		registry.Keys = append(registry.Keys, migrationcapability.OwnerKey{
			Owner: owners[subsystem], KeyID: qualificationOwnerKeyID, Algorithm: migrationcapability.OwnerProofAlgorithm,
			PublicKey: base64.StdEncoding.EncodeToString(keys[subsystem].Public().(ed25519.PublicKey)),
		})
	}
	return registry
}

func qualificationOwnerPrivateKeys() map[migrationcapability.Subsystem]ed25519.PrivateKey {
	return map[migrationcapability.Subsystem]ed25519.PrivateKey{
		migrationcapability.SubsystemGoose:        ed25519.NewKeyFromSeed(bytes.Repeat([]byte{31}, ed25519.SeedSize)),
		migrationcapability.SubsystemRiverJobs:    ed25519.NewKeyFromSeed(bytes.Repeat([]byte{32}, ed25519.SeedSize)),
		migrationcapability.SubsystemDuckLake:     ed25519.NewKeyFromSeed(bytes.Repeat([]byte{33}, ed25519.SeedSize)),
		migrationcapability.SubsystemPhysicalPool: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{34}, ed25519.SeedSize)),
	}
}

func admission(id string, image, revision byte) artifactadmission.Admission {
	revisionText := strings.Repeat(string(revision), 40)
	return artifactadmission.Admission{
		Version:            artifactadmission.AdmissionVersion,
		Release:            compatibility.ReleaseIdentity{ReleaseID: id, Version: "1.0." + string(revision), SourceRevision: revisionText, Image: immutableImage(image), Distribution: "distroless", Platform: "linux/amd64"},
		ArchitectureMarker: transitionpreflight.ArchitecturePostgreSQL, Repository: "ghcr.io/flidai/leapview", OCIDigest: digest(image), Decision: artifactadmission.DecisionAdmitted,
		Provenance:     artifactadmission.ProvenanceResult{Reference: digest('c'), Repository: artifactadmission.SourceRepository, Workflow: "flidai/leapview/.github/workflows/release.yml", SourceRevision: revisionText},
		SBOM:           artifactadmission.SBOMResult{Reference: digest('d'), PredicateType: artifactadmission.SBOMPredicateSPDX, Producer: artifactadmission.SBOMProducerBuildx},
		SecurityPolicy: artifactadmission.SecurityPolicyResult{Version: artifactadmission.SecurityPolicyVersion, Reference: digest('e'), Scanner: artifactadmission.SecurityScannerTrivy},
		AdmittedAt:     time.Date(2026, 9, 14, 12, 0, int(revision-'0'), 0, time.UTC),
	}
}

func immutableImage(value byte) string { return "ghcr.io/flidai/leapview@" + digest(value) }
func digest(value byte) string         { return "sha256:" + strings.Repeat(string(value), 64) }

func shortName(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}
