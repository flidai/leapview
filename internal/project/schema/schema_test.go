package configschema

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

func TestValidateBytesRejectsUnknownEnvelopeField(t *testing.T) {
	err := ValidateBytes(KindProject, "leapview.yaml", []byte(`
apiVersion: leapview.dev/v1
kind: Project
metadata:
  id: project:test
  name: test
spec:
  connections:
    include: [connections/*.yaml]
  sources:
    include: [sources/*.yaml]
  workspaces:
    include: [workspaces/*/workspace.yaml]
surprise: true
`))
	assertDiagnostic(t, err, "schema.unknown_field", "field not allowed")
}

func TestValidateBytesRejectsRemovedWorkspaceAgentPolicyInclude(t *testing.T) {
	err := ValidateBytes(KindGroup, "group.yaml", []byte(`
apiVersion: leapview.dev/v1
kind: Group
metadata:
  id: group:sales
  name: sales
spec:
  agentPolicy:
    include: [agent/*.yaml]
`))
	assertDiagnostic(t, err, "schema.unknown_field", "agentPolicy")
}

func TestValidateBytesRejectsWrongEnvelopeType(t *testing.T) {
	err := ValidateBytes(KindGroup, "group.yaml", []byte(`
apiVersion: leapview.dev/v1
kind: Group
metadata:
  id: group:sales
  name: sales
spec: []
`))
	assertDiagnostic(t, err, "schema.type", "mismatched types")
}

func TestValidateBytesRejectsUnsupportedEnum(t *testing.T) {
	err := ValidateBytes(KindDashboard, "dashboard.yaml", []byte(`
apiVersion: leapview.dev/v1
kind: Dashboard
metadata:
  id: dashboard:sales
  name: sales
spec:
  semanticModel: sales
  visuals:
    revenue:
      type: volcano
      query:
        metrics:
          revenue:
  pages:
    - id: overview
      title: Overview
      visuals: []
`))
	assertDiagnostic(t, err, "schema.enum", "type")
}

func TestCanonicalModelAndSemanticModelContract(t *testing.T) {
	model := []byte(`
apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:sales_orders, name: sales_orders}
aiContext:
  instructions: Use governed order identity.
spec:
  source: source:orders
  entities:
    order: {type: primary, fields: [order_id]}
    customer: {type: foreign, fields: [customer_id]}
  grain: {entity: order}
  fields:
    order_id: {datatype: String}
    customer_id: {datatype: String}
    purchased_at: {datatype: DateTimeTz}
    revenue: {datatype: Decimal}
`)
	if err := ValidateBytes(KindModel, "model.yaml", model); err != nil {
		t.Fatalf("canonical Model rejected: %v", err)
	}
	semantic := []byte(`
apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic-model:sales, name: sales}
aiContext:
  synonyms: [sales analysis]
spec:
  datasets:
    orders: {model: sales_orders, defaultTimeDimension: purchase_date}
    customers: {model: sales_customers}
  relationships:
    orders_customers:
      from: {dataset: orders, entity: customer}
      to: {dataset: customers, entity: customer}
  filters:
    captured:
      field: orders.status
      operator: in
      value: [captured, settled]
  dimensions:
    purchase_date:
      datatype: Date
      time: {nativeGrain: day, grains: [day, week, month], calendar: iso8601}
      bindings: {orders: {field: orders.purchased_at}}
  metrics:
    order_count:
      type: aggregate
      dataset: orders
      aggregation: count_distinct
      input: {field: orders.order_id}
      where: [captured]
      empty: zero
    revenue:
      type: aggregate
      dataset: orders
      aggregation: sum
      input: {field: orders.revenue}
      unit: BRL
      format: currency
    average_order_value:
      type: ratio
      numerator: revenue
      denominator: order_count
      unit: BRL
`)
	if err := ValidateBytes(KindSemanticModel, "semantic-model.yaml", semantic); err != nil {
		t.Fatalf("canonical SemanticModel rejected: %v", err)
	}
}

