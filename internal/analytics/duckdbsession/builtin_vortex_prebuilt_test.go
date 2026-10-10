//go:build leapview_static_vortex && !duckdb_use_static_lib

package duckdbsession

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/extension"
)

func TestCompiledVortexDescriptorsRejectPrebuiltEngine(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, name := range []string{"vortex"} {
		builtin, ok := extension.CompiledBuiltin(name, "linux_amd64")
		if !ok {
			t.Fatalf("missing compiled %s descriptor", name)
		}
		identity := extension.Identity{Builtin: true, Name: name, DuckDBVersion: builtin.DuckDBVersion,
			ExtensionVersion: builtin.SourceRevision, GOOS: "linux", GOARCH: "amd64", Platform: builtin.Platform,
			Digest: builtin.Digest(), SupportProfile: "test"}
		if err := VerifyCompiledBuiltin(context.Background(), db, identity); !errors.Is(err, extension.ErrExtensionIntegrity) {
			t.Fatalf("%s descriptor accepted by a prebuilt engine: %v", name, err)
		}
	}
}
