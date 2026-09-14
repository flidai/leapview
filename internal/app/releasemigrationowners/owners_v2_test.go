package releasemigrationowners

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	physicalpoolpostgres "github.com/flidai/leapview/internal/analytics/physicalpool/postgres"
	"github.com/flidai/leapview/internal/deployment/extensionsupply"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/extension"
	"github.com/flidai/leapview/internal/platform/compatibility"
	postgresmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/release/artifactadmission"
	"github.com/flidai/leapview/internal/release/migrationcompatibility"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestConcreteOwnersProduceBoundEvidenceFromAuthoritativeState(t *testing.T) {
	fixture := newOwnerFixture(t, false)
	goose, err := fixture.goose.Resolve(t.Context(), fixture.selection)
	if err != nil {
		t.Fatalf("resolve Goose owner: %v", err)
	}
	river, err := fixture.river.Resolve(t.Context(), fixture.selection)
	if err != nil {
		t.Fatalf("resolve River/jobs owner: %v", err)
	}
	duckLake, err := fixture.duckLake.Resolve(t.Context(), fixture.selection)
	if err != nil {
		t.Fatalf("resolve DuckLake owner: %v", err)
	}
	pool, err := fixture.physicalPool.Resolve(t.Context(), fixture.selection)
	if err != nil {
		t.Fatalf("resolve PhysicalPool owner: %v", err)
	}
	evidence, err := migrationcompatibility.NewEvidenceV2(goose, river, duckLake, pool)
	if err != nil {
		t.Fatalf("assemble owner evidence: %v", err)
	}
	if evidence.OverallCompatibility != transitionpreflight.CompatibilityBackwardCompatible {
		t.Fatalf("compatibility = %q, want backward compatible", evidence.OverallCompatibility)
	}
	if evidence.Binding != goose.Binding || evidence.Binding != river.Binding || evidence.Binding != duckLake.Binding || evidence.Binding != pool.Binding {
		t.Fatal("concrete owners did not independently converge on one binding")
	}
	canonical, err := evidence.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := migrationcompatibility.ParseEvidenceV2(canonical)
	if err != nil || parsed != evidence {
		t.Fatalf("owner evidence round trip = %#v, %v", parsed, err)
	}
	second, err := fixture.goose.Resolve(t.Context(), fixture.selection)
	if err != nil || second != goose {
		t.Fatalf("Goose evidence is not deterministic: %#v, %v", second, err)
	}
}

func TestConcreteOwnersRejectCallerOnlyAndWrongBindings(t *testing.T) {
	typeOfSelection := reflect.TypeFor[SelectionV2]()
	for i := 0; i < typeOfSelection.NumField(); i++ {
		name := typeOfSelection.Field(i).Name
		if strings.Contains(name, "Digest") || strings.Contains(name, "Compatibility") || strings.Contains(name, "Evidence") || strings.Contains(name, "Binding") {
			t.Fatalf("production selection exposes caller projection field %q", name)
		}
	}
	fixture := newOwnerFixture(t, false)
	for name, selection := range map[string]SelectionV2{
		"wrong predecessor": withSelection(fixture.selection, func(value *SelectionV2) { value.PredecessorArtifactReference = immutableImage('9') }),
		"wrong candidate":   withSelection(fixture.selection, func(value *SelectionV2) { value.CandidateArtifactReference = immutableImage('8') }),
		"wrong target":      withSelection(fixture.selection, func(value *SelectionV2) { value.TargetID = "target-other" }),
		"wrong pool":        withSelection(fixture.selection, func(value *SelectionV2) { value.PhysicalPoolID = digest('7') }),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.goose.Resolve(t.Context(), selection); err == nil && name != "wrong pool" {
				t.Fatal("Goose owner accepted a wrong authoritative selector")
			}
			if _, err := fixture.river.Resolve(t.Context(), selection); err == nil && name != "wrong pool" {
				t.Fatal("River/jobs owner accepted a wrong authoritative selector")
			}
			if _, err := fixture.duckLake.Resolve(t.Context(), selection); err == nil {
				t.Fatal("DuckLake owner accepted a wrong authoritative selector")
			}
			if _, err := fixture.physicalPool.Resolve(t.Context(), selection); err == nil {
				t.Fatal("PhysicalPool owner accepted a wrong authoritative selector")
			}
		})
	}
}

