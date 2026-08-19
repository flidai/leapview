package authoring

import (
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/project/graph"
)

// AuthoredDashboardMetadata is the descriptive resource identity retained
// with an authored dashboard source. It is owned by dashboard authoring so
// source consumers do not need to depend on project artifact internals.
type AuthoredDashboardMetadata struct {
	Project     graph.ResourceID
	Name        string
	Title       string
	Description string
	Owner       string
	// Domain is descriptive authored metadata and never an authorization
	// namespace or serving scope.
	Domain string
	Tags   []string
}

// AuthoredDashboardSource is an immutable, capability-scoped authored
// dashboard projection. Implementations must return detached copies so
// callers can safely mutate the result.
type AuthoredDashboardSource struct {
	Document document.DashboardDocument
	Metadata AuthoredDashboardMetadata
	Path     string
}
