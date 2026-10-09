package app

import (
	"context"

	credentialmodule "github.com/flidai/leapview/internal/credential/module"
)

func (a *sourceCredentialActivation) installRuntime(ctx context.Context, record credentialmodule.ActivationRecord) error {
	return a.withRuntimeAdmission(ctx, func(ctx context.Context) error { return a.config.Install(ctx, record) })
}

// Reconstructing a sealed runtime discovers snapshot schemas in DuckDB. Hold
// the configured deployment preparation lease through reconstruction and its
// synchronous cleanup, while the credential coordinator keeps providers closed.
func (a *sourceCredentialActivation) withRuntimeAdmission(ctx context.Context, run func(context.Context) error) error {
	if a.config.CandidateAdmission == nil || run == nil {
		return credentialmodule.ErrValidationUnavailable
	}
	lease, err := a.config.CandidateAdmission.AcquireCandidatePreparation(ctx)
	if err != nil {
		return err
	}
	if lease == nil {
		return credentialmodule.ErrValidationUnavailable
	}
	defer lease.Release()
	admitted := lease.Context()
	if admitted == nil {
		return credentialmodule.ErrValidationUnavailable
	}
	return run(admitted)
}
