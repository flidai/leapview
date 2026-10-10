package extensionfixture

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/flidai/leapview/internal/analytics/duckdbsession"
	"github.com/flidai/leapview/internal/extension"
)

// compiledFixture requires the engine's actual static registration before
// staging its descriptor. Build tags alone never turn a downloaded fixture into
// an admitted builtin or permit a descriptor to be passed to file-backed LOAD.
func compiledFixture(t testing.TB, name, version, platform, root string) (extension.AdmittedExtension, bool) {
	t.Helper()
	builtin, ok := extension.CompiledBuiltin(name, platform)
	if !ok {
		return extension.AdmittedExtension{}, false
	}
	identity := extension.Identity{Builtin: true, Name: name, DuckDBVersion: version,
		ExtensionVersion: builtin.SourceRevision, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Platform: platform, Digest: builtin.Digest(), SupportProfile: "test-fixture"}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := duckdbsession.VerifyCompiledBuiltin(context.Background(), db, identity); err != nil {
		t.Fatalf("verify compiled fixture %s: %v", name, err)
	}
	if name == "postgres" || name == "mysql" || name == "excel" || name == "avro" || name == "delta" {
		var revision string
		if err := db.QueryRow("SELECT extension_version FROM duckdb_extensions() WHERE extension_name = ?", extension.ArtifactFilenameStem(name)).Scan(&revision); err != nil || revision != builtin.SourceRevision {
			t.Fatalf("compiled %s source revision %q differs: %v", name, revision, err)
		}
	}
	canonical, err := identity.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	stem := extension.ArtifactFilenameStem(name)
	path := filepath.Join(root, stem+".duckdb_extension")
	for _, target := range []string{path, filepath.Join(root, stem+"-"+builtin.SourceRevision+"-"+platform+".duckdb_extension")} {
		if err := os.WriteFile(target, builtin.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return extension.AdmittedExtension{Builtin: true, Name: name, Identity: canonical,
		Version: builtin.SourceRevision, ExtensionVersion: builtin.SourceRevision, DuckDBVersion: version,
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Platform: platform, SupportProfile: "test-fixture",
		Digest: builtin.Digest(), Path: path, Origin: "reviewed-local-test-fixture",
		Provenance: builtin.Provenance(), Signature: "package:compiled-engine"}, true
}