func TestCanonicalContractRejectsRemovedSemanticForms(t *testing.T) {
	model := []byte(`
apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:sales_orders, name: sales_orders}
spec:
  primaryKey: order_id
  fields: {order_id: {type: String}}
`)
	if err := ValidateBytes(KindModel, "model.yaml", model); err == nil {
		t.Fatal("canonical Model accepted removed scalar primaryKey/type fields")
	}
	semantic := []byte(`
apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic-model:sales, name: sales}
spec:
  datasets: {orders: {model: model:sales_orders}}
  relationships:
    orders_customers:
      from: orders.customer_id
      to: customers.customer_id
      cardinality: many_to_one
  filters:
    captured: {field: orders.status, operator: in, values: [captured]}
  measures:
    revenue: {fact: orders, aggregation: sum, input: {field: orders.revenue}, empty: zero}
  metrics:
    revenue: {type: aggregate, dataset: orders, aggregation: sum, input: {field: orders.revenue}, expression: bad}
`)
	if err := ValidateBytes(KindSemanticModel, "semantic-model.yaml", semantic); err == nil {
		t.Fatal("canonical SemanticModel accepted removed/legacy semantic forms")
	}
}

func TestCanonicalDatasetModelUsesAuthoringName(t *testing.T) {
	err := ValidateBytes(KindSemanticModel, "semantic-model.yaml", []byte(`
apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic-model:sales, name: sales}
spec:
  datasets: {orders: {model: model:sales_orders}}
  metrics:
    revenue: {type: aggregate, dataset: orders, aggregation: sum, input: {field: orders.revenue}}
`))
	if err == nil {
		t.Fatal("SemanticModel accepted external Model resource ID in dataset.model")
	}
}

func TestCanonicalFilterUsesValueAndRejectsValuesAlias(t *testing.T) {
	valid := []byte(`
apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic-model:sales, name: sales}
spec:
  datasets: {orders: {model: sales_orders}}
  filters: {captured: {field: orders.status, operator: in, value: [captured, settled]}}
  metrics: {revenue: {type: aggregate, dataset: orders, aggregation: sum, input: {field: orders.revenue}, where: [captured]}}
`)
	if err := ValidateBytes(KindSemanticModel, "semantic-model.yaml", valid); err != nil {
		t.Fatalf("filter value list rejected: %v", err)
	}
	invalid := strings.Replace(string(valid), "value: [captured, settled]", "values: [captured, settled]", 1)
	if err := ValidateBytes(KindSemanticModel, "semantic-model.yaml", []byte(invalid)); err == nil {
		t.Fatal("SemanticModel accepted removed filter values property")
	}
}

func TestCanonicalMetricTagsRejectIncompatibleFields(t *testing.T) {
	err := ValidateBytes(KindSemanticModel, "semantic-model.yaml", []byte(`
apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic-model:sales, name: sales}
spec:
  datasets: {orders: {model: sales_orders}}
  metrics:
    revenue:
      type: aggregate
      dataset: orders
      aggregation: sum
      input: {field: orders.revenue}
      expression: forbidden
`))
	if err == nil {
		t.Fatal("aggregate metric accepted derived-only expression field")
	}
}

func TestDashboardVisualContractUnifiesChartsAndTables(t *testing.T) {
	err := ValidateBytes(KindDashboard, "dashboard.yaml", []byte(`
apiVersion: leapview.dev/v1
kind: Dashboard
metadata:
  id: dashboard:sales
  name: sales
spec:
  semanticModel: sales
  filters:
    state:
      label: State
      field: customers.state
      predicates:
        - kind: set
          operators: [in, not_in]
      options: {kind: distinct, limit: 50}
  filter_bindings:
    state:
      filter: state
      targets:
        include: [overview/revenue]
      default: {kind: unfiltered}
  visuals:
    revenue:
      type: line
      title: Revenue
      query:
        dimensions: [orders.purchase_month]
        metrics: [revenue]
    total:
      type: kpi
      query:
        metrics: [revenue]
    orders:
      type: table
      title: Orders
      cardinality: bounded
      query:
        table: orders
        fields: [orders.order_id, orders.revenue]
    state_status:
      type: matrix
      title: State status
      query:
        rows: [customers.state]
        columns: [orders.status]
        metrics: [order_count]
    category_status:
      type: pivot
      title: Category status
      query:
        rows: [orders.category]
        columns: [orders.status]
        metrics: [order_count]
  pages:
    - id: overview
      title: Overview
      components:
        - id: revenue
          kind: visual
          visual: revenue
          placement: {col: 1, row: 1, col_span: 6, row_span: 4}
        - id: state
          kind: slicer
          binding: {scope: report, id: state}
          presentation: {style: dropdown}
          placement: {col: 7, row: 1, col_span: 3, row_span: 2}
        - id: heading
          kind: header
          title: Sales
          placement: {col: 1, row: 5, col_span: 12, row_span: 1}
`))
	if err != nil {
		t.Fatalf("ValidateBytes() error = %v", err)
	}
}

