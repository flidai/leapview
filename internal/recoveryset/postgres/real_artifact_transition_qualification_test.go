//go:build fai518qualification && fai518artifactqualification

package postgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	releasetransitionapp "github.com/flidai/leapview/internal/app/releasetransitionpreflightproduction"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	recoverysetpostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	"github.com/flidai/leapview/internal/release/artifactadmission"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/flidai/leapview/internal/release/transitionoperation"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/flidai/leapview/internal/release/transitionrunner"
)

const realArtifactEvidenceDirEnv = "LEAPVIEW_TEST_FAI518_REAL_ARTIFACT_EVIDENCE_DIR"

func TestFAI518RealCandidateArtifactTransitionQualification(t *testing.T) {
	image := requiredRealArtifactEnv(t, "LEAPVIEW_TEST_FAI518_CANDIDATE_IMAGE")
	revision := requiredRealArtifactEnv(t, "LEAPVIEW_TEST_FAI518_CANDIDATE_REVISION")
	evidenceDir := requiredRealArtifactEnv(t, realArtifactEvidenceDirEnv)
	admission := verifiedRealCandidateAdmission(t, image, revision, evidenceDir)

	pool, migratorDB := revision019PreflightDB(t)
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
	predecessor := productionAdmission("predecessor-fixture", 'a', '1')
	predecessor.Release.Version = "0.2.0-rc.0"
	predecessorIdentity := publishProductionAdmission(t, releases, predecessor)
	candidateIdentity := publishProductionAdmission(t, releases, admission)
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
	request := transitionpreflight.ResolutionRequest{PredecessorRef: predecessor.Release.Image, CandidateRef: image, TargetRef: "target", RecoveryFrontier: transitionpreflight.RecoveryFrontierRef{SetID: frontier.ID, Digest: frontier.FrontierDigest}}
	baseline, err := representativeStateDigest(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	transitions := releasepostgres.NewTransitionRepositoryWithBootstrap(pool, func(ctx context.Context) error {
		return migrations.BootstrapTransitionOperation(ctx, pool, migratorDB)
	})

	wrong := request
	wrong.CandidateRef = image[:len(image)-1] + alternateDigestCharacter(image[len(image)-1])
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
	if _, err := rejected.Run(t.Context(), transitionrunner.Request{OwnerID: "real-artifact-rejected", IdempotencyKey: "real-artifact-wrong-digest", Preflight: wrong}); !errors.Is(err, transitionrunner.ErrStalePreflight) {
		t.Fatalf("mismatched candidate digest error = %v, want stale preflight", err)
	}
	if migrationBegan {
		t.Fatal("mismatched artifact entered the migration effect")
	}
	if err := pool.QueryRow(t.Context(), `SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&before); err != nil || before != 19 {
		t.Fatalf("rejected artifact changed revision to %d: %v", before, err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('release.release_transition_operation')::text`).Scan(&operationTable); err != nil || operationTable != nil {
		t.Fatalf("rejected artifact created operation table: %v, %v", operationTable, err)
	}

	effects := newForwardQualificationEffects(t, pool, predecessor.Release.Image, resolver, request)
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
	if !effects.migrationsApplied || !artifactConsumed || result.Evidence.Candidate.Release.Image != image || result.Evidence.Candidate.ArtifactAdmissionDigest != candidateIdentity.ArtifactAdmissionDigest || result.Operation.CandidateArtifactDigest != candidateDigest {
		t.Fatalf("candidate migration/admission consumption: migrated=%t consumed=%t evidenceImage=%q admission=%q operationArtifact=%q", effects.migrationsApplied, artifactConsumed, result.Evidence.Candidate.Release.Image, result.Evidence.Candidate.ArtifactAdmissionDigest, result.Operation.CandidateArtifactDigest)
	}
	assertForwardPhaseResults(t, result.Operation)
	assertAppliedMigrationRevision(t, pool)
	after, err := representativeStateDigest(t.Context(), pool)
	if err != nil || after != baseline {
		t.Fatalf("predecessor state after migration = %q, error = %v; want %q", after, err, baseline)
	}
	readback := readForwardTransitionAfterRepositoryRestart(t, pool, result.Operation.OperationID)
	if readback.Status != transitionoperation.StatusCompleted || readback.CandidateArtifactDigest != result.Operation.CandidateArtifactDigest {
		t.Fatalf("durable real-artifact result = %#v", readback)
	}
	assertForwardPhaseResults(t, readback)
	if err := writeRealArtifactReport(evidenceDir, realArtifactReport{Image: image, SourceRevision: revision, AdmissionDigest: candidateIdentity.ArtifactAdmissionDigest, ProvenanceEvidenceDigest: admission.Provenance.Reference, SBOMEvidenceDigest: admission.SBOM.Reference, SecurityPolicyDigest: admission.SecurityPolicy.Reference, PreflightDigest: result.EvidenceDigest, OperationID: result.Operation.OperationID, MigrationRevision: migrations.CurrentRevision, PredecessorStateDigest: baseline, CandidateRuntimeVersion: admission.Release.Version}); err != nil {
		t.Fatal(err)
	}
	t.Logf("real candidate %s admitted as %s, consumed by post-validation, Goose revision %d", image, candidateIdentity.ArtifactAdmissionDigest, migrations.CurrentRevision)
}

type realCandidateVersion struct {
	Product     string `json:"product"`
	Version     string `json:"version"`
	Revision    string `json:"revision"`
	Dirty       bool   `json:"dirty"`
	Development bool   `json:"development"`
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

func verifiedRealCandidateAdmission(t *testing.T, image, revision, evidenceDir string) artifactadmission.Admission {
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
		Release:            compatibility.ReleaseIdentity{ReleaseID: "fai518-candidate", Version: runtime.Version, SourceRevision: revision, Image: image, Distribution: "distroless", Platform: "linux/amd64"},
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
	Image                    string `json:"image"`
	SourceRevision           string `json:"sourceRevision"`
	AdmissionDigest          string `json:"artifactAdmissionDigest"`
	ProvenanceEvidenceDigest string `json:"provenanceEvidenceDigest"`
	SBOMEvidenceDigest       string `json:"sbomEvidenceDigest"`
	SecurityPolicyDigest     string `json:"securityPolicyDigest"`
	PreflightDigest          string `json:"preflightEvidenceDigest"`
	OperationID              string `json:"operationId"`
	MigrationRevision        int64  `json:"migrationRevision"`
	PredecessorStateDigest   string `json:"predecessorStateDigest"`
	CandidateRuntimeVersion  string `json:"candidateRuntimeVersion"`
}

func writeRealArtifactReport(dir string, report realArtifactReport) error {
	contents, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dir+"/transition-report.json", append(contents, '\n'), 0o644)
}
