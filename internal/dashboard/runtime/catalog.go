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
	// Catalog.Workspace is a legacy transport shape. Keep its descriptive
	// fields only; never encode a project ResourceID into a workspace ID.
	result := catalog.Catalog{Workspace: catalog.Workspace{Title: definition.Title(), Description: definition.Description()}}
	for modelID, model := range definition.Models() {
		if model == nil {
			continue
		}
		result.Models = append(result.Models, catalog.Model{ID: modelID.String(), Title: model.Title, Description: model.Description})
	}
	for dashboardID, dashboard := range definition.Dashboards() {
		result.Dashboards = append(result.Dashboards, catalog.Dashboard{ID: dashboardID.String(), Title: dashboard.Title, Description: dashboard.Description, SemanticModel: dashboard.SemanticModel, PageCount: len(dashboard.Pages)})
	}
	return result
}
