package runtime

import (
	"sync"

	"github.com/flidai/leapview/internal/dashboard/catalog"
)

type CatalogService struct {
	mu      *sync.RWMutex
	catalog catalog.Catalog
}

func NewCatalogService(mu *sync.RWMutex, definition *ProjectDefinition) *CatalogService {
	service := &CatalogService{mu: mu}
	service.catalog = service.catalogView(definition)
	return service
}

func (m *Service) Catalog() catalog.Catalog {
	return m.catalog.Catalog()
}

func (s *CatalogService) Catalog() catalog.Catalog {
	return s.catalog
}

func (s *CatalogService) catalogView(definition *ProjectDefinition) catalog.Catalog {
	if definition == nil {
		return catalog.Catalog{}
	}
	// This projection carries project-level descriptive metadata only; resource
	// identity remains on the canonical project graph.
	result := catalog.Catalog{Workspace: catalog.Workspace{Title: definition.Title(), Description: definition.Description()}}
	models := definition.Models()
	for _, modelID := range definition.ModelIDs() {
		model := models[modelID]
		if model == nil {
			continue
		}
		result.Models = append(result.Models, catalog.Model{ID: modelID.String(), Title: model.Title, Description: model.Description})
	}
	dashboards := definition.Dashboards()
	for _, dashboardID := range definition.DashboardIDs() {
		dashboard := dashboards[dashboardID]
		result.Dashboards = append(result.Dashboards, catalog.Dashboard{ID: dashboardID.String(), Title: dashboard.Title, Description: dashboard.Description, SemanticModel: dashboard.SemanticModel, PageCount: len(dashboard.Pages)})
	}
	return result
}
