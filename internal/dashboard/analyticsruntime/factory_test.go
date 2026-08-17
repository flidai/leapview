package analyticsruntime

import (
	"testing"

	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
)

func TestRequiredProjectExtensionsIncludesSpatialForTiledMaps(t *testing.T) {
	if got := requiredProjectExtensions((*dashboardruntime.ProjectDefinition)(nil)); got != nil {
		t.Fatalf("required extensions = %#v, want nil for missing project", got)
	}
}
