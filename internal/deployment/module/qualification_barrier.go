package module

import (
	"context"

	"github.com/flidai/leapview/internal/deployment/qualificationbarrier"
)

// WaitBeforeQualificationActivation exposes the inert evaluation hook through
// Deployment's composition surface. Native PostgreSQL activation is unchanged
// unless the exact evaluation marker has been armed by the qualification
// harness.
func WaitBeforeQualificationActivation(ctx context.Context, environment string) error {
	return qualificationbarrier.WaitBeforeActivation(ctx, environment)
}
