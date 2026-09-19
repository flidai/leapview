//go:build fai518qualification

package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flidai/leapview/internal/release/migrationcapability"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/flidai/leapview/internal/release/transitionoperation"
	"github.com/jackc/pgx/v5/pgxpool"
)

func publishArtifactMigrationCapabilities(t *testing.T, authority *releasepostgres.MigrationCapabilityAuthority, set artifactMigrationSet, target string, candidate bool) {
	t.Helper()
	for _, capability := range productionCapabilities(t, set.identity.ArtifactAdmissionDigest, target, candidate) {
		if capability.Subsystem == migrationcapability.SubsystemGoose {
			capability.Goose.SchemaVersion = fmt.Sprintf("goose/v%d", set.revision)
			capability.Goose.RunnableSchemaVersions = []string{"goose/v19", "goose/v20"}
			capability.Goose.MigrationGraphDigest = set.graphDigest
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

func validateArtifactObservedRevision(ctx context.Context, pool *pgxpool.Pool, evidence artifactMigrationEvidence) error {
	var observed int64
	if err := pool.QueryRow(ctx, `SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&observed); err != nil {
		return err
	}
	return evidence.validateObserved(observed)
}

func assertArtifactMigrationReadback(t *testing.T, operation transitionoperation.Operation, expected artifactMigrationEvidence) {
	t.Helper()
	for _, phase := range operation.PhaseResults {
		if phase.Phase != transitionoperation.PhaseMigrations {
			continue
		}
		var observed artifactMigrationEvidence
		if err := json.Unmarshal(phase.Result, &observed); err != nil {
			t.Fatal(err)
		}
		if observed != expected {
			t.Fatalf("durable migration evidence = %#v, want %#v", observed, expected)
		}
		return
	}
	t.Fatal("durable migration evidence missing")
}
