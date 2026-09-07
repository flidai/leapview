package module

import (
	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	"github.com/flidai/leapview/internal/analytics/materialization"
)

// CommitMarker is the analytics module's public name for the canonical
// physical-build marker exchanged with deployment composition.
type CommitMarker = catalogartifact.CommitMarker

// MaterializationExecutor is the analytics module's public execution seam for
// a physical build environment.
type MaterializationExecutor = materialization.Executor
