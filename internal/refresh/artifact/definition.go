// Package artifact defines the immutable project projection consumed by the
// refresh capability.
package artifact

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

type Definition struct {
	Models map[string]*semanticmodel.Model
	// ModelTables is the complete project-wide authored Model catalog keyed by
	// authored Model name. Unlike the semantic model projections in Models, it
	// is not limited to Models exposed as semantic datasets, so refresh planning
	// can close over every upstream Model dependency.
	ModelTables   map[string]semanticmodel.Table
	Pipelines     map[string]refreshschedule.Definition
	ConnectionIDs map[string]string
}
