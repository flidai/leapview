using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Workspace;

model WorkspaceResponse {
  id: string;
  title: string;
  description: string;
  activeServingStateId?: string;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
}

model WorkspaceListResponse {
  items: WorkspaceResponse[];
  page: PageInfo;
}

model WorkspaceAdministrationSubjectResponse {
  subjectType: string;
  subjectId: string;
  email?: string;
  displayName: string;
  role?: string;
}

model WorkspaceAdministrationRuntimeResponse {
  environment: string;
  activeServingStateId?: string;
  activeServingStateStatus?: string;
  activeServingStateSince?: utcDateTime;
  projectId?: string;
  currentDeploymentId?: string;
  currentDeploymentStatus?: string;
  currentDeploymentSince?: utcDateTime;
  currentReleaseId?: string;
}

model WorkspaceAdministrationCapabilitiesResponse {
  manageWorkspace: boolean;
  manageAccess: boolean;
  managePublications: boolean;
  manageConnections: boolean;
  viewManagedData: boolean;
  ingestManagedData: boolean;
  publishReleases: boolean;
  requestDeployments: boolean;
  viewDeployments: boolean;
  useAgent: boolean;
  viewAgent: boolean;
}

model WorkspaceAdministrationLinksResponse {
  self: string;
  workspace: string;
  groups?: string;
  roles?: string;
  roleBindings?: string;
  grants?: string;
  publications?: string;
  managedConnections?: string;
  releases?: string;
  deployments?: string;
  agentConversations?: string;
}

model WorkspaceAdministrationResponse {
  workspace: WorkspaceResponse;
  owner?: WorkspaceAdministrationSubjectResponse;
  administrators: WorkspaceAdministrationSubjectResponse[];
  runtime: WorkspaceAdministrationRuntimeResponse;
  capabilities: WorkspaceAdministrationCapabilitiesResponse;
  links: WorkspaceAdministrationLinksResponse;
}

model AssetResponse {
  id: string;
  snapshotId: string;
  workspaceId: string;
  servingStateId: string;
  type: string;
  key: string;
  parentId?: string;
  title: string;
  description: string;
  sourceFile?: string;
  payloadSchema: string;
  payload: JSONObject;
  href?: string;
}

model AssetSummaryResponse {
  id: string;
  snapshotId: string;
  workspaceId: string;
  servingStateId: string;
  type: string;
  key: string;
  parentId?: string;
  title: string;
  description: string;
  sourceFile?: string;
  payloadSchema: string;
  contentHash: string;
  href?: string;
}

model AssetGraphAssetResponse {
  id: string;
  snapshotId: string;
  workspaceId: string;
  servingStateId: string;
  type: string;
  key: string;
  parentId?: string;
  title: string;
  description: string;
  sourceFile?: string;
  payloadSchema: string;
  payload: JSONObject;
  contentHash: string;
}

model WorkspaceAssetGraphResponse {
  assets: AssetGraphAssetResponse[];
  edges: AssetEdgeResponse[];
}

model AssetListResponse {
  items: AssetSummaryResponse[];
  page: PageInfo;
}

model AssetEdgeResponse {
  id: string;
  workspaceId: string;
  servingStateId: string;
  fromAssetId: string;
  toAssetId: string;
  type: string;
}

model AssetEdgeListResponse {
  items: AssetEdgeResponse[];
  page: PageInfo;
}

model AssetLineageResponse {
  assetId: string;
  upstream: string[];
  downstream: string[];
}

model DashboardAppearancePatchRequest {
  icon?: string;
  color?: string;
}

model DashboardAppearanceResponse {
  icon: string;
  color: string;
  revision: int64;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "standard" })
model DashboardAppearanceUpdatedAuditPayload {
  @apigen.auditPublic
  fields: string[];
}

alias ListWorkspacesOK = OkJson<WorkspaceListResponse>;

