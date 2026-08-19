package module

import (
	"testing"

	"github.com/flidai/leapview/pkg/jobs"
	"github.com/stretchr/testify/require"
)

func TestFinalizeReleaseRejectsJobHandlerRegistrationDrift(t *testing.T) {
	execution, err := loadFinalizeExecutionContract()
	require.NoError(t, err)
	err = validateFinalizeJobHandlers(execution, []jobs.Handler{jobs.HandlerFunc{JobKind: "release.wrong"}})
	require.ErrorContains(t, err, "does not match generated kind")
}
