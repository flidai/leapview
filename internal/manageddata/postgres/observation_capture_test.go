package postgres

import (
	"strings"
	"testing"
)

func TestProviderObservationRevisionBoundIncludesOwnershipIdentity(t *testing.T) {
	base := capturedProjectionRevision{ProjectID: "project", CollectionID: "collection", RevisionID: "revision", ManifestDigest: "sha256:" + strings.Repeat("a", 64)}
	expanded := base
	expanded.ProjectID += strings.Repeat("p", 128)
	expanded.CollectionID += strings.Repeat("c", 128)

	if got, want := providerObservationRevisionBytes(expanded)-providerObservationRevisionBytes(base), 6*(128+128); got != want {
		t.Fatalf("ownership identity byte bound increase = %d, want %d", got, want)
	}
}
