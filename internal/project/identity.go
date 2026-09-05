package project

import (
	"context"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// IdentityRepository is the narrow persistence port used by project module
// code that only needs to make a project identity durable.  Implementations
// own their storage engine; callers do not receive a database handle and must
// not select a fallback engine at runtime.
//
// EnsureIdentity is intentionally metadata-blind.  It installs the minimum
// identity row and leaves any authored title or description already present
// in the authority untouched.
type IdentityRepository interface {
	EnsureIdentity(context.Context, projectgraph.ResourceID) error
}
