package requestmodelgo

import (
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func jsonContent(ref ir.SchemaRef) []ir.BodyContent {
	return []ir.BodyContent{{ContentType: "application/json", BodyKind: "json", Schema: &ref}}
}

func binaryContent() []ir.BodyContent {
	return []ir.BodyContent{{ContentType: "application/octet-stream", BodyKind: "binary", Schema: &ir.SchemaRef{Type: "string", Format: "binary"}}}
}

func textContent() []ir.BodyContent {
	return []ir.BodyContent{{ContentType: "text/plain", BodyKind: "text", Schema: &ir.SchemaRef{Type: "string"}}}
}

func fileContent() []ir.BodyContent {
	return []ir.BodyContent{{ContentType: "application/octet-stream", BodyKind: "file", Schema: &ir.SchemaRef{Type: "string", Format: "binary"}}}
}

func TestEmit_AliasesRequestRoots(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Info: ir.Info{Namespace: "LeapViewAPI"},
		Schemas: map[string]ir.Schema{
			"CreateWidgetRequest": {Type: "object"},
		},
		Endpoints: []ir.Endpoint{{OperationID: "createWidget", RequestBody: &ir.RequestBody{Contents: jsonContent(ir.SchemaRef{Ref: "CreateWidgetRequest"})}}},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	require.Contains(t, string(b), "type GenSchemaCreateWidgetRequest = CreateWidgetRequest")
}

func TestEmit_AliasesAuditPayloadRoots(t *testing.T) {
	doc := ir.Document{
		Schemas:   map[string]ir.Schema{"WidgetAuditPayload": {Type: "object"}},
		Endpoints: []ir.Endpoint{{OperationID: "createWidget", Command: &ir.Command{Audit: ir.AuditPolicy{Payload: &ir.AuditPayload{Schema: ir.SchemaRef{Ref: "WidgetAuditPayload"}}}}}},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	require.Contains(t, string(b), "type GenSchemaWidgetAuditPayload = WidgetAuditPayload")
}

func TestEmit_ReferencesImportedResponseModelsWithoutRegeneratingThem(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "LeapViewAPI"},
		Schemas: map[string]ir.Schema{
			"DashboardResponse": {
				Type: "object", Namespace: "LeapViewAPI",
				Properties: map[string]ir.SchemaProperty{"visual": {Schema: ir.SchemaRef{Ref: "VisualizationEnvelope"}}},
				Required:   []string{"visual"},
			},
			"VisualizationEnvelope": {Type: "object", Namespace: "LeapViewVisualization"},
		},
		Endpoints: []ir.Endpoint{{OperationID: "dashboard", Responses: []ir.Response{{StatusCode: 200, Contents: jsonContent(ir.SchemaRef{Ref: "DashboardResponse"})}}}},
	}

	b, err := Emit(doc, Options{PackageName: "gen", ContractImports: map[string]ContractImport{
		"LeapViewVisualization": {GoPackage: "example.com/project/visualization", GoAlias: "visualizationir"},
	}})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, `visualizationir "example.com/project/visualization"`)
	require.Contains(t, content, "Visual visualizationir.VisualizationEnvelope")
	require.NotContains(t, content, "type VisualizationEnvelope struct")
}

func TestEmit_AliasesNonStructRequestRoots(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"SetDefaultCatalogRequest": {Type: "string"},
		},
		Endpoints: []ir.Endpoint{{OperationID: "setDefaultCatalog", RequestBody: &ir.RequestBody{Contents: jsonContent(ir.SchemaRef{Ref: "SetDefaultCatalogRequest"})}}},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	require.Contains(t, string(b), "type GenSchemaSetDefaultCatalogRequest = SetDefaultCatalogRequest")
}

func TestEmit_SkipsInlineScalarTransportRequestBodies(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"Artifact": {Type: "object"},
		},
		Endpoints: []ir.Endpoint{
			{OperationID: "uploadArtifact", RequestBody: &ir.RequestBody{Required: true, Contents: binaryContent()}},
			{OperationID: "replaceDescription", RequestBody: &ir.RequestBody{Required: true, Contents: textContent()}},
			{OperationID: "uploadFile", RequestBody: &ir.RequestBody{Required: true, Contents: fileContent()}},
			{OperationID: "getArtifact", Responses: []ir.Response{{StatusCode: 200, Contents: jsonContent(ir.SchemaRef{Ref: "Artifact"})}}},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)

	require.Contains(t, content, "type Artifact struct")
	require.Contains(t, content, "type GenSchemaArtifact = Artifact")
	require.NotContains(t, content, "UploadArtifactRequest")
	require.NotContains(t, content, "ReplaceDescriptionRequest")
	require.NotContains(t, content, "UploadFileRequest")
}

