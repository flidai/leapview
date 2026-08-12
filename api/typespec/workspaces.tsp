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

alias ListWorkspacesOK = OkJson<WorkspaceListResponse>;

@tag("Workspaces")
@summary("List workspaces")
@route("/workspaces")
@get
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
@apigen.cli(#{ command: #["workspaces", "list"], output: #{ mode: "raw" } })
@operationId("listWorkspaces")
op listWorkspaces(@query limit?: ListLimit, @query pageToken?: PageToken): ListWorkspacesOK | CommonErrors;

@tag("Workspaces")
@summary("Get a workspace")
@route("/workspaces/{workspace}")
@get
@apigen.authz(#{ mode: "privilege", privilege: "VIEW_ITEM" })
@operationId("getWorkspace")
op getWorkspace(@path workspace: string): OkJson<WorkspaceResponse> | CommonErrors;

@tag("Workspaces")
@summary("Get the workspace administration overview")
@route("/workspaces/{workspace}/administration")
@get
@apigen.authz(#{ mode: "privilege", privilege: "MANAGE_WORKSPACE" })
@operationId("getWorkspaceAdministration")
op getWorkspaceAdministration(@path workspace: string): OkJson<WorkspaceAdministrationResponse> | CommonErrors;

alias ListWorkspaceAssetEdgesOK = OkJson<AssetEdgeListResponse>;

@tag("Workspaces")
@summary("List workspace asset edges")
@route("/workspaces/{workspace}/asset-edges")
@get
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
@apigen.cli(#{ command: #["assets", "edges"], args: #[#{ source: "path", name: "workspace", displayName: "workspace" }], output: #{ mode: "raw" } })
@operationId("listWorkspaceAssetEdges")
op listWorkspaceAssetEdges(@path workspace: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListWorkspaceAssetEdgesOK | CommonErrors;

alias ListWorkspaceAssetsOK = OkJson<AssetListResponse>;

@tag("Workspaces")
@summary("List workspace assets")
@route("/workspaces/{workspace}/assets")
@get
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
@apigen.cli(#{ command: #["assets", "list"], args: #[#{ source: "path", name: "workspace", displayName: "workspace" }], output: #{ mode: "raw" } })
@operationId("listWorkspaceAssets")
op listWorkspaceAssets(@path workspace: string, @query type?: string, @query q?: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListWorkspaceAssetsOK | CommonErrors;

@tag("Workspaces")
@summary("Get the active asset graph")
@route("/workspaces/{workspace}/active-asset-graph")
@get
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
@operationId("getWorkspaceActiveAssetGraph")
op getWorkspaceActiveAssetGraph(@path workspace: string): OkJson<WorkspaceAssetGraphResponse> | CommonErrors;

@tag("Workspaces")
@summary("Describe a workspace asset")
@route("/workspaces/{workspace}/assets/{assetId}")
@get
@apigen.authz(#{ mode: "privilege", privilege: "VIEW_ITEM" })
@extension("x-leapview-object-scope", "workspace-asset")
@apigen.cli(#{ command: #["assets", "describe"], args: #[#{ source: "path", name: "workspace", displayName: "workspace" }, #{ source: "path", name: "assetId", displayName: "asset-id" }], output: #{ mode: "raw" } })
@operationId("getWorkspaceAsset")
op getWorkspaceAsset(@path workspace: string, @path assetId: string): OkJson<AssetResponse> | CommonErrors;

@tag("Workspaces")
@summary("Get workspace asset lineage")
@route("/workspaces/{workspace}/assets/{assetId}/lineage")
@get
@apigen.authz(#{ mode: "privilege", privilege: "VIEW_ITEM" })
@extension("x-leapview-object-scope", "workspace-asset")
@apigen.cli(#{ command: #["assets", "lineage"], args: #[#{ source: "path", name: "workspace", displayName: "workspace" }, #{ source: "path", name: "assetId", displayName: "asset-id" }], output: #{ mode: "raw" } })
@operationId("getWorkspaceAssetLineage")
op getWorkspaceAssetLineage(@path workspace: string, @path assetId: string): OkJson<AssetLineageResponse> | CommonErrors;
