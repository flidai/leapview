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
	require.Contains(t, content, "Page DashboardPageSignal `json:\"page\" yaml:\"page\"`")
	require.Contains(t, content, "Visuals map[string]DashboardVisual `json:\"visuals\" yaml:\"visuals\"`")
	require.Contains(t, content, "type DashboardVisual struct")
	require.Contains(t, content, "Data map[string]any `json:\"data\" yaml:\"data\"`")
	require.Contains(t, content, "Note *string `json:\"note,omitempty\" yaml:\"note,omitempty\"`")
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
	require.NotContains(t, content, "UnmarshalYAML")
	require.Contains(t, content, "decoder.DisallowUnknownFields()")
	require.Contains(t, content, `case "chart":`)
	require.Contains(t, content, `if _, ok := fields["points"]; !ok`)
	require.Contains(t, content, `required property points is missing`)
}

func TestEmit_GeneratesStrictScalarObjectUnion(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "Dashboard"},
		Schemas: map[string]ir.Schema{
			"Selection": {Type: "union", OneOf: []ir.SchemaRef{{Type: "string"}, {Ref: "Reference"}}},
			"Reference": {Type: "object", Properties: map[string]ir.SchemaProperty{"name": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"name"}},
		},
		Contracts: []ir.Contract{{Name: "selection", Schema: ir.SchemaRef{Ref: "Selection"}}},
	}
	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "type Selection struct {")
	require.Contains(t, content, "String *string")
	require.Contains(t, content, "Reference *Reference")
	require.Contains(t, content, "func (value Selection) MarshalJSON()")
	require.Contains(t, content, "expected a string or object")
	require.NotContains(t, content, "type Selection = any")
}

func TestEmit_GeneratesStrictObjectUnion(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "Pipeline"},
		Schemas: map[string]ir.Schema{
			"Spec": {Type: "union", OneOf: []ir.SchemaRef{{Ref: "ManualSpec"}, {Ref: "ScheduledSpec"}}},
			"Selection": {Type: "object", Properties: map[string]ir.SchemaProperty{
				"semanticModel": {Schema: ir.SchemaRef{Type: "string"}},
			}, Required: []string{"semanticModel"}},
			"ManualSpec": {Type: "object", Properties: map[string]ir.SchemaProperty{
				"selection": {Schema: ir.SchemaRef{Ref: "Selection"}},
			}, Required: []string{"selection"}},
			"ScheduledSpec": {Type: "object", Properties: map[string]ir.SchemaProperty{
				"selection": {Schema: ir.SchemaRef{Ref: "Selection"}},
				"schedules": {Schema: ir.SchemaRef{Type: "object"}},
			}, Required: []string{"selection", "schedules"}},
		},
		Contracts: []ir.Contract{{Name: "spec", Schema: ir.SchemaRef{Ref: "Spec"}}},
	}
	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "type SpecVariant interface")
	require.Contains(t, content, `fields["schedules"]`)
	require.Contains(t, content, "decoder.DisallowUnknownFields()")
	require.Contains(t, content, "object matches both")
}

