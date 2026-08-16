// Package artifact defines the immutable project projection consumed by the
// refresh capability.
package artifact

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

type Definition struct {
	Models        map[string]*semanticmodel.Model
	Pipelines     map[string]refreshschedule.Definition
	ConnectionIDs map[string]string
}
