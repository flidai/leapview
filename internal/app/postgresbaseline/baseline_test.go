package postgresbaseline

import (
	"context"
	"errors"
	"strings"
	"testing"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

type revisionReader struct {
	revisions map[int64]platformpostgres.SchemaRevision
	err       error
}

func (r revisionReader) SchemaRevision(_ context.Context, revision int64) (platformpostgres.SchemaRevision, error) {
	if r.err != nil {
		return platformpostgres.SchemaRevision{}, r.err
	}
	value, ok := r.revisions[revision]
	if !ok {
		return platformpostgres.SchemaRevision{}, errors.New("revision not found")
	}
	return value, nil
}

func TestBaselineOwnsOnlyReconciledCapabilities(t *testing.T) {
	components := Components()
	if len(components) != 4 || components[0].Name != "platform.bootstrap" || components[1].Name != "access" || components[2].Name != "physical_pool" || components[3].Name != "ducklake.bootstrap" {
		t.Fatalf("components = %#v, want bootstrap, access, physical pool, then DuckLake bootstrap", components)
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
	migration := Migrations()[0]
	revisions := map[int64]platformpostgres.SchemaRevision{
		BaselineRevision:   {Revision: BaselineRevision, MigrationID: BaselineMigrationID, Checksum: Checksum()},
		migration.Revision: {Revision: migration.Revision, MigrationID: migration.MigrationID, Checksum: migration.Checksum()},
	}
	if err := Verify(context.Background(), revisionReader{revisions: revisions}); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	bad := make(map[int64]platformpostgres.SchemaRevision, len(revisions))
	for key, value := range revisions {
		bad[key] = value
	}
	value := bad[migration.Revision]
	value.Checksum = strings.Repeat("f", 64)
	bad[migration.Revision] = value
	if err := Verify(context.Background(), revisionReader{revisions: bad}); err == nil {
		t.Fatal("Verify() accepted a mismatched checksum")
	}
	if err := Verify(context.Background(), nil); err == nil {
		t.Fatal("Verify(nil) unexpectedly succeeded")
	}
}
