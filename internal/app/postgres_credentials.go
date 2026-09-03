package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/gcadapter"
	"github.com/flidai/leapview/internal/app/postgresducklake"
	"github.com/flidai/leapview/internal/extension"
)

// Production DuckDB secret names are process-owned and deliberately never
// persisted in serving roots.
const (
	postgresDuckLakeSecret   = postgresducklake.DuckLakeSecret
	postgresConnectionSecret = postgresducklake.PostgresSecret
)

func newPostgresDuckLakeCredentialBootstrapFactory(cfg config.Config, extensionAdmission extension.Admission) func(context.Context, *ducklake.PoolContract) (ducklake.CredentialBootstrap, error) {
	return func(ctx context.Context, contract *ducklake.PoolContract) (ducklake.CredentialBootstrap, error) {
		return newPostgresDuckLakeCredentialBootstrap(cfg, contract, extensionAdmission)
	}
}

func newPostgresDuckLakeCredentialBootstrap(cfg config.Config, contract *ducklake.PoolContract, extensionAdmission extension.Admission) (ducklake.CredentialBootstrap, error) {
	return postgresducklake.NewCredentialBootstrap(postgresducklake.CredentialConfig{
		PostgresURL:        cfg.PostgresDuckLakeURL,
		Contract:           contract,
		ExtensionAdmission: extensionAdmission,
		S3:                 postgresPoolS3Config(cfg, extensionAdmission),
	})
}

// postgresPoolS3Config is the single production projection of target-owned
// S3 credentials and encryption-key resolution used by admitted physical
// pools. Pool identity (bucket, prefix, and opaque encryption reference)
// remains authoritative in the validated PoolContract.
func postgresPoolS3Config(cfg config.Config, extensionAdmission extension.Admission) gcadapter.S3Config {
	return gcadapter.S3Config{
		Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID,
		SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken,
		Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle,
		ExtensionAdmission: extensionAdmission,
		ResolveEncryptionKey: func(_ context.Context, reference string) (string, error) {
			expected := strings.TrimSpace(cfg.ObjectStoreS3EncryptionKeyRef)
			providerKey := strings.TrimSpace(cfg.ObjectStoreS3EncryptionProviderKey)
			if expected == "" || reference != expected || providerKey == "" {
				return "", fmt.Errorf("physical-pool encryption reference %q is not configured", reference)
			}
			return providerKey, nil
		},
	}
}
