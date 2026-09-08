package contractodcs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/project/contractprojection"
	"github.com/flidai/leapview/internal/project/contractpublication"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestOfficialSchemaIsPinnedAndDetectsDrift(t *testing.T) {
	digest := sha256.Sum256(OfficialSchema())
	if got := hex.EncodeToString(digest[:]); got != SchemaSHA256 {
		t.Fatalf("official ODCS schema drifted: sha256=%s", got)
	}
	if !strings.Contains(string(OfficialSchema()), `"default": "v3.1.0"`) {
		t.Fatal("official schema is not the pinned ODCS 3.1.0 document")
	}
}

func TestGoldenExportsAndReports(t *testing.T) {
	tests := []struct {
		name        string
		publication contractpublication.ContractPublication
	}{
		{name: "source", publication: sourcePublication(t)},
		{name: "model", publication: modelPublication(t)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Export(test.publication)
			if err != nil {
				t.Fatalf("export: %v", err)
			}
			if err := Validate(result.Document); err != nil {
				t.Fatalf("validate exported document: %v", err)
			}
			assertGolden(t, "testdata/"+test.name+".odcs.json", result.Document)
			mapping, err := json.MarshalIndent(result.MappingReport, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			assertGolden(t, "testdata/"+test.name+".mapping.json", append(mapping, '\n'))
			loss, err := json.MarshalIndent(result.LossReport, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			assertGolden(t, "testdata/"+test.name+".loss.json", append(loss, '\n'))
		})
	}
}

func TestSchemaAndExtensionValidationRejectUnsupportedFields(t *testing.T) {
	result, err := Export(sourcePublication(t))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(result.Document, &document); err != nil {
		t.Fatal(err)
	}
	document["unsupported"] = true
	invalid, _ := json.Marshal(document)
	if err := Validate(invalid); err == nil || !strings.Contains(err.Error(), "official schema") {
		t.Fatalf("unknown ODCS field error = %v", err)
	}
	delete(document, "unsupported")
	extension := document["customProperties"].([]any)[0].(map[string]any)["value"].(map[string]any)
	extension["runtimeSQL"] = "select secret"
	invalid, _ = json.Marshal(document)
	if err := Validate(invalid); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown LeapView extension field error = %v", err)
	}
	delete(extension, "runtimeSQL")
	extension["namespace"] = "leapview.dev/odcs-extension/v999"
	invalid, _ = json.Marshal(document)
	if err := Validate(invalid); err == nil || !strings.Contains(err.Error(), "unsupported LeapView extension") {
		t.Fatalf("extension version error = %v", err)
	}
}

func TestOracleNegativeFixturesViolateOfficialSchema(t *testing.T) {
	for _, name := range []string{"invalid-unknown", "invalid-version", "invalid-format", "invalid-logical-type"} {
		t.Run(name, func(t *testing.T) {
			document, err := os.ReadFile("testdata/" + name + ".odcs.json")
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(document); err == nil || !strings.Contains(err.Error(), "official schema") {
				t.Fatalf("fixture must fail the upstream schema, not just LeapView extension validation: %v", err)
			}
		})
	}
}

func TestUnsupportedFeaturesRejectWithoutPartialDocument(t *testing.T) {
	var authored projectcontracts.SemanticModel
	if err := json.Unmarshal([]byte(`{"apiVersion":"leapview.dev/v1","kind":"SemanticModel","metadata":{"id":"semantic-model:sales","name":"sales"},"spec":{"datasets":{"orders":{"model":"orders_model"}},"metrics":{"orders":{"type":"aggregate","dataset":"orders","aggregation":"count","input":{"field":"orders.id"}}}}}`), &authored); err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "model:orders", Name: "orders_model", Kind: projectgraph.KindModel}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	context, err := contractprojection.NewReferenceContext(graph)
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := contractprojection.ProjectSemanticModel(authored, contract(), context)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Export(publication(t, semantic))
	if !errors.Is(err, ErrUnsupportedMapping) || len(result.Document) != 0 || len(result.LossReport.Entries) != 2 || !hasLoss(result.LossReport, LossUnsupported, "contract") {
		t.Fatalf("SemanticModel result=%#v err=%v", result, err)
	}

	var authoredSource projectcontracts.Source
	if err := json.Unmarshal([]byte(`{"apiVersion":"leapview.dev/v1","kind":"Source","metadata":{"id":"source:opaque","name":"opaque"},"spec":{"connection":"connection:warehouse","location":{"type":"path","path":"/tmp/opaque.csv","format":"csv"},"schema":{"mode":"strict","fields":{"payload":{"datatype":"Opaque"}}}}}`), &authoredSource); err != nil {
		t.Fatal(err)
	}
	opaque, err := contractprojection.ProjectSource(authoredSource, contract())
	if err != nil {
		t.Fatal(err)
	}
	result, err = Export(publication(t, opaque))
	if !errors.Is(err, ErrUnsupportedMapping) || len(result.Document) != 0 || !hasLoss(result.LossReport, LossUnsupported, "contract.schema.fields.payload.datatype") {
		t.Fatalf("Opaque result=%#v err=%v", result, err)
	}
}

