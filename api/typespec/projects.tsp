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

@tag("Projects")
@summary("Get a materialized project")
@route("/projects/{project}")
@get
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
@extension("x-leapview-object-scope", "project")
op getProject(@path project: ResourceId): OkJson<ProjectResponse> | CommonErrors;