func TestEmit_RejectsUnsupportedScalarObjectUnions(t *testing.T) {
	tests := []struct {
		name   string
		oneOf  []ir.SchemaRef
		schema map[string]ir.Schema
		want   string
	}{
		{
			name:  "multiple object branches",
			oneOf: []ir.SchemaRef{{Type: "string"}, {Ref: "FirstReference"}, {Ref: "SecondReference"}},
			schema: map[string]ir.Schema{
				"FirstReference":  {Type: "object"},
				"SecondReference": {Type: "object"},
			},
			want: "exactly one object-model branch",
		},
		{
			name:  "multiple scalar branches",
			oneOf: []ir.SchemaRef{{Type: "integer"}, {Type: "number"}, {Ref: "Reference"}},
			schema: map[string]ir.Schema{
				"Reference": {Type: "object"},
			},
			want: "exactly one scalar branch",
		},
		{
			name:  "reference is not object",
			oneOf: []ir.SchemaRef{{Type: "string"}, {Ref: "Reference"}},
			schema: map[string]ir.Schema{
				"Reference": {Type: "string"},
			},
			want: "object-model schema",
		},
		{
			name:   "inline object branch",
			oneOf:  []ir.SchemaRef{{Type: "string"}, {Type: "object"}},
			schema: map[string]ir.Schema{},
			want:   "scalar or named object-model ref",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.schema["Selection"] = ir.Schema{Type: "union", OneOf: test.oneOf}
			doc := ir.Document{Info: ir.Info{Namespace: "Dashboard"}, Schemas: test.schema, Contracts: []ir.Contract{{Name: "selection", Schema: ir.SchemaRef{Ref: "Selection"}}}}
			_, err := Emit(doc, Options{})
			require.Error(t, err)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestEmit_GeneratedScalarObjectUnionRuntime(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "Dashboard"},
		Schemas: map[string]ir.Schema{
			"Selection": {Type: "union", OneOf: []ir.SchemaRef{{Type: "string"}, {Ref: "Reference"}}},
			"Reference": {Type: "object", Properties: map[string]ir.SchemaProperty{"name": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"name"}},
		},
		Contracts: []ir.Contract{{Name: "selection", Schema: ir.SchemaRef{Ref: "Selection"}}},
	}
	generated, err := Emit(doc, Options{PackageName: "generated"})
	require.NoError(t, err)

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/generated\n\ngo 1.24\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "models.gen.go"), generated, 0o600))
	testSource := `package generated

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScalarObjectUnionRuntime(t *testing.T) {
	var scalar Selection
	if err := json.Unmarshal([]byte("month"), &scalar); err == nil { t.Fatal("unquoted scalar unexpectedly decoded") }
	if err := json.Unmarshal([]byte(` + "`\"month\"`" + `), &scalar); err != nil { t.Fatal(err) }
	if scalar.String == nil || *scalar.String != "month" || scalar.Reference != nil { t.Fatalf("scalar variant = %#v", scalar) }
	encoded, err := json.Marshal(scalar)
	if err != nil || string(encoded) != ` + "`\"month\"`" + ` { t.Fatalf("scalar marshal = %s, %v", encoded, err) }

	var object Selection
	if err := json.Unmarshal([]byte(` + "`{\"name\":\"month\"}`" + `), &object); err != nil { t.Fatal(err) }
	if object.Reference == nil || object.Reference.Name != "month" || object.String != nil { t.Fatalf("object variant = %#v", object) }
	encoded, err = json.Marshal(object)
	if err != nil || string(encoded) != ` + "`{\"name\":\"month\"}`" + ` { t.Fatalf("object marshal = %s, %v", encoded, err) }

	for _, input := range []string{"true", "null", "1", "[]"} {
		var rejected Selection
		if err := json.Unmarshal([]byte(input), &rejected); err == nil { t.Errorf("wrong scalar kind %s accepted", input) }
	}
	for _, input := range []string{` + "`{}`" + `, ` + "`{\"name\":\"month\",\"extra\":true}`" + `} {
		var rejected Selection
		if err := json.Unmarshal([]byte(input), &rejected); err == nil { t.Errorf("invalid object %s accepted", input) }
	}
	name := "month"
	if _, err := json.Marshal(Selection{String: &name, Reference: &Reference{Name: name}}); err == nil { t.Fatal("multiple set variants accepted") }
	if _, err := json.Marshal(Selection{}); err == nil { t.Fatal("empty union accepted") }
	if err := json.Unmarshal([]byte(` + "`{\"name\":\"month\"} trailing`" + `), &Selection{}); err == nil { t.Fatal("trailing object data accepted") }
	if !strings.Contains(string(encoded), "name") { t.Fatal("object output lost required field") }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "models_test.go"), []byte(testSource), 0o600))
	command := exec.Command("go", "test", "./...")
	command.Dir = root
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestEmit_GeneratedObjectUnionUsesRequiredStringLiterals(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "Filter"},
		Schemas: map[string]ir.Schema{
			"Filter": {Type: "union", OneOf: []ir.SchemaRef{{Ref: "EqualsFilter"}, {Ref: "NotEqualsFilter"}}},
			"EqualsFilter": {Type: "object", Properties: map[string]ir.SchemaProperty{
				"operator": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"eq"}}},
				"value":    {Schema: ir.SchemaRef{}},
			}, Required: []string{"operator", "value"}},
			"NotEqualsFilter": {Type: "object", Properties: map[string]ir.SchemaProperty{
				"operator": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"neq"}}},
				"value":    {Schema: ir.SchemaRef{}},
			}, Required: []string{"operator", "value"}},
		},
		Contracts: []ir.Contract{{Name: "filter", Schema: ir.SchemaRef{Ref: "Filter"}}},
	}

	generated, err := Emit(doc, Options{PackageName: "generated"})
	require.NoError(t, err)
	require.Contains(t, string(generated), `value, ok := actual.(string); !ok || value != "eq"`)
	require.Contains(t, string(generated), `value, ok := actual.(string); !ok || value != "neq"`)

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/generated\n\ngo 1.24\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "models.gen.go"), generated, 0o600))
	testSource := `package generated