func TestSecretsBindingsAndExecutableSQLCannotLeak(t *testing.T) {
	for _, test := range []struct {
		name        string
		publication contractpublication.ContractPublication
		forbidden   []string
	}{
		{name: "source", publication: sourcePublication(t), forbidden: []string{"connection:private", "/private/customer.csv", "secret-owner", "private provenance"}},
		{name: "model", publication: modelPublication(t), forbidden: []string{"EXECUTABLE_SQL_SENTINEL", "SELECT", "targetBinding", "credential"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := Export(test.publication)
			if err != nil {
				t.Fatal(err)
			}
			all := string(result.Document)
			mapping, _ := json.Marshal(result.MappingReport)
			loss, _ := json.Marshal(result.LossReport)
			all += string(mapping) + string(loss)
			for _, forbidden := range test.forbidden {
				if strings.Contains(all, forbidden) {
					t.Fatalf("ODCS export leaked forbidden sentinel %q: %s", forbidden, all)
				}
			}
		})
	}
}

func TestInvalidQualityMappingRejects(t *testing.T) {
	projection := modelProjection(true)
	result, err := Export(publication(t, projection))
	if !errors.Is(err, ErrUnsupportedMapping) || !hasLoss(result.LossReport, LossUnsupported, "contract.checks.bad_values") {
		t.Fatalf("invalid quality result=%#v err=%v", result, err)
	}
}

func TestRelationshipMappingRequiresAnExportedTarget(t *testing.T) {
	for _, test := range []struct {
		name   string
		target string
		valid  bool
	}{
		{name: "same object and property", target: "orders.id", valid: true},
		{name: "missing property", target: "orders.missing"},
		{name: "external object", target: "customers.id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := Export(publication(t, modelProjectionWithRelationship(false, test.target)))
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(result.Document), `"to": "orders.id"`) {
					t.Fatal("resolved relationship was not exported")
				}
				return
			}
			if !errors.Is(err, ErrUnsupportedMapping) || len(result.Document) != 0 || !hasLoss(result.LossReport, LossUnsupported, "contract.checks.customer_link") {
				t.Fatalf("unresolved relationship emitted %d bytes, loss=%#v err=%v", len(result.Document), result.LossReport, err)
			}
		})
	}
}

func TestExternalContractReferenceIsRejectedByProjectionAuthority(t *testing.T) {
	if _, err := projectModelProjectionWithRelationship(false, "customers.yaml#/schema/customers/properties/id"); err == nil {
		t.Fatal("external contract reference bypassed the canonical projection authority")
	}
}

func sourcePublication(t *testing.T) contractpublication.ContractPublication {
	t.Helper()
	var source projectcontracts.Source
	if err := json.Unmarshal([]byte(`{
  "apiVersion":"leapview.dev/v1","kind":"Source",
  "metadata":{"id":"source:customers","name":"customers","owner":"secret-owner","description":"private provenance","contract":{"version":"1.2.3","compatibility":"backward"}},
  "spec":{"connection":"connection:private","location":{"type":"path","path":"/private/customer.csv","format":"csv"},"schema":{"mode":"strict","fields":{
    "customer_id":{"datatype":"String","nullable":false,"criticalDataElement":true,"classification":"restricted","authoritativeDefinitions":[{"type":"businessDefinition","url":"https://example.com/glossary/customer"}]},
    "amount":{"datatype":"Decimal","nullable":true,"deprecation":{"since":"1.2.0","reason":"Use total","replacement":"total"}},
    "total":{"datatype":"Decimal","nullable":true}
  }},"freshness":{"basis":"field","field":"customer_id","warningAfter":{"amount":2,"unit":"hour"},"errorAfter":{"amount":4,"unit":"hour"}}}
}`), &source); err != nil {
		t.Fatalf("decode generated Source: %v", err)
	}
	projection, err := contractprojection.ProjectSource(source, contract())
	if err != nil {
		t.Fatalf("project Source: %v", err)
	}
	return publication(t, projection)
}

