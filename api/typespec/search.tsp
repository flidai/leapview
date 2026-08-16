using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Project;

@minLength(1)
@maxLength(200)
scalar SearchQuery extends string;

enum SearchKind {
  project,
  connection,
  source,
  modelKind: "model",
  semanticModel: "semantic_model",
  pipelineKind: "pipeline",
  dashboardKind: "dashboard",
}

model SearchReference {
  id: ResourceId;
  kind: SearchKind;
}

model SearchResult {
  reference: SearchReference;
  name: string;
  displayName?: string;
  description?: string;
  domain?: string;
  owner?: string;
  tags: string[];
  href?: string;
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
@apigen.authz(#{ mode: "authenticated" })
@apigen.cli(#{ command: #["search"], args: #[#{ source: "query", name: "q", displayName: "query" }], output: #{ mode: "raw" } })
op search(
  @query q: SearchQuery,
  @query kind?: SearchKind[],
  @query domain?: string,
  @query limit?: ListLimit,
  @query cursor?: PageToken,
): SearchOK | CommonErrors;
