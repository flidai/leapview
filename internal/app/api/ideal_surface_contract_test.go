package api_test

import (
	"strings"
	"testing"
)

func TestIdealV1Surface(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	paths := openAPIMap(t, spec, "paths")

	required := map[string][]string{
		"/api/v1/capabilities":                                                                       {"get"},
		"/api/v1/projects/{project}":                                                                 {"get"},
		"/api/v1/projects/{project}/workspaces":                                                      {"get"},
		"/api/v1/projects/{project}/connections":                                                     {"get"},
		"/api/v1/projects/{project}/connections/{connection}":                                        {"get"},
		"/api/v1/projects/{project}/connections/{connection}/active-revision":                        {"get"},
		"/api/v1/projects/{project}/releases":                                                        {"get", "post"},
		"/api/v1/projects/{project}/releases/{release}":                                              {"get"},
		"/api/v1/projects/{project}/releases/{release}/workspaces/{workspace}/artifact":              {"put"},
		"/api/v1/projects/{project}/releases/{release}/finalize":                                     {"post"},
		"/api/v1/projects/{project}/releases/{release}/events":                                       {"get"},
		"/api/v1/projects/{project}/deployments":                                                     {"get", "post"},
		"/api/v1/projects/{project}/deployments/{deployment}/events":                                 {"get"},
		"/api/v1/projects/{project}/deployments/{deployment}/cancel":                                 {"post"},
		"/api/v1/projects/{project}/deployments/{deployment}/retry":                                  {"post"},
		"/api/v1/projects/{project}/deployments/{deployment}/rollback":                               {"post"},
		"/api/v1/projects/{project}/deployments/{deployment}/activate":                               {"post"},
		"/api/v1/projects/{project}/deployments/{deployment}/approval-requests":                      {"post"},
		"/api/v1/projects/{project}/deployments/{deployment}/approval-requests/{approval}/approve":   {"post"},
		"/api/v1/projects/{project}/deployments/{deployment}/approval-requests/{approval}/deny":      {"post"},
		"/api/v1/projects/{project}/deployments/{deployment}/approval-requests/{approval}/revoke":    {"post"},
		"/api/v1/workspaces/{workspace}":                                                             {"get"},
		"/api/v1/workspaces/{workspace}/dashboards/{dashboard}/pages/{page}":                         {"get"},
		"/api/v1/workspaces/{workspace}/dashboards/{dashboard}/pages/{page}/visuals/{visual}":        {"get"},
		"/api/v1/workspaces/{workspace}/dashboards/{dashboard}/pages/{page}/filters/{filter}":        {"get"},
		"/api/v1/workspaces/{workspace}/dashboards/{dashboard}/pages/{page}/visuals/{visual}/query":  {"post"},
		"/api/v1/workspaces/{workspace}/dashboards/{dashboard}/pages/{page}/filters/{filter}/values": {"post"},
		"/api/v1/workspaces/{workspace}/semantic-models/{model}/relationships":                       {"get"},
		"/api/v1/workspaces/{workspace}/semantic-models/{model}/sources":                             {"get"},
		"/api/v1/workspaces/{workspace}/refresh-runs/{run}/events":                                   {"get"},
		"/api/v1/workspaces/{workspace}/refresh-runs/{run}/cancel":                                   {"post"},
		"/api/v1/agent/conversations/{conversation}/runs":                                            {"get", "post"},
		"/api/v1/agent/conversations/{conversation}/runs/{run}/cancel":                               {"post"},
		"/api/v1/workspaces/{workspace}/grants/{grant}":                                              {"get", "patch", "delete"},
		"/api/v1/workspaces/{workspace}/data-policies/{policy}":                                      {"get", "patch", "delete"},
	}
	for path, methods := range required {
		for _, method := range methods {
			_ = openAPIOperation(t, paths, path, method)
		}
	}

	removed := []string{
		"/api/v1/projects/{project}/workspaces/{workspace}/deployment-candidates",
		"/api/v1/workspaces/{workspace}/dashboards/{dashboard}/pages/{page}/components",
		"/api/v1/workspaces/{workspace}/dashboards/{dashboard}/tables/{table}/query",
		"/api/v1/workspaces/{workspace}/dashboards/{dashboard}/pages/{page}/tables/{table}",
		"/api/v1/workspaces/{workspace}/dashboards/{dashboard}/pages/{page}/tables/{table}/query",
		"/api/v1/workspaces/{workspace}/dashboards/{dashboard}/pages/{page}/tables/{table}/data",
		"/api/v1/workspaces/{workspace}/dashboards/{dashboard}/pages/{page}/filters/{filter}/options",
		"/api/v1/workspaces/{workspace}/semantic-models/{model}/datasets/{dataset}/query",
		"/api/v1/agent/conversations/{conversation}/turns",
	}
	for _, path := range removed {
		if _, ok := paths[path]; ok {
			t.Errorf("legacy path remains in OpenAPI: %s", path)
		}
	}
}