import (
	"encoding/json"
	"testing"
)

func TestObjectUnionRequiredStringLiterals(t *testing.T) {
	var equals Filter
	if err := json.Unmarshal([]byte("{\"operator\":\"eq\",\"value\":\"one\"}"), &equals); err != nil { t.Fatal(err) }
	if _, ok := equals.Value.(*EqualsFilter); !ok { t.Fatalf("decoded equals filter has type %T", equals.Value) }

	var notEquals Filter
	if err := json.Unmarshal([]byte("{\"operator\":\"neq\",\"value\":\"one\"}"), &notEquals); err != nil { t.Fatal(err) }
	if _, ok := notEquals.Value.(*NotEqualsFilter); !ok { t.Fatalf("decoded not-equals filter has type %T", notEquals.Value) }

	var exact Filter
	if err := json.Unmarshal([]byte("{\"operator\":\"eq\",\"value\":9007199254740993}"), &exact); err != nil { t.Fatal(err) }
	number, ok := exact.Value.(*EqualsFilter).Value.(json.Number)
	if !ok || number.String() != "9007199254740993" { t.Fatalf("numeric value = %#v (%T)", exact.Value.(*EqualsFilter).Value, exact.Value.(*EqualsFilter).Value) }

	for _, input := range []string{
		"{\"operator\":\"other\",\"value\":\"one\"}",
		"{\"operator\":null,\"value\":\"one\"}",
	} {
		var rejected Filter
		if err := json.Unmarshal([]byte(input), &rejected); err == nil { t.Errorf("invalid operator accepted: %s", input) }
	}
}
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "models_test.go"), []byte(testSource), 0o600))
	command := exec.Command("go", "test", "./...")
	command.Dir = root
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestEmit_ArrayUnionUsesSliceWireType(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "Values"},
		Schemas: map[string]ir.Schema{
			"AllowedValues": {Type: "union", OneOf: []ir.SchemaRef{
				{Type: "array", Items: &ir.SchemaRef{Type: "string"}},
				{Type: "array", Items: &ir.SchemaRef{Type: "boolean"}},
			}},
			"Payload": {Type: "object", Properties: map[string]ir.SchemaProperty{
				"values": {Schema: ir.SchemaRef{Ref: "AllowedValues"}},
			}, Required: []string{"values"}},
		},
		Contracts: []ir.Contract{{Name: "payload", Schema: ir.SchemaRef{Ref: "Payload"}}},
	}

	generated, err := Emit(doc, Options{PackageName: "generated"})
	require.NoError(t, err)
	require.Contains(t, string(generated), "type AllowedValues []any")
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

