package module

import (
	"context"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ExecutionGrantSelector is the explicit authority port used to select the
// grant for one scheduled occurrence. Implementations must return an ID only
// when exactly one current grant is bound to the process instance, project,
// and pipeline; scheduler or worker identities are not valid substitutes.
type ExecutionGrantSelector interface {
	SelectCurrentExecutionGrantID(context.Context, string, projectgraph.ResourceID, projectgraph.ResourceID) (string, error)
}
