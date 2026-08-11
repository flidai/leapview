package modelgo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func TestEmit_GeneratesContractRootsAndDependencies(t *testing.T) {
	doc := contractDoc()

	b, err := Emit(doc, Options{PackageName: "contracts"})
	require.NoError(t, err)
	content := string(b)

	require.Contains(t, content, "package contracts")
	require.Contains(t, content, "type DashboardEnvelope struct")
	require.Contains(t, content, "Page DashboardPageSignal `json:\"page\"`")
	require.Contains(t, content, "Visuals map[string]DashboardVisual `json:\"visuals\"`")
	require.Contains(t, content, "type DashboardVisual struct")
	require.Contains(t, content, "Data map[string]any `json:\"data\"`")
	require.Contains(t, content, "Note *string `json:\"note,omitempty\"`")
}

func TestEmit_PreservesGoInitialismsInJSONFieldNames(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "Signals"},
		Schemas: map[string]ir.Schema{
			"Envelope": {Type: "object", Namespace: "Signals", Properties: map[string]ir.SchemaProperty{
				"dashboardId": {Schema: ir.SchemaRef{Type: "string"}},
				"urlParams":   {Schema: ir.SchemaRef{Type: "string"}},
			}, Required: []string{"dashboardId", "urlParams"}},
		},
		Contracts: []ir.Contract{{Name: "Envelope", Schema: ir.SchemaRef{Ref: "Envelope"}}},
	}
	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	require.Contains(t, string(b), "DashboardID string")
	require.Contains(t, string(b), "URLParams string")
}

func TestEmit_GeneratesStrictDiscriminatedUnion(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "LeapViewSignals"},
		Schemas: map[string]ir.Schema{
			"Visual":      {Type: "union", OneOf: []ir.SchemaRef{{Ref: "ChartVisual"}, {Ref: "TextVisual"}}, Discriminator: &ir.Discriminator{PropertyName: "shape", Mapping: map[string]string{"chart": "ChartVisual", "text": "TextVisual"}}},
			"VisualBase":  {Type: "object", Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"shape"}},
			"ChartVisual": {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}, Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"chart"}}}, "points": {Schema: ir.SchemaRef{Type: "array", Items: &ir.SchemaRef{Type: "integer"}}}}, Required: []string{"shape", "points"}},
			"TextVisual":  {Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"}, Properties: map[string]ir.SchemaProperty{"shape": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"text"}}}}, Required: []string{"shape"}},
		},
		Contracts: []ir.Contract{{Name: "visual", Schema: ir.SchemaRef{Ref: "Visual"}}},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "type VisualVariant interface")
	require.Contains(t, content, "Value VisualVariant")
	require.Contains(t, content, "VisualBase")
	require.Contains(t, content, "func (*ChartVisual) isVisualVariant()")
	require.NotContains(t, content, "func (ChartVisual) isVisualVariant()")
	require.NotContains(t, content, "case ChartVisual:")
	require.Contains(t, content, "type VisualVisitor interface")
	require.Contains(t, content, "VisitChartVisual(*ChartVisual) error")
	require.Contains(t, content, "func (value *Visual) Visit(visitor VisualVisitor) error")
	require.Contains(t, content, "func (value *Visual) Base() (*VisualBase, error)")
	require.Contains(t, content, "func (value *Visual) Shape() (string, error)")
	require.Contains(t, content, "func (value *Visual) UnmarshalJSON")
	require.Contains(t, content, "decoder.DisallowUnknownFields()")
	require.Contains(t, content, `case "chart":`)
	require.Contains(t, content, `if _, ok := fields["points"]; !ok`)
	require.Contains(t, content, `required property points is missing`)
}

func TestEmit_ReferencesImportedContractNamespaceWithoutRegeneratingIt(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "LeapViewSignals"},
		Schemas: map[string]ir.Schema{
			"DashboardEnvelope": {
				Type: "object", Namespace: "LeapViewSignals",
				Properties: map[string]ir.SchemaProperty{"visual": {Schema: ir.SchemaRef{Ref: "VisualizationEnvelope"}}},
				Required:   []string{"visual"},
			},
			"VisualizationEnvelope": {Type: "object", Namespace: "LeapViewVisualization"},
		},
		Contracts: []ir.Contract{{Name: "DashboardEnvelope", Schema: ir.SchemaRef{Ref: "DashboardEnvelope"}}},
	}

	b, err := Emit(doc, Options{PackageName: "signals", ContractImports: map[string]ContractImport{
		"LeapViewVisualization": {GoPackage: "example.com/project/visualization", GoAlias: "visualizationir"},
	}})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, `visualizationir "example.com/project/visualization"`)
	require.Contains(t, content, "Visual visualizationir.VisualizationEnvelope")
	require.NotContains(t, content, "type VisualizationEnvelope struct")
}

func TestEmit_ImportedProducerAndConsumerPackagesCompile(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "LeapViewSignals"},
		Schemas: map[string]ir.Schema{
			"DashboardEnvelope": {
				Type: "object", Namespace: "LeapViewSignals",
				Properties: map[string]ir.SchemaProperty{"visual": {Schema: ir.SchemaRef{Ref: "VisualizationEnvelope"}}},
				Required:   []string{"visual"},
			},
			"VisualizationEnvelope": {Type: "object", Namespace: "LeapViewVisualization"},
		},
		Contracts: []ir.Contract{{Name: "DashboardEnvelope", Schema: ir.SchemaRef{Ref: "DashboardEnvelope"}}},
	}

	generated, err := Emit(doc, Options{PackageName: "signals", ContractImports: map[string]ContractImport{
		"LeapViewVisualization": {GoPackage: "example.com/contracts/visualization", GoAlias: "visualizationir"},
	}})
	require.NoError(t, err)

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "signals"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "visualization"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/contracts\n\ngo 1.24\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "visualization", "models.go"), []byte("package visualization\n\ntype VisualizationEnvelope struct{}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "signals", "models.gen.go"), generated, 0o600))
	command := exec.Command("go", "test", "./...")
	command.Dir = root
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func contractDoc() ir.Document {
	return ir.Document{
		Contracts: []ir.Contract{{Name: "DashboardEnvelope", Schema: ir.SchemaRef{Ref: "DashboardEnvelope"}}},
		Schemas: map[string]ir.Schema{
			"DashboardEnvelope": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"page":    {Schema: ir.SchemaRef{Ref: "DashboardPageSignal"}},
					"visuals": {Schema: ir.SchemaRef{Type: "object", AdditionalProperties: &ir.AdditionalProperties{Schema: &ir.SchemaRef{Ref: "DashboardVisual"}}}},
				},
				PropertyOrder: []string{"page", "visuals"},
				Required:      []string{"page", "visuals"},
			},
			"DashboardPageSignal": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"dashboardId": {Schema: ir.SchemaRef{Type: "string"}},
				},
				Required: []string{"dashboardId"},
			},
			"DashboardVisual": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"id":   {Schema: ir.SchemaRef{Type: "string"}},
					"data": {Schema: ir.SchemaRef{Type: "object", AdditionalProperties: &ir.AdditionalProperties{Schema: &ir.SchemaRef{}}}},
					"note": {Schema: ir.SchemaRef{Type: "string"}},
				},
				PropertyOrder: []string{"id", "data", "note"},
				Required:      []string{"id", "data"},
			},
		},
	}
}
