package app

import (
	"path/filepath"

	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
)

func applicationAssets(config config.Config, production bool) staticasset.Resolver {
	return staticasset.New(staticasset.Config{
		// The released local authoring runtime uses development serving policy,
		// but must not expose source-contributor diagnostics merely because its
		// environment is named dev. Only the explicit contributor workflow opts
		// into the non-production asset surface.
		Production: production || !config.ContributorDiagnostics,
		Version:    config.AssetVersion,
		GeneratedVersionPath: filepath.Join(
			"static",
			"asset-version.txt",
		),
	})
}
