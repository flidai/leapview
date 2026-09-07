package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func identity(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func metadataFor(body []byte) ObjectMetadata {
	return ObjectMetadata{
		StorageSecurityDomain: "domain-a",
		Digest:                identity(body),
		SizeBytes:             int64(len(body)),
		ContentType:           "application/octet-stream",
		MetadataDigest:        identity([]byte(`{"kind":"test"}`)),
	}
}

func TestMemoryStoreExactReplayAndConflict(t *testing.T) {
	store, err := NewMemoryStore(MemoryStoreConfig{StorageSecurityDomain: "domain-a"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	body := []byte("payload")
	metadata := metadataFor(body)
	first, err := store.PutImmutable(ctx, "artifacts/a", bytes.NewReader(body), metadata)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.PutImmutable(ctx, "artifacts/a", bytes.NewReader(body), metadata)
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if replay.Digest != first.Digest || !replay.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("replay changed immutable identity: first=%+v replay=%+v", first, replay)
	}
	if _, err := store.PutImmutable(ctx, "artifacts/a", strings.NewReader("different"), metadataFor([]byte("different"))); !errors.Is(err, ErrConflict) {
		t.Fatalf("different bytes error = %v, want ErrConflict", err)
	}
	changed := metadataFor(body)
	changed.ContentType = "text/plain"
	if _, err := store.PutImmutable(ctx, "artifacts/a", bytes.NewReader(body), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("different metadata error = %v, want ErrConflict", err)
	}
}

func TestMemoryStoreOpenVerifiesContentIdentityAndCopiesBuffers(t *testing.T) {
	store, err := NewMemoryStore(MemoryStoreConfig{StorageSecurityDomain: "domain-a"})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("payload")
	metadata := metadataFor(body)
	info, err := store.PutImmutable(context.Background(), "artifacts/a", bytes.NewReader(body), metadata)
	if err != nil {
		t.Fatal(err)
	}
	body[0] = 'X'
	obj, err := store.Open(context.Background(), "artifacts/a")
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Body.Close()
	got, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "payload" {
		t.Fatalf("opened bytes = %q, want payload", got)
	}
	if obj.Info != info || obj.Info.Digest != identity([]byte("payload")) || obj.Info.SizeBytes != int64(len("payload")) || obj.Info.StorageSecurityDomain != "domain-a" || obj.Info.MetadataDigest == "" {
		t.Fatalf("opened identity = %+v, want %+v", obj.Info, info)
	}
	// A reader returned by Open is independent from the store's retained body.
	obj2, err := store.Open(context.Background(), "artifacts/a")
	if err != nil {
		t.Fatal(err)
	}
	defer obj2.Body.Close()
	opened := make([]byte, 1)
	if _, err := obj2.Body.Read(opened); err != nil {
		t.Fatal(err)
	}
	if opened[0] != 'p' {
		t.Fatalf("opened body unexpectedly aliases caller buffer: %q", opened)
	}
}

func TestMemoryStoreRejectsTraversalAndDomainMismatch(t *testing.T) {
	if _, err := NewMemoryStore(MemoryStoreConfig{StorageSecurityDomain: " domain-a"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("whitespace domain error = %v, want ErrInvalid", err)
	}
	store, err := NewMemoryStore(MemoryStoreConfig{StorageSecurityDomain: "domain-a"})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("x")
	for _, key := range []string{"", "/absolute", "a//b", "a/../b", "a/./b", "a\\b", "a\x00b", "C:/absolute"} {
		if _, err := store.PutImmutable(context.Background(), key, bytes.NewReader(body), metadataFor(body)); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("key %q error = %v, want ErrInvalidKey", key, err)
		}
	}
	for _, prefix := range []string{"/absolute", "a//", "a/../"} {
		if _, _, err := store.List(context.Background(), prefix, "", 1); !errors.Is(err, ErrInvalidPrefix) {
			t.Errorf("prefix %q error = %v, want ErrInvalidPrefix", prefix, err)
		}
	}
	wrong := metadataFor(body)
	wrong.StorageSecurityDomain = "domain-b"
	if _, err := store.PutImmutable(context.Background(), "artifacts/b", bytes.NewReader(body), wrong); !errors.Is(err, ErrDomainMismatch) {
		t.Fatalf("domain error = %v, want ErrDomainMismatch", err)
	}
}

func TestMemoryStoreRejectsUnexpectedDigestAndSize(t *testing.T) {
	store, err := NewMemoryStore(MemoryStoreConfig{StorageSecurityDomain: "domain-a"})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("payload")
	metadata := metadataFor(body)
	metadata.Digest = identity([]byte("different"))
	if _, err := store.PutImmutable(context.Background(), "objects/digest", bytes.NewReader(body), metadata); !errors.Is(err, ErrInvalid) {
		t.Fatalf("digest mismatch error = %v, want ErrInvalid", err)
	}
	metadata = metadataFor(body)
	metadata.SizeBytes++
	if _, err := store.PutImmutable(context.Background(), "objects/size", bytes.NewReader(body), metadata); !errors.Is(err, ErrInvalid) {
		t.Fatalf("size mismatch error = %v, want ErrInvalid", err)
	}
}

func TestMemoryStoreLostAcknowledgementReconciles(t *testing.T) {
	store, err := NewMemoryStore(MemoryStoreConfig{StorageSecurityDomain: "domain-a"})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("payload")
	metadata := metadataFor(body)
	store.SimulateLostCommitAcknowledgement()
	info, err := store.PutImmutable(context.Background(), "artifacts/lost", bytes.NewReader(body), metadata)
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("lost acknowledgement error = %v, want ErrAmbiguous", err)
	}
	opened, err := store.Open(context.Background(), "artifacts/lost")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Body.Close()
	if opened.Info != info {
		t.Fatalf("reconciled info = %+v, want %+v", opened.Info, info)
	}
	if _, err := store.PutImmutable(context.Background(), "artifacts/lost", bytes.NewReader(body), metadata); err != nil {
		t.Fatalf("retry after reconciliation: %v", err)
	}
}

func TestMemoryStoreBoundedSortedListingAndCursor(t *testing.T) {
	store, err := NewMemoryStore(MemoryStoreConfig{StorageSecurityDomain: "domain-a"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"objects/c", "objects/a", "objects/b", "objects-other/x", "other/z"} {
		body := []byte(key)
		if _, err := store.PutImmutable(context.Background(), key, bytes.NewReader(body), metadataFor(body)); err != nil {
			t.Fatal(err)
		}
	}
	page, cursor, err := store.List(context.Background(), "objects", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Key != "objects/a" || page[1].Key != "objects/b" || cursor != "objects/b" {
		t.Fatalf("first page = %#v cursor=%q", page, cursor)
	}
	page, cursor, err = store.List(context.Background(), "objects", cursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Key != "objects/c" || cursor != "" {
		t.Fatalf("second page = %#v cursor=%q", page, cursor)
	}
	if _, _, err := store.List(context.Background(), "objects", "", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero limit error = %v, want ErrInvalid", err)
	}
}

func TestMemoryStoreDeleteAndCancellation(t *testing.T) {
	store, err := NewMemoryStore(MemoryStoreConfig{StorageSecurityDomain: "domain-a"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	body := []byte("a")
	if err := store.Delete(ctx, "objects/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing delete error = %v, want ErrNotFound", err)
	}
	if _, err := store.PutImmutable(ctx, "objects/a", bytes.NewReader(body), metadataFor(body)); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "objects/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(ctx, "objects/a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted open error = %v, want ErrNotFound", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.PutImmutable(canceled, "objects/c", bytes.NewReader(body), metadataFor(body)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled put error = %v, want context.Canceled", err)
	}
	if _, err := store.Open(canceled, "objects/c"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled open error = %v, want context.Canceled", err)
	}
	if _, _, err := store.List(canceled, "objects", "", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list error = %v, want context.Canceled", err)
	}
	if err := store.Delete(canceled, "objects/a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled delete error = %v, want context.Canceled", err)
	}
}

func TestMemoryStoreTimestampAndMetadataDigest(t *testing.T) {
	created := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	store, err := NewMemoryStore(MemoryStoreConfig{StorageSecurityDomain: "domain-a", Now: func() time.Time { return created }})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("a")
	metadata := metadataFor(body)
	metadata.MetadataDigest = "not-a-digest"
	if _, err := store.PutImmutable(context.Background(), "objects/malformed", bytes.NewReader(body), metadata); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed metadata digest error = %v, want ErrInvalid", err)
	}
	metadata = metadataFor(body)
	metadata.MetadataDigest = identity([]byte("wrong"))
	if _, err := store.PutImmutable(context.Background(), "objects/a", bytes.NewReader(body), metadata); err != nil {
		// MetadataDigest is an externally precommitted identity; a syntactically
		// valid value is accepted even though this reference store cannot inspect
		// the opaque metadata envelope.
		t.Fatal(err)
	}
	info, err := store.Open(context.Background(), "objects/a")
	if err != nil {
		t.Fatal(err)
	}
	defer info.Body.Close()
	if !info.Info.CreatedAt.Equal(created) {
		t.Fatalf("created at = %s, want %s", info.Info.CreatedAt, created)
	}
	zeroStore, err := NewMemoryStore(MemoryStoreConfig{StorageSecurityDomain: "domain-a", Now: func() time.Time { return time.Time{} }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zeroStore.PutImmutable(context.Background(), "objects/zero-time", bytes.NewReader(body), metadataFor(body)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero creation time error = %v, want ErrInvalid", err)
	}
}
