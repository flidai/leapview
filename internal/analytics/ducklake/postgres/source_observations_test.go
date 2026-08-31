package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	materialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSourceObservationCaptureExactReplayReadMismatchAndImmutability(t *testing.T) {
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "ducklake_source_observations_test")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	const poolID, catalogID = "pool-observations", "catalog-observations"
	r := New(p)
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: testCatalogUUID, MetadataSchema: "lake", CompatibilityDigest: digest('a'), CatalogSchemaVersion: "ducklake-v1"}); err != nil {
		t.Fatal(err)
	}
	const attemptID = "0198f2c0-7c7a-7f00-8a11-000000000021"
	requestDigest, planDigest := digest('b'), digest('c')
	if _, err := r.BeginAttempt(t.Context(), BeginAttemptInput{AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "builder-observations", FencingEpoch: 1, SessionIdentity: "session-observations", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	marker := ducklake.CommitMarker{SchemaVersion: ducklake.CommitMarkerSchemaVersion, DeliveryID: "delivery-observations", GenerationID: "generation-observations", AttemptID: attemptID, LeaseEpoch: 1, RequestDigest: requestDigest, PlanDigest: planDigest, Project: "project-observations", Environment: "prod", PhysicalPoolID: poolID}
	observations := []materialize.SourceObservation{{ID: "orders", Revision: "revision-1", Schema: []semanticmodel.ColumnSchema{{Name: "id", Ordinal: 0, PhysicalType: "BIGINT"}}, ObservationQueries: 1, ObservationRows: 2, ObservationMillis: 3}}
	capture, err := NewSourceObservationCapture(attemptID, marker, observations, time.Date(2026, time.August, 31, 1, 2, 3, 4_000, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.RecordSourceObservationCapture(t.Context(), capture)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.RecordSourceObservationCapture(t.Context(), capture)
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if second.ContentDigest != first.ContentDigest || string(second.ObservationEnvelope) != string(first.ObservationEnvelope) {
		t.Fatalf("replay changed capture: first=%#v second=%#v", first, second)
	}
	loaded, err := r.LoadSourceObservationCapture(t.Context(), attemptID)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := loaded.Observations()
	if err != nil || len(decoded) != 1 || decoded[0].ID != "orders" {
		t.Fatalf("decoded observations=%#v err=%v", decoded, err)
	}
	conflictingObservations := []materialize.SourceObservation{{ID: "orders", Revision: "revision-2", Schema: []semanticmodel.ColumnSchema{{Name: "id", Ordinal: 0, PhysicalType: "BIGINT"}}, ObservationQueries: 1, ObservationRows: 3, ObservationMillis: 4}}
	conflictingCapture, err := NewSourceObservationCapture(attemptID, marker, conflictingObservations, capture.CapturedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if conflictingCapture.ContentDigest == capture.ContentDigest {
		t.Fatal("valid conflicting capture unexpectedly reused the original content digest")
	}
	if _, err := r.RecordSourceObservationCapture(t.Context(), conflictingCapture); !errors.Is(err, ErrConflict) {
		t.Fatalf("valid conflicting replay err=%v, want ErrConflict", err)
	}
	mismatch := capture
	mismatch.ContentDigest = "sha256:" + strings.Repeat("f", 64)
	if _, err := r.RecordSourceObservationCapture(t.Context(), mismatch); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched replay err=%v, want ErrConflict", err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE ducklake.source_observation_capture SET content_digest=$2 WHERE attempt_id=$1`, attemptID, digest('f')); err == nil {
		t.Fatal("source observation capture identity mutation was accepted")
	}
	if _, err := p.Exec(t.Context(), `DELETE FROM ducklake.source_observation_capture WHERE attempt_id=$1`, attemptID); err == nil {
		t.Fatal("source observation capture deletion was accepted")
	}
}

func TestNewSourceObservationCaptureRejectsNonCanonicalAttemptID(t *testing.T) {
	const attemptID = "0198f2c0-7c7a-7f00-8a11-0000000000AB"
	marker := ducklake.CommitMarker{SchemaVersion: ducklake.CommitMarkerSchemaVersion, DeliveryID: "delivery-observations", GenerationID: "generation-observations", AttemptID: attemptID, LeaseEpoch: 1, RequestDigest: digest('b'), PlanDigest: digest('c'), Project: "project-observations", Environment: "prod", PhysicalPoolID: "pool-observations"}
	if _, err := NewSourceObservationCapture(attemptID, marker, nil, time.Now().UTC()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-canonical attempt id error=%v, want ErrInvalid", err)
	}
}
