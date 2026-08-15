// Package runtime defines the neutral project-generation capability used by
// compilers, query services, and authoring adapters. Runtime-host implements
// these contracts; consumers do not import runtime-host or select another
// project generation.
package runtime

import (
	"context"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type Runtime interface{ Close() error }

type Lease interface {
	Runtime() Runtime
	Identity() projectgraph.ServingIdentity
	Release()
}

type Provider interface {
	Acquire(context.Context) (Lease, error)
}