func TestDashboardVisualContractAcceptsDecisionContext(t *testing.T) {
	err := ValidateBytes(KindDashboard, "dashboard.yaml", []byte(`
apiVersion: leapview.dev/v1
kind: Dashboard
metadata:
  id: dashboard:sales
  name: sales
spec:
  semanticModel: sales
  visuals:
    revenue:
      type: line
      title: Revenue
      query:
        dimensions: [orders.purchase_month]
        series: {field: orders.status, alias: status}
        metrics: [revenue]
      presentation:
        axes:
          - {id: x, title: Month, tick_density: sparse}
          - {id: primary_y, title: Revenue, scale: linear, zero: include, minimum: 0, maximum: 100, unit: USD}
        reference_lines:
          - {id: target, axis: primary_y, value: {number: 80}, label: Target, tone: success}
        reference_bands:
          - id: healthy
            axis: primary_y
            from: {field: value, reducer: minimum}
            to: {field: value, reducer: maximum}
            label: Healthy range
        event_annotations:
          - {id: launch, axis: x, value: {text: "2026-03-01"}, label: Launch}
        tooltip: [label, value]
        stacking: percent
        series_order: [delivered, processing]
        series_colors: {delivered: success, processing: data_3}
        conditional_formatting:
          - id: revenue-health
            target: mark_fill
            field: value
            kind: gradient
            minimum: 0
            maximum: 100
            low: {color: danger}
            high: {color: success}
            null: {color: neutral}
  pages:
    - id: overview
      title: Overview
      components: []
`))
	if err != nil {
		t.Fatalf("ValidateBytes() error = %v", err)
	}
}

