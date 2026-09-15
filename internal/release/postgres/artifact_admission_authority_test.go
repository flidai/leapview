package postgres

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/release/artifactadmission"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOCIArtifactAdmissionAuthorityPublishesResolvesAndRestarts(t *testing.T) {
	pool := testDB(t)
	repository := New(pool)
	predecessor := artifactAdmission("predecessor", "a", "1")
	candidate := artifactAdmission("candidate", "b", "2")

	first, err := repository.PublishArtifactAdmission(t.Context(), predecessor)
	if err != nil {
		t.Fatalf("publish predecessor: %v", err)
	}
	replayed, err := repository.PublishArtifactAdmission(t.Context(), predecessor)
	if err != nil {
		t.Fatalf("replay predecessor: %v", err)
	}
	if !reflect.DeepEqual(first, replayed) {
		t.Fatalf("replayed identity changed:\n%#v\n%#v", first, replayed)
	}
	if _, err := repository.PublishArtifactAdmission(t.Context(), candidate); err != nil {
		t.Fatalf("publish candidate: %v", err)
	}

	reopened, err := pgxpool.New(t.Context(), pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	for _, admission := range []artifactadmission.Admission{predecessor, candidate} {
		resolved, err := New(reopened).ResolveArtifact(t.Context(), admission.Release.Image)
		if err != nil {
			t.Fatalf("resolve %s after restart: %v", admission.Release.ReleaseID, err)
		}
		expected, _ := admission.ArtifactIdentity()
		if !reflect.DeepEqual(resolved, expected) {
			t.Fatalf("resolved identity = %#v, want %#v", resolved, expected)
		}
	}
}

func TestOCIArtifactAdmissionAuthorityFailsClosed(t *testing.T) {
	repository := New(testDB(t))
	admission := artifactAdmission("candidate", "a", "1")

	if _, err := repository.ResolveArtifact(t.Context(), admission.Release.Image); !errors.Is(err, ErrArtifactAdmissionNotFound) {
		t.Fatalf("missing admission error = %v", err)
	}
	if _, err := repository.ResolveArtifact(t.Context(), "ghcr.io/flidai/leapview:latest"); !errors.Is(err, ErrArtifactAdmissionInvalid) {
		t.Fatalf("mutable tag error = %v", err)
	}

	for name, mutate := range map[string]func(*artifactadmission.Admission){
		"unapproved provenance repository": func(value *artifactadmission.Admission) {
			value.Provenance.Repository = "attacker/example"
		},
		"unapproved provenance workflow": func(value *artifactadmission.Admission) {
			value.Provenance.Workflow = "flidai/leapview/.github/workflows/untrusted.yml"
		},
		"mutable source revision": func(value *artifactadmission.Admission) {
			value.Release.SourceRevision = "refs/heads/main"
			value.Provenance.SourceRevision = "refs/heads/main"
		},
		"unsupported SBOM producer": func(value *artifactadmission.Admission) {
			value.SBOM.Producer = "unknown/producer"
		},
		"unapproved security scanner": func(value *artifactadmission.Admission) {
			value.SecurityPolicy.Scanner = "unknown-scanner"
		},
		"fabricated positive result": func(value *artifactadmission.Admission) {
			value.Provenance.Reference = ""
			value.Provenance.Verified = true
			value.SBOM.Verified = true
			value.SecurityPolicy.Passed = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := admission
			mutate(&invalid)
			if _, err := repository.PublishArtifactAdmission(t.Context(), invalid); !errors.Is(err, ErrArtifactAdmissionInvalid) {
				t.Fatalf("publish error = %v", err)
			}
			if _, err := repository.ResolveArtifact(t.Context(), admission.Release.Image); !errors.Is(err, ErrArtifactAdmissionNotFound) {
				t.Fatalf("invalid admission became authoritative: %v", err)
			}
		})
	}

	unsupported := admission
	unsupported.Version = "oci-artifact-admission/v2"
	if _, err := repository.PublishArtifactAdmission(t.Context(), unsupported); !errors.Is(err, ErrArtifactAdmissionInvalid) {
		t.Fatalf("unsupported version error = %v", err)
	}

	digestMismatch := admission
	digestMismatch.OCIDigest = digest("f")
	if _, err := repository.PublishArtifactAdmission(t.Context(), digestMismatch); !errors.Is(err, ErrArtifactAdmissionInvalid) {
		t.Fatalf("digest mismatch error = %v", err)
	}

	if _, err := repository.PublishArtifactAdmission(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	conflict := admission
	conflict.Release.ReleaseID = "different-release"
	if _, err := repository.PublishArtifactAdmission(t.Context(), conflict); !errors.Is(err, ErrArtifactAdmissionConflict) {
		t.Fatalf("conflicting admission error = %v", err)
	}

	revokedAt := time.Date(2026, 9, 13, 13, 0, 0, 0, time.UTC)
	if err := repository.RevokeArtifactAdmission(t.Context(), admission.Release.Image, "superseded security decision", revokedAt); err != nil {
		t.Fatalf("revoke admission: %v", err)
	}
	if err := repository.RevokeArtifactAdmission(t.Context(), admission.Release.Image, "superseded security decision", revokedAt); err != nil {
		t.Fatalf("replay revocation: %v", err)
	}
	if _, err := repository.ResolveArtifact(t.Context(), admission.Release.Image); !errors.Is(err, ErrArtifactAdmissionRevoked) {
		t.Fatalf("revoked admission error = %v", err)
	}
	if err := repository.RevokeArtifactAdmission(t.Context(), admission.Release.Image, "different reason", revokedAt); !errors.Is(err, ErrArtifactAdmissionConflict) {
		t.Fatalf("conflicting revocation error = %v", err)
	}
}

