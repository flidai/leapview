package app

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectmodule "github.com/flidai/leapview/internal/project/module"
)

func semanticCatalogVisibility(resolve func(context.Context) (access.SemanticAttributeResolution, error), audit ...projectmodule.SemanticCatalogAuditConfig) projectcatalog.SemanticModelVisibility {
	return projectmodule.SemanticCatalogVisibility(resolve, audit...)
}
