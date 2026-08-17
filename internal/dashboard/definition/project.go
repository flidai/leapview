package definition

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// Project is the immutable dashboard capability projection of a compiled
// project artifact. Individual dashboards remain immutable Definitions.
type Project struct {
	Catalog    catalog.Catalog
	Models     map[projectgraph.ResourceID]*semanticmodel.Model
	Dashboards map[projectgraph.ResourceID]Definition
}
