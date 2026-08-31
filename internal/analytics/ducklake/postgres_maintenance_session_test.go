package ducklake

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/extension"
)

type maintenanceBootstrapAdmission struct{}

func (maintenanceBootstrapAdmission) AdmitExtension(context.Context, string) (extension.AdmittedExtension, error) {
	return extension.AdmittedExtension{
		Name: "postgres", Identity: "fixture/postgres", Version: "v1", Platform: "linux_amd64",
		Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Path:   "/opt/leapview/extensions/postgres_scanner.duckdb_extension",
	}, nil
}

type maintenanceBootstrapExecer struct{ statements []string }

func (e *maintenanceBootstrapExecer) ExecContext(_ context.Context, statement string, _ []driver.NamedValue) (driver.Result, error) {
	e.statements = append(e.statements, statement)
	return driver.RowsAffected(0), nil
}

func TestParseMaintenanceURLSupportsReviewedTLSOptions(t *testing.T) {
	parsed, err := parseMaintenanceURL("postgres://maintenance:secret@catalog.example:5433/ducklake?sslmode=verify-full&sslrootcert=%2Fetc%2Fssl%2Fca.pem&sslcert=%2Fetc%2Fssl%2Fclient.crt&sslkey=%2Fetc%2Fssl%2Fclient.key", "maintenance")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SSLRootCert != "/etc/ssl/ca.pem" || parsed.SSLCert != "/etc/ssl/client.crt" || parsed.SSLKey != "/etc/ssl/client.key" {
		t.Fatalf("TLS options = %#v", parsed)
	}
	statement := postgresSecretStatement("pg_maintenance", parsed)
	for _, want := range []string{
		"SSLMODE 'verify-full'", "SSLROOTCERT '/etc/ssl/ca.pem'",
		"SSLCERT '/etc/ssl/client.crt'", "SSLKEY '/etc/ssl/client.key'",
	} {
		if !strings.Contains(statement, want) {
			t.Fatalf("secret statement missing %q: %s", want, statement)
		}
	}
}

func TestParseMaintenanceURLRejectsUnreviewedTLSOptions(t *testing.T) {
	for _, raw := range []string{
		"postgres://maintenance:secret@catalog/ducklake?sslmode=require&application_name=leak",
		"postgres://maintenance:secret@catalog/ducklake?sslmode=require&sslrootcert=/a&sslrootcert=/b",
	} {
		if _, err := parseMaintenanceURL(raw, "maintenance"); !errors.Is(err, ErrPostgresCatalogMaintenanceURL) {
			t.Fatalf("URL %q error = %v", raw, err)
		}
	}
}

func TestOpenPostgresCatalogMaintenanceSessionFailsBeforeConnectorOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := OpenPostgresCatalogMaintenanceSession(ctx, PostgresCatalogMaintenanceSessionConfig{})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrPostgresCatalogMaintenanceSession) {
		t.Fatalf("canceled session error = %v", err)
	}
}

func TestMaintenanceSessionRequiresAndAppliesBoundedResourcePolicy(t *testing.T) {
	base := PostgresCatalogMaintenanceSessionConfig{
		Catalog: PostgresCatalogConfig{
			DuckLakeSecret: "lake_maintenance", PostgresSecret: "pg_maintenance",
			MetadataSchema: "catalog_metadata", Mode: PostgresCatalogWriter,
		},
		PostgresURL: "postgres://leapview_ducklake_maintenance:secret@catalog/ducklake?sslmode=require",
		DataPath:    "s3://bucket/lake",
	}
	if err := base.Validate(); !errors.Is(err, ErrPostgresCatalogMaintenanceSession) {
		t.Fatalf("zero resource policy error = %v", err)
	}
	base.MemoryMaxBytes = 256 << 20
	base.TempMaxBytes = 512 << 20
	base.MaxThreads = 2
	base.TempDir = "/tmp/leapview-maintenance"
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	statements := maintenanceResourceStatements(base)
	joined := strings.Join(statements, "\n")
	for _, want := range []string{
		"SET allow_persistent_secrets = false",
		"SET autoinstall_known_extensions = false",
		"SET autoload_known_extensions = false",
		"SET memory_limit = '268435456B'",
		"SET max_temp_directory_size = '536870912B'",
		"SET threads = 2",
		"SET temp_directory = '/tmp/leapview-maintenance'",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("resource policy missing %q: %s", want, joined)
		}
	}
	allowed, err := maintenanceAllowedDirectories(base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(allowed, "SET allowed_directories = ['s3://bucket/lake'") || !strings.Contains(allowed, "/tmp/leapview-maintenance") {
		t.Fatalf("allowed-directories policy = %q", allowed)
	}
}

func TestMaintenanceCredentialBootstrapOnlyRequiresIdentity(t *testing.T) {
	c := PostgresCatalogMaintenanceSessionConfig{
		Catalog:            PostgresCatalogConfig{DuckLakeSecret: "lake", PostgresSecret: "pg", MetadataSchema: "metadata", Mode: PostgresCatalogWriter},
		PostgresURL:        "postgres://leapview_ducklake_maintenance:secret@catalog/ducklake?sslmode=require",
		ExtensionAdmission: maintenanceBootstrapAdmission{},
	}
	bootstrap, err := PostgresCatalogMaintenanceCredentialBootstrap(c)
	if err != nil {
		t.Fatal(err)
	}
	execer := &maintenanceBootstrapExecer{}
	if err := bootstrap(context.Background(), execer); err != nil {
		t.Fatal(err)
	}
	if len(execer.statements) != 2 || !strings.Contains(execer.statements[1], "TEMPORARY SECRET") {
		t.Fatalf("bootstrap statements = %#v", execer.statements)
	}
}
