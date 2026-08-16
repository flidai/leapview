package module

import (
	"net/http"

	"github.com/flidai/leapview/internal/access"
	dashboardhttp "github.com/flidai/leapview/internal/dashboard/http"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// DashboardObjectRefs resolves the authorization chain for dashboard
// operations without exposing the dashboard HTTP adapter to composition.
func DashboardObjectRefs(r *http.Request, projectID projectgraph.ResourceID) []access.ResourceRef {
	return dashboardhttp.DashboardObjectRefs(r, projectID)
}