func TestEveryPublicOperationUsesGlobalBearerSecurity(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	security, _ := spec["security"].([]any)
	if len(security) != 1 {
		t.Fatalf("global security = %#v", security)
	}
	requirement, _ := security[0].(map[string]any)
	if _, ok := requirement["BearerAuth"]; !ok {
		t.Fatalf("global bearer security missing: %#v", security)
	}
	schemes := openAPIMap(t, openAPIMap(t, spec, "components"), "securitySchemes")
	bearer := openAPIMap(t, schemes, "BearerAuth")
	if bearer["type"] != "http" || !strings.EqualFold(bearer["scheme"].(string), "bearer") {
		t.Fatalf("BearerAuth = %#v", bearer)
	}
	for path, rawPath := range openAPIMap(t, spec, "paths") {
		pathItem, _ := rawPath.(map[string]any)
		for method, rawOperation := range pathItem {
			operation, ok := rawOperation.(map[string]any)
			if !ok || method == "parameters" {
				continue
			}
			if override, exists := operation["security"]; exists {
				if values, _ := override.([]any); len(values) == 0 {
					t.Errorf("%s %s disables bearer security", method, path)
				}
			}
		}
	}
}

func TestIdealQueryAndEventRepresentations(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	paths := openAPIMap(t, spec, "paths")

	for _, tc := range []struct {
		path   string
		method string
		media  string
	}{
		{"/api/v1/workspaces/{workspace}/semantic-models/{model}/query", "post", "application/vnd.apache.arrow.stream"},
		{"/api/v1/workspaces/{workspace}/semantic-models/{model}/datasets/{dataset}/preview", "post", "application/vnd.apache.arrow.stream"},
		{"/api/v1/workspaces/{workspace}/dashboards/{dashboard}/pages/{page}/visuals/{visual}/query", "post", "application/vnd.apache.arrow.stream"},
		{"/api/v1/projects/{project}/releases/{release}/events", "get", "text/event-stream"},
		{"/api/v1/projects/{project}/deployments/{deployment}/events", "get", "text/event-stream"},
		{"/api/v1/workspaces/{workspace}/refresh-runs/{run}/events", "get", "text/event-stream"},
		{"/api/v1/agent/conversations/{conversation}/runs/{run}/events", "get", "text/event-stream"},
	} {
		op := openAPIOperation(t, paths, tc.path, tc.method)
		if !operationHasResponseMedia(op, "200", tc.media) {
			t.Errorf("%s %s does not declare %s", tc.method, tc.path, tc.media)
		}
	}

	schemas := openAPIMap(t, openAPIMap(t, spec, "components"), "schemas")
	columns := schemaProperty(t, openAPISchema(t, schemas, "SemanticQueryResponse"), "columns")
	if items, _ := columns["items"].(map[string]any); items == nil || items["$ref"] != "#/components/schemas/QueryColumn" {
		t.Fatalf("semantic query columns are not typed descriptors: %#v", columns)
	}
	rows := schemaProperty(t, openAPISchema(t, schemas, "SemanticQueryResponse"), "rows")
	if rows["type"] != "array" {
		t.Fatalf("semantic query rows are not positional arrays: %#v", rows)
	}
}

