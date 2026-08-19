package query

import (
	_ "embed"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
	"gopkg.in/yaml.v3"
)

// expressionConformanceFixture is intentionally data-only. The test builds a
// semantic model from this fixture and exercises the exported expression,
// activation compiler, and planner APIs. It does not inspect parser nodes or
// use a second evaluator as a source of truth.
type expressionConformanceFixture struct {
	Model expressionConformanceModel  `yaml:"model"`
	Cases []expressionConformanceCase `yaml:"cases"`
}

type expressionConformanceModel struct {
	Name    string                             `yaml:"name"`
	Dataset string                             `yaml:"dataset"`
	Fields  map[string]expressionFixtureField  `yaml:"fields"`
	Metrics map[string]expressionFixtureMetric `yaml:"metrics"`
}

type expressionFixtureField struct {
	Datatype string `yaml:"datatype"`
}

type expressionFixtureMetric struct {
	Type        string `yaml:"type"`
	Dataset     string `yaml:"dataset"`
	Aggregation string `yaml:"aggregation"`
	Input       string `yaml:"input"`
	Empty       string `yaml:"empty"`
	Unit        string `yaml:"unit"`
	Expression  string `yaml:"expression"`
	Numerator   string `yaml:"numerator"`
	Denominator string `yaml:"denominator"`
}

type expressionConformanceCase struct {
	Name       string                             `yaml:"name"`
	Target     string                             `yaml:"target"`
	Expression string                             `yaml:"expression"`
	Unit       string                             `yaml:"unit"`
	Values     map[string]expressionFixtureValue  `yaml:"values"`
	Metrics    map[string]expressionFixtureMetric `yaml:"metrics"`
	Expect     expressionConformanceExpectation   `yaml:"expect"`
}

type expressionFixtureValue struct {
	Kind  string `yaml:"kind"`
	Value string `yaml:"value"`
}

type expressionConformanceExpectation struct {
	Accepted     bool                       `yaml:"accepted"`
	FailureAt    string                     `yaml:"failure_at"`
	Error        string                     `yaml:"error"`
	References   []string                   `yaml:"references"`
	Dependencies []string                   `yaml:"dependencies"`
	Result       *expressionFixtureValue    `yaml:"result"`
	Datatype     string                     `yaml:"datatype"`
	Unit         string                     `yaml:"unit"`
	Plan         *expressionConformancePlan `yaml:"plan"`
}

type expressionConformancePlan struct {
	Node        string   `yaml:"node"`
	Function    string   `yaml:"function"`
	Refs        []string `yaml:"refs"`
	Literals    []string `yaml:"literals"`
	Numerator   string   `yaml:"numerator"`
	Denominator string   `yaml:"denominator"`
	ResultType  string   `yaml:"result_type"`
}

//go:embed testdata/expression_conformance.yaml
var expressionConformanceYAML []byte

func TestExpressionConformanceCorpus(t *testing.T) {
	var fixture expressionConformanceFixture
	if err := yaml.Unmarshal(expressionConformanceYAML, &fixture); err != nil {
		t.Fatalf("decode expression conformance fixture: %v", err)
	}
	if fixture.Model.Dataset == "" || len(fixture.Model.Fields) == 0 || len(fixture.Model.Metrics) == 0 {
		t.Fatal("expression conformance fixture has no executable model")
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("expression conformance fixture has no cases")
	}

	for _, testCase := range fixture.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			runExpressionConformanceCase(t, fixture.Model, testCase)
		})
	}
}

