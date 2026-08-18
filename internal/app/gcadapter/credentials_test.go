package gcadapter

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

type recordingExecer struct{ statements []string }

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

func TestNewPoolCredentialBootstrapS3IsPerConnection(t *testing.T) {
	contract := &ducklake.PoolContract{Pool: physicalpool.PhysicalPool{Identity: physicalpool.PoolIdentity{StorageLocation: "s3://bucket/prefix"}}, Tuple: physicalpool.Compatibility{StorageImplementation: "s3"}}
	bootstrap, err := NewPoolCredentialBootstrap(contract, S3Config{AccessKeyID: "key", SecretAccessKey: "secret", Endpoint: "http://minio:9000", PathStyle: true})
	if err != nil || bootstrap == nil {
		t.Fatalf("S3 bootstrap = %v/%v", bootstrap, err)
	}
	execer := &recordingExecer{}
	if err := bootstrap(context.Background(), execer); err != nil {
		t.Fatal(err)
	}
	if len(execer.statements) != 3 || execer.statements[0] != "INSTALL httpfs FROM core" || execer.statements[1] != "LOAD httpfs" || !strings.Contains(execer.statements[2], "CREATE OR REPLACE SECRET") || !strings.Contains(execer.statements[2], "KEY_ID 'key'") {
		t.Fatalf("bootstrap statements = %#v", execer.statements)
	}
}
