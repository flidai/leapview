// Package manifest owns the compiler's mutable assembly model. Capability
// consumers receive immutable projections from project/artifact instead of
// depending on this cross-capability aggregate.
package manifest

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/publication"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/workspace"
)

type Catalog struct {
	Workspace      CatalogWorkspace   `yaml:"workspace"`
	SemanticModels []CatalogModel     `yaml:"semantic_models"`
	Dashboards     []CatalogDashboard `yaml:"dashboards"`
}

type CatalogWorkspace struct {
	ID          string `yaml:"id"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
}

type CatalogModel struct {
	ID          string `yaml:"id"`
	Title       string `yaml:"title"`
	Path        string `yaml:"path"`
	Description string `yaml:"description"`
}

type CatalogDashboard struct {
	ID          string                    `yaml:"id"`
	Title       string                    `yaml:"title"`
	Path        string                    `yaml:"path"`
	Description string                    `yaml:"description"`
	Tags        []string                  `yaml:"tags"`
	Appearance  dashboardappearance.Patch `yaml:"appearance,omitempty" json:"appearance,omitempty"`
}

// Workspace is compiler-private mutable state. It is serialized into the
// immutable project artifact and never exposed directly to a capability.
type Workspace struct {
	Catalog              Catalog
	Models               map[string]*semanticmodel.Model
	DashboardDefinitions map[string]dashboarddefinition.Definition
	Publications         map[string]publication.Definition
	Access               workspace.AccessPolicy
	RefreshPipelines     map[string]refreshschedule.Definition
	BaseDir              string
	SourceIDs            map[string]string
	SourceFiles          map[string]string
}