func TestEmit_GeneratedModelsCompileAndRoundTripJSONAndYAML(t *testing.T) {
	doc := ir.Document{
		Info: ir.Info{Namespace: "LeapViewSignals"},
		Schemas: map[string]ir.Schema{
			"Visual": {
				Type:  "union",
				OneOf: []ir.SchemaRef{{Ref: "ChartVisual"}, {Ref: "TextVisual"}},
				Discriminator: &ir.Discriminator{PropertyName: "shape", Mapping: map[string]string{
					"chart": "ChartVisual",
					"text":  "TextVisual",
				}},
			},
			"VisualBase": {
				Type:       "object",
				Properties: map[string]ir.SchemaProperty{"visualId": {Schema: ir.SchemaRef{Type: "string"}}},
				Required:   []string{"visualId"},
			},
			"ChartVisual": {
				Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"},
				Properties: map[string]ir.SchemaProperty{
					"shape":       {Schema: ir.SchemaRef{Type: "string", Enum: []string{"chart"}}},
					"points":      {Schema: ir.SchemaRef{Type: "array", Items: &ir.SchemaRef{Type: "integer"}}},
					"displayMode": {Schema: ir.SchemaRef{Type: "string"}},
				},
				Required: []string{"shape", "points"},
			},
			"TextVisual": {
				Type: "object", Base: &ir.SchemaRef{Ref: "VisualBase"},
				Properties: map[string]ir.SchemaProperty{
					"shape": {Schema: ir.SchemaRef{Type: "string", Enum: []string{"text"}}},
					"title": {Schema: ir.SchemaRef{Type: "string"}},
				},
				Required: []string{"shape", "title"},
			},
		},
		Contracts: []ir.Contract{{Name: "visual", Schema: ir.SchemaRef{Ref: "Visual"}}},
	}

	generated, err := Emit(doc, Options{PackageName: "generated"})
	require.NoError(t, err)
	require.NotContains(t, string(generated), "UnmarshalYAML")

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/generated\n\ngo 1.25.8\n\nrequire go.yaml.in/yaml/v4 v4.0.0-rc.4\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "models.gen.go"), generated, 0o600))
	testSource := `package generated

import (
	"encoding/json"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v4"
)

func TestGeneratedRoundTrip(t *testing.T) {
	displayMode := "compact"
	original := ChartVisual{
		VisualBase:  VisualBase{VisualID: "v-1"},
		Shape:       "chart",
		Points:      []int32{1, 2},
		DisplayMode: &displayMode,
	}
	jsonBytes, err := json.Marshal(original)
	if err != nil { t.Fatal(err) }
	yamlBytes, err := yaml.Marshal(original)
	if err != nil { t.Fatal(err) }
	var jsonFields map[string]any
	if err := json.Unmarshal(jsonBytes, &jsonFields); err != nil { t.Fatal(err) }
	if _, ok := jsonFields["displayMode"]; !ok { t.Fatalf("JSON omitted camelCase key: %s", jsonBytes) }
	if _, ok := jsonFields["displaymode"]; ok { t.Fatalf("JSON used non-camelCase key: %s", jsonBytes) }
	var yamlFields map[string]any
	if err := yaml.Unmarshal(yamlBytes, &yamlFields); err != nil { t.Fatal(err) }
	if _, ok := yamlFields["displayMode"]; !ok { t.Fatalf("YAML omitted camelCase key: %s", yamlBytes) }
	if _, ok := yamlFields["displaymode"]; ok { t.Fatalf("YAML used non-camelCase key: %s", yamlBytes) }
	var fromJSON, fromYAML ChartVisual
	if err := json.Unmarshal(jsonBytes, &fromJSON); err != nil { t.Fatal(err) }
	if err := yaml.Unmarshal(yamlBytes, &fromYAML); err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(original, fromJSON) || !reflect.DeepEqual(original, fromYAML) {
		t.Fatalf("round-trip mismatch: original=%#v json=%#v yaml=%#v", original, fromJSON, fromYAML)
	}

	var union Visual
	if err := json.Unmarshal([]byte("{\"visualId\":\"v-1\",\"shape\":\"chart\",\"points\":[1]}"), &union); err != nil { t.Fatal(err) }
	if _, ok := union.Value.(*ChartVisual); !ok { t.Fatalf("decoded union has type %T", union.Value) }
	if err := json.Unmarshal([]byte("{\"visualId\":\"v-1\",\"shape\":\"other\",\"points\":[1]}"), &Visual{}); err == nil { t.Fatal("unknown discriminator accepted") }
	if err := json.Unmarshal([]byte("{\"visualId\":\"v-1\",\"shape\":\"chart\",\"points\":[1],\"title\":\"wrong variant\"}"), &Visual{}); err == nil { t.Fatal("foreign variant field accepted") }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "models_test.go"), []byte(testSource), 0o600))
	command := exec.Command("go", "test", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
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