func TestOCIArtifactAdmissionAuthorityRejectsMutation(t *testing.T) {
	pool := testDB(t)
	repository := New(pool)
	admission := artifactAdmission("candidate", "a", "1")
	if _, err := repository.PublishArtifactAdmission(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"update":   `UPDATE release.oci_artifact_admission SET admission_version = 'oci-artifact-admission/v2'`,
		"delete":   `DELETE FROM release.oci_artifact_admission`,
		"truncate": `TRUNCATE release.oci_artifact_admission`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := pool.Exec(t.Context(), statement); err == nil {
				t.Fatalf("%s immutable admission succeeded", name)
			}
		})
	}
}

func artifactAdmission(releaseID, imageDigest, revision string) artifactadmission.Admission {
	return artifactadmission.Admission{
		Version: artifactadmission.AdmissionVersion,
		Release: compatibility.ReleaseIdentity{
			ReleaseID: releaseID, Version: "v1.2." + revision, SourceRevision: strings.Repeat(revision, 40),
			Image: "ghcr.io/flidai/leapview@" + digest(imageDigest), Distribution: "distroless", Platform: "linux/amd64",
		},
		ArchitectureMarker: "postgres-control/v1",
		Repository:         "ghcr.io/flidai/leapview",
		OCIDigest:          digest(imageDigest),
		Decision:           artifactadmission.DecisionAdmitted,
		Provenance: artifactadmission.ProvenanceResult{
			Reference: digest("c"), Repository: artifactadmission.SourceRepository, Workflow: "flidai/leapview/.github/workflows/release.yml", SourceRevision: strings.Repeat(revision, 40), Verified: true,
		},
		SBOM: artifactadmission.SBOMResult{Reference: digest("d"), PredicateType: artifactadmission.SBOMPredicateSPDX, Producer: artifactadmission.SBOMProducerBuildx, Verified: true},
		SecurityPolicy: artifactadmission.SecurityPolicyResult{
			Version: artifactadmission.SecurityPolicyVersion, Reference: digest("e"), Scanner: artifactadmission.SecurityScannerTrivy, Passed: true,
		},
		AdmittedAt: time.Date(2026, 9, 13, 12, 0, 0, int(revision[0]), time.UTC),
	}
}
