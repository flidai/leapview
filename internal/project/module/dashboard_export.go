package module

import (
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/project/compiler"
)

// ExportDashboard is the project capability's canonical authored-dashboard
// exporter exposed through the project module surface. Dashboard authoring
// consumes this narrow function port without importing project internals.
func ExportDashboard(document authoring.Dashboard, metadata authoring.DashboardExportMetadata) ([]byte, error) {
	return compiler.ExportDashboard(document, metadata)
}
