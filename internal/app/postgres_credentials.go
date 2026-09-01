package app

import (
	"context"

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
		S3: gcadapter.S3Config{
			Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID,
			SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken,
			Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle,
			ExtensionAdmission: extensionAdmission,
		},
	})
}
