package signals

import workspaceview "github.com/flidai/leapview/internal/workspace"

const (
	RouteDashboard       RouteKind = "dashboard"
	RoutePipelines       RouteKind = "pipelines"
	RouteConnections     RouteKind = "connections"
	RouteConnectionAsset RouteKind = "connection_asset"
	RouteData            RouteKind = "data"
)

type WorkspaceAccessResponse struct {
	Workspace    workspaceview.WorkspaceView     `json:"workspace"`
	ObjectType   string                          `json:"objectType,omitempty"`
	ObjectID     string                          `json:"objectId,omitempty"`
	ObjectTitle  string                          `json:"objectTitle,omitempty"`
	Mode         string                          `json:"mode,omitempty"`
	Roles        []workspaceview.RoleView        `json:"roles"`
	Bindings     []workspaceview.RoleBindingView `json:"bindings"`
	Candidates   []WorkspaceAccessCandidate      `json:"candidates"`
	CanManage    bool                            `json:"canManage"`
	Search       string                          `json:"search"`
	SearchStatus WorkspaceAccessSearchStatus     `json:"searchStatus"`
	Status       WorkspaceAccessStatus           `json:"status"`
}

func WorkspaceAccessSignals(access WorkspaceAccessResponse) WorkspaceAccessSignal {
	roles := make([]any, len(access.Roles))
	for index := range access.Roles {
		roles[index] = access.Roles[index]
	}
	bindings := make([]any, len(access.Bindings))
	for index := range access.Bindings {
		bindings[index] = access.Bindings[index]
	}
	candidates := access.Candidates
	if candidates == nil {
		candidates = []WorkspaceAccessCandidate{}
	}
	return WorkspaceAccessSignal{
		Workspace:    access.Workspace,
		ObjectType:   optionalValue(access.ObjectType),
		ObjectID:     optionalValue(access.ObjectID),
		ObjectTitle:  optionalValue(access.ObjectTitle),
		Mode:         optionalValue(access.Mode),
		Roles:        roles,
		Bindings:     bindings,
		Candidates:   candidates,
		CanManage:    access.CanManage,
		Status:       access.Status,
		Command:      WorkspaceAccessCommand{Subjects: []WorkspaceAccessSubject{}},
		Search:       access.Search,
		SearchStatus: access.SearchStatus,
	}
}
