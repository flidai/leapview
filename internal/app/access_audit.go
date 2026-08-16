package app

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
)

// accessRepository resolves the repository port exposed by the access module's
// canonical HTTP surface. Composition uses this narrow port for cross-module
// audit sinks without reaching into the module's implementation state.
func accessRepository(module *accessmodule.Module) (access.Repository, error) {
	if module == nil {
		return nil, fmt.Errorf("access module is unavailable")
	}
	provider := module.HTTP().Repository
	if provider == nil {
		return nil, fmt.Errorf("access repository is unavailable")
	}
	repository, err := provider()
	if err != nil {
		return nil, err
	}
	if repository == nil {
		return nil, fmt.Errorf("access repository is unavailable")
	}
	return repository, nil
}

// authorizationSnapshotInstaller resolves the access persistence port needed
// by runtimehost during generation publication. The public access.Repository
// contract intentionally does not expose generation snapshot installation, so
// composition must verify that the concrete persistence adapter supports the
// runtime-owned installer contract before serving starts.
func authorizationSnapshotInstaller(repository access.Repository) (runtimehostmodule.AuthorizationSnapshotInstaller, error) {
	if repository == nil {
		return nil, fmt.Errorf("access repository is unavailable")
	}
	installer, ok := repository.(runtimehostmodule.AuthorizationSnapshotInstaller)
	if !ok {
		return nil, fmt.Errorf("access repository does not support authorization snapshot installation")
	}
	return installer, nil
}

func recordAccessAudit(ctx context.Context, module *accessmodule.Module, input access.AuditEventInput) error {
	repository, err := accessRepository(module)
	if err != nil {
		return err
	}
	return repository.RecordAuditEvent(ctx, input)
}

func accessAuditRecorder(module *accessmodule.Module) func(context.Context, access.AuditEventInput) error {
	return func(ctx context.Context, input access.AuditEventInput) error {
		return recordAccessAudit(ctx, module, input)
	}
}
