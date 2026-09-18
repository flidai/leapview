//go:build fai518qualification && fai518artifactqualification

package postgres_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	releasetransitionapp "github.com/flidai/leapview/internal/app/releasetransitionpreflightproduction"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	recoverysetpostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	"github.com/flidai/leapview/internal/release/artifactadmission"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/flidai/leapview/internal/release/transitionoperation"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/flidai/leapview/internal/release/transitionrunner"
	"github.com/jackc/pgx/v5/pgxpool"
)

const realArtifactEvidenceDirEnv = "LEAPVIEW_TEST_FAI518_REAL_ARTIFACT_EVIDENCE_DIR"

func TestFAI518RealPredecessorCandidateTransitionQualification(t *testing.T) {
	image := requiredRealArtifactEnv(t, "LEAPVIEW_TEST_FAI518_CANDIDATE_IMAGE")
	revision := requiredRealArtifactEnv(t, "LEAPVIEW_TEST_FAI518_CANDIDATE_REVISION")
	predecessorImage := requiredRealArtifactEnv(t, "LEAPVIEW_TEST_FAI518_PREDECESSOR_IMAGE")
	predecessorRevision := requiredRealArtifactEnv(t, "LEAPVIEW_TEST_FAI518_PREDECESSOR_REVISION")
	evidenceDir := requiredRealArtifactEnv(t, realArtifactEvidenceDirEnv)
	admission := verifiedRealArtifactAdmission(t, image, revision, evidenceDir, "fai518-candidate")
	predecessorAdmission := verifiedRealArtifactAdmission(t, predecessorImage, predecessorRevision, filepath.Join(evidenceDir, "predecessor"), "fai518-predecessor")
	if predecessorImage == image || predecessorRevision == revision {
		t.Fatal("predecessor and candidate must be distinct immutable distributions")
	}

	pool, migratorDB, predecessorRuntime, predecessorAdminID := realPredecessorDB(t, predecessorImage, predecessorRevision)
	var before int64
	if err := pool.QueryRow(t.Context(), `SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&before); err != nil || before != 19 {
		t.Fatalf("predecessor revision = %d, error = %v; want 19", before, err)
	}
	var operationTable *string
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('release.release_transition_operation')::text`).Scan(&operationTable); err != nil || operationTable != nil {
		t.Fatalf("revision-019 operation table = %v, error = %v; want absent", operationTable, err)
	}
	releases := releasepostgres.New(pool)
	targets := deploymentpostgres.New(pool)
	recoverySets := recoverysetpostgres.New(pool)
	capabilities, err := releasepostgres.NewMigrationCapabilityAuthority(releases, productionOwnerRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := targets.CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: "target", ProjectID: "project", Environment: "production", TargetRevision: 1}); err != nil {
		t.Fatal(err)
	}
	targetDigest, err := (transitionpreflight.TargetIdentity{TargetID: "target", TargetRevision: 1}).Digest()
	if err != nil {
		t.Fatal(err)
	}
	predecessorIdentity := publishProductionAdmission(t, releases, predecessorAdmission)
	candidateIdentity := publishProductionAdmission(t, releases, admission)
	resolvedPredecessor, err := releases.ResolveArtifact(t.Context(), predecessorImage)
	if err != nil || resolvedPredecessor != predecessorIdentity {
		t.Fatalf("durable predecessor admission = %#v, error = %v; want %#v", resolvedPredecessor, err, predecessorIdentity)
	}
	resolvedCandidate, err := releases.ResolveArtifact(t.Context(), image)
	if err != nil || resolvedCandidate != candidateIdentity {
		t.Fatalf("durable candidate admission = %#v, error = %v; want %#v", resolvedCandidate, err, candidateIdentity)
	}
	candidateDigest, err := candidateIdentity.Digest()
	if err != nil {
		t.Fatal(err)
	}
	publishRevision019Capabilities(t, capabilities, predecessorIdentity.ArtifactAdmissionDigest, targetDigest, false)
	publishRevision019Capabilities(t, capabilities, candidateIdentity.ArtifactAdmissionDigest, targetDigest, true)
	publishProductionPolicy(t, releases, predecessorIdentity, candidateIdentity)
	frontier := productionPublishedFrontier(t, recoverySets, productionRecoverySetFixture(t))
	resolver, err := releasetransitionapp.NewProductionResolver(releasetransitionapp.ProductionDependencies{Releases: releases, Targets: targets, MigrationCapabilities: capabilities, RecoverySets: recoverySets})
	if err != nil {
		t.Fatal(err)
	}
	request := transitionpreflight.ResolutionRequest{PredecessorRef: predecessorImage, CandidateRef: image, TargetRef: "target", RecoveryFrontier: transitionpreflight.RecoveryFrontierRef{SetID: frontier.ID, Digest: frontier.FrontierDigest}}
	baseline, err := representativeStateDigest(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	transitions := releasepostgres.NewTransitionRepositoryWithBootstrap(pool, func(ctx context.Context) error {
		return migrations.BootstrapTransitionOperation(ctx, pool, migratorDB)
	})

	wrongCandidate := request
	wrongCandidate.CandidateRef = image[:len(image)-1] + alternateDigestCharacter(image[len(image)-1])
	wrongPredecessor := request
	wrongPredecessor.PredecessorRef = predecessorImage[:len(predecessorImage)-1] + alternateDigestCharacter(predecessorImage[len(predecessorImage)-1])
	var migrationBegan bool
	rejected, err := transitionrunner.New(transitionrunner.Options{Operations: transitions, Preflight: resolver, Fences: transitions,
		Effects: transitionrunner.EffectFuncs{MigrationsFunc: func(context.Context, transitionrunner.EffectInput) (transitionrunner.EffectResult, error) {
			migrationBegan = true
			return transitionrunner.EffectResult{}, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, mismatch := range []struct {
		name    string
		request transitionpreflight.ResolutionRequest
	}{{"candidate", wrongCandidate}, {"predecessor", wrongPredecessor}} {
		if _, err := rejected.Run(t.Context(), transitionrunner.Request{OwnerID: "real-artifact-rejected", IdempotencyKey: "real-artifact-wrong-" + mismatch.name, Preflight: mismatch.request}); !errors.Is(err, transitionrunner.ErrStalePreflight) {
			t.Fatalf("mismatched %s digest error = %v, want stale preflight", mismatch.name, err)
		}
		if migrationBegan {
			t.Fatalf("mismatched %s artifact entered the migration effect", mismatch.name)
		}
		if err := pool.QueryRow(t.Context(), `SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&before); err != nil || before != 19 {
			t.Fatalf("rejected %s artifact changed revision to %d: %v", mismatch.name, before, err)
		}
		if err := pool.QueryRow(t.Context(), `SELECT to_regclass('release.release_transition_operation')::text`).Scan(&operationTable); err != nil || operationTable != nil {
			t.Fatalf("rejected %s artifact created operation table: %v, %v", mismatch.name, operationTable, err)
		}
	}

	effects := newForwardQualificationEffects(t, pool, predecessorImage, resolver, request)
	effects.candidateImage = image
	t.Cleanup(effects.cleanup)
	var artifactConsumed bool
	runner, err := transitionrunner.New(transitionrunner.Options{Operations: transitions, Preflight: resolver, Fences: transitions,
		Effects: transitionrunner.EffectFuncs{
			MigrationsFunc: effects.Migrations,
			StageFunc:      effects.Stage,
			ActivateFunc:   effects.Activate,
			RestartFunc:    effects.Restart,
			PostValidateFunc: func(ctx context.Context, in transitionrunner.EffectInput) (transitionrunner.EffectResult, error) {
				out, err := effects.PostValidate(ctx, in)
				if err != nil {
					return out, err
				}
				identity, err := realArtifactRuntimeIdentity(ctx, image)
				if err != nil {
					return out, err
				}
				if identity.Revision != revision || identity.Version != admission.Release.Version || identity.Dirty || identity.Development {
					return out, fmt.Errorf("candidate runtime identity changed: %#v", identity)
				}
				artifactConsumed = true
				return out, nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), transitionrunner.Request{OwnerID: "real-artifact-owner", IdempotencyKey: "real-artifact-019-to-020", Preflight: request})
	if err != nil || result.Operation.Status != transitionoperation.StatusCompleted {
		t.Fatalf("real-artifact transition status = %q, error = %v", result.Operation.Status, err)
	}
	predecessorDigest, err := predecessorIdentity.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if !effects.migrationsApplied || !artifactConsumed || result.Evidence.Predecessor.Release.Image != predecessorImage || result.Evidence.Predecessor.ArtifactAdmissionDigest != predecessorIdentity.ArtifactAdmissionDigest || result.Operation.PredecessorArtifactDigest != predecessorDigest || result.Evidence.Candidate.Release.Image != image || result.Evidence.Candidate.ArtifactAdmissionDigest != candidateIdentity.ArtifactAdmissionDigest || result.Operation.CandidateArtifactDigest != candidateDigest {
		t.Fatalf("paired migration/admission consumption: migrated=%t consumed=%t predecessorImage=%q predecessorAdmission=%q predecessorOperationArtifact=%q candidateImage=%q candidateAdmission=%q candidateOperationArtifact=%q", effects.migrationsApplied, artifactConsumed, result.Evidence.Predecessor.Release.Image, result.Evidence.Predecessor.ArtifactAdmissionDigest, result.Operation.PredecessorArtifactDigest, result.Evidence.Candidate.Release.Image, result.Evidence.Candidate.ArtifactAdmissionDigest, result.Operation.CandidateArtifactDigest)
	}
	assertForwardPhaseResults(t, result.Operation)
	assertAppliedMigrationRevision(t, pool)
	after, err := representativeStateDigest(t.Context(), pool)
	if err != nil || after != baseline {
		t.Fatalf("predecessor state after migration = %q, error = %v; want %q", after, err, baseline)
	}
	var adminAfter string
	if err := pool.QueryRow(t.Context(), `SELECT id::text FROM access.principal WHERE lower(email)='admin@localhost' AND revoked_at IS NULL`).Scan(&adminAfter); err != nil || adminAfter != predecessorAdminID {
		t.Fatalf("predecessor administrator after migration = %q, error = %v; want %q", adminAfter, err, predecessorAdminID)
	}
	readback := readForwardTransitionAfterRepositoryRestart(t, pool, result.Operation.OperationID)
	if readback.Status != transitionoperation.StatusCompleted || readback.CandidateArtifactDigest != result.Operation.CandidateArtifactDigest {
		t.Fatalf("durable real-artifact result = %#v", readback)
	}
	assertForwardPhaseResults(t, readback)
	var fenceGeneration int64
	var fenceOperation *string
	var fenceOwner string
	if err := pool.QueryRow(t.Context(), `SELECT fencing_generation, operation_id::text, owner_id FROM release.release_transition_fence WHERE target_identity_digest=$1`, targetDigest).Scan(&fenceGeneration, &fenceOperation, &fenceOwner); err != nil || fenceGeneration < 1 || fenceOperation != nil || fenceOwner != "" {
		t.Fatalf("durable released fence generation=%d operation=%v owner=%q error=%v", fenceGeneration, fenceOperation, fenceOwner, err)
	}
	if err := writeRealArtifactReport(evidenceDir, realArtifactReport{Image: image, SourceRevision: revision, AdmissionDigest: candidateIdentity.ArtifactAdmissionDigest, ProvenanceEvidenceDigest: admission.Provenance.Reference, SBOMEvidenceDigest: admission.SBOM.Reference, SecurityPolicyDigest: admission.SecurityPolicy.Reference, PreflightDigest: result.EvidenceDigest, OperationID: result.Operation.OperationID, OperationStatus: string(readback.Status), FenceGeneration: fenceGeneration, MigrationRevision: migrations.CurrentRevision, PredecessorStateDigest: baseline, CandidateRuntimeVersion: admission.Release.Version, PredecessorImage: predecessorImage, PredecessorRevision: predecessorRevision, PredecessorAdmissionDigest: predecessorIdentity.ArtifactAdmissionDigest, PredecessorRuntimeVersion: predecessorRuntime.Version, PredecessorMigrationRevision: 19, PredecessorAdminPrincipalID: predecessorAdminID}); err != nil {
		t.Fatal(err)
	}
	t.Logf("real predecessor %s and candidate %s admitted and executed through Goose 019→020", predecessorImage, image)
}

type realCandidateVersion struct {
	Product     string `json:"product"`
	Version     string `json:"version"`
	Revision    string `json:"revision"`
	Dirty       bool   `json:"dirty"`
	Development bool   `json:"development"`
}

func realPredecessorDB(t *testing.T, image, revision string) (*pgxpool.Pool, *sql.DB, realCandidateVersion, string) {
	t.Helper()
	harness := postgrestest.StartTLS(t)
	owner := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Password: rand.Text(), Login: true})
	runtime := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: rand.Text(), Login: true})
	maintenance := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: rand.Text(), Login: true})
	for _, name := range []string{"leapview_control_readonly", "leapview_control_backup"} {
		harness.EnsureRole(t, postgrestest.Role{Name: name})
	}
	harness.GrantRole(t, owner, migrator)
	database := harness.NewDatabase(t, "fai518_real_predecessor")
	harness.GrantDatabase(t, database.Name, owner, "CREATE")
	harness.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
	harness.GrantDatabase(t, database.Name, runtime, "CONNECT")
	harness.GrantDatabase(t, database.Name, maintenance, "CONNECT")
	pool, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), `ALTER DATABASE `+database.Name+` OWNER TO leapview_control_owner; REVOKE ALL ON SCHEMA public FROM PUBLIC; GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	migratorDB, err := sql.Open("pgx", database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migratorDB.Close() })
	certBytes, err := os.ReadFile(harness.RootCertPath())
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(t.TempDir(), "postgres-root.pem")
	if err := os.WriteFile(certPath, certBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	const containerCertPath = "/tmp/fai518-postgres-root.pem"
	containerURL := func(role postgrestest.Role) string {
		t.Helper()
		value, err := url.Parse(database.URL(role))
		if err != nil {
			t.Fatal(err)
		}
		value.Host = "localhost:" + value.Port()
		query := value.Query()
		query.Set("sslmode", "verify-full")
		query.Set("sslrootcert", containerCertPath)
		value.RawQuery = query.Encode()
		return value.String()
	}
	command := exec.CommandContext(t.Context(), "docker", "run", "--rm", "--network", "host",
		"--mount", "type=bind,src="+certPath+",dst="+containerCertPath+",readonly",
		"--env", "LEAPVIEW_PRODUCTION=1",
		"--env", "LEAPVIEW_ENVIRONMENT=prod",
		"--env", "LEAPVIEW_HOME=/tmp/fai518-predecessor-home",
		"--env", "LEAPVIEW_LOCAL_AUTH=1",
		"--env", "LEAPVIEW_COOKIE_SECURE=true",
		"--env", "LEAPVIEW_PUBLIC_URL=https://localhost",
		"--env", "LEAPVIEW_ALLOWED_HOSTS=localhost",
		"--env", "LEAPVIEW_BOOTSTRAP_ADMIN_EMAIL=admin@localhost",
		"--env", "LEAPVIEW_CSRF_KEY="+rand.Text()+rand.Text(),
		"--env", "LEAPVIEW_POSTGRES_REQUIRE_TLS=true",
		"--env", "LEAPVIEW_POSTGRES_CONTROL_URL="+containerURL(runtime),
		"--env", "LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE="+runtime.Name,
		"--env", "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL="+containerURL(migrator),
		"--env", "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE="+migrator.Name,
		"--env", "LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL="+containerURL(maintenance),
		"--env", "LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_ROLE="+maintenance.Name,
		image, "admin", "initialize")
	command.Stdout = io.Discard // Initialization returns one-time credentials.
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("execute predecessor initialization from %s: %v: %s", image, err, strings.TrimSpace(stderr.String()))
	}
	identity, err := realArtifactRuntimeIdentity(t.Context(), image)
	if err != nil || identity.Revision != revision || identity.Dirty || identity.Development {
		t.Fatalf("executed predecessor identity = %#v, error = %v; want clean revision %s", identity, err, revision)
	}
	var gooseRevision int64
	if err := pool.QueryRow(t.Context(), `SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&gooseRevision); err != nil || gooseRevision != 19 {
		t.Fatalf("predecessor image migrated Goose to %d, error = %v; want 19", gooseRevision, err)
	}
	var adminID string
	if err := pool.QueryRow(t.Context(), `SELECT id::text FROM access.principal WHERE lower(email)='admin@localhost' AND revoked_at IS NULL`).Scan(&adminID); err != nil || adminID == "" {
		t.Fatalf("predecessor image administrator = %q, error = %v", adminID, err)
	}
	return pool, migratorDB, identity, adminID
}

func realArtifactRuntimeIdentity(ctx context.Context, image string) (realCandidateVersion, error) {
	command := exec.CommandContext(ctx, "docker", "run", "--rm", image, "version", "--json")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return realCandidateVersion{}, fmt.Errorf("execute exact candidate image version: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var identity realCandidateVersion
	if err := json.Unmarshal(output, &identity); err != nil {
		return realCandidateVersion{}, fmt.Errorf("decode exact candidate runtime identity: %w", err)
	}
	if identity.Product != "leapview" {
		return realCandidateVersion{}, fmt.Errorf("candidate product = %q, want leapview", identity.Product)
	}
	return identity, nil
}

type realCandidateAdmissionEvidence struct {
	SchemaVersion  int    `json:"schemaVersion"`
	Image          string `json:"image"`
	Digest         string `json:"digest"`
	RegistryDigest string `json:"registryDigest"`
	Attestation    struct {
		Verified       bool   `json:"verified"`
		Repository     string `json:"repository"`
		Workflow       string `json:"workflow"`
		SourceRevision string `json:"sourceRevision"`
	} `json:"attestation"`
	SBOM struct {
		Discoverable  bool   `json:"discoverable"`
		PredicateType string `json:"predicateType"`
	} `json:"sbom"`
	VulnerabilityPolicy struct {
		SHA256   string `json:"sha256"`
		Scanner  string `json:"scanner"`
		Passed   bool   `json:"passed"`
		Platform string `json:"platform"`
	} `json:"vulnerabilityPolicy"`
}

func verifiedRealArtifactAdmission(t *testing.T, image, revision, evidenceDir, releaseID string) artifactadmission.Admission {
	t.Helper()
	if err := artifactadmission.ValidateReference(image); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(evidenceDir + "/oci-admission.json")
	if err != nil {
		t.Fatal(err)
	}
	var evidence realCandidateAdmissionEvidence
	if err := json.Unmarshal(bytes, &evidence); err != nil {
		t.Fatal(err)
	}
	digest := image[strings.LastIndexByte(image, '@')+1:]
	workflow := "flidai/leapview/.github/workflows/release.yml"
	if evidence.SchemaVersion != 1 || evidence.Image != image || evidence.Digest != digest || evidence.RegistryDigest != digest || !evidence.Attestation.Verified || evidence.Attestation.Repository != artifactadmission.SourceRepository || evidence.Attestation.Workflow != workflow || evidence.Attestation.SourceRevision != revision || !evidence.SBOM.Discoverable || evidence.SBOM.PredicateType != artifactadmission.SBOMPredicateSPDX || !evidence.VulnerabilityPolicy.Passed || evidence.VulnerabilityPolicy.Scanner != artifactadmission.SecurityScannerTrivy || evidence.VulnerabilityPolicy.Platform != "linux/amd64" {
		t.Fatalf("live OCI admission evidence does not bind exact candidate %s and revision %s", image, revision)
	}
	policyRef := realArtifactFileDigest(t, evidenceDir+"/container-vulnerability-policy.json")
	if evidence.VulnerabilityPolicy.SHA256 != strings.TrimPrefix(policyRef, "sha256:") {
		t.Fatalf("live OCI admission policy hash = %q, want %q", evidence.VulnerabilityPolicy.SHA256, policyRef)
	}
	runtime, err := realArtifactRuntimeIdentity(t.Context(), image)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Revision != revision || runtime.Dirty || runtime.Development {
		t.Fatalf("candidate image runtime identity = %#v, want clean release revision %s", runtime, revision)
	}
	return artifactadmission.Admission{
		Version:            artifactadmission.AdmissionVersion,
		Release:            compatibility.ReleaseIdentity{ReleaseID: releaseID, Version: runtime.Version, SourceRevision: revision, Image: image, Distribution: "distroless", Platform: "linux/amd64"},
		ArchitectureMarker: transitionpreflight.ArchitecturePostgreSQL,
		Repository:         "ghcr.io/flidai/leapview", OCIDigest: digest, Decision: artifactadmission.DecisionAdmitted,
		Provenance:     artifactadmission.ProvenanceResult{Reference: realArtifactFileDigest(t, evidenceDir+"/verified-attestation.json"), Repository: artifactadmission.SourceRepository, Workflow: workflow, SourceRevision: revision},
		SBOM:           artifactadmission.SBOMResult{Reference: realArtifactFileDigest(t, evidenceDir+"/sbom.json"), PredicateType: artifactadmission.SBOMPredicateSPDX, Producer: artifactadmission.SBOMProducerBuildx},
		SecurityPolicy: artifactadmission.SecurityPolicyResult{Version: artifactadmission.SecurityPolicyVersion, Reference: policyRef, Scanner: artifactadmission.SecurityScannerTrivy},
		AdmittedAt:     time.Now().UTC(),
	}
}

func realArtifactFileDigest(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) == 0 {
		t.Fatalf("artifact evidence file is empty: %s", path)
	}
	sum := sha256.Sum256(contents)
	return fmt.Sprintf("sha256:%x", sum)
}

func requiredRealArtifactEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required for real-artifact qualification", name)
	}
	return value
}

func alternateDigestCharacter(last byte) string {
	if last == '0' {
		return "1"
	}
	return "0"
}

type realArtifactReport struct {
	PredecessorImage             string `json:"predecessorImage"`
	PredecessorRevision          string `json:"predecessorRevision"`
	PredecessorAdmissionDigest   string `json:"predecessorArtifactAdmissionDigest"`
	PredecessorRuntimeVersion    string `json:"predecessorRuntimeVersion"`
	PredecessorMigrationRevision int64  `json:"predecessorMigrationRevision"`
	PredecessorAdminPrincipalID  string `json:"predecessorAdminPrincipalId"`
	Image                        string `json:"image"`
	SourceRevision               string `json:"sourceRevision"`
	AdmissionDigest              string `json:"artifactAdmissionDigest"`
	ProvenanceEvidenceDigest     string `json:"provenanceEvidenceDigest"`
	SBOMEvidenceDigest           string `json:"sbomEvidenceDigest"`
	SecurityPolicyDigest         string `json:"securityPolicyDigest"`
	PreflightDigest              string `json:"preflightEvidenceDigest"`
	OperationID                  string `json:"operationId"`
	OperationStatus              string `json:"operationStatus"`
	FenceGeneration              int64  `json:"fenceGeneration"`
	MigrationRevision            int64  `json:"migrationRevision"`
	PredecessorStateDigest       string `json:"predecessorStateDigest"`
	CandidateRuntimeVersion      string `json:"candidateRuntimeVersion"`
}

func writeRealArtifactReport(dir string, report realArtifactReport) error {
	contents, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dir+"/transition-report.json", append(contents, '\n'), 0o644)
}
