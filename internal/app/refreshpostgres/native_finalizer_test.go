package refreshpostgres

import (
	"context"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	postgresrepo "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/google/uuid"
)

func TestPostgresNativeRefreshFinalizerRejectsWhitespaceResolverResult(t *testing.T) {
	f := &PostgresNativeRefreshFinalizerAdapter{TargetResolver: PostgresNativeRefreshTargetResolverFunc(func(context.Context, postgresrepo.Tx, refreshrun.JobRecord) (string, error) {
		return " target", nil
	})}
	if _, err := f.resolveTarget(t.Context(), nil, refreshrun.JobRecord{}); err == nil {
		t.Fatal("whitespace target resolver result unexpectedly accepted")
	}
}

func TestNativeRefreshIdentitiesAreDeterministicUUIDv7Consequences(t *testing.T) {
	const generationID = "0198f2c0-7c7a-7f00-8a11-000000000111"
	job := refreshrun.JobRecord{
		RunID: "run-1",
		Identity: projectgraph.ServingIdentity{
			ProjectID: "project-1", Environment: "prod", GenerationID: "0198f2c0-7c7a-7f00-8a11-000000000105",
		},
	}
	result := refreshrun.CanonicalRefreshResult{PlanID: "plan-1", NativeGenerationID: generationID, SnapshotID: 7}
	evidence := postgresrepo.PublicationInput{ExpectedTargetRevision: 2, PhysicalPoolID: "pool-1", CatalogID: "catalog-1"}

	publicationID, leaseID, correlationID, digest := NativeRefreshIdentities(job, result, evidence)
	replayedPublicationID, replayedLeaseID, replayedCorrelationID, replayedDigest := NativeRefreshIdentities(job, result, evidence)
	if publicationID != replayedPublicationID || leaseID != replayedLeaseID || correlationID != replayedCorrelationID || digest != replayedDigest {
		t.Fatal("native refresh identities changed across an exact replay")
	}
	if publicationID == leaseID || publicationID == correlationID || leaseID == correlationID {
		t.Fatal("native refresh identity roles collided")
	}
	generation := uuid.MustParse(generationID)
	wantTimestampPrefix := generation[:6]
	for label, value := range map[string]string{"publication": publicationID, "lease": leaseID, "correlation": correlationID} {
		id, err := uuid.Parse(value)
		if err != nil || id.Version() != 7 || id.Variant() != uuid.RFC4122 || id.String() != value {
			t.Fatalf("%s identity = %q, want canonical UUIDv7: %v", label, value, err)
		}
		if string(id[:6]) != string(wantTimestampPrefix) {
			t.Fatalf("%s identity did not preserve the generation timestamp prefix", label)
		}
	}
}
