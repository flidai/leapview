package gcadapter

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/extension"
)

type recordingExecer struct{ statements []string }

type staticExtensionAdmission struct{}

func (staticExtensionAdmission) AdmitExtension(context.Context, string) (extension.AdmittedExtension, error) {
	return extension.AdmittedExtension{Name: "httpfs", Identity: "fixture/httpfs", Version: "fixture", Path: "/opt/leapview/extensions/httpfs.duckdb_extension", Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}, nil
}

func (execer *recordingExecer) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	execer.statements = append(execer.statements, query)
	return driver.RowsAffected(0), nil
}

func TestNewPoolCredentialBootstrapLocalNeedsNone(t *testing.T) {
	bootstrap, err := NewPoolCredentialBootstrap(&ducklake.PoolContract{Tuple: physicalpool.Compatibility{StorageImplementation: "local"}}, S3Config{})
	if err != nil || bootstrap != nil {
		t.Fatalf("local bootstrap = %v/%v, want nil", bootstrap, err)
	}
}

func TestNewPoolCredentialBootstrapS3RequiresTargetKeys(t *testing.T) {
	contract := &ducklake.PoolContract{Pool: physicalpool.PhysicalPool{Identity: physicalpool.PoolIdentity{StorageLocation: "s3://bucket/prefix"}}, Tuple: physicalpool.Compatibility{StorageImplementation: "s3"}}
	if _, err := NewPoolCredentialBootstrap(contract, S3Config{}); err == nil || !strings.Contains(err.Error(), "target-owned S3 access") {
		t.Fatalf("missing S3 credentials error = %v", err)
	}
}

func TestNewPoolStoreS3RequiresTargetKeysBeforeAWSConfig(t *testing.T) {
	contract := &ducklake.PoolContract{Pool: physicalpool.PhysicalPool{Identity: physicalpool.PoolIdentity{StorageLocation: "s3://bucket/prefix", StorageNamespace: "delivery"}}, Tuple: physicalpool.Compatibility{StorageImplementation: "s3"}}
	if _, err := NewPoolStore(context.Background(), contract, S3Config{}); err == nil || !strings.Contains(err.Error(), "target-owned S3 access") {
		t.Fatalf("missing S3 credentials error = %v", err)
	}
}

func TestNewPoolStoreS3ResolvesAdmittedEncryptionKey(t *testing.T) {
	contract := &ducklake.PoolContract{Pool: physicalpool.PhysicalPool{Identity: physicalpool.PoolIdentity{StorageLocation: "s3://bucket/prefix", StorageNamespace: "delivery", EncryptionKeyRef: "opaque-epoch-1"}}, Tuple: physicalpool.Compatibility{StorageImplementation: "s3"}}
	var resolved string
	_, err := NewPoolStore(context.Background(), contract, S3Config{AccessKeyID: "key", SecretAccessKey: "secret", Endpoint: "http://minio:9000", ResolveEncryptionKey: func(_ context.Context, reference string) (string, error) {
		resolved = reference
		return "arn:aws:kms:us-east-1:123:key/real", nil
	}})
	if err != nil {
		t.Fatalf("encrypted pool store = %v", err)
	}
	if resolved != "opaque-epoch-1" {
		t.Fatalf("resolver reference = %q", resolved)
	}
}

func TestNewPoolStoreS3EncryptionResolverFailsClosed(t *testing.T) {
	contract := &ducklake.PoolContract{Pool: physicalpool.PhysicalPool{Identity: physicalpool.PoolIdentity{StorageLocation: "s3://bucket/prefix", StorageNamespace: "delivery", EncryptionKeyRef: "opaque-epoch-1"}}, Tuple: physicalpool.Compatibility{StorageImplementation: "s3"}}
	if _, err := NewPoolStore(context.Background(), contract, S3Config{AccessKeyID: "key", SecretAccessKey: "secret", ResolveEncryptionKey: func(context.Context, string) (string, error) {
		return "", errors.New("resolver unavailable")
	}}); err == nil || !strings.Contains(err.Error(), "resolve target-owned S3 encryption key") {
		t.Fatalf("resolver error = %v", err)
	}
}

func TestNewPoolCredentialBootstrapS3IsPerConnection(t *testing.T) {
	contract := &ducklake.PoolContract{Pool: physicalpool.PhysicalPool{Identity: physicalpool.PoolIdentity{StorageLocation: "s3://bucket/prefix"}}, Tuple: physicalpool.Compatibility{StorageImplementation: "s3"}}
	bootstrap, err := NewPoolCredentialBootstrap(contract, S3Config{AccessKeyID: "key", SecretAccessKey: "secret", Endpoint: "http://minio:9000", PathStyle: true, ExtensionAdmission: staticExtensionAdmission{}})
	if err != nil || bootstrap == nil {
		t.Fatalf("S3 bootstrap = %v/%v", bootstrap, err)
	}
	execer := &recordingExecer{}
	if err := bootstrap(context.Background(), execer); err != nil {
		t.Fatal(err)
	}
	if len(execer.statements) != 2 || execer.statements[0] != "LOAD '/opt/leapview/extensions/httpfs.duckdb_extension'" || !strings.Contains(execer.statements[1], "CREATE OR REPLACE SECRET") || !strings.Contains(execer.statements[1], "KEY_ID 'key'") {
		t.Fatalf("bootstrap statements = %#v", execer.statements)
	}
}