@tag("Workspaces")
@summary("List workspaces")
@route("/workspaces")
@get
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
@apigen.cli(#{ command: #["workspaces", "list"], output: #{ mode: "raw" } })
op listWorkspaces(@query limit?: ListLimit, @query pageToken?: PageToken): ListWorkspacesOK | CommonErrors;

@tag("Workspaces")
@summary("Get a workspace")
@route("/workspaces/{workspace}")
@get
@apigen.authz(#{ mode: "privilege", privilege: "VIEW_ITEM" })
op getWorkspace(@path workspace: string): OkJson<WorkspaceResponse> | CommonErrors;

@tag("Workspaces")
@summary("Get the workspace administration overview")
@route("/workspaces/{workspace}/administration")
@get
@apigen.authz(#{ mode: "privilege", privilege: "MANAGE_WORKSPACE" })
op getWorkspaceAdministration(@path workspace: string): OkJson<WorkspaceAdministrationResponse> | CommonErrors;

alias ListWorkspaceAssetEdgesOK = OkJson<AssetEdgeListResponse>;

@tag("Workspaces")
@summary("List workspace asset edges")
@route("/workspaces/{workspace}/asset-edges")
@get
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
@apigen.cli(#{ command: #["assets", "edges"], args: #[#{ source: "path", name: "workspace", displayName: "workspace" }], output: #{ mode: "raw" } })
op listWorkspaceAssetEdges(@path workspace: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListWorkspaceAssetEdgesOK | CommonErrors;

alias ListWorkspaceAssetsOK = OkJson<AssetListResponse>;

@tag("Workspaces")
@summary("List workspace assets")
@route("/workspaces/{workspace}/assets")
@get
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
@apigen.cli(#{ command: #["assets", "list"], args: #[#{ source: "path", name: "workspace", displayName: "workspace" }], output: #{ mode: "raw" } })
op listWorkspaceAssets(@path workspace: string, @query type?: string, @query q?: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListWorkspaceAssetsOK | CommonErrors;

@tag("Workspaces")
@summary("Get the active asset graph")
@route("/workspaces/{workspace}/active-asset-graph")
@get
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
op getWorkspaceActiveAssetGraph(@path workspace: string): OkJson<WorkspaceAssetGraphResponse> | CommonErrors;

@tag("Workspaces")
@summary("Describe a workspace asset")
@route("/workspaces/{workspace}/assets/{assetId}")
@get
@apigen.authz(#{ mode: "privilege", privilege: "VIEW_ITEM" })
@extension("x-leapview-object-scope", "workspace-asset")
@apigen.cli(#{ command: #["assets", "describe"], args: #[#{ source: "path", name: "workspace", displayName: "workspace" }, #{ source: "path", name: "assetId", displayName: "asset-id" }], output: #{ mode: "raw" } })
op getWorkspaceAsset(@path workspace: string, @path assetId: string): OkJson<AssetResponse> | CommonErrors;

@tag("Workspaces")
@summary("Get workspace asset lineage")
@route("/workspaces/{workspace}/assets/{assetId}/lineage")
@get
@apigen.authz(#{ mode: "privilege", privilege: "VIEW_ITEM" })
@extension("x-leapview-object-scope", "workspace-asset")
@apigen.cli(#{ command: #["assets", "lineage"], args: #[#{ source: "path", name: "workspace", displayName: "workspace" }, #{ source: "path", name: "assetId", displayName: "asset-id" }], output: #{ mode: "raw" } })
op getWorkspaceAssetLineage(@path workspace: string, @path assetId: string): OkJson<AssetLineageResponse> | CommonErrors;

@tag("Workspaces")
@summary("Update a dashboard's catalog appearance")
@route("/workspaces/{workspace}/dashboards/{dashboard}/appearance")
@put
@apigen.authz(#{ mode: "privilege", privilege: "MANAGE_WORKSPACE" })
@apigen.command(#{ auditAction: "dashboard.appearance.updated", guarantee: "best-effort", failures: #[
  #{ kind: "invalid", statusCode: 422, code: "INVALID_DASHBOARD_APPEARANCE", publicDetail: "The selected dashboard appearance is invalid." },
] })
@apigen.auditPayload(DashboardAppearanceUpdatedAuditPayload)
@apigen.ui("dashboard.appearance.update")
op updateDashboardAppearance(
  @path @apigen.target workspace: string,
  @path dashboard: string,
  @body body: DashboardAppearancePatchRequest,
): OkJson<DashboardAppearanceResponse> | CommonErrors;
