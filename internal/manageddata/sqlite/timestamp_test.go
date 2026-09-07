package sqlite

import (
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
)

func TestUploadSessionTimestampsUseCanonicalRFC3339Nano(t *testing.T) {
	ctx, _, repo := testRepository(t)
	collection := createCollection(t, ctx, repo, "timestamp", "project-timestamp", "warehouse")
	expiresAt := time.Now().UTC().Add(time.Hour)
	session, err := repo.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{
		ID: "upload-timestamp", CollectionID: collection.ID, Manifest: manageddata.Manifest{},
		StorageBackend: "s3", StagingPrefix: "uploads/upload-timestamp", ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := expiresAt.UTC().Format(time.RFC3339Nano)
	if session.ExpiresAt != want {
		t.Fatalf("session expiry = %q, want canonical %q", session.ExpiresAt, want)
	}
	if _, err := time.Parse(time.RFC3339Nano, session.ExpiresAt); err != nil {
		t.Fatalf("session expiry is not RFC3339Nano: %v", err)
	}
}
