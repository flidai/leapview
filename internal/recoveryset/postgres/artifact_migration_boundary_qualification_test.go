//go:build fai518qualification

package postgres_test

import (
	"encoding/json"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/release/migrationcapability"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

func artifactBoundaryFixture(t *testing.T) (artifactMigrationSet, transitionpreflight.Evidence, migrationcapability.Capability) {
	t.Helper()
	admission := productionAdmission("candidate", 'b', '2')
	admission.Release.SourceRevision = "69652fb20101de80497cbf47ecfb402f700d6552"
	identity := transitionpreflight.ArtifactIdentity{Release: admission.Release, ArchitectureMarker: admission.ArchitectureMarker, ArtifactAdmissionDigest: productionDigest('c')}
	set, err := loadArtifactMigrations(t.Context(), identity, 20)
	if err != nil {
		t.Fatal(err)
	}
	set.predecessorAdmissionDigest, set.targetIdentityDigest = productionDigest('a'), productionDigest('d')
	evidence := transitionpreflight.Evidence{Candidate: identity, Predecessor: transitionpreflight.ArtifactIdentity{ArtifactAdmissionDigest: set.predecessorAdmissionDigest}, TargetIdentityDigest: set.targetIdentityDigest,
		Control: transitionpreflight.PostgreSQLControlProjection{PredecessorSchemaVersion: "goose/v19", CandidateSchemaVersion: "goose/v20", TargetIdentityDigest: set.targetIdentityDigest}}
	capability := productionCapabilities(t, identity.ArtifactAdmissionDigest, set.targetIdentityDigest, true)[0]
	capability.Goose.SchemaVersion, capability.Goose.RunnableSchemaVersions, capability.Goose.MigrationGraphDigest = "goose/v20", []string{"goose/v19", "goose/v20"}, set.graphDigest
	return set, evidence, capability
}

func TestFAI518ArtifactMigrationBoundary(t *testing.T) {
	set, evidence, capability := artifactBoundaryFixture(t)
	if _, err := fs.Stat(migrations.MigrationFS(), "021_api_token_descriptions.sql"); err != nil {
		t.Fatal("regression requires a checkout with migration 021", err)
	}
	if _, err := fs.Stat(set.files, "021_api_token_descriptions.sql"); err == nil {
		t.Fatal("checkout migration leaked into candidate")
	}
	pool, db := revision019PreflightDB(t)
	if err := migrations.BootstrapTransitionOperation(t.Context(), pool, db); err != nil {
		t.Fatal(err)
	}
	bound, err := set.apply(t.Context(), pool, db, evidence, capability)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateArtifactObservedRevision(t.Context(), pool, bound); err != nil {
		t.Fatal(err)
	}
	// Applying every available checkout migration proves the post-validation
	// boundary cannot report success once the database exceeds candidate 020.
	if err := migrations.ApplyRiverAndGoose(t.Context(), pool, db, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateArtifactObservedRevision(t.Context(), pool, bound); err == nil {
		t.Fatal("revision 021 accepted as candidate 020")
	}
}

func TestFAI518ArtifactMigrationRejectsExtraMigrationBeforeEffect(t *testing.T) {
	set, evidence, capability := artifactBoundaryFixture(t)
	set.files["021_unadmitted.sql"] = &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n")}
	// Nil connections prove rejection precedes any database effect.
	if _, err := set.apply(t.Context(), nil, nil, evidence, capability); err == nil {
		t.Fatal("extra migration accepted")
	}
}

func TestFAI518ArtifactMigrationRejectsInconsistentBinding(t *testing.T) {
	for _, name := range []string{"candidate admission", "predecessor admission", "image", "source", "target", "capability artifact", "capability target", "capability graph", "capability revision", "preflight revision", "source bytes"} {
		t.Run(name, func(t *testing.T) {
			set, evidence, capability := artifactBoundaryFixture(t)
			switch name {
			case "candidate admission":
				evidence.Candidate.ArtifactAdmissionDigest = productionDigest('f')
			case "predecessor admission":
				evidence.Predecessor.ArtifactAdmissionDigest = productionDigest('f')
			case "image":
				evidence.Candidate.Release.Image = "ghcr.io/flidai/leapview@" + productionDigest('f')
			case "source":
				evidence.Candidate.Release.SourceRevision = "5d870e7cf5f7e174dce115c416f6cab5e8259dcf"
			case "target":
				evidence.TargetIdentityDigest = productionDigest('f')
			case "capability artifact":
				capability.ArtifactAdmissionDigest = productionDigest('f')
			case "capability target":
				capability.TargetIdentityDigest = productionDigest('f')
			case "capability graph":
				capability.Goose.MigrationGraphDigest = productionDigest('f')
			case "capability revision":
				capability.Goose.SchemaVersion = "goose/v21"
			case "preflight revision":
				evidence.Control.CandidateSchemaVersion = "goose/v21"
			case "source bytes":
				set.files["020_release_transition_operation.sql"].Data = []byte("-- substituted migration")
			}
			if _, err := set.apply(t.Context(), nil, nil, evidence, capability); err == nil {
				t.Fatal("inconsistent binding accepted before migration")
			}
		})
	}
}

func TestFAI518ArtifactMigrationEvidence(t *testing.T) {
	set, evidence, capability := artifactBoundaryFixture(t)
	bound, err := set.bind(evidence, capability)
	if err != nil {
		t.Fatal(err)
	}
	bound.ObservedRevision = 20
	payload, err := bound.payload()
	if err != nil {
		t.Fatal(err)
	}
	var readback artifactMigrationEvidence
	if err := json.Unmarshal(payload, &readback); err != nil {
		t.Fatal(err)
	}
	if readback != bound || readback.CandidateRevision != 20 || readback.CandidateAdmissionDigest != evidence.Candidate.ArtifactAdmissionDigest || readback.TargetIdentityDigest != evidence.TargetIdentityDigest {
		t.Fatalf("migration evidence lost binding: %#v", readback)
	}
	if err := readback.validateObserved(21); err == nil {
		t.Fatal("reported revision 021 accepted")
	}
}
