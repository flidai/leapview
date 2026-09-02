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
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	"github.com/flidai/leapview/internal/project/identityledger"
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
		publication identityledger.ContractPublication
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

func TestUnsupportedFeaturesRejectWithoutPartialDocument(t *testing.T) {
	semantic := contractprojection.SemanticModel{
		Profile: contractprojection.Profile, APIVersion: "leapview.dev/v1", Kind: "SemanticModel",
		Metadata: contractprojection.Metadata{ID: "semantic-model:sales", Name: "sales", Contract: contract()},
		Contract: contractprojection.SemanticModelContract{Datasets: map[string]contractprojection.SemanticDataset{}, Metrics: map[string]contractprojection.SemanticMetric{}},
	}
	result, err := Export(publication(t, semantic))
	if !errors.Is(err, ErrUnsupportedMapping) || len(result.Document) != 0 || len(result.LossReport.Entries) != 2 || !hasLoss(result.LossReport, LossUnsupported, "contract") {
		t.Fatalf("SemanticModel result=%#v err=%v", result, err)
	}

	opaque := contractprojection.Source{
		Profile: contractprojection.Profile, APIVersion: "leapview.dev/v1", Kind: "Source",
		Metadata: contractprojection.Metadata{ID: "source:opaque", Name: "opaque", Contract: contract()},
		Contract: contractprojection.SourceContract{Schema: contractprojection.SourceSchema{Mode: "strict", Fields: &map[string]contractprojection.Field{"payload": {Datatype: pointer("Opaque")}}}},
	}
	result, err = Export(publication(t, opaque))
	if !errors.Is(err, ErrUnsupportedMapping) || len(result.Document) != 0 || !hasLoss(result.LossReport, LossUnsupported, "contract.schema.fields.payload.datatype") {
		t.Fatalf("Opaque result=%#v err=%v", result, err)
	}
}

func TestSecretsBindingsAndExecutableSQLCannotLeak(t *testing.T) {
	for _, test := range []struct {
		name        string
		publication identityledger.ContractPublication
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
	projection := modelProjection()
	checks := []contractprojection.ModelCheck{{ID: "bad_values", Type: "accepted_values", Field: pointer("email")}}
	projection.Contract.Checks = &checks
	result, err := Export(publication(t, projection))
	if !errors.Is(err, ErrUnsupportedMapping) || !hasLoss(result.LossReport, LossUnsupported, "contract.checks.bad_values") {
		t.Fatalf("invalid quality result=%#v err=%v", result, err)
	}
}

func sourcePublication(t *testing.T) identityledger.ContractPublication {
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

func modelPublication(t *testing.T) identityledger.ContractPublication {
	t.Helper()
	return publication(t, modelProjection())
}

func modelProjection() contractprojection.Model {
	fields := map[string]contractprojection.Field{
		"id":     {Datatype: pointer("String"), Nullable: boolPointer(false)},
		"email":  {Datatype: pointer("String"), Nullable: boolPointer(true)},
		"amount": {Datatype: pointer("Decimal"), Nullable: boolPointer(true)},
	}
	checks := []contractprojection.ModelCheck{
		{ID: "id_present", Type: "non_null", Field: pointer("id"), Severity: pointer("error")},
		{ID: "email_values", Type: "accepted_values", Field: pointer("email"), Values: &[]string{"a@example.com", "b@example.com"}, Severity: pointer("warning")},
		{ID: "email_unique", Type: "unique", Fields: &[]string{"email"}, Severity: pointer("error")},
		{ID: "customer_link", Type: "relationship", Field: pointer("id"), To: pointer("customers.id"), Severity: pointer("warning")},
		{ID: "row_bounds", Type: "row_count", Minimum: int64Pointer(1), Maximum: int64Pointer(100), Severity: pointer("error")},
	}
	return contractprojection.Model{
		Profile: contractprojection.Profile, APIVersion: "leapview.dev/v1", Kind: "Model",
		Metadata: contractprojection.Metadata{ID: "model:orders", Name: "orders", Contract: contract()},
		Contract: contractprojection.ModelContract{
			Definition: contractprojection.ModelDefinition{Type: "sql", SQLAst: pointer("EXECUTABLE_SQL_SENTINEL")},
			Entities: map[string]contractprojection.ModelEntity{
				"order":       {Type: "primary", Fields: []string{"id"}},
				"email_index": {Type: "unique", Fields: []string{"email"}},
			},
			Grain: contractprojection.ModelGrain{Entity: "order"}, Fields: fields, Checks: &checks,
		},
	}
}

func publication(t *testing.T, projection contractprojection.Projection) identityledger.ContractPublication {
	t.Helper()
	value, err := identityledger.PrepareContractPublication(identityledger.ContractPublicationInput{
		InstanceID: "instance:acme", Projection: projection,
		Validation: identityledger.ValidationEvidence{Version: 1, Checks: []identityledger.ValidationCheck{{Name: "projection", Outcome: identityledger.ValidationPassed, Reference: "leapview.contract/v1"}}},
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

func pointer(value string) *string    { return &value }
func boolPointer(value bool) *bool    { return &value }
func int64Pointer(value int64) *int64 { return &value }

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