func runExpressionConformanceCase(t *testing.T, spec expressionConformanceModel, testCase expressionConformanceCase) {
	t.Helper()
	if testCase.Target == "" {
		t.Fatal("target metric is required")
	}

	var parsed semanticmodel.Expression
	var parseErr error
	if testCase.Expression != "" {
		parsed, parseErr = semanticmodel.ParseExpression(testCase.Expression)
	}
	if testCase.Expect.FailureAt == "parse" {
		if parseErr == nil {
			t.Fatalf("ParseExpression(%q) succeeded", testCase.Expression)
		}
		assertCorpusError(t, parseErr, testCase.Expect.Error)
		return
	}
	if parseErr != nil {
		t.Fatalf("ParseExpression(%q): %v", testCase.Expression, parseErr)
	}

	model := expressionConformanceSemanticModel(spec)
	for name, metricSpec := range testCase.Metrics {
		model.Metrics[name] = expressionFixtureMetricToModel(metricSpec)
	}
	if testCase.Expression != "" {
		metric := model.Metrics[testCase.Target]
		metric.Type = "derived"
		metric.Expression = testCase.Expression
		metric.Unit = testCase.Unit
		model.Metrics[testCase.Target] = metric
	}

	// CompileModel is the activation/compiler boundary. The planner is then
	// constructed from the same authored model so PlanIR is tested separately
	// from semantic validation and dependency compilation.
	compiled, compileErr := CompileModel(model)
	if testCase.Expect.FailureAt == "compile" {
		if compileErr == nil {
			t.Fatalf("CompileModel() succeeded for invalid case")
		}
		assertCorpusError(t, compileErr, testCase.Expect.Error)
		return
	}
	if compileErr != nil {
		t.Fatalf("CompileModel(): %v", compileErr)
	}
	if compiled == nil {
		t.Fatal("CompileModel() returned a nil compiled model")
	}
	if !testCase.Expect.Accepted {
		t.Fatal("invalid case did not declare parse or compile failure")
	}
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatalf("NewCompiledPlanner(): %v", err)
	}

	compiledMetric, ok := planner.CompiledModel().Metric(testCase.Target)
	if !ok {
		t.Fatalf("compiled metric %q is missing", testCase.Target)
	}
	if got, want := compiledMetric.Dependencies, testCase.Expect.Dependencies; !reflect.DeepEqual(got, want) {
		t.Fatalf("dependencies = %#v, want %#v", got, want)
	}
	if testCase.Expect.Unit != "" && compiledMetric.Unit != testCase.Expect.Unit {
		t.Fatalf("unit = %q, want %q", compiledMetric.Unit, testCase.Expect.Unit)
	}
	if testCase.Expect.Datatype != "" {
		got, err := model.MetricDataType(testCase.Target)
		if err != nil {
			t.Fatalf("MetricDataType(%q): %v", testCase.Target, err)
		}
		if string(got) != testCase.Expect.Datatype {
			t.Fatalf("datatype = %q, want %q", got, testCase.Expect.Datatype)
		}
	}

	if testCase.Expression != "" {
		if got, want := parsed.References(), testCase.Expect.References; !reflect.DeepEqual(got, want) {
			t.Fatalf("references = %#v, want %#v", got, want)
		}
		if testCase.Expect.Result != nil {
			got, err := parsed.Evaluate(func(reference string) (any, error) {
				value, ok := testCase.Values[reference]
				if !ok {
					return nil, fmt.Errorf("fixture value for %q is missing", reference)
				}
				return expressionFixtureRuntimeValue(value)
			})
			if err != nil {
				t.Fatalf("Evaluate(): %v", err)
			}
			want, err := expressionFixtureRuntimeValue(*testCase.Expect.Result)
			if err != nil {
				t.Fatalf("expected result: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Evaluate() = %#v (%T), want %#v (%T)", got, got, want, want)
			}
		}
	}

	if testCase.Expect.Plan == nil {
		return
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: testCase.Target}}})
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	assertExpressionConformancePlan(t, plan, testCase.Target, *testCase.Expect.Plan)
}

func expressionConformanceSemanticModel(spec expressionConformanceModel) *semanticmodel.Model {
	fields := make(map[string]semanticmodel.MetricDimension, len(spec.Fields))
	for name, field := range spec.Fields {
		datatype := semanticmodel.LogicalDataType(field.Datatype)
		fields[name] = semanticmodel.MetricDimension{Type: strings.ToLower(string(datatype)), Datatype: datatype}
	}
	table := semanticmodel.Table{
		ModelName:   spec.Dataset,
		GrainEntity: "row",
		Entities: map[string]semanticmodel.EntityDefinition{
			"row": {Type: "primary", Fields: []string{"row_id"}},
		},
		Dimensions: fields,
	}
	metrics := make(map[string]semanticmodel.Metric, len(spec.Metrics)+1)
	for name, metric := range spec.Metrics {
		metrics[name] = expressionFixtureMetricToModel(metric)
	}
	return &semanticmodel.Model{
		Name:     spec.Name,
		Tables:   map[string]semanticmodel.Table{spec.Dataset: table},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{spec.Dataset: {Model: spec.Dataset}},
		Metrics:  metrics,
	}
}

