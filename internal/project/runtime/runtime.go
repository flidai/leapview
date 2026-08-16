// Package runtime contains the neutral project-generation execution seam.
// Capability packages depend on this contract instead of runtimehost.
package runtime

import (
	"context"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type Runtime interface{ Close() error }

type Lease interface {
	Runtime() Runtime
	Identity() projectgraph.ServingIdentity
	DuckLakeSnapshotID() int64
	Release()
}

type Provider interface {
	Acquire(context.Context) (Lease, error)
}
