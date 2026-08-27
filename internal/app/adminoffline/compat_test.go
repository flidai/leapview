package adminoffline

import (
	"context"
	"io"
	"path/filepath"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	offline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/app/config"
)

type initialInstanceCredentials = offline.InitialCredentials

func runAdminInitialize(ctx context.Context, format string, out io.Writer) error {
	return (Operations{}).Initialize(ctx, offline.InitializeRequest{Format: format}, out)
}

func acknowledgeInitialCredentials(ctx context.Context) error {
	return (Operations{}).AcknowledgeInitialCredentials(ctx)
}

func initialCredentialRecoveryPath(home string) string {
	return filepath.Join(home, offline.CredentialRecoveryFileName)
}

func fullInstanceDerivedPaths(cfg config.Config) ([]string, error) {
	return offline.New(offline.Config{
		HomeDir:            cfg.HomeDir,
		ManagedDataDir:     cfg.ManagedDataDir,
		ManagedDataBackend: cfg.ManagedDataBackend,
	}, offline.Dependencies{}).FullInstanceDerivedPaths()
}

func runAdminBackup(ctx context.Context, options admincli.Options, out io.Writer) error {
	return (Operations{}).Backup(ctx, offline.BackupRequest{
		Out: options.BackupOut, DatabaseOnly: options.DatabaseOnly,
	}, out)
}

func runAdminRestore(ctx context.Context, options admincli.Options, in io.Reader, out io.Writer) error {
	return (Operations{}).Restore(ctx, offline.RestoreRequest{
		From: options.RestoreFrom, CurrentBackup: options.RestoreBefore,
		Confirm: options.ConfirmRestore, DatabaseOnly: options.DatabaseOnly, PreflightOnly: options.PreflightOnly,
	}, in, out)
}

func runAdminMaintenance(ctx context.Context, options admincli.Options, out io.Writer) error {
	return (Operations{}).Maintenance(ctx, offline.MaintenanceRequest{
		Apply: options.Apply, AuditDays: options.AuditDays, QueryDays: options.QueryDays,
		ArchivedAgentDays: options.ArchivedAgentDays, AuthStateDays: options.AuthStateDays,
	}, out)
}

func runAdminStorageCleanup(ctx context.Context, options admincli.Options, out io.Writer) error {
	return (Operations{}).StorageCleanup(ctx, offline.StorageCleanupRequest{Apply: options.Apply}, out)
}
