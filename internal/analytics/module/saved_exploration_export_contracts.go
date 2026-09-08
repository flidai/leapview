package module

import (
	"context"

	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
)

// savedExplorationQueryExecutor is deliberately optional on the transport
// port. Existing browser/API test doubles and read-only compositions do not
// acquire an execution capability merely because exports were added; the
// concrete application service implements both governed entry points.
type savedExplorationQueryExecutor interface {
	Execute(context.Context, saved.ExecuteRequest) (saved.ExecuteResult, error)
	ExecuteSpec(context.Context, saved.ExecuteSpecRequest) (saved.ExecuteResult, error)
}
