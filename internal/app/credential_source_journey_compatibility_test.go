package app

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	extensionfixture "github.com/flidai/leapview/internal/app/testing/extensionfixture"
	"github.com/flidai/leapview/internal/workload"
)

// Admit the actual pinned fixture runtime, including its native catalog format,
// rather than an assumed extension version. The full conformance run below
// still proves this tuple before the PostgreSQL pool is bootstrapped.
func sourceJourneyCompatibility(t *testing.T, extensions extensionfixture.Fixture) physicalpool.Compatibility {
	t.Helper()
	environment, err := ducklake.Open(t.Context(), ducklake.Config{RootDir: t.TempDir(), ExtensionAdmission: extensions.Admission})
	if err != nil {
		t.Fatal(err)
	}
	defer environment.Close()
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	admission, err := controller.Acquire(t.Context(), workload.Request{Class: workload.Maintenance, PrincipalID: "source-fixture", Operation: "read-runtime-compatibility", EstimatedMemoryBytes: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Release()
	lease, err := environment.Acquire(admission.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	session, err := environment.Session(lease.Context())
	if err != nil {
		t.Fatal(err)
	}
	var runtime, extension, catalog string
	if err := session.QueryRowContext(lease.Context(), "SELECT version(), extension_version FROM lake.settings() LIMIT 1").Scan(&runtime, &extension); err != nil {
		t.Fatal(err)
	}
	if err := session.QueryRowContext(lease.Context(), "SELECT CAST(value AS VARCHAR) FROM lake.options() WHERE lower(option_name)='version' AND upper(scope)='GLOBAL' LIMIT 1").Scan(&catalog); err != nil {
		t.Fatal(err)
	}
	return physicalpool.Compatibility{DuckDBRuntime: "duckdb:" + strings.TrimPrefix(runtime, "v"), DuckLakeExtension: "ducklake:" + strings.TrimPrefix(extension, "v"), CatalogFormat: "ducklake:" + catalog, StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1"}
}