func TestEmit_CollectsMultipartPartSchemaRoots(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"Metadata": {Type: "object"},
		},
		Endpoints: []ir.Endpoint{{
			OperationID: "uploadArtifact",
			RequestBody: &ir.RequestBody{Required: true, Contents: []ir.BodyContent{{
				ContentType: "multipart/form-data",
				BodyKind:    "multipart",
				Parts: []ir.MultipartPart{
					{Name: "metadata", WireName: "metadata", PartKind: "model", Required: true, ContentType: "application/json", BodyKind: "json", Schema: &ir.SchemaRef{Ref: "Metadata"}},
					{Name: "blob", WireName: "blob", PartKind: "model", Required: true, ContentType: "application/octet-stream", BodyKind: "file", Schema: &ir.SchemaRef{Type: "string", Format: "binary"}},
				},
			}}},
		}},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	require.Contains(t, string(b), "type GenSchemaMetadata = Metadata")
}

func TestEmit_AliasesSafeDirectResponseSchemas(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"SemanticModel": {},
		},
		Endpoints: []ir.Endpoint{
			{OperationID: "createSemanticModel", Responses: []ir.Response{{StatusCode: 201, Contents: jsonContent(ir.SchemaRef{Ref: "SemanticModel"})}}},
			{OperationID: "createModel", Responses: []ir.Response{{StatusCode: 201, Contents: jsonContent(ir.SchemaRef{Type: "string"}), Extensions: map[string]any{ir.ResponseShapeExtensionKey: map[string]any{"kind": "wrapped_json", "body_type": "Model"}}}}},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)

	require.Contains(t, content, "type GenSchemaSemanticModel = SemanticModel")
	require.Contains(t, content, "type GenSchemaModel = Model")
}

func TestEmit_EmitsAPIGenOwnedGenericResponse(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"GenericResponse": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"data": {Schema: ir.SchemaRef{Ref: "Record"}},
				},
			},
		},
		Endpoints: []ir.Endpoint{
			{OperationID: "listWidgets", Responses: []ir.Response{{StatusCode: 200, Contents: jsonContent(ir.SchemaRef{Ref: "GenericResponse"})}}},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)

	require.Contains(t, content, "type GenSchemaRecord map[string]any")
	require.Contains(t, content, "type GenSchemaGenericResponse struct")
	require.Contains(t, content, "Data *GenSchemaRecord `json:\"data,omitempty\"`")
}

func TestEmit_PreservesSchemaRootWhenResponseShapeMetadataExists(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"GenericResponse": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"data": {Schema: ir.SchemaRef{Ref: "Record"}},
				},
			},
			"PaginatedTags": {},
		},
		Endpoints: []ir.Endpoint{
			{OperationID: "listTags", Responses: []ir.Response{{
				StatusCode: 200,
				Contents:   jsonContent(ir.SchemaRef{Ref: "GenericResponse"}),
				Extensions: map[string]any{ir.ResponseShapeExtensionKey: map[string]any{"kind": "wrapped_json", "body_type": "PaginatedTags"}},
			}}},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)

	require.Contains(t, content, "type GenSchemaPaginatedTags = PaginatedTags")
	require.Contains(t, content, "type GenSchemaGenericResponse struct")
}

func TestEmit_ApigenOwnedSchemaNames(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"HealthResponse": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"status": {Schema: ir.SchemaRef{Type: "string"}},
				},
				Required: []string{"status"},
			},
			"QueryResponse": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"columns": {Schema: ir.SchemaRef{Type: "array"}},
				},
				Required: []string{"columns"},
			},
		},
		Endpoints: []ir.Endpoint{
			{OperationID: "getHealth", Responses: []ir.Response{{StatusCode: 200, Contents: jsonContent(ir.SchemaRef{Ref: "HealthResponse"})}}},
			{OperationID: "executeQuery", Responses: []ir.Response{{StatusCode: 200, Contents: jsonContent(ir.SchemaRef{Ref: "QueryResponse"})}}},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)

	require.Contains(t, content, "type GenSchemaHealthResponse struct")
	require.Contains(t, content, "Status string `json:\"status\"`")
	require.Contains(t, content, "type GenSchemaQueryResponse struct")
	require.Contains(t, content, "Columns []any `json:\"columns\"`")
}

func TestEmit_PreservesIntegerFormatsRecursively(t *testing.T) {
	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"Metrics": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"small":   {Schema: ir.SchemaRef{Type: "integer", Format: "int32"}},
					"large":   {Schema: ir.SchemaRef{Type: "integer", Format: "int64"}},
					"history": {Schema: ir.SchemaRef{Type: "array", Items: &ir.SchemaRef{Type: "integer", Format: "int64"}}},
				},
				Required: []string{"small", "large", "history"},
			},
		},
		Endpoints: []ir.Endpoint{{OperationID: "putMetrics", RequestBody: &ir.RequestBody{Contents: jsonContent(ir.SchemaRef{Ref: "Metrics"})}}},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "Small int32 `json:\"small\"`")
	require.Contains(t, content, "Large int64 `json:\"large\"`")
	require.Contains(t, content, "History []int64 `json:\"history\"`")
}

