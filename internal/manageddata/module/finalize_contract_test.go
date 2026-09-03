package module

import (
	"testing"

	"github.com/flidai/leapview/pkg/jobs"
	"github.com/stretchr/testify/require"
)

func TestFinalizeManagedDataUploadRejectsJobHandlerRegistrationDrift(t *testing.T) {
	execution, err := loadFinalizeUploadExecutionContract()
	require.NoError(t, err)
	err = validateFinalizeUploadJobHandlers(execution, []jobs.Handler{jobs.HandlerFunc{JobKind: "upload.wrong"}})
	require.ErrorContains(t, err, "does not match generated kind")
}
