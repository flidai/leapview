using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Project;

model ProjectResponse {
  id: string;
  title: string;
  description?: string;
  latestReleaseId?: string;
  activeDeploymentId?: string;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
}

model ProjectListResponse {
  items: ProjectResponse[];
  page: PageInfo;
}

model ProjectWorkspaceResponse {
  id: string;
  title: string;
  description?: string;
  activeServingStateId?: string;
}

model ProjectWorkspaceListResponse {
  items: ProjectWorkspaceResponse[];
  page: PageInfo;
}

@tag("Projects")
@summary("List materialized projects")
@route("/projects")
@get
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
op listProjects(@query limit?: ListLimit, @query pageToken?: PageToken): OkJson<ProjectListResponse> | CommonErrors;

@tag("Projects")
@summary("Get a materialized project")
@route("/projects/{project}")
@get
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
@extension("x-leapview-object-scope", "project-environment")
op getProject(@path project: string): OkJson<ProjectResponse> | CommonErrors;

@tag("Projects")
@summary("List project workspaces")
@route("/projects/{project}/workspaces")
@get
@apigen.authz(#{ mode: "privilege", privilege: "VIEW_ITEM" })
@extension("x-leapview-object-scope", "project-environment")
op listProjectWorkspaces(@path project: string, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<ProjectWorkspaceListResponse> | CommonErrors;