func TestEmit_PreservesRecordValueTypesRecursively(t *testing.T) {
	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"Item": {Type: "object"},
			"Records": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"strings": {Schema: ir.SchemaRef{Type: "object", AdditionalProperties: &ir.AdditionalProperties{Schema: &ir.SchemaRef{Type: "string"}}}},
					"counts":  {Schema: ir.SchemaRef{Type: "object", AdditionalProperties: &ir.AdditionalProperties{Schema: &ir.SchemaRef{Type: "integer", Format: "int64"}}}},
					"items":   {Schema: ir.SchemaRef{Type: "object", AdditionalProperties: &ir.AdditionalProperties{Schema: &ir.SchemaRef{Ref: "Item"}}}},
					"nested":  {Schema: ir.SchemaRef{Type: "object", AdditionalProperties: &ir.AdditionalProperties{Schema: &ir.SchemaRef{Type: "array", Items: &ir.SchemaRef{Type: "object", AdditionalProperties: &ir.AdditionalProperties{Schema: &ir.SchemaRef{Type: "boolean"}}}}}}},
					"unknown": {Schema: ir.SchemaRef{Type: "object", AdditionalProperties: &ir.AdditionalProperties{Any: true}}},
				},
				Required: []string{"strings", "counts", "items", "nested", "unknown"},
			},
		},
		Endpoints: []ir.Endpoint{{OperationID: "putRecords", RequestBody: &ir.RequestBody{Contents: jsonContent(ir.SchemaRef{Ref: "Records"})}}},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "Strings map[string]string `json:\"strings\"`")
	require.Contains(t, content, "Counts map[string]int64 `json:\"counts\"`")
	require.Contains(t, content, "Items map[string]Item `json:\"items\"`")
	require.Contains(t, content, "Nested map[string][]map[string]bool `json:\"nested\"`")
	require.Contains(t, content, "Unknown map[string]any `json:\"unknown\"`")
}

func TestEmit_PreservesDiscriminatedUnionInRequestModels(t *testing.T) {
	doc := ir.Document{
		Schemas: map[string]ir.Schema{
			"Visual":      {Type: "union", OneOf: []ir.SchemaRef{{Ref: "ChartVisual"}, {Ref: "TextVisual"}}, Discriminator: &ir.Discriminator{PropertyName: "shape", Mapping: map[string]string{"chart": "ChartVisual", "text": "TextVisual"}}},
			"VisualBase":  {Type: "object", Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"shape"}},
			"ChartVisual": {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}, Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"chart"}}}}, Required: []string{"shape"}},
			"TextVisual":  {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}, Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"text"}}}}, Required: []string{"shape"}},
			"Request":     {Type: "object", Properties: map[string]ir.SchemaProperty{"visuals": {Schema: ir.SchemaRef{Type: "array", Items: &ir.SchemaRef{Ref: "Visual"}}}}, Required: []string{"visuals"}},
		},
		Endpoints: []ir.Endpoint{{OperationID: "putVisuals", RequestBody: &ir.RequestBody{Contents: jsonContent(ir.SchemaRef{Ref: "Request"})}}},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "Visuals []Visual")
	require.Contains(t, content, "type VisualVariant interface")
	require.Contains(t, content, "func (*ChartVisual) isVisualVariant()")
	require.NotContains(t, content, "func (ChartVisual) isVisualVariant()")
	require.Contains(t, content, "type VisualVisitor interface")
	require.Contains(t, content, "func (value *Visual) Base() (*VisualBase, error)")
	require.Contains(t, content, "func (value *Visual) UnmarshalJSON")
}

func TestEmit_FailsForUnresolvedRequestBodySchema(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		Endpoints: []ir.Endpoint{
			{
				OperationID: "createWidget",
				RequestBody: &ir.RequestBody{Contents: jsonContent(ir.SchemaRef{Type: "object"})},
			},
		},
	}

	_, err := Emit(doc, Options{})
	require.Error(t, err)
	require.ErrorContains(t, err, "request body generation")
	require.ErrorContains(t, err, "createWidget")
}

func TestEmit_DoesNotEmitCompatibilityPlaceholders(t *testing.T) {
	t.Helper()

	b, err := Emit(ir.Document{}, Options{})
	require.NoError(t, err)
	content := string(b)

	require.NotContains(t, content, "type GenericRequest struct")
	require.NotContains(t, content, "type GenericResponse struct")
	require.NotContains(t, content, "JSONRequestBody")
}
