package app

import (
	"context"

	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/extensionsupplyloader"
	"github.com/flidai/leapview/internal/deployment/extensionsupply"
)

const maxExtensionSupplyDocumentBytes = extensionsupplyloader.MaxExtensionSupplyDocumentBytes

// loadExtensionSupply is kept as the application composition seam while the
// implementation lives in a child package shared by offline/admin adapters.
func loadExtensionSupply(ctx context.Context, cfg config.Config) (*extensionsupply.Supply, error) {
	return extensionsupplyloader.Load(ctx, cfg)
}

func readSupplyDocument(path string) ([]byte, error) {
	return extensionsupplyloader.ReadSupplyDocument(path)
}

func runtimeTarget(ctx context.Context) (string, string, error) {
	return extensionsupplyloader.RuntimeTarget(ctx)
}
