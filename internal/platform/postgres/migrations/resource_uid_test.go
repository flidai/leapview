package migrations

import (
	"io/fs"
	"strings"
	"testing"

	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
)

func TestResourceUIDForwardMigrationMatchesCapability(t *testing.T) {
	contents, err := fs.ReadFile(MigrationFS(), "005_resource_uid_registry.sql")
	if err != nil {
		t.Fatal(err)
	}
	want := "-- +goose Up\n-- +goose StatementBegin\nSET LOCAL ROLE leapview_control_owner;\n\n" +
		strings.TrimSpace(projectpostgres.ResourceUIDSchemaSQL()) +
		"\n\nRESET ROLE;\n-- +goose StatementEnd\n"
	if string(contents) != want {
		t.Fatal("ResourceUID migration differs from reviewed capability schema")
	}
	if CurrentRevision != 7 {
		t.Fatalf("current schema revision = %d, want 7", CurrentRevision)
	}
}
