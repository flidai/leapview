package ducklake

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/extension"
)

func TestAdmittedExtensionLoadRejectsUnregisteredBuiltin(t *testing.T) {
	a := extension.AdmittedExtension{Builtin: true, Name: "httpfs", Path: "/descriptors/httpfs.duckdb_extension", Digest: "sha256:" + strings.Repeat("0", 64)}
	if statement, err := admittedExtensionLoadStatement(a, "httpfs"); err == nil {
		t.Fatalf("unregistered builtin accepted: %q", statement)
	}
}

func TestAdmittedExtensionLoadUsesCompiledRegistration(t *testing.T) {
	for name, want := range map[string]string{"lance": "LOAD lance", "sqlite": "LOAD sqlite_scanner"} {
		t.Run(name, func(t *testing.T) {
			builtin, enabled := extension.CompiledBuiltin(name, "linux_amd64")
			if !enabled {
				t.Skip("compiled registry is disabled in this build")
			}
			a := extension.AdmittedExtension{Builtin: true, Name: name, DuckDBVersion: builtin.DuckDBVersion,
				ExtensionVersion: builtin.SourceRevision, GOOS: "linux", GOARCH: "amd64", Platform: builtin.Platform,
				Digest: builtin.Digest(), SupportProfile: "test", Path: "/descriptors/" + extension.ArtifactFilenameStem(name) + ".duckdb_extension"}
			if got, err := admittedExtensionLoadStatement(a, name); err != nil || got != want {
				t.Fatalf("compiled extension load = %q, error = %v; want %q", got, err, want)
			}
			a.Digest = "sha256:" + strings.Repeat("0", 64)
			if statement, err := admittedExtensionLoadStatement(a, name); err == nil {
				t.Fatalf("changed compiled identity accepted: %q", statement)
			}
		})
	}
}

func TestAdmittedExtensionLoadPreservesExactFile(t *testing.T) {
	a := extension.AdmittedExtension{Name: "httpfs", Path: "/artifacts/httpfs'v1.duckdb_extension", Digest: "sha256:" + strings.Repeat("0", 64)}
	if got, err := admittedExtensionLoadStatement(a, "httpfs"); err != nil || got != "LOAD '/artifacts/httpfs''v1.duckdb_extension'" {
		t.Fatalf("file extension load = %q, error = %v", got, err)
	}
}
