package app

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/extension"
)

type postgresCredentialExecer struct{ statements []string }

func (e *postgresCredentialExecer) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	e.statements = append(e.statements, query)
	return driver.RowsAffected(0), nil
}

type postgresCredentialAdmission struct{}

func (postgresCredentialAdmission) AdmitExtension(_ context.Context, name string) (extension.AdmittedExtension, error) {
	return extension.AdmittedExtension{Name: name, Identity: "fixture/" + name, Version: "v1", Platform: "linux_amd64", Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000", Path: "/opt/leapview/extensions/" + extension.ArtifactFilenameStem(name) + ".duckdb_extension"}, nil
}

type invalidPostgresCredentialAdmission struct {
	extension.AdmittedExtension
}

func (a invalidPostgresCredentialAdmission) AdmitExtension(context.Context, string) (extension.AdmittedExtension, error) {
	return a.AdmittedExtension, nil
}

func TestPostgresCredentialBootstrapValidatesTLSAndLoadsAdmittedScanner(t *testing.T) {
	cfg := config.Config{PostgresDuckLakeURL: "postgres://user:p%27ass@db.example/ducklake?sslmode=require"}
	bootstrap, err := newPostgresDuckLakeCredentialBootstrap(cfg, nil, postgresCredentialAdmission{})
	if err != nil {
		t.Fatal(err)
	}
	execer := &postgresCredentialExecer{}
	if err := bootstrap(context.Background(), execer); err != nil {
		t.Fatal(err)
	}
	if len(execer.statements) != 2 || strings.Contains(strings.Join(execer.statements, "\n"), "INSTALL") {
		t.Fatalf("unexpected bootstrap statements: %#v", execer.statements)
	}
	if !strings.HasPrefix(execer.statements[0], "LOAD '/opt/leapview/extensions/postgres_scanner.duckdb_extension'") {
		t.Fatalf("scanner statement = %q", execer.statements[0])
	}
	if !strings.Contains(execer.statements[1], "PASSWORD 'p''ass'") || !strings.Contains(execer.statements[1], "SSLMODE 'require'") {
		t.Fatalf("secret statement did not escape credentials: %q", execer.statements[1])
	}
}

func TestPostgresCredentialBootstrapRejectsUnsafeURLOptions(t *testing.T) {
	for _, raw := range []string{
		"postgres://user:secret@db/ducklake?sslmode=disable",
		"postgres://user:secret@db/ducklake?application_name=leak",
		"postgres://user:secret@db/a/b",
		"postgres://user:secret@db/ducklake#fragment",
	} {
		if _, err := newPostgresDuckLakeCredentialBootstrap(config.Config{PostgresDuckLakeURL: raw}, nil, postgresCredentialAdmission{}); err == nil {
			t.Fatalf("URL %q unexpectedly accepted", raw)
		}
	}
}

func TestPostgresCredentialBootstrapAllowsLoopbackDevelopmentTLSDisable(t *testing.T) {
	bootstrap, err := newPostgresDuckLakeCredentialBootstrap(config.Config{PostgresDuckLakeURL: "postgres://user:secret@127.0.0.1:55432/ducklake?sslmode=disable"}, nil, postgresCredentialAdmission{})
	if err != nil {
		t.Fatal(err)
	}
	execer := &postgresCredentialExecer{}
	if err := bootstrap(context.Background(), execer); err != nil {
		t.Fatal(err)
	}
	if len(execer.statements) != 2 || !strings.Contains(execer.statements[1], "SSLMODE 'disable'") {
		t.Fatalf("loopback development bootstrap statements = %#v", execer.statements)
	}
}

func TestPostgresCredentialBootstrapProductionRejectsLoopbackTLSDisable(t *testing.T) {
	_, err := newPostgresDuckLakeCredentialBootstrap(config.Config{
		Production:          true,
		PostgresDuckLakeURL: "postgres://user:secret@127.0.0.1:55432/ducklake?sslmode=disable",
	}, nil, postgresCredentialAdmission{})
	if err == nil || !strings.Contains(err.Error(), "requires TLS") {
		t.Fatalf("production loopback plaintext credential unexpectedly accepted: %v", err)
	}
}

func TestPostgresCredentialBootstrapRejectsUntrustedScannerAdmission(t *testing.T) {
	base := extension.AdmittedExtension{Name: "postgres", Identity: "fixture/postgres", Version: "v1", Platform: "linux_amd64", Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000", Path: "/opt/leapview/extensions/postgres_scanner.duckdb_extension"}
	for _, mutate := range []func(*extension.AdmittedExtension){
		func(v *extension.AdmittedExtension) {
			v.Path = "/tmp/postgres_scanner/../postgres_scanner.duckdb_extension"
		},
		func(v *extension.AdmittedExtension) {
			v.Path = "/opt/leapview/extensions/not-postgres.duckdb_extension"
		},
		func(v *extension.AdmittedExtension) { v.Digest = "sha256:not-a-digest" },
		func(v *extension.AdmittedExtension) { v.Identity = "" },
	} {
		candidate := base
		mutate(&candidate)
		bootstrap, err := newPostgresDuckLakeCredentialBootstrap(config.Config{PostgresDuckLakeURL: "postgres://user:secret@db/ducklake"}, nil, invalidPostgresCredentialAdmission{AdmittedExtension: candidate})
		if err != nil {
			t.Fatal(err)
		}
		if err := bootstrap(context.Background(), &postgresCredentialExecer{}); err == nil {
			t.Fatalf("invalid scanner admission %#v unexpectedly accepted", candidate)
		}
	}
}

func TestPostgresCredentialBootstrapChainsS3Bootstrap(t *testing.T) {
	contract := &ducklake.PoolContract{Pool: physicalpool.PhysicalPool{Identity: physicalpool.PoolIdentity{StorageLocation: "s3://bucket/prefix"}}, Tuple: physicalpool.Compatibility{StorageImplementation: "s3"}}
	cfg := config.Config{PostgresDuckLakeURL: "postgres://user:secret@db/ducklake", ManagedDataS3AccessKeyID: "key", ManagedDataS3SecretAccessKey: "secret", ManagedDataS3Endpoint: "http://minio:9000"}
	bootstrap, err := newPostgresDuckLakeCredentialBootstrap(cfg, contract, postgresCredentialAdmission{})
	if err != nil {
		t.Fatal(err)
	}
	execer := &postgresCredentialExecer{}
	if err := bootstrap(context.Background(), execer); err != nil {
		t.Fatal(err)
	}
	if len(execer.statements) != 4 || !strings.Contains(execer.statements[3], "TYPE S3") || !strings.Contains(execer.statements[3], "KEY_ID 'key'") {
		t.Fatalf("S3 bootstrap statements = %#v", execer.statements)
	}
}

func TestPostgresPoolS3ConfigResolvesOnlyAdmittedEncryptionReference(t *testing.T) {
	cfg := config.Config{
		ObjectStoreS3EncryptionKeyRef:      "logical-key",
		ObjectStoreS3EncryptionProviderKey: "arn:aws:kms:eu-west-1:123:key/provider",
	}
	storage := postgresPoolS3Config(cfg, postgresCredentialAdmission{})
	resolved, err := storage.ResolveEncryptionKey(t.Context(), "logical-key")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != cfg.ObjectStoreS3EncryptionProviderKey {
		t.Fatalf("resolved encryption key = %q", resolved)
	}
	for _, reference := range []string{"", "other-key"} {
		if _, err := storage.ResolveEncryptionKey(t.Context(), reference); err == nil {
			t.Fatalf("encryption reference %q unexpectedly resolved", reference)
		}
	}
}
