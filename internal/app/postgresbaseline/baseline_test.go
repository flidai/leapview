package postgresbaseline

import (
	"context"
	"strings"
	"testing"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

type revisionReader struct {
	revision platformpostgres.SchemaRevision
	err      error
}

func (r revisionReader) SchemaRevision(context.Context, int64) (platformpostgres.SchemaRevision, error) {
	return r.revision, r.err
}

func TestBaselineOwnsOnlyReconciledCapabilities(t *testing.T) {
	components := Components()
	if len(components) != 2 || components[0].Name != "platform.bootstrap" || components[1].Name != "access" {
		t.Fatalf("components = %#v, want bootstrap then access", components)
	}
	for _, unrelated := range []string{"jobs", "cache", "lineage", "deployment", "attribute"} {
		for _, component := range components {
			if strings.Contains(component.SQL, "CREATE SCHEMA "+unrelated) {
				t.Fatalf("component %q contains deferred capability %q", component.Name, unrelated)
			}
		}
	}
}

func TestVerifyRequiresExactBaselineIdentity(t *testing.T) {
	want := platformpostgres.SchemaRevision{Revision: BaselineRevision, MigrationID: BaselineMigrationID, Checksum: Checksum()}
	if err := Verify(context.Background(), revisionReader{revision: want}); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	want.Checksum = strings.Repeat("f", 64)
	if err := Verify(context.Background(), revisionReader{revision: want}); err == nil {
		t.Fatal("Verify() accepted a mismatched checksum")
	}
	if err := Verify(context.Background(), nil); err == nil {
		t.Fatal("Verify(nil) unexpectedly succeeded")
	}
}
