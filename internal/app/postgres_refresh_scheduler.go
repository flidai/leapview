package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
)

// postgresScheduledExecutionGrantIDResolver binds each claimed occurrence to
// an explicit current-grant selection in the PostgreSQL access authority. A
// missing selector returns nil so native refresh Build fails closed before the
// scheduler can start; it never falls back to a scheduler or worker identity.
func postgresScheduledExecutionGrantIDResolver(repository any, instanceID string) func(context.Context, refreshmodule.ScheduledOccurrence) (string, error) {
	selector, ok := repository.(refreshmodule.ExecutionGrantSelector)
	if !ok {
		return nil
	}
	return func(ctx context.Context, occurrence refreshmodule.ScheduledOccurrence) (string, error) {
		if err := refreshmodule.ValidateScheduledOccurrence(occurrence); err != nil {
			return "", fmt.Errorf("scheduled refresh occurrence identity: %w", err)
		}
		if err := occurrence.PipelineID.Validate(); err != nil {
			return "", fmt.Errorf("scheduled refresh occurrence pipeline: %w", err)
		}
		grantID, err := selector.SelectCurrentExecutionGrantID(ctx, instanceID, occurrence.Identity.ProjectID, occurrence.PipelineID)
		if err != nil {
			return "", fmt.Errorf("select scheduled refresh execution grant: %w", err)
		}
		if grantID == "" || grantID != strings.TrimSpace(grantID) {
			return "", errors.New("selected scheduled refresh execution grant ID is invalid")
		}
		return grantID, nil
	}
}