func TestConcreteOwnersRejectMissingAndConflictingOwnerState(t *testing.T) {
	fixture := newOwnerFixture(t, true)
	if _, err := fixture.duckLake.Resolve(t.Context(), fixture.selection); !errors.Is(err, ErrOwnerState) {
		t.Fatalf("conflicting DuckLake/PhysicalPool state error = %v, want owner state", err)
	}
	if _, err := fixture.physicalPool.Resolve(t.Context(), fixture.selection); !errors.Is(err, ErrOwnerState) {
		t.Fatalf("conflicting PhysicalPool state error = %v, want owner state", err)
	}

	missing := fixture.selection
	missing.PhysicalPoolID = digest('6')
	if _, err := fixture.duckLake.Resolve(t.Context(), missing); !errors.Is(err, ErrOwnerState) {
		t.Fatalf("missing DuckLake owner error = %v, want owner state", err)
	}
}

func TestConcreteOwnerDetectsStaleTargetBinding(t *testing.T) {
	fixture := newOwnerFixture(t, false)
	resolved, err := fixture.goose.binding.resolve(t.Context(), fixture.selection)
	if err != nil {
		t.Fatal(err)
	}
	resolved.target.TargetRevision--
	if err := fixture.goose.binding.confirm(t.Context(), fixture.selection, resolved); !errors.Is(err, ErrOwnerStale) {
		t.Fatalf("stale target error = %v, want stale owner", err)
	}
}

func TestOwnerConstructorsRequireConcreteAuthorities(t *testing.T) {
	if _, err := NewGooseOwnerV2(nil, nil, nil); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("Goose constructor error = %v", err)
	}
	if _, err := NewRiverJobsOwnerV2(nil, nil, nil, nil); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("River constructor error = %v", err)
	}
	if _, err := NewDuckLakeOwnerV2(nil, nil, nil, nil, nil); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("DuckLake constructor error = %v", err)
	}
	if _, err := NewPhysicalPoolOwnerV2(nil, nil, nil, nil, nil); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("PhysicalPool constructor error = %v", err)
	}
}

type ownerFixture struct {
	selection    SelectionV2
	goose        *GooseOwnerV2
	river        *RiverJobsOwnerV2
	duckLake     *DuckLakeOwnerV2
	physicalPool *PhysicalPoolOwnerV2
}

