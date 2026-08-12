package api

type PageInfo struct {
	NextCursor *string `json:"nextCursor,omitempty"`
}

type WorkspaceResponse struct {
	ID                   string `json:"id"`
	Title                string `json:"title"`
	Description          string `json:"description"`
	ActiveServingStateID string `json:"activeServingStateId,omitempty"`
	CreatedAt            string `json:"createdAt"`
	UpdatedAt            string `json:"updatedAt"`
}

type WorkspaceAdministrationSubjectResponse struct {
	SubjectType string `json:"subjectType"`
	SubjectID   string `json:"subjectId"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role,omitempty"`
}

type WorkspaceAdministrationRuntimeResponse struct {
	Environment              string `json:"environment"`
	ActiveServingStateID     string `json:"activeServingStateId,omitempty"`
	ActiveServingStateStatus string `json:"activeServingStateStatus,omitempty"`
	ActiveServingStateSince  string `json:"activeServingStateSince,omitempty"`
	ProjectID                string `json:"projectId,omitempty"`
	CurrentDeploymentID      string `json:"currentDeploymentId,omitempty"`
	CurrentDeploymentStatus  string `json:"currentDeploymentStatus,omitempty"`
	CurrentDeploymentSince   string `json:"currentDeploymentSince,omitempty"`
	CurrentReleaseID         string `json:"currentReleaseId,omitempty"`
}

type WorkspaceAdministrationCapabilitiesResponse struct {
	ManageWorkspace    bool `json:"manageWorkspace"`
	ManageAccess       bool `json:"manageAccess"`
	ManagePublications bool `json:"managePublications"`
	ManageConnections  bool `json:"manageConnections"`
	ViewManagedData    bool `json:"viewManagedData"`
	IngestManagedData  bool `json:"ingestManagedData"`
	PublishReleases    bool `json:"publishReleases"`
	RequestDeployments bool `json:"requestDeployments"`
	ViewDeployments    bool `json:"viewDeployments"`
	UseAgent           bool `json:"useAgent"`
	ViewAgent          bool `json:"viewAgent"`
}

type WorkspaceAdministrationLinksResponse struct {
	Self               string `json:"self"`
	Workspace          string `json:"workspace"`
	Groups             string `json:"groups,omitempty"`
	Roles              string `json:"roles,omitempty"`
	RoleBindings       string `json:"roleBindings,omitempty"`
	Grants             string `json:"grants,omitempty"`
	Publications       string `json:"publications,omitempty"`
	ManagedConnections string `json:"managedConnections,omitempty"`
	Releases           string `json:"releases,omitempty"`
	Deployments        string `json:"deployments,omitempty"`
	AgentConversations string `json:"agentConversations,omitempty"`
}

type WorkspaceAdministrationResponse struct {
	Workspace      WorkspaceResponse                           `json:"workspace"`
	Owner          *WorkspaceAdministrationSubjectResponse     `json:"owner,omitempty"`
	Administrators []WorkspaceAdministrationSubjectResponse    `json:"administrators"`
	Runtime        WorkspaceAdministrationRuntimeResponse      `json:"runtime"`
	Capabilities   WorkspaceAdministrationCapabilitiesResponse `json:"capabilities"`
	Links          WorkspaceAdministrationLinksResponse        `json:"links"`
}

type SearchParams struct {
	Query            *string
	Workspaces       *[]string
	Types            *[]string
	ContextWorkspace *string
	ContextDashboard *string
	ContextPage      *string
	Limit            *int32
	PageToken        *string
}

type SearchContextTag string

type SearchLocation struct {
	DashboardID   *string `json:"dashboardId,omitempty"`
	DashboardName *string `json:"dashboardName,omitempty"`
	Href          string  `json:"href"`
	PageID        *string `json:"pageId,omitempty"`
	PageName      *string `json:"pageName,omitempty"`
}

type SearchReference struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	WorkspaceID string `json:"workspaceId"`
}

type SearchWorkspaceSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SearchResult struct {
	Context     []SearchContextTag     `json:"context"`
	Description *string                `json:"description,omitempty"`
	Href        string                 `json:"href"`
	Locations   []SearchLocation       `json:"locations"`
	Name        string                 `json:"name"`
	Reference   SearchReference        `json:"reference"`
	VisualType  *string                `json:"visualType,omitempty"`
	Workspace   SearchWorkspaceSummary `json:"workspace"`
}

type SearchResponse struct {
	Items []SearchResult `json:"items"`
	Page  PageInfo       `json:"page"`
}

type AssetResponse struct {
	ID             string         `json:"id"`
	SnapshotID     string         `json:"snapshotId"`
	WorkspaceID    string         `json:"workspaceId"`
	ServingStateID string         `json:"servingStateId"`
	Type           string         `json:"type"`
	Key            string         `json:"key"`
	ParentID       string         `json:"parentId,omitempty"`
	Title          string         `json:"title"`
	Description    string         `json:"description"`
	SourceFile     string         `json:"sourceFile,omitempty"`
	PayloadSchema  string         `json:"payloadSchema"`
	Payload        map[string]any `json:"payload"`
	Href           string         `json:"href,omitempty"`
}

type AssetSummaryResponse struct {
	ID             string `json:"id"`
	SnapshotID     string `json:"snapshotId"`
	WorkspaceID    string `json:"workspaceId"`
	ServingStateID string `json:"servingStateId"`
	Type           string `json:"type"`
	Key            string `json:"key"`
	ParentID       string `json:"parentId,omitempty"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	SourceFile     string `json:"sourceFile,omitempty"`
	PayloadSchema  string `json:"payloadSchema"`
	ContentHash    string `json:"contentHash"`
	Href           string `json:"href,omitempty"`
}

type AssetGraphAssetResponse struct {
	ID             string         `json:"id"`
	SnapshotID     string         `json:"snapshotId"`
	WorkspaceID    string         `json:"workspaceId"`
	ServingStateID string         `json:"servingStateId"`
	Type           string         `json:"type"`
	Key            string         `json:"key"`
	ParentID       string         `json:"parentId,omitempty"`
	Title          string         `json:"title"`
	Description    string         `json:"description"`
	SourceFile     string         `json:"sourceFile,omitempty"`
	PayloadSchema  string         `json:"payloadSchema"`
	Payload        map[string]any `json:"payload"`
	ContentHash    string         `json:"contentHash"`
}

type WorkspaceAssetGraphResponse struct {
	Assets []AssetGraphAssetResponse `json:"assets"`
	Edges  []AssetEdgeResponse       `json:"edges"`
}

type AssetEdgeResponse struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspaceId"`
	ServingStateID string `json:"servingStateId"`
	FromAssetID    string `json:"fromAssetId"`
	ToAssetID      string `json:"toAssetId"`
	Type           string `json:"type"`
}

type AssetLineageResponse struct {
	AssetID    string   `json:"assetId"`
	Upstream   []string `json:"upstream"`
	Downstream []string `json:"downstream"`
}
