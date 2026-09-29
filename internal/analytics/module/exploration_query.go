package module

import (
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	explorationlowering "github.com/flidai/leapview/internal/analytics/exploration/lowering"
)

// ExplorationQueryLowerer is the analytics module surface for the governed
// ExplorationSpec-to-dataquery transformation used by product adapters.
type ExplorationQueryLowerer = exploration.QueryLowerer

// NewExplorationQueryLowerer exposes the lowering use case through the
// analytics module boundary required by application composition.
func NewExplorationQueryLowerer() ExplorationQueryLowerer {
	return explorationlowering.Service{}
}