func newOwnerFixture(t *testing.T, conflictingRuntime bool) ownerFixture {
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
	riverPool, err := pgxpool.New(t.Context(), database.URL(migratorRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(riverPool.Close)
	if err := postgresmigrations.ApplyRiverAndGoose(t.Context(), riverPool, control, nil); err != nil {
		t.Fatalf("apply control schema: %v", err)
	}

	releases := releasepostgres.New(pool)
	predecessor := admission("predecessor", 'a', '1')
	candidate := admission("candidate", 'b', '2')
	if _, err := releases.PublishArtifactAdmission(t.Context(), predecessor); err != nil {
		t.Fatal(err)
	}
	if _, err := releases.PublishArtifactAdmission(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
	targets := deploymentpostgres.New(pool)
	const targetID = "target-production"
	if _, err := targets.CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: targetID, ProjectID: "project", Environment: "production", TargetRevision: 7}); err != nil {
		t.Fatal(err)
	}

	extensionBytes := []byte("qualified ducklake extension")
	supply := extensionSupply(t, extensionBytes)
	tuple := physicalpool.Compatibility{
		DuckDBRuntime: "duckdb:1.5.4", DuckLakeExtension: "ducklake:0.3.0", CatalogFormat: "ducklake:v1",
		StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1",
	}
	poolIdentity, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{
		StorageLocation: "s3://qualified-bucket/data", StorageNamespace: "release-transition", Region: "us-east-1", Tenant: "tenant",
		EncryptionDomain: "kms-profile", IsolationBoundary: targetID, RetentionAuthority: "provider", Compatibility: tuple,
	})
	if err != nil {
		t.Fatal(err)
	}
	checks := make([]physicalpool.EvidenceCheck, 0, len(ducklake.SharedPoolConformanceChecks))
	for _, check := range ducklake.SharedPoolConformanceChecks {
		checks = append(checks, physicalpool.EvidenceCheck{ID: check, Passed: true, ObservationDigest: digest('e')})
	}
	poolEvidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{Compatibility: tuple, ConformanceVersion: ducklake.SharedPoolConformanceVersion, Checks: checks})
	if err != nil {
		t.Fatal(err)
	}
	pools := physicalpoolpostgres.New(pool)
	if _, _, err := pools.CreateAndAdmit(t.Context(), poolIdentity, poolEvidence); err != nil {
		t.Fatal(err)
	}

	runtimeTuple := tuple
	if conflictingRuntime {
		runtimeTuple.DuckDBRuntime = "duckdb:9.9.9"
	}
	runtimeDigest, err := runtimeTuple.Digest()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ducklakepostgres.DeriveCatalogIdentity(poolIdentity.ID.String(), database.Name)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ducklakepostgres.BootstrapCatalog(t.Context(), tx, identity, ducklakepostgres.RuntimeCompatibility{
		RuntimeTuple:        ducklakepostgres.RuntimeTuple{DuckDBRuntime: runtimeTuple.DuckDBRuntime, DuckLakeExtension: runtimeTuple.DuckLakeExtension, CatalogFormat: runtimeTuple.CatalogFormat},
		CompatibilityDigest: runtimeDigest, CatalogSchemaVersion: "catalog/v1",
	})
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	duckRepo := ducklakepostgres.New(pool)

	goose, err := NewGooseOwnerV2(releases, targets, control)
	if err != nil {
		t.Fatal(err)
	}
	river, err := NewRiverJobsOwnerV2(releases, targets, control, riverPool)
	if err != nil {
		t.Fatal(err)
	}
	duckOwner, err := NewDuckLakeOwnerV2(releases, targets, duckRepo, pools, supply)
	if err != nil {
		t.Fatal(err)
	}
	physicalOwner, err := NewPhysicalPoolOwnerV2(releases, targets, duckRepo, pools, supply)
	if err != nil {
		t.Fatal(err)
	}
	return ownerFixture{
		selection: SelectionV2{PredecessorArtifactReference: predecessor.Release.Image, CandidateArtifactReference: candidate.Release.Image, TargetID: targetID, PhysicalPoolID: poolIdentity.ID.String()},
		goose:     goose, river: river, duckLake: duckOwner, physicalPool: physicalOwner,
	}
}

func extensionSupply(t *testing.T, content []byte) *extensionsupply.Supply {
	t.Helper()
	sourceDir := t.TempDir()
	if err := os.Chmod(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := sourceDir + "/ducklake.duckdb_extension"
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	if err := os.Chmod(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	identity := extension.Identity{
		DuckDBVersion: "v1.5.4", ExtensionVersion: "v0.3.0", GOOS: "linux", GOARCH: "amd64", Platform: "linux_amd64",
		Name: "ducklake", Digest: "sha256:" + hex.EncodeToString(sum[:]), SupportProfile: "leapview-supported/v1",
	}
	supply, err := extensionsupply.New(extensionsupply.Config{
		DuckDBVersion: "v1.5.4", GOOS: "linux", GOARCH: "amd64", Platform: "linux_amd64", SupportProfile: "leapview-supported/v1", CacheDir: cacheDir,
		Manifest:        extensionsupply.Manifest{Version: extensionsupply.ManifestVersion, DuckDBVersion: "v1.5.4", GOOS: "linux", GOARCH: "amd64", Platform: "linux_amd64", SupportProfile: "leapview-supported/v1", Artifacts: []extensionsupply.Artifact{{Identity: identity, Origins: []string{"qualified"}, Provenance: "attest:qualified", Signature: "sig:qualified"}}},
		Origins:         []extensionsupply.Origin{{ID: "qualified", URL: source, Reviewed: true}},
		VerifySignature: func(context.Context, extensionsupply.Artifact, []byte) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return supply
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

func withSelection(value SelectionV2, mutate func(*SelectionV2)) SelectionV2 {
	mutate(&value)
	return value
}

func immutableImage(value byte) string { return "ghcr.io/flidai/leapview@" + digest(value) }
func digest(value byte) string         { return "sha256:" + strings.Repeat(string(value), 64) }

func shortName(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}
