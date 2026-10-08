package module

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/manageddata/maintenance"
	"github.com/flidai/leapview/internal/manageddata/runtimeview"
	"github.com/flidai/leapview/internal/manageddata/s3multipart"
	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/flidai/leapview/internal/manageddata/storage/filesystem"
)

func TestMaintenanceRuntimeCollectionContinuesAfterIndependentFailure(t *testing.T) {
	for _, stage := range []string{"upload", "multipart", "blob"} {
		t.Run(stage, func(t *testing.T) {
			ctx := t.Context()
			cache, leased, idleRoot := maintenanceRuntimeFixture(t)
			runtimeCollector, err := maintenance.NewRuntimeViewCollector(cache, maintenance.RuntimeViewGCConfig{
				GraceAge: time.Hour, Limit: 10, Now: func() time.Time { return time.Now().UTC().Add(2 * time.Hour) },
			})
			if err != nil {
				t.Fatal(err)
			}
			inventory := &maintenanceProgressInventory{}
			collector, err := maintenance.NewBlobCollector(inventory, maintenanceProgressReachability{}, maintenance.BlobGCConfig{GraceAge: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			expireRepo := &maintenanceProgressExpireRepo{}
			if stage == "upload" {
				expireRepo.err = control.ErrInternal
			}
			uploads, err := control.New(expireRepo, &unusedMaintenanceBlobStore{}, control.Config{UploadTTL: time.Hour, Transport: unusedMaintenanceTransport{}})
			if err != nil {
				t.Fatal(err)
			}
			multipartRepo := &maintenanceProgressMultipartRepo{}
			if stage == "multipart" {
				multipartRepo.err = control.ErrInternal
			}
			multipart, err := s3multipart.New(multipartRepo, &unusedMaintenanceMultipartStore{}, s3multipart.Config{Backend: "s3"})
			if err != nil {
				t.Fatal(err)
			}
			if stage == "blob" {
				inventory.err = storage.ErrIntegrity
			}
			pass := Maintenance{uploads: uploads, multipart: multipart, uploadTTL: time.Hour, collector: collector, runtime: runtimeCollector}
			result, err := pass.ExpireUploads(ctx)
			wantErr := error(control.ErrInternal)
			if stage == "blob" {
				wantErr = storage.ErrIntegrity
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("maintenance error = %v, want stage failure retained", err)
			}
			wantExpired := int64(7)
			if stage == "upload" {
				wantExpired = 0
			}
			if result.Expired != wantExpired {
				t.Fatalf("partial upload result = %#v, want expired=%d", result, wantExpired)
			}
			if _, err := os.Stat(idleRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("eligible idle runtime revision was not collected after %s failure: %v", stage, err)
			}
			content, err := os.ReadFile(filepath.Join(leased.Root(), "data.csv"))
			if err != nil || string(content) != "leased revision" {
				t.Fatalf("live leased runtime revision changed: %q, %v", content, err)
			}
			if stage != "blob" && inventory.walks != 0 {
				t.Fatalf("blob collector crossed unresolved upload/recovery gate: walks=%d", inventory.walks)
			}
			if stage == "blob" && inventory.walks != 1 {
				t.Fatalf("blob failure fixture not reached: walks=%d", inventory.walks)
			}
		})
	}
}

func TestMaintenanceCancellationStopsIndependentCollectors(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cache, leased, idleRoot := maintenanceRuntimeFixture(t)
	runtimeCollector, err := maintenance.NewRuntimeViewCollector(cache, maintenance.RuntimeViewGCConfig{
		GraceAge: time.Hour, Limit: 10, Now: func() time.Time { return time.Now().UTC().Add(2 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	inventory := &maintenanceProgressInventory{}
	collector, err := maintenance.NewBlobCollector(inventory, maintenanceProgressReachability{}, maintenance.BlobGCConfig{GraceAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	uploads, err := control.New(&maintenanceProgressExpireRepo{cancel: cancel}, &unusedMaintenanceBlobStore{}, control.Config{UploadTTL: time.Hour, Transport: unusedMaintenanceTransport{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (Maintenance{uploads: uploads, collector: collector, runtime: runtimeCollector}).ExpireUploads(ctx)
	if !errors.Is(err, context.Canceled) || result.Expired != 7 || inventory.walks != 0 {
		t.Fatalf("canceled pass = %#v, %v; blob walks=%d", result, err, inventory.walks)
	}
	for _, root := range []string{idleRoot, leased.Root()} {
		if _, err := os.Stat(root); err != nil {
			t.Fatalf("runtime work after cancellation removed %s: %v", root, err)
		}
	}
}

func maintenanceRuntimeFixture(t *testing.T) (*runtimeview.Cache, manageddata.RevisionLease, string) {
	t.Helper()
	store, err := filesystem.New(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	cache, err := runtimeview.New(filepath.Join(t.TempDir(), "runtime"), store)
	if err != nil {
		t.Fatal(err)
	}
	materialize := func(content string) manageddata.RevisionLease {
		t.Helper()
		body := []byte(content)
		sum := sha256.Sum256(body)
		blob := storage.Blob{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
		if _, err := store.Put(t.Context(), blob, bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "data.csv", SHA256: blob.SHA256, Size: blob.Size}}}
		lease, err := cache.MaterializeRevision(t.Context(), manifest.RevisionID(), manifest)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := lease.Release(); err != nil {
				t.Error(err)
			}
			if err := cache.DeleteRevision(cleanupCtx, manifest.RevisionID()); err != nil {
				t.Error(err)
			}
		})
		return lease
	}
	leased := materialize("leased revision")
	idle := materialize("idle revision")
	if leased.Root() == idle.Root() {
		t.Fatal("leased/idle control revisions are not distinct")
	}
	idleRoot := idle.Root()
	if err := idle.Release(); err != nil {
		t.Fatal(err)
	}
	return cache, leased, idleRoot
}

type maintenanceProgressExpireRepo struct {
	control.Repository
	err    error
	cancel context.CancelFunc
}

func (r *maintenanceProgressExpireRepo) ExpireUploadSessions(context.Context, time.Time) (int64, error) {
	if r.cancel != nil {
		r.cancel()
	}
	return 7, r.err
}

type unusedMaintenanceBlobStore struct{ storage.BlobStore }
type unusedMaintenanceTransport struct{ control.Transport }

func (unusedMaintenanceTransport) Backend() string { return "test" }

type unusedMaintenanceMultipartStore struct{ s3multipart.MultipartStore }

type maintenanceProgressMultipartRepo struct {
	s3multipart.Repository
	err error
}

func (r *maintenanceProgressMultipartRepo) ListRecoverableS3MultipartUploads(ctx context.Context, _ time.Time, _ int64) ([]manageddata.S3MultipartUpload, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, r.err
}

func (*maintenanceProgressMultipartRepo) ClaimS3MultipartDigest(context.Context, string, string, time.Time) (int64, bool, error) {
	panic("empty scan must not claim digests")
}
func (*maintenanceProgressMultipartRepo) RenewS3MultipartDigest(context.Context, string, string, int64, time.Time) (bool, error) {
	panic("empty scan must not renew digests")
}
func (*maintenanceProgressMultipartRepo) ReleaseS3MultipartDigest(context.Context, string, string, int64) error {
	panic("empty scan must not release digests")
}

type maintenanceProgressInventory struct {
	walks int
	err   error
}

func (i *maintenanceProgressInventory) WalkBlobs(context.Context, func(storage.BlobMetadata) error) error {
	i.walks++
	return i.err
}
func (*maintenanceProgressInventory) DeleteBlobs(context.Context, []string) error {
	panic("empty inventory must not delete blobs")
}

type maintenanceProgressReachability struct{}

func (maintenanceProgressReachability) Snapshot(context.Context) (maintenance.ReachabilitySnapshot, error) {
	return maintenance.ReachabilitySnapshot{Generation: 1, SHA256s: []string{strings.Repeat("a", 64)}}, nil
}
func (maintenanceProgressReachability) WithStableSnapshot(context.Context, uint64, func(maintenance.ReachabilitySnapshot) error) error {
	panic("empty inventory must not request delete authority")
}
