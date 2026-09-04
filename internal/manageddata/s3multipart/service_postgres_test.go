package s3multipart

import (
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/control"
	managedpostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresOpenSessionCreateSignCompleteAndExpiry(t *testing.T) {
	harness := postgrestest.Start(t)
	runtimeRole := harness.EnsureRole(t, postgrestest.Role{
		Name: "leapview_control_runtime", Password: "runtime-secret", Login: true,
	})
	database := harness.NewDatabase(t, "")

	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := managedpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	pool, err := pgxpool.New(t.Context(), database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	repo := managedpostgres.New(pool)
	collection, err := repo.CreateCollection(t.Context(), manageddata.CreateCollectionInput{
		ID: "collection-s3-multipart", ProjectID: projectgraph.ResourceID("project-s3-multipart"),
		ConnectionID: projectgraph.ResourceID("warehouse"), Name: "Warehouse",
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	manifest := manageddata.Manifest{Files: []manageddata.File{{
		Path: "data.csv", Size: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}}
	session, err := repo.CreateUploadSession(t.Context(), manageddata.CreateUploadSessionInput{
		ID: "upload-s3-multipart", CollectionID: collection.ID, Manifest: manifest,
		StorageBackend: "s3", StagingPrefix: "uploads/upload-s3-multipart", ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339Nano, session.ExpiresAt); err != nil {
		t.Fatalf("native upload expiry %q is not RFC3339Nano: %v", session.ExpiresAt, err)
	}

	provider := &fakeMultipartStore{}
	clock := now
	service, err := New(repo, provider, Config{
		Backend: "s3", Clock: func() time.Time { return clock }, SignExpiry: 15 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	create := CreateRequest{
		Project: "project-s3-multipart", Connection: "warehouse", UploadSessionID: session.ID.String(),
		Path: "data.csv", IdempotencyKey: "create-postgres",
	}
	upload, err := service.Create(t.Context(), create)
	if err != nil {
		t.Fatalf("create against PostgreSQL session: %v", err)
	}
	signed, err := service.SignPart(t.Context(), SignPartRequest{
		Project: "project-s3-multipart", Connection: "warehouse", UploadSessionID: session.ID.String(),
		MultipartUploadID: upload.ID, PartNumber: 1, Size: 1,
	})
	if err != nil {
		t.Fatalf("sign against PostgreSQL session: %v", err)
	}
	if signed.ExpiresAt != now.Add(15*time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("signed expiry = %q, want %q", signed.ExpiresAt, now.Add(15*time.Minute).Format(time.RFC3339Nano))
	}

	clock = now.Add(2 * time.Hour)
	if _, err := service.SignPart(t.Context(), SignPartRequest{
		Project: "project-s3-multipart", Connection: "warehouse", UploadSessionID: session.ID.String(),
		MultipartUploadID: upload.ID, PartNumber: 1, Size: 1,
	}); !errors.Is(err, control.ErrExpired) {
		t.Fatalf("expired sign error = %v, want expired", err)
	}

	clock = now
	completed, err := service.Complete(t.Context(), CompleteRequest{
		Project: "project-s3-multipart", Connection: "warehouse", UploadSessionID: session.ID.String(),
		MultipartUploadID: upload.ID, IdempotencyKey: "complete-postgres",
		Parts: []CompletedPart{{PartNumber: 1, ETag: "etag"}},
	})
	if err != nil {
		t.Fatalf("complete against PostgreSQL session: %v", err)
	}
	if completed.Status != StatusCompleted {
		t.Fatalf("completed status = %q, want %q", completed.Status, StatusCompleted)
	}
}
