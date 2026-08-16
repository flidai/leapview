package runtime

import (
	"fmt"
	"sync"

	"github.com/flidai/leapview/internal/dashboard/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type CatalogService struct {
	mu      *sync.RWMutex
	catalog catalog.Catalog
}

func NewCatalogService(mu *sync.RWMutex, definition *ProjectDefinition) (*CatalogService, error) {
	service := &CatalogService{mu: mu}
	view, err := service.catalogView(definition)
	if err != nil {
		return nil, err
	}
	service.catalog = view
	return service, nil
}

func (m *Service) Catalog() catalog.Catalog {
	return m.catalog.Catalog()
}

func (s *CatalogService) Catalog() catalog.Catalog {
	return s.catalog
}

func (s *CatalogService) catalogView(definition *ProjectDefinition) (catalog.Catalog, error) {
	if definition == nil {
		return catalog.Catalog{}, nil
	}
	// This projection carries project-level descriptive metadata only; resource
	// identity remains on the canonical project graph.
	result := catalog.Catalog{Project: catalog.Project{ID: definition.ProjectID(), Title: definition.Title(), Description: definition.Description()}}
	models := definition.Models()
	for _, modelID := range definition.ModelIDs() {
		model := models[modelID]
		if model == nil {
			return catalog.Catalog{}, fmt.Errorf("compiled model %q is missing from project models", modelID)
		}
		result.Models = append(result.Models, catalog.Model{ID: modelID, Title: model.Title, Description: model.Description})
	}
	dashboards := definition.Dashboards()
	for _, dashboardID := range definition.DashboardIDs() {
		dashboard := dashboards[dashboardID]
		semanticModelID, err := projectgraph.NewResourceID(dashboard.SemanticModel)
		if err != nil {
			return catalog.Catalog{}, fmt.Errorf("compiled dashboard %q semantic model: %w", dashboardID, err)
		}
		result.Dashboards = append(result.Dashboards, catalog.Dashboard{ID: dashboardID, Title: dashboard.Title, Description: dashboard.Description, SemanticModel: semanticModelID, PageCount: len(dashboard.Pages)})
	}
	return result, nil
}
