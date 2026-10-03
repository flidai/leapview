package app

import (
	"context"
	"errors"

	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

var errRefreshLocalCredentialUnsupported = errors.New("local credential pins are not supported by refresh candidate acquisition")

// refreshBaseCredentialCheck checks immutable evidence without decrypting any
// credential. Provider-only refresh cannot preserve a local pin in its successor.
func refreshBaseCredentialCheck(source activeConnectionEvidenceSource) func(context.Context, refreshrun.JobRecord) error {
	return func(ctx context.Context, job refreshrun.JobRecord) error {
		if ctx == nil || job.Identity.Environment != source.environment {
			return errors.New("refresh base credential scope invalid")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := job.Identity.Validate(); err != nil {
			return err
		}
		evidence, err := source.BindingEvidence(ctx, job.Identity.GenerationID, job.Identity.ProjectID.String())
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("refresh base credential evidence unavailable")
		}
		for _, binding := range evidence {
			if binding.CredentialVersionID != "" {
				return errRefreshLocalCredentialUnsupported
			}
		}
		return ctx.Err()
	}
}
