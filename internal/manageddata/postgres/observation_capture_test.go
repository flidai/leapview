package postgres

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/recoveryset/observation"
)

func TestProviderObservationRevisionFileBoundMatchesManagedDataAdmission(t *testing.T) {
	if got, want := providerObservationMaxRevisionFiles, manageddata.MaxManifestFiles; got != want {
		t.Fatalf("capture file bound = %d, managed-data admission bound = %d", got, want)
	}
	if got, want := observation.MaxRevisionFiles, manageddata.MaxManifestFiles; got != want {
		t.Fatalf("observation file bound = %d, managed-data admission bound = %d", got, want)
	}
	if got, want := providerObservationMaxObjects, observation.MaxManifestObjects; got != want {
		t.Fatalf("capture aggregate object bound = %d, observation aggregate object bound = %d", got, want)
	}
	if providerObservationMaxRevisionBytes > providerObservationMaxInventoryBytes {
		t.Fatalf("legal single-revision estimate = %d exceeds inventory budget = %d", providerObservationMaxRevisionBytes, providerObservationMaxInventoryBytes)
	}
	for _, count := range []int{4096, 4097, manageddata.MaxManifestFiles} {
		if count > providerObservationMaxRevisionFiles {
			t.Fatalf("admitted file count %d is not capturable", count)
		}
	}
	for _, total := range []int{16384, manageddata.MaxManifestFiles * 2} {
		if total > providerObservationMaxObjects {
			t.Fatalf("aggregate admitted file count %d is not capturable", total)
		}
	}
	if providerObservationMaxRevisionFiles >= manageddata.MaxManifestFiles+1 {
		t.Fatalf("capture bound unexpectedly exceeds hard admission bound: %d", providerObservationMaxRevisionFiles)
	}
}

func TestProviderObservationInventoryBudgetCoversAdmittedRevisionBoundary(t *testing.T) {
	base := capturedProjectionRevision{
		ProjectID: "project", CollectionID: "collection", RevisionID: "revision",
		ManifestDigest: "sha256:" + strings.Repeat("a", 64),
	}
	for _, count := range []int{4096, 4097, manageddata.MaxManifestFiles} {
		revision := base
		revision.Files = make([]capturedProjectionFile, count)
		for index := range revision.Files {
			revision.Files[index] = capturedProjectionFile{
				Path:       "file-" + strings.Repeat("p", 100) + "/" + strings.Repeat("x", 4) + string(rune('a'+index%26)),
				SHA256:     strings.Repeat("b", 64),
				StorageKey: "s3://bucket/" + strings.Repeat("k", 80),
			}
		}
		// The helper is conservative by design, so this test uses the same
		// estimate as the capture guard and proves the former 4,096/8 MiB
		// boundary no longer rejects an admitted 10,000-file revision.
		if got := providerObservationRevisionBytes(revision); got > providerObservationMaxInventoryBytes {
			t.Fatalf("file count %d estimated inventory bytes = %d, budget = %d", count, got, providerObservationMaxInventoryBytes)
		}
	}
}

func TestProviderObservationRevisionBoundIncludesOwnershipIdentity(t *testing.T) {
	base := capturedProjectionRevision{ProjectID: "project", CollectionID: "collection", RevisionID: "revision", ManifestDigest: "sha256:" + strings.Repeat("a", 64)}
	expanded := base
	expanded.ProjectID += strings.Repeat("p", 128)
	expanded.CollectionID += strings.Repeat("c", 128)

	if got, want := providerObservationRevisionBytes(expanded)-providerObservationRevisionBytes(base), 6*(128+128); got != want {
		t.Fatalf("ownership identity byte bound increase = %d, want %d", got, want)
	}
}
