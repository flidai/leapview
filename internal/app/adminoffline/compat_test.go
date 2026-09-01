package adminoffline

import (
	"context"
	"io"
	"path/filepath"

	offline "github.com/flidai/leapview/internal/admin/offline"
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
