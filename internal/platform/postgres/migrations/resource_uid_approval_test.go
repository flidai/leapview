package migrations

import (
	"io/fs"
	"strings"
	"testing"

	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
)

func TestApprovalResourceUIDRestoreMigrationMatchesCapability(t *testing.T) {
	contents, err := fs.ReadFile(MigrationFS(), "022_approval_resource_uid_restore.sql")
	if err != nil {
		t.Fatal(err)
	}
	want := "-- +goose Up\n-- +goose StatementBegin\nSET LOCAL ROLE leapview_control_owner;\n\n" +
		strings.TrimSpace(projectpostgres.ResourceUIDApprovalSchemaSQL()) +
		"\n\nRESET ROLE;\n-- +goose StatementEnd\n"
	if string(contents) != want {
		t.Fatal("approval resource UID restore migration differs from reviewed capability schema")
	}
}
