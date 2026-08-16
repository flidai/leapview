using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Project;

enum SearchResultType {
  connection,
  source,
  model_table,
  semantic_model,
  semantic_table,
  field,
  measure,
  dashboard,
  page,
  visual,
  filter,
  refresh_pipeline,
}

enum SearchContextTag {
  current_page,
  current_dashboard,
}

model SearchReference {
  projectId: ResourceId;
  type: SearchResultType;
  id: string;
}

model SearchLocation {
  dashboardId?: string;
  dashboardName?: string;
  pageId?: string;
  pageName?: string;
  href: string;
}

model SearchResult {
  reference: SearchReference;
  name: string;
  description?: string;
  visualType?: string;
  href: string;
  locations: SearchLocation[];
  context: SearchContextTag[];
}

model SearchResponse {
  items: SearchResult[];
  page: PageInfo;
}

alias SearchOK = OkJson<SearchResponse>;

@tag("Search")
@summary("Search accessible product objects")
@route("/search")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
@apigen.cli(#{ command: #["search"], args: #[#{ source: "query", name: "q", displayName: "query" }], output: #{ mode: "raw" } })
op search(
  @query q?: string,
  @query project?: ResourceId,
  @query type?: SearchResultType[],
  @query contextDashboard?: ResourceId,
  @query contextPage?: string,
  @query limit?: ListLimit,
  @query pageToken?: PageToken,
): SearchOK | CommonErrors;