func TestDashboardVisualResponsesUseVersionedVisualizationEnvelope(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	schemas := openAPIMap(t, openAPIMap(t, spec, "components"), "schemas")
	envelope := openAPISchema(t, schemas, "VisualizationEnvelope")
	properties := openAPIMap(t, envelope, "properties")
	for _, name := range []string{"schemaVersion", "rendererID", "specRevision", "dataRevision", "spec", "dataState", "selection", "status", "diagnostics"} {
		if _, ok := properties[name]; !ok {
			t.Errorf("visualization envelope is missing %q: %#v", name, envelope)
		}
	}
	visual := openAPISchema(t, schemas, "VisualizationSpec")
	discriminator, _ := visual["discriminator"].(map[string]any)
	if discriminator["propertyName"] != "kind" {
		t.Fatalf("visualization spec discriminator = %#v", discriminator)
	}
	variants, _ := visual["oneOf"].([]any)
	if len(variants) != 10 {
		t.Fatalf("visualization spec variants = %d, want 10: %#v", len(variants), visual)
	}
	dataState := openAPISchema(t, schemas, "VisualizationDataState")
	stateDiscriminator, _ := dataState["discriminator"].(map[string]any)
	if stateDiscriminator["propertyName"] != "kind" {
		t.Fatalf("visualization data-state discriminator = %#v", stateDiscriminator)
	}
}

func TestDashboardQueryExposesIndependentFilterAndSpatialState(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	schemas := openAPIMap(t, openAPIMap(t, spec, "components"), "schemas")
	request := openAPISchema(t, schemas, "DashboardPageQueryRequest")
	filterState := schemaProperty(t, request, "filterState")
	if filterState["$ref"] != "#/components/schemas/DashboardAppliedFilterInput" {
		t.Fatalf("dashboard filter state is not independently typed: %#v", filterState)
	}
	spatialSelections := schemaProperty(t, request, "spatialSelections")
	if spatialSelections["type"] != "array" {
		t.Fatalf("dashboard spatial selections are not an array: %#v", spatialSelections)
	}
	items, _ := spatialSelections["items"].(map[string]any)
	if items["$ref"] != "#/components/schemas/DashboardSpatialInteractionSelection" {
		t.Fatalf("dashboard spatial selection items are not typed: %#v", spatialSelections)
	}
}

func TestDashboardPageComponentsAreKindDiscriminated(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	schemas := openAPIMap(t, openAPIMap(t, spec, "components"), "schemas")
	component := openAPISchema(t, schemas, "DashboardComponentResponse")
	discriminator, _ := component["discriminator"].(map[string]any)
	if discriminator["propertyName"] != "kind" {
		t.Fatalf("page component discriminator = %#v", discriminator)
	}
	variants, _ := component["oneOf"].([]any)
	if len(variants) != 3 {
		t.Fatalf("page component variants = %d, want 3: %#v", len(variants), component)
	}
	for _, name := range []string{"DashboardVisualComponentResponse", "DashboardSlicerComponentResponse", "DashboardHeaderComponentResponse"} {
		variant := openAPISchema(t, schemas, name)
		allOf, _ := variant["allOf"].([]any)
		base, _ := firstOpenAPIRef(allOf)
		if len(allOf) != 1 || base != "#/components/schemas/DashboardComponentResponseBase" {
			t.Errorf("%s does not refine the shared component schema: %#v", name, variant)
		}
	}
}

func TestDashboardManifestModelsSlicersExplicitly(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	schemas := openAPIMap(t, openAPIMap(t, spec, "components"), "schemas")
	manifest := openAPISchema(t, schemas, "DashboardManifestComponent")
	kind := schemaProperty(t, manifest, "kind")
	assertEnum(t, kind, "visual", "slicer", "header")
	slicer := openAPISchema(t, schemas, "DashboardSlicerComponentResponse")
	assertEnum(t, schemaProperty(t, slicer, "kind"), "slicer")
}

func TestCapabilitiesUseCanonicalEnums(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	schemas := openAPIMap(t, openAPIMap(t, spec, "components"), "schemas")
	assertEnum(t, openAPISchema(t, schemas, "AuthenticationMode"), "bearer")
	assertEnum(t, openAPISchema(t, schemas, "QueryFormat"), "application/json", "application/vnd.apache.arrow.stream")
	assertEnum(t, openAPISchema(t, schemas, "UploadProtocol"), "tus", "s3_multipart")
	assertEnum(t, openAPISchema(t, schemas, "VisualizationRendererID"), "echarts", "tanstack", "html", "maplibre")
	assertEnum(t, openAPISchema(t, schemas, "VisualizationSpecKind"), "cartesian", "point", "proportional", "hierarchy", "polar", "table", "matrix", "pivot", "kpi", "geographic")
}

func operationHasResponseMedia(operation map[string]any, status, media string) bool {
	responses, _ := operation["responses"].(map[string]any)
	response, _ := responses[status].(map[string]any)
	content, _ := response["content"].(map[string]any)
	_, ok := content[media]
	return ok
}