func TestDashboardVisualContractRejectsLegacyChartTableSplit(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "dashboard tables",
			body: `
  tables:
    orders:
      kind: data_table
      title: Orders
      query: {table: orders, fields: [orders.order_id]}
`,
		},
		{
			name: "visual kind",
			body: `
  visuals:
    total:
      kind: kpi
      query: {metrics: [revenue]}
`,
		},
		{
			name: "page visuals",
			body: `
  pages:
    - id: overview
      title: Overview
      visuals: []
`,
		},
		{
			name: "page name",
			body: `
  pages:
    - name: overview
      title: Overview
      components: []
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := `
apiVersion: leapview.dev/v1
kind: Dashboard
metadata:
  id: dashboard:sales
  name: sales
spec:
  semanticModel: sales
  visuals:
    revenue:
      type: line
      title: Revenue
      query: {dimensions: [orders.status], metrics: [revenue]}
  pages:
    - id: overview
      title: Overview
      components: []
` + tt.body
			if err := ValidateBytes(KindDashboard, "dashboard.yaml", []byte(content)); err == nil {
				t.Fatal("ValidateBytes() unexpectedly accepted legacy dashboard syntax")
			}
		})
	}
}

func TestValidateBytesRejectsRemovedLocalConnectionKind(t *testing.T) {
	err := ValidateBytes(KindConnection, "local.yaml", []byte(`
apiVersion: leapview.dev/v1
kind: Connection
metadata:
  id: connection:files
  name: files
spec:
  kind: local
`))
	assertDiagnostic(t, err, "schema.enum", "local")
}

func TestValidateBytesRejectsInvalidIdentifierKey(t *testing.T) {
	err := ValidateBytes(KindModel, "orders.yaml", []byte(`
apiVersion: leapview.dev/v1
kind: Model
metadata:
  id: model:orders
  name: orders
spec:
  entities: {order: {type: primary, fields: [order_id]}}
  grain: {entity: order}
  fields:
    invalid-name:
      datatype: String
    order_id:
      datatype: String
`))
	assertDiagnostic(t, err, "schema.unknown_field", "invalid-name")
}

func TestValidateBytesRejectsMissingRequiredRootFields(t *testing.T) {
	tests := []struct {
		name     string
		kind     Kind
		content  string
		contains string
	}{
		{
			name: "project spec",
			kind: KindProject,
			content: `
apiVersion: leapview.dev/v1
kind: Project
metadata:
  id: project:test
  name: test
`,
			contains: "spec",
		},
		{
			name: "project access",
			kind: KindProject,
			content: `
apiVersion: leapview.dev/v1
kind: Project
metadata:
  id: project:test
  name: sales
spec:
  connections:
    include: [connections/*.yaml]
  sources:
    include: [sources/*.yaml]
  models:
    include: [models/*.yaml]
  semanticModels:
    include: [semantic-models/*.yaml]
  pipelines:
    include: [pipelines/*.yaml]
  dashboards:
    include: [dashboards/*.yaml]
`,
			contains: "access",
		},
		{
			name: "dashboard semantic model",
			kind: KindDashboard,
			content: `
apiVersion: leapview.dev/v1
kind: Dashboard
metadata:
  id: dashboard:sales
  name: sales
spec:
  visuals: {}
  pages: []
`,
			contains: "semanticModel",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBytes(tt.kind, tt.name+".yaml", []byte(tt.content))
			assertDiagnosticMessage(t, err, "schema.contract", tt.contains)
		})
	}
}

func TestValidateFileAcceptsShowcaseResources(t *testing.T) {
	root := filepath.Join("..", "..", "..", "dashboards")
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	for _, path := range files {
		kind, ok := kindForResourceFile(t, path)
		if !ok {
			continue
		}
		t.Run(path, func(t *testing.T) {
			if err := ValidateFile(kind, path); err != nil {
				t.Fatalf("ValidateFile() error = %v", err)
			}
		})
	}
}

func TestGeneratedJSONSchemasRejectInvalidDocuments(t *testing.T) {
	tests := []struct {
		name     string
		kind     Kind
		instance any
	}{
		{
			name: "project missing spec",
			kind: KindProject,
			instance: map[string]any{
				"apiVersion": "leapview.dev/v1",
				"kind":       "Project",
				"metadata":   map[string]any{"name": "test"},
			},
		},
		{
			name: "project missing access",
			kind: KindProject,
			instance: map[string]any{
				"apiVersion": "leapview.dev/v1",
				"kind":       "Project",
				"metadata":   map[string]any{"id": "project:sales", "name": "sales"},
				"spec": map[string]any{
					"connections":    map[string]any{"include": []any{"connections/*.yaml"}},
					"sources":        map[string]any{"include": []any{"sources/*.yaml"}},
					"models":         map[string]any{"include": []any{"models/*.yaml"}},
					"semanticModels": map[string]any{"include": []any{"semantic-models/*.yaml"}},
					"pipelines":      map[string]any{"include": []any{"pipelines/*.yaml"}},
					"dashboards":     map[string]any{"include": []any{"dashboards/*.yaml"}},
				},
			},
		},
		{
			name: "model missing primary key",
			kind: KindModel,
			instance: map[string]any{
				"apiVersion": "leapview.dev/v1",
				"kind":       "Model",
				"metadata":   map[string]any{"id": "model:orders", "name": "orders"},
				"spec":       map[string]any{},
			},
		},
		{
			name: "dashboard empty pages",
			kind: KindDashboard,
			instance: map[string]any{
				"apiVersion": "leapview.dev/v1",
				"kind":       "Dashboard",
				"metadata":   map[string]any{"id": "dashboard:sales", "name": "sales"},
				"spec": map[string]any{
					"semanticModel": "sales",
					"visuals":       map[string]any{"revenue": map[string]any{"query": map[string]any{}}},
					"pages":         []any{},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := compileGeneratedSchema(t, tt.kind)
			if err := schema.Validate(tt.instance); err == nil {
				t.Fatal("generated JSON Schema accepted invalid document")
			}
		})
	}
}

func TestJSONSchemaFilesAreFresh(t *testing.T) {
	files, err := JSONSchemaFiles()
	if err != nil {
		t.Fatalf("JSONSchemaFiles() error = %v", err)
	}
	for name, content := range files {
		path := filepath.Join("..", "..", "..", "schemas", "json", name)
		onDisk, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read generated schema %s: %v", name, err)
		}
		if string(onDisk) != string(content) {
			t.Fatalf("%s is stale; run leapview schema export --format json-schema --out schemas/json", path)
		}
	}
}

func TestProjectContractIsProjectWide(t *testing.T) {
	valid := []byte(`
apiVersion: leapview.dev/v1
kind: Project
metadata:
  id: project:showcase
  name: showcase
  displayName: Showcase
  domain: analytics
  documentation: docs/project.md
  provenance: {origin: git, path: dashboards/leapview.yaml}
spec:
  connections: {include: [connections/*.yaml]}
  sources: {include: [sources/*.yaml]}
  models: {include: [models/*.yaml]}
  semanticModels: {include: [semantic-models/*.yaml]}
  pipelines: {include: [pipelines/*.yaml]}
  dashboards: {include: [dashboards/*.yaml]}
  access: {include: [access/*.yaml]}
  publications: {include: [publications/*.yaml]}
`)
	if err := ValidateBytes(KindProject, "project.yaml", valid); err != nil {
		t.Fatalf("ValidateBytes() error = %v", err)
	}
	for _, legacy := range []string{"workspace", "workspaces"} {
		content := strings.Replace(string(valid), "  access:", "  "+legacy+": {include: [workspaces/*.yaml]}\n  access:", 1)
		if err := ValidateBytes(KindProject, "project.yaml", []byte(content)); err == nil {
			t.Fatalf("ValidateBytes() accepted removed project field %q", legacy)
		}
	}
}

func TestProjectContractRejectsLegacyWorkspaceJSON(t *testing.T) {
	valid := `{"apiVersion":"leapview.dev/v1","kind":"Project","metadata":{"id":"project:showcase","name":"showcase"},"spec":{"connections":{"include":["connections/*.yaml"]},"sources":{"include":["sources/*.yaml"]},"models":{"include":["models/*.yaml"]},"semanticModels":{"include":["semantic-models/*.yaml"]},"pipelines":{"include":["pipelines/*.yaml"]},"dashboards":{"include":["dashboards/*.yaml"]},"access":{"include":["access/*.yaml"]}}}`
	for _, legacy := range []string{
		strings.Replace(valid, `"access":{"include":["access/*.yaml"]}`, `"workspaces":{"include":["workspaces/*/workspace.yaml"]},"access":{"include":["access/*.yaml"]}`, 1),
		strings.Replace(valid, `"metadata":{"id":"project:showcase","name":"showcase"}`, `"metadata":{"id":"project:showcase","name":"showcase","workspace":"sales"}`, 1),
	} {
		if err := ValidateBytes(KindProject, "project.json", []byte(legacy)); err == nil {
			t.Fatalf("ValidateBytes() accepted legacy workspace JSON: %s", legacy)
		}
	}
}

func TestMetadataRequiresOpaqueIDAndSymbolicName(t *testing.T) {
	base := `
apiVersion: leapview.dev/v1
kind: Connection
metadata:
  id: connection:files
  name: files
spec:
  kind: managed
`
	if err := ValidateBytes(KindConnection, "connection.yaml", []byte(base)); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}
	for _, field := range []string{"id", "name"} {
		value := map[string]string{"id": "connection:files", "name": "files"}[field]
		content := strings.Replace(base, "  "+field+": "+value+"\n", "", 1)
		if err := ValidateBytes(KindConnection, "connection.yaml", []byte(content)); err == nil {
			t.Fatalf("ValidateBytes() accepted metadata without %s", field)
		}
	}
	if err := ValidateBytes(KindConnection, "connection.yaml", []byte(strings.Replace(base, "  id: connection:files", "  id: files/connection", 1))); err == nil {
		t.Fatal("ValidateBytes() accepted malformed opaque resource ID")
	}
	if err := ValidateBytes(KindConnection, "connection.yaml", []byte(strings.Replace(base, "  name: files", "  name: 9files", 1))); err == nil {
		t.Fatal("ValidateBytes() accepted malformed symbolic resource name")
	}
	if err := ValidateBytes(KindConnection, "connection.yaml", []byte(strings.Replace(base, "  name: files", "  workspace: sales\n  name: files", 1))); err == nil {
		t.Fatal("ValidateBytes() accepted removed metadata.workspace")
	}
}

func TestProjectSidecarsAndCanonicalGrantContract(t *testing.T) {
	tests := []struct {
		name string
		kind Kind
		doc  string
	}{
		{"group", KindGroup, `
apiVersion: leapview.dev/v1
kind: Group
metadata: {id: group:analysts, name: analysts}
spec: {members: [{email: analysts@example.com}]}
`},
		{"role binding", KindRoleBinding, `
apiVersion: leapview.dev/v1
kind: RoleBinding
metadata: {id: binding:analysts, name: analysts_binding}
spec: {role: viewer, subject: {kind: group, group: group:analysts}}
`},
		{"grant", KindGrant, `
apiVersion: leapview.dev/v1
kind: Grant
metadata: {id: grant:dashboard, name: dashboard_view}
spec: {object: {id: dashboard:sales, kind: dashboard}, subject: {kind: group, group: group:analysts}, capability: RESOURCE_READ}
`},
		{"data policy", KindDataPolicy, `
apiVersion: leapview.dev/v1
kind: DataPolicy
metadata: {id: policy:region, name: region_filter}
spec: {object: {kind: semantic_model, id: semantic_model:sales}, policyType: row_filter, expression: {field: region}}
`},
		{"publication", KindDashboardPublication, `
apiVersion: leapview.dev/v1
kind: DashboardPublication
metadata: {id: publication:website, name: website}
spec: {dashboard: dashboard:sales, defaultPage: overview, embedding: {allowedOrigins: [https://example.com]}}
`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateBytes(tt.kind, tt.name+".yaml", []byte(tt.doc)); err != nil {
				t.Fatalf("ValidateBytes() error = %v", err)
			}
		})
	}
	grant := tests[2].doc
	for _, legacy := range []string{"privilege: VIEW_ITEM", "object: {type: dashboard, id: dashboard:sales}"} {
		content := strings.Replace(grant, "capability: RESOURCE_READ", legacy, 1)
		if strings.HasPrefix(legacy, "object:") {
			content = strings.Replace(grant, "object: {id: dashboard:sales, kind: dashboard}", legacy, 1)
		}
		if err := ValidateBytes(KindGrant, "grant.yaml", []byte(content)); err == nil {
			t.Fatalf("accepted removed Grant contract %q", legacy)
		}
	}
}

func kindForResourceFile(t *testing.T, path string) (Kind, bool) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(content, &header); err != nil {
		t.Fatal(err)
	}
	if header.APIVersion != "leapview.dev/v1" {
		return "", false
	}
	switch header.Kind {
	case "Project":
		return KindProject, true
	case "Connection":
		return KindConnection, true
	case "Source":
		return KindSource, true
	case "Model":
		return KindModel, true
	case "Pipeline":
		return KindPipeline, true
	case "SemanticModel":
		return KindSemanticModel, true
	case "Dashboard":
		return KindDashboard, true
	case "Group":
		return KindGroup, true
	case "RoleBinding":
		return KindRoleBinding, true
	case "Grant":
		return KindGrant, true
	case "DataPolicy":
		return KindDataPolicy, true
	case "DashboardPublication":
		return KindDashboardPublication, true
	default:
		return "", false
	}
}

func compileGeneratedSchema(t *testing.T, kind Kind) *jsonschema.Schema {
	t.Helper()
	content, err := JSONSchema(kind)
	if err != nil {
		t.Fatalf("JSONSchema(%s): %v", kind, err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("unmarshal JSON Schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	location := fmt.Sprintf("memory://%s.schema.json", kind)
	if err := compiler.AddResource(location, document); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	schema, err := compiler.Compile(location)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return schema
}

func assertDiagnostic(t *testing.T, err error, code, contains string) {
	t.Helper()
	got := assertDiagnosticMessage(t, err, code, contains)
	if got.File == "" || got.Line == 0 || got.Column == 0 {
		t.Fatalf("diagnostic lacks source position: %#v", got)
	}
}

func assertDiagnosticMessage(t *testing.T, err error, code, contains string) Diagnostic {
	t.Helper()
	if err == nil {
		t.Fatalf("ValidateBytes() error = nil, want %s", code)
	}
	var schemaErr *Error
	if !errors.As(err, &schemaErr) {
		t.Fatalf("error type = %T, want *Error: %v", err, err)
	}
	if len(schemaErr.Diagnostics) == 0 {
		t.Fatal("diagnostics empty")
	}
	got := schemaErr.Diagnostics[0]
	if got.Code != code {
		t.Fatalf("diagnostic code = %q, want %q: %#v", got.Code, code, schemaErr.Diagnostics)
	}
	if !strings.Contains(got.Message, contains) {
		t.Fatalf("diagnostic message = %q, want containing %q", got.Message, contains)
	}
	return got
}
