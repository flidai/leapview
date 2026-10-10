package runtimeview

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/flidai/leapview/internal/manageddata/storage/filesystem"
)

func managedCapacityCSV(rows int) []byte {
	var body bytes.Buffer
	body.WriteString("order_id,customer_id,total_cents,region\n")
	for index := range rows {
		fmt.Fprintf(&body, "%d,%d,%d,region-%d\n", index+1, index%1000+1, index%100000, index%8)
	}
	return body.Bytes()
}

func managedCapacityFixture(tb testing.TB, rows int) (*Cache, manageddata.Manifest, []byte) {
	tb.Helper()
	body := managedCapacityCSV(rows)
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "orders.csv", SHA256: digestOf(body), Size: int64(len(body))}}}
	store, err := filesystem.New(filepath.Join(tb.TempDir(), "blobs"))
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := store.Put(tb.Context(), storage.Blob{SHA256: manifest.Files[0].SHA256, Size: int64(len(body))}, bytes.NewReader(body)); err != nil {
		tb.Fatal(err)
	}
	cache, err := New(filepath.Join(tb.TempDir(), "runtime"), store)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := cache.DeleteRevision(ctx, manifest.RevisionID()); err != nil {
			tb.Errorf("delete managed capacity revision: %v", err)
		}
	})
	tb.Logf("fixture_rows=%d fixture_bytes=%d fixture_sha256=%s revision=%s", rows, len(body), manifest.Files[0].SHA256, manifest.RevisionID())
	return cache, manifest, body
}

func materializeCapacityBatch(tb testing.TB, cache *Cache, manifest manageddata.Manifest, readers int) {
	tb.Helper()
	start := make(chan struct{})
	var ready, finished sync.WaitGroup
	ready.Add(readers)
	finished.Add(readers)
	errors := make([]error, readers)
	for index := range readers {
		go func() {
			defer finished.Done()
			ready.Done()
			<-start
			view, err := cache.MaterializeRevision(tb.Context(), manifest.RevisionID(), manifest)
			if err != nil {
				errors[index] = err
				return
			}
			errors[index] = view.Release()
		}()
	}
	ready.Wait()
	close(start)
	finished.Wait()
	for index, err := range errors {
		if err != nil {
			tb.Fatalf("managed reader %d: %v", index, err)
		}
	}
}

// The 100,000-row immutable CSV is admitted into real content-addressed local
// storage. Cold creates a revision tree; warm re-verifies that same tree. One
// operation is a simultaneous shared-revision reader batch with every lease
// released. This measures materialization, not PostgreSQL activation, remote
// ingestion, dashboard settlement or user p95. File contents are guarded by
// the production streaming digest check on every reader.
func BenchmarkManagedRevisionCapacity(b *testing.B) {
	for _, warm := range []bool{false, true} {
		mode := "cold"
		if warm {
			mode = "warm"
		}
		for _, readers := range []int{1, 10, 20, 100} {
			b.Run(fmt.Sprintf("%s/readers=%d", mode, readers), func(b *testing.B) {
				cache, manifest, body := managedCapacityFixture(b, 100000)
				if warm {
					materializeCapacityBatch(b, cache, manifest, 1)
				}
				b.ReportAllocs()
				b.ResetTimer()
				b.StopTimer()
				for range b.N {
					if !warm {
						if err := cache.DeleteRevision(b.Context(), manifest.RevisionID()); err != nil {
							b.Fatal(err)
						}
					}
					b.StartTimer()
					materializeCapacityBatch(b, cache, manifest, readers)
					b.StopTimer()
				}
				b.ReportMetric(float64(readers), "leases/op")
				b.ReportMetric(float64(len(body)*readers), "verified-bytes/op")
			})
		}
	}
}

func TestManagedCapacityMaterializesExactBytesAndReleasesLeases(t *testing.T) {
	cache, manifest, body := managedCapacityFixture(t, 128)
	materializeCapacityBatch(t, cache, manifest, 4)
	actual, err := os.ReadFile(filepath.Join(cache.revisionPath(manifest.RevisionID()), "orders.csv"))
	if err != nil || !bytes.Equal(actual, body) {
		t.Fatalf("materialized content differs: %v", err)
	}
	if err := cache.DeleteRevision(t.Context(), manifest.RevisionID()); err != nil {
		t.Fatalf("revision remained leased: %v", err)
	}
	if _, err := os.Stat(cache.revisionPath(manifest.RevisionID())); !os.IsNotExist(err) {
		t.Fatalf("revision persisted after all leases released: %v", err)
	}
}
