package gcstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/deployment/gc"
)

func TestLocalStoreScopesAndConditionalVersion(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "orphan.parquet"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := store.ListPoolObjects(context.Background(), "pool")
	if err != nil || len(objects) != 1 {
		t.Fatalf("objects=%v err=%v", objects, err)
	}
	if _, err := store.Stat(context.Background(), "pool", "../outside"); !errors.Is(err, gc.ErrObjectOutsidePool) {
		t.Fatalf("outside stat err=%v", err)
	}
	if _, err := store.DeleteConditional(context.Background(), gc.DeleteRequest{PhysicalPoolID: "pool", Key: "orphan.parquet", Digest: objects[0].Digest, Version: "replacement"}); err == nil {
		t.Fatal("replacement version was deleted")
	}
	result, err := store.DeleteConditional(context.Background(), gc.DeleteRequest{PhysicalPoolID: "pool", Key: "orphan.parquet", Digest: objects[0].Digest, Version: objects[0].Version})
	if err != nil || !result.Deleted {
		t.Fatalf("delete=%#v err=%v", result, err)
	}
}

func TestLocalNamespaceOwnershipIsConditionalAndIdempotent(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	claim := physicalpool.OwnershipClaim{PoolID: physicalpool.PoolID("sha256:" + strings.Repeat("a", 64)), CompatibilityDigest: "sha256:" + strings.Repeat("b", 64), EvidenceDigest: "sha256:" + strings.Repeat("c", 64), OwnerID: "instance-a"}
	if err := store.AcquireNamespaceOwnership(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if err := store.AcquireNamespaceOwnership(context.Background(), claim); err != nil {
		t.Fatalf("same-owner retry: %v", err)
	}
	upgraded := claim
	upgraded.CompatibilityDigest = "sha256:" + strings.Repeat("d", 64)
	upgraded.EvidenceDigest = "sha256:" + strings.Repeat("e", 64)
	if err := store.AcquireNamespaceOwnership(context.Background(), upgraded); err != nil {
		t.Fatalf("same durable DB owner must survive tuple upgrade: %v", err)
	}
	conflict := claim
	conflict.OwnerID = "lvinst_other_database"
	if err := store.AcquireNamespaceOwnership(context.Background(), conflict); !errors.Is(err, physicalpool.ErrOwnershipConflict) {
		t.Fatalf("conflicting owner error=%v", err)
	}
}

func TestLocalDeletionLeaseFencesClonedMetadataDatabases(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.AcquireNamespaceDeletionLease(context.Background(), "lvinst_a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireNamespaceDeletionLease(context.Background(), "lvinst_a", time.Minute); !errors.Is(err, physicalpool.ErrDeletionLeaseConflict) {
		t.Fatalf("second metadata DB unexpectedly acquired lease: %v", err)
	}
	if err := store.VerifyNamespaceDeletionLease(context.Background(), "lvinst_a", token); err != nil {
		t.Fatalf("lease verification: %v", err)
	}
	if err := store.ReleaseNamespaceDeletionLease(context.Background(), "lvinst_a", token); err != nil {
		t.Fatalf("lease release: %v", err)
	}
	if _, err := store.AcquireNamespaceDeletionLease(context.Background(), "lvinst_b", time.Minute); err != nil {
		t.Fatalf("lease after release: %v", err)
	}
}
