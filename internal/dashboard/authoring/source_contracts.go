package authoring

// AuthoredDashboardMetadata is the descriptive resource identity retained
// with an authored dashboard source. It is owned by dashboard authoring so
// source consumers do not need to depend on project artifact internals.
type AuthoredDashboardMetadata struct {
	Workspace   string
	Name        string
	Title       string
	Description string
	Owner       string
	Tags        []string
}

// AuthoredDashboardSource is an immutable, capability-scoped authored
// dashboard projection. Implementations must return detached copies so
// callers can safely mutate the result.
type AuthoredDashboardSource struct {
	Document Dashboard
	Metadata AuthoredDashboardMetadata
	Path     string
}

// DashboardExportMetadata is the resource metadata surrounding an authored
// dashboard export. Name and title fall back to the authored document when
// omitted; Workspace is optional in the resource schema.
type DashboardExportMetadata struct {
	Name        string
	Workspace   string
	Title       string
	Description string
	Owner       string
	Tags        []string
}

// DashboardExporter emits a deterministic canonical dashboard resource.
// Project/compiler owns the production implementation; dashboard authoring
// receives it as a narrow composition port to keep capability direction
// explicit.
type DashboardExporter func(Dashboard, DashboardExportMetadata) ([]byte, error)