func expressionFixtureMetricToModel(spec expressionFixtureMetric) semanticmodel.Metric {
	metric := semanticmodel.Metric{
		Type: spec.Type, Dataset: spec.Dataset, Aggregation: spec.Aggregation,
		Empty: spec.Empty, Unit: spec.Unit, Expression: spec.Expression,
		Numerator: spec.Numerator, Denominator: spec.Denominator,
	}
	if spec.Input != "" {
		metric.Input = &semanticmodel.MetricInput{Field: spec.Input}
	}
	return metric
}

func expressionFixtureRuntimeValue(value expressionFixtureValue) (any, error) {
	switch strings.ToLower(value.Kind) {
	case "null":
		return nil, nil
	case "decimal":
		return value.Value, nil
	case "integer":
		parsed, err := strconv.ParseInt(value.Value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("integer %q: %w", value.Value, err)
		}
		return parsed, nil
	case "float":
		parsed, err := strconv.ParseFloat(value.Value, 64)
		if err != nil {
			return nil, fmt.Errorf("float %q: %w", value.Value, err)
		}
		return parsed, nil
	default:
		return nil, fmt.Errorf("unsupported fixture value kind %q", value.Kind)
	}
}

func assertCorpusError(t *testing.T, err error, want string) {
	t.Helper()
	if want != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
		t.Fatalf("error = %q, want substring %q", err, want)
	}
}

func assertExpressionConformancePlan(t *testing.T, plan Plan, target string, want expressionConformancePlan) {
	t.Helper()
	if plan.IR == nil {
		t.Fatal("PlanIR is nil")
	}
	var found bool
	for _, node := range plan.IR.Nodes {
		switch value := node.(type) {
		case planir.ComputeDerived:
			if want.Node != "derived" || value.Output != target {
				continue
			}
			found = true
			refs, literals, function := flattenPlanIRScalar(value.Expression)
			assertPlanSlice(t, "PlanIR metric refs", refs, want.Refs)
			assertPlanSlice(t, "PlanIR literals", literals, want.Literals)
			if function != want.Function {
				t.Fatalf("PlanIR function = %q, want %q", function, want.Function)
			}
			assertPlanMetricType(t, value.Meta().AvailableMetrics, target, want.ResultType)
		case planir.ComputeRatio:
			if want.Node != "ratio" || value.Output != target {
				continue
			}
			found = true
			if value.Numerator != want.Numerator || value.Denominator != want.Denominator {
				t.Fatalf("PlanIR ratio = %s/%s, want %s/%s", value.Numerator, value.Denominator, want.Numerator, want.Denominator)
			}
			assertPlanMetricType(t, value.Meta().AvailableMetrics, target, want.ResultType)
		}
	}
	if !found {
		t.Fatalf("PlanIR has no %s node for %q", want.Node, target)
	}
}

func flattenPlanIRScalar(expression planir.ScalarExpr) (refs, literals []string, function string) {
	var walk func(planir.ScalarExpr)
	walk = func(value planir.ScalarExpr) {
		switch value.Kind {
		case planir.ScalarMetricRef:
			refs = append(refs, value.Metric)
		case planir.ScalarLiteral:
			literals = append(literals, value.Literal.NumberText)
		case planir.ScalarFunction:
			if function == "" {
				function = value.Function
			}
		}
		for _, child := range value.Children {
			walk(child)
		}
	}
	walk(expression)
	return refs, literals, function
}

func assertPlanSlice(t *testing.T, label string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", label, got, want)
	}
}

func assertPlanMetricType(t *testing.T, metrics []planir.Metric, target, want string) {
	t.Helper()
	for _, metric := range metrics {
		if metric.Name == target {
			if metric.Type != want {
				t.Fatalf("PlanIR metric %q type = %q, want %q", target, metric.Type, want)
			}
			return
		}
	}
	t.Fatalf("PlanIR metric %q is missing", target)
}