func requiredOperationParameter(t *testing.T, operation map[string]any, location, name string) map[string]any {
	t.Helper()
	parameters, _ := operation["parameters"].([]any)
	for _, raw := range parameters {
		parameter, _ := raw.(map[string]any)
		if parameter["in"] == location && parameter["name"] == name {
			return parameter
		}
	}
	t.Fatalf("operation parameter %s %s is missing", location, name)
	return nil
}

func TestIdealAPIUsesProblemDetailsAndMutationHeaders(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	paths := openAPIMap(t, spec, "paths")
	schemas := openAPIMap(t, openAPIMap(t, spec, "components"), "schemas")
	problem := openAPISchema(t, schemas, "ProblemDetails")
	for _, name := range []string{"type", "title", "status", "detail", "instance", "code", "requestId", "errors"} {
		_ = schemaProperty(t, problem, name)
	}

	for _, tc := range []struct {
		path   string
		method string
	}{
		{"/api/v1/projects/{project}/releases", "post"},
		{"/api/v1/projects/{project}/releases/{release}/finalize", "post"},
		{"/api/v1/projects/{project}/deployments", "post"},
		{"/api/v1/workspaces/{workspace}/refresh-runs", "post"},
		{"/api/v1/agent/conversations/{conversation}/runs", "post"},
	} {
		op := openAPIOperation(t, paths, tc.path, tc.method)
		if !operationHasParameter(op, "header", "Idempotency-Key") {
			t.Errorf("%s %s does not require Idempotency-Key", tc.method, tc.path)
		}
	}

	encoded := string(mustJSON(t, spec))
	if !strings.Contains(encoded, "application/problem+json") {
		t.Fatal("OpenAPI does not declare application/problem+json")
	}
}

func TestIdealAPIUsesBoundedInputsAndBodylessDeletes(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	paths := openAPIMap(t, spec, "paths")
	for _, path := range []string{
		"/api/v1/me/api-tokens/{token}",
		"/api/v1/me/sessions/{session}",
		"/api/v1/service-principals/{servicePrincipal}",
		"/api/v1/workspaces/{workspace}/groups/{group}",
		"/api/v1/workspaces/{workspace}/role-bindings/{binding}",
		"/api/v1/workspaces/{workspace}/grants/{grant}",
		"/api/v1/workspaces/{workspace}/data-policies/{policy}",
	} {
		op := openAPIOperation(t, paths, path, "delete")
		responses := openAPIMap(t, op, "responses")
		if _, ok := responses["204"]; !ok {
			t.Errorf("DELETE %s does not declare 204: %#v", path, responses)
		}
		if _, ok := responses["200"]; ok {
			t.Errorf("DELETE %s still declares a response body", path)
		}
	}

	schemas := openAPIMap(t, openAPIMap(t, spec, "components"), "schemas")
	queryLimit := schemaProperty(t, openAPISchema(t, schemas, "SemanticQueryRequest"), "limit")
	if queryLimit["minimum"] != float64(1) || queryLimit["maximum"] != float64(1000) {
		t.Fatalf("query limit schema = %#v", queryLimit)
	}
	visual := openAPISchema(t, schemas, "VisualizationEnvelope")
	properties := openAPIMap(t, visual, "properties")
	if _, ok := properties["rendererOptions"]; ok {
		t.Fatalf("renderer-specific options leaked at the top level: %#v", properties)
	}
	if _, ok := properties["options"]; ok {
		t.Fatalf("unrestricted visual options leaked at the top level: %#v", properties)
	}
	for _, forbidden := range []string{"shape", "renderer", "extensions", "interaction"} {
		if _, ok := properties[forbidden]; ok {
			t.Fatalf("legacy visual field %q leaked at the top level: %#v", forbidden, properties)
		}
	}
	specProperty := schemaProperty(t, visual, "spec")
	if specProperty["$ref"] != "#/components/schemas/VisualizationSpec" {
		t.Fatalf("visual specification is not the canonical typed IR: %#v", specProperty)
	}
}

func firstOpenAPIRef(values []any) (string, bool) {
	if len(values) == 0 {
		return "", false
	}
	value, ok := values[0].(map[string]any)
	if !ok {
		return "", false
	}
	ref, ok := value["$ref"].(string)
	return ref, ok
}
