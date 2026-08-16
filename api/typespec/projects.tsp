using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Project;

model ProjectResponse {
  id: ResourceId;
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

@tag("Projects")
@summary("List materialized projects")
@route("/projects")
@get
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
op listProjects(@query limit?: ListLimit, @query pageToken?: PageToken): OkJson<ProjectListResponse> | CommonErrors;

@tag("Projects")
@summary("Get a materialized project")
@route("/projects/{project}")
@get
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
@extension("x-leapview-object-scope", "project")
op getProject(@path project: ResourceId): OkJson<ProjectResponse> | CommonErrors;