func modelPublication(t *testing.T) contractpublication.ContractPublication {
	t.Helper()
	return publication(t, modelProjection(false))
}

func modelProjection(unsupported bool) contractprojection.Model {
	return modelProjectionWithRelationship(unsupported, "orders.id")
}

func modelProjectionWithRelationship(unsupported bool, target string) contractprojection.Model {
	value, err := projectModelProjectionWithRelationship(unsupported, target)
	if err != nil {
		panic(err)
	}
	return value
}

func projectModelProjectionWithRelationship(unsupported bool, target string) (contractprojection.Model, error) {
	encodedTarget, err := json.Marshal(target)
	if err != nil {
		return contractprojection.Model{}, err
	}
	checks := `
    ,"checks":[
      {"id":"id_present","type":"non_null","field":"id","severity":"error"},
      {"id":"email_values","type":"accepted_values","field":"email","values":["a@example.com","b@example.com"],"severity":"warning"},
      {"id":"email_unique","type":"unique","fields":["email"],"severity":"error"},
      {"id":"customer_link","type":"relationship","field":"id","to":` + string(encodedTarget) + `,"severity":"warning"},
      {"id":"row_bounds","type":"row_count","minimum":1,"maximum":100,"severity":"error"}`
	if unsupported {
		checks += `,{"id":"bad_values","type":"accepted_values","field":"unknown","values":["unused"]}`
	}
	checks += `]`
	encoded := []byte(`{"apiVersion":"leapview.dev/v1","kind":"Model","metadata":{"id":"model:orders","name":"orders","contract":{"version":"1.2.3","compatibility":"backward"}},"spec":{"definition":{"type":"direct","source":"source:customers"},"entities":{"order":{"type":"primary","fields":["id"]},"email_index":{"type":"unique","fields":["email"]}},"grain":{"entity":"order"},"fields":{"id":{"datatype":"String","nullable":false},"email":{"datatype":"String","nullable":true},"amount":{"datatype":"Decimal","nullable":true}}`)
	encoded = append(encoded, []byte(checks+`}}`)...)
	var authored projectcontracts.Model
	if err := json.Unmarshal(encoded, &authored); err != nil {
		return contractprojection.Model{}, err
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "source:customers", Name: "customers_source", Kind: projectgraph.KindSource},
		{ID: "model:orders", Name: "orders", Kind: projectgraph.KindModel},
		{ID: "model:customers", Name: "customers", Kind: projectgraph.KindModel},
	}, nil)
	if err != nil {
		return contractprojection.Model{}, err
	}
	context, err := contractprojection.NewReferenceContext(graph)
	if err != nil {
		return contractprojection.Model{}, err
	}
	return contractprojection.ProjectModel(authored, contract(), context)
}

func publication(t *testing.T, projection contractprojection.Projection) contractpublication.ContractPublication {
	t.Helper()
	value, err := contractpublication.PrepareContractPublication(contractpublication.ContractPublicationInput{
		InstanceID: "instance:acme", Projection: projection,
		Validation: contractpublication.ValidationEvidence{Version: 1, Checks: []contractpublication.ValidationCheck{{Name: "projection", Outcome: contractpublication.ValidationPassed, Reference: "leapview.contract/v1"}}},
	})
	if err != nil {
		t.Fatalf("prepare publication: %v", err)
	}
	value.PublishedAt = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	return value
}

func contract() contractprojection.Contract {
	return contractprojection.Contract{Version: "1.2.3", Compatibility: "backward"}
}

func hasLoss(report LossReport, kind LossKind, source string) bool {
	for _, entry := range report.Entries {
		if entry.Kind == kind && entry.SourceField == source {
			return true
		}
	}
	return false
}

func assertGolden(t *testing.T, path string, actual []byte) {
	t.Helper()
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\n--- actual ---\n%s", path, err, actual)
	}
	if string(expected) != string(actual) {
		t.Fatalf("golden %s drifted\n--- expected ---\n%s\n--- actual ---\n%s", path, expected, actual)
	}
}
