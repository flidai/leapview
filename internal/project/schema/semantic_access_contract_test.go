package configschema

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func semanticAccessDocument(name, model, accessGrants string) []byte {
	return []byte(fmt.Sprintf(`apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic-model:sales, name: %s}
spec:
  accessGrants:%s
  datasets:
    orders:
      model: %s
      requiredAccessGrants: [canViewSales]
      accessFilters:
        - {field: region, userAttribute: allowedRegions}
  dimensions:
    region:
      datatype: String
      bindings: {orders: {field: orders.region}}
      requiredAccessGrants: [canViewSales]
  metrics:
    revenue:
      type: aggregate
      dataset: orders
      aggregation: sum
      input: {field: orders.revenue}
      requiredAccessGrants: [canViewSales]
`, name, accessGrants, model))
}

func semanticAccessGrantValues(count int) string {
	values := make([]string, count)
	for i := range values {
		values[i] = "sales"
	}
	return "[" + strings.Join(values, ", ") + "]"
}

func TestSemanticModelAccessPolicyStructuralContract(t *testing.T) {
	t.Parallel()

	valid := semanticAccessDocument("sales", "orders", `
    canViewSales:
      userAttribute: department
      allowedValues: [sales, finance]`)
	if err := ValidateBytes(KindSemanticModel, "semantic-access.yaml", valid); err != nil {
		t.Fatalf("valid access-policy shape rejected: %v\n%s", err, valid)
	}
	for name, values := range map[string]string{
		"numbers":  "[1, 2.5]",
		"booleans": "[true, false]",
	} {
		t.Run("homogeneous "+name, func(t *testing.T) {
			document := semanticAccessDocument("sales", "orders", "\n    canViewSales:\n      userAttribute: department\n      allowedValues: "+values)
			if err := ValidateBytes(KindSemanticModel, "semantic-access.yaml", document); err != nil {
				t.Fatalf("valid homogeneous access values rejected: %v\n%s", err, document)
			}
		})
	}
	emptyAccessFilters := bytesReplace(valid, "accessFilters:\n        - {field: region, userAttribute: allowedRegions}", "accessFilters: []")
	if err := ValidateBytes(KindSemanticModel, "semantic-access.yaml", emptyAccessFilters); err != nil {
		t.Fatalf("empty accessFilters rejected without a normative minItems rule: %v", err)
	}

	legacyNames := []struct {
		name  string
		model string
	}{
		{name: "a..b", model: "orders"},
		{name: "sales", model: "a.1"},
		{name: "a.", model: "orders"},
	}
	for _, tc := range legacyNames {
		t.Run("legacy name "+tc.name, func(t *testing.T) {
			document := semanticAccessDocument(tc.name, tc.model, `
    canViewSales:
      userAttribute: department
      allowedValues: [sales]`)
			if err := ValidateBytes(KindSemanticModel, "semantic-access.yaml", document); err != nil {
				t.Fatalf("legacy SemanticModel name grammar rejected: %v\n%s", err, document)
			}
		})
	}

	tests := []struct {
		name string
		doc  []byte
	}{
		{
			name: "grant missing user attribute",
			doc: semanticAccessDocument("sales", "orders", `
    canViewSales:
      allowedValues: [sales]`),
		},
		{
			name: "grant missing allowed values",
			doc: semanticAccessDocument("sales", "orders", `
    canViewSales:
      userAttribute: department`),
		},
		{
			name: "grant allowed values empty",
			doc: semanticAccessDocument("sales", "orders", `
    canViewSales:
      userAttribute: department
      allowedValues: []`),
		},
		{
			name: "grant allowed value object",
			doc: semanticAccessDocument("sales", "orders", `
    canViewSales:
      userAttribute: department
      allowedValues: [{department: sales}]`),
		},
		{
			name: "grant allowed values mixed types",
			doc: semanticAccessDocument("sales", "orders", `
    canViewSales:
      userAttribute: department
      allowedValues: [sales, 2]`),
		},
		{
			name: "grant allowed values duplicate",
			doc: semanticAccessDocument("sales", "orders", `
    canViewSales:
      userAttribute: department
      allowedValues: [sales, sales]`),
		},
		{
			name: "grant allowed values over bound",
			doc:  semanticAccessDocument("sales", "orders", "\n    canViewSales:\n      userAttribute: department\n      allowedValues: "+semanticAccessGrantValues(1025)),
		},
		{
			name: "required grants empty on dataset",
			doc: bytesReplace(semanticAccessDocument("sales", "orders", `
    canViewSales:
      userAttribute: department
      allowedValues: [sales]`), "requiredAccessGrants: [canViewSales]", "requiredAccessGrants: []"),
		},
		{
			name: "required grants duplicate on dataset",
			doc: bytesReplace(semanticAccessDocument("sales", "orders", `
    canViewSales:
      userAttribute: department
      allowedValues: [sales]`), "requiredAccessGrants: [canViewSales]", "requiredAccessGrants: [canViewSales, canViewSales]"),
		},
		{
			name: "required grants empty on dimension",
			doc: bytesReplaceNth(semanticAccessDocument("sales", "orders", `
    canViewSales:
      userAttribute: department
				allowedValues: [sales]`), "requiredAccessGrants: [canViewSales]", "requiredAccessGrants: []", 2),
		},
		{
			name: "required grants empty on metric",
			doc: bytesReplaceNth(semanticAccessDocument("sales", "orders", `
    canViewSales:
      userAttribute: department
				allowedValues: [sales]`), "requiredAccessGrants: [canViewSales]", "requiredAccessGrants: []", 3),
		},
		{
			name: "unknown grant field",
			doc: semanticAccessDocument("sales", "orders", `
    canViewSales:
      userAttribute: department
      allowedValues: [sales]
      expression: forbidden`),
		},
		{
			name: "unknown filter field",
			doc: bytesReplace(semanticAccessDocument("sales", "orders", `
    canViewSales:
      userAttribute: department
      allowedValues: [sales]`), "userAttribute: allowedRegions", "userAttribute: allowedRegions, expression: forbidden"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateBytes(KindSemanticModel, "semantic-access.yaml", tc.doc); err == nil {
				t.Fatalf("invalid access-policy shape accepted\n%s", tc.doc)
			}
		})
	}
}

