import "@yacobolo/apigen";
import "../visualization/main.tsp";
import "./common.tsp";
import "./capabilities.tsp";
import "./projects.tsp";
import "./releases.tsp";
import "./current_user.tsp";
import "./instance.tsp";
import "./search.tsp";
import "./bi.tsp";
import "./deployments.tsp";
import "./refresh_runs.tsp";
import "./agent.tsp";
import "./access.tsp";
import "./audit.tsp";
import "./query_audit.tsp";
import "./connection_bindings.tsp";
import "./dashboard_publications.tsp";
import "./dashboard_authoring.tsp";
import "./managed_data.tsp";
import "./upload_tus.tsp";
import "./upload_s3_multipart.tsp";

using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

@service(#{ title: "LeapView Headless API" })
@doc("Headless API for LeapView projects, deployments, managed data, access control, refreshes, and agent operations.")
@info(#{ version: "1.0.0" })
@useAuth(BearerAuth)
@tagMetadata(#[#{ name: "System", description: "API capabilities and protocol discovery." }, #{ name: "Instance", description: "The immutable serving identity of this LeapView installation." }, #{ name: "Projects", description: "Active project graph assertion." }, #{ name: "Releases", description: "Immutable project releases and artifacts." }, #{ name: "Current User", description: "Current principal, sessions, tokens, and capabilities." }, #{ name: "Search", description: "Progressive project-graph discovery across governed resources." }, #{ name: "BI", description: "Dashboard and semantic model read APIs." }, #{ name: "Connections", description: "Target-owned logical connection bindings, tests, refreshes, and redacted health." }, #{ name: "Deployments", description: "Atomic project release deployment." }, #{ name: "Refresh Runs", description: "Headless refresh run operations." }, #{ name: "Agent", description: "Headless agent conversation and run operations." }, #{ name: "Access", description: "Principals, groups, roles, and role bindings." }, #{ name: "Audit", description: "Project audit event discovery." }, #{ name: "Publications", description: "Anonymous public dashboard publication lifecycle." }, #{ name: "Managed Data", description: "Project-global managed-data revisions and uploads." }])
@apigen.transportErrors(ProblemDetails, #{
  contentType: "application/problem+json",
  failures: #[
    #{ kind: "path_parameter", statusCode: 400, code: "INVALID_REQUEST", publicDetail: "Invalid path parameter." },
    #{ kind: "query_parameter", statusCode: 400, code: "INVALID_REQUEST", publicDetail: "Invalid query parameter." },
    #{ kind: "header_parameter", statusCode: 400, code: "INVALID_REQUEST", publicDetail: "Invalid request header." },
    #{ kind: "malformed_body", statusCode: 400, code: "INVALID_REQUEST_BODY", publicDetail: "Invalid request body." },
    #{ kind: "multipart", statusCode: 400, code: "INVALID_MULTIPART_BODY", publicDetail: "Invalid multipart body." },
    #{ kind: "unsupported_media_type", statusCode: 415, code: "UNSUPPORTED_MEDIA_TYPE", publicDetail: "Unsupported media type." },
    #{ kind: "handler", statusCode: 500, code: "INTERNAL_ERROR", publicDetail: "Internal server error." },
    #{ kind: "response_serialization", statusCode: 500, code: "INTERNAL_ERROR", publicDetail: "Internal server error." },
  ],
})
namespace LeapViewAPI;