func TestSemanticAccessNormativeYAMLExamplesValidateGeneratedSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "..", "adr", "specifications", "semantic-access-policy-conformance.md")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	validated := 0
	for _, section := range strings.Split(string(content), "```yaml\n")[1:] {
		document, _, ok := strings.Cut(section, "\n```")
		if !ok || !strings.Contains(document, "kind: SemanticModel") {
			continue
		}
		validated++
		if err := ValidateBytes(KindSemanticModel, path, []byte(document)); err != nil {
			t.Fatalf("normative SemanticModel YAML example rejected: %v\n%s", err, document)
		}
	}
	if validated == 0 {
		t.Fatal("no normative SemanticModel YAML examples found")
	}
}

func TestSemanticModelAccessPolicyFieldsStayOnSemanticModel(t *testing.T) {
	t.Parallel()

	connection := []byte(`apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:files, name: files}
spec:
  type: managed
  accessGrants: {canViewSales: {userAttribute: department, allowedValues: [sales]}}
`)
	if err := ValidateBytes(KindConnection, "connection.yaml", connection); err == nil {
		t.Fatal("Connection accepted SemanticModel access policy fields")
	}

	model := []byte(`apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders}
spec:
  definition: {type: direct, source: source:orders}
  entities: {order: {type: primary, fields: [order_id]}}
  grain: {entity: order}
  fields: {order_id: {datatype: String}}
  accessGrants: {canViewSales: {userAttribute: department, allowedValues: [sales]}}
`)
	if err := ValidateBytes(KindModel, "model.yaml", model); err == nil {
		t.Fatal("Model accepted SemanticModel access policy fields")
	}
}

func TestSemanticModelStructuralAuthorityIsGeneratedFromTypeSpec(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "..")
	typeSpec, err := os.ReadFile(filepath.Join(root, "api", "data-resources", "main.tsp"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"model SemanticModelSpec",
		"model SemanticModel {",
		`@apigen.contract(#{ kind: "resource", tags: #["semantic-model", "data-resource"] })`,
	} {
		if !strings.Contains(string(typeSpec), required) {
			t.Errorf("api/data-resources/main.tsp is missing SemanticModel authority marker %q", required)
		}
	}
	legacyCUE, err := os.ReadFile(filepath.Join(root, "internal", "project", "schema", "contracts", "contracts.cue"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"#SemanticModelResource", "#ProjectSemanticModelSpec", "#SemanticDataset", "#SemanticFilter", "#AggregateMetric"} {
		if strings.Contains(string(legacyCUE), forbidden) {
			t.Errorf("contracts.cue retains handwritten SemanticModel structure %q", forbidden)
		}
	}
	compilerRoot := filepath.Join(root, "internal", "project", "compiler")
	err = filepath.WalkDir(compilerRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(body), "type projectSemanticModelSpec struct") {
			t.Errorf("%s retains handwritten SemanticModel authoring DTO", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	projectCompiler, err := os.ReadFile(filepath.Join(compilerRoot, "project.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(projectCompiler), "map[string]projectcontracts.SemanticModelSpec") {
		t.Error("compiler Project does not consume the generated SemanticModelSpec authority")
	}
}

func bytesReplace(document []byte, old, replacement string) []byte {
	return []byte(strings.Replace(string(document), old, replacement, 1))
}

func bytesReplaceNth(document []byte, old, replacement string, occurrence int) []byte {
	value := string(document)
	offset := 0
	for index := 1; index <= occurrence; index++ {
		position := strings.Index(value[offset:], old)
		if position < 0 {
			return document
		}
		offset += position
		if index < occurrence {
			offset += len(old)
		}
	}
	return []byte(value[:offset] + replacement + value[offset+len(old):])
}
