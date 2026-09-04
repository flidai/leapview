package contractprojection

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

var testContract = Contract{Version: "1.2.3", Compatibility: "backward"}

func TestRFC8785GoldenVector(t *testing.T) {
	input, err := os.ReadFile("testdata/rfc8785-numbers.input.json")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/rfc8785-numbers.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	want = bytes.TrimSpace(want)
	got, err := canonicalizeRFC8785(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("canonical bytes = %s, want %s", got, want)
	}
}

func TestCrossLanguageProjectionFixture(t *testing.T) {
	input, err := os.ReadFile("testdata/cross-language-projection.input.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSourcePublication(input); err == nil {
		t.Fatal("non-canonical publication bytes were accepted")
	}
	wire, err := os.ReadFile("testdata/cross-language-projection.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	wire = bytes.TrimSpace(wire)
	got, err := canonicalPublicationBytes(input, "Source")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wire) {
		t.Fatalf("canonical bytes = %s, want %s", got, wire)
	}
	if _, err := DecodeSourcePublication(wire); err != nil {
		t.Fatal(err)
	}
}

func TestSourceProjectionIsAllowlistedAndStable(t *testing.T) {
	first := decodeSource(t, sourceJSON(false))
	second := decodeSource(t, sourceJSON(true))
	projectedFirst, err := ProjectSource(first, testContract)
	if err != nil {
		t.Fatal(err)
	}
	projectedSecond, err := ProjectSource(second, testContract)
	if err != nil {
		t.Fatal(err)
	}
	canonicalFirst, err := CanonicalBytes(projectedFirst)
	if err != nil {
		t.Fatal(err)
	}
	canonicalSecond, err := CanonicalBytes(projectedSecond)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonicalFirst, canonicalSecond) {
		t.Fatalf("map insertion order changed canonical output:\n%s\n%s", canonicalFirst, canonicalSecond)
	}

	text := string(canonicalFirst)
	for _, allowed := range []string{`"profile":"leapview.contract/v1"`, `"datatype":"String"`, `"criticalDataElement":true`, `"classification":"restricted"`, `"url":"https://example.com/~glossary"`} {
		if !strings.Contains(text, allowed) {
			t.Errorf("allowed contract field is absent: %s\n%s", allowed, text)
		}
	}
	for _, excluded := range []string{"connection:private", "/private/customer.csv", "INTERNAL DESCRIPTION", "internal-tag", "secret-owner", "private provenance"} {
		if strings.Contains(text, excluded) {
			t.Errorf("excluded/internal field leaked: %q\n%s", excluded, text)
		}
	}

	digest, err := Digest(projectedFirst)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonicalFirst)
	wantDigest := "sha256:" + hex.EncodeToString(sum[:])
	if digest != wantDigest {
		t.Fatalf("digest = %q, want %q", digest, wantDigest)
	}
	const goldenDigest = "sha256:20ef53d1a51b2fbf45f168d6512185a9e13f0ec05ca605ed019044c5b9c2fa2b"
	if digest != goldenDigest {
		t.Fatalf("source projection digest = %q; update golden after intentional review", digest)
	}
}

func TestModelProjectionDoesNotLeakDescriptiveOrAIFields(t *testing.T) {
	var input projectcontracts.Model
	decodeContract(t, []byte(`{
  "apiVersion":"leapview.dev/v1","kind":"Model",
  "metadata":{"id":"model:orders","name":"orders","displayName":"INTERNAL MODEL LABEL","owner":"secret-owner","contract":{"version":"1.2.3","compatibility":"backward"}},
  "aiContext":{"instructions":"INTERNAL AI INSTRUCTIONS"},
  "spec":{
    "definition":{"type":"direct","source":"source:orders"},
    "entities":{"order":{"type":"primary","fields":["order_id"],"description":"INTERNAL ENTITY DESCRIPTION"}},
    "grain":{"entity":"order"},
    "fields":{"order_id":{"datatype":"String","label":"INTERNAL FIELD LABEL","description":"INTERNAL FIELD DESCRIPTION","nullable":false}},
    "checks":[{"id":"order_id_present","type":"non_null","field":"order_id","severity":"error","description":"INTERNAL CHECK DESCRIPTION"}]
  }
}`), &input)
	projected, err := ProjectModel(input, testContract)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalBytes(projected)
	if err != nil {
		t.Fatal(err)
	}
	text := string(canonical)
	for _, allowed := range []string{`"source":"source:orders"`, `"entity":"order"`, `"id":"order_id_present"`, `"severity":"error"`} {
		if !strings.Contains(text, allowed) {
			t.Errorf("allowed Model field is absent: %s", allowed)
		}
	}
	if strings.Contains(text, "INTERNAL") || strings.Contains(text, "secret-owner") {
		t.Fatalf("Model internal field leaked: %s", text)
	}
}

func TestModelSQLProjectionUsesAnalyzedASTInsteadOfAuthoredFormatting(t *testing.T) {
	modelJSON := func(sql string) []byte {
		encoded, err := json.Marshal(map[string]any{
			"apiVersion": "leapview.dev/v1", "kind": "Model",
			"metadata": map[string]any{"id": "model:orders", "name": "orders", "contract": map[string]any{"version": "1.2.3", "compatibility": "backward"}},
			"spec": map[string]any{
				"definition": map[string]any{"type": "sql", "sql": sql},
				"entities":   map[string]any{"order": map[string]any{"type": "primary", "fields": []string{"order_id"}}},
				"grain":      map[string]any{"entity": "order"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	var firstInput, secondInput projectcontracts.Model
	decodeContract(t, modelJSON("SELECT o.order_id FROM source.orders AS o WHERE o.amount > 0"), &firstInput)
	decodeContract(t, modelJSON("-- formatting is not identity\n SELECT  o.order_id\nFROM source.orders AS o\nWHERE (o.amount > 0)"), &secondInput)
	first, err := ProjectModel(firstInput, testContract)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ProjectModel(secondInput, testContract)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, err := CanonicalBytes(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := CanonicalBytes(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("authored SQL formatting changed projection identity:\n%s\n%s", firstBytes, secondBytes)
	}
	if strings.Contains(string(firstBytes), "SELECT o.order_id") || strings.Contains(string(firstBytes), "formatting is not identity") {
		t.Fatalf("authored SQL text leaked into projection: %s", firstBytes)
	}
}

func TestSemanticProjectionPreservesTypedExactValuesAndExcludesPresentation(t *testing.T) {
	var input projectcontracts.SemanticModel
	decodeContract(t, []byte(`{
  "apiVersion":"leapview.dev/v1","kind":"SemanticModel",
  "metadata":{"id":"semantic-model:sales","name":"sales","displayName":"INTERNAL SEMANTIC LABEL","description":"INTERNAL DESCRIPTION"},
  "aiContext":{"instructions":"INTERNAL AI INSTRUCTIONS"},
  "spec":{
    "datasets":{"orders":{"model":"orders","requiredAccessGrants":["region_access"],"displayName":"INTERNAL DATASET LABEL"}},
    "accessGrants":{"region_access":{"userAttribute":"region","allowedValues":["emea"]}},
    "dimensions":{"order_id":{"datatype":"Integer","bindings":{"orders":{"field":"orders.order_id"}},"label":"INTERNAL DIMENSION LABEL"}},
    "filters":{"large_order":{"field":"orders.order_id","operator":"equals","value":9007199254740993,"aiContext":{"instructions":"INTERNAL FILTER AI"}}},
    "metrics":{"order_count":{"type":"aggregate","dataset":"orders","aggregation":"count_distinct","input":{"field":"orders.order_id"},"empty":"zero","format":"integer","hidden":true,"label":"INTERNAL METRIC LABEL"}}
  }
}`), &input)
	projected, err := ProjectSemanticModel(input, testContract)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalBytes(projected)
	if err != nil {
		t.Fatal(err)
	}
	text := string(canonical)
	for _, allowed := range []string{`"requiredAccessGrants":["region_access"]`, `"userAttribute":"region"`, `"type":"Integer"`, `"value":"9007199254740993"`, `"format":"integer"`} {
		if !strings.Contains(text, allowed) {
			t.Errorf("allowed SemanticModel field is absent: %s\n%s", allowed, text)
		}
	}
	if strings.Contains(text, "INTERNAL") || strings.Contains(text, `"hidden"`) {
		t.Fatalf("SemanticModel internal field leaked: %s", text)
	}
}

func TestDecodeSemanticPublicationRejectsNestedUnknownUnionField(t *testing.T) {
	canonical := []byte(`{"apiVersion":"leapview.dev/v1","contract":{"datasets":{},"filters":{"active":{"field":"orders.id","operator":"equals","value":{"type":"String","value":"yes"}}},"metrics":{}},"kind":"SemanticModel","metadata":{"contract":{"compatibility":"backward","version":"1.2.3"},"id":"semantic-model:sales","name":"sales"},"profile":"leapview.contract/v1"}`)
	if _, err := DecodeSemanticModelPublication(canonical); err != nil {
		t.Fatalf("valid SemanticModel publication was rejected: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(canonical, &payload); err != nil {
		t.Fatal(err)
	}
	contract := payload["contract"].(map[string]any)
	filters := contract["filters"].(map[string]any)
	active := filters["active"].(map[string]any)
	value := active["value"].(map[string]any)
	value["unknown"] = true
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	withUnknown, err := canonicalizeRFC8785(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSemanticModelPublication(withUnknown); err == nil {
		t.Fatal("nested unknown field in SemanticModel union was accepted")
	}
}

func TestSemanticSetNormalizationAndLiteralBoundaries(t *testing.T) {
	values, err := canonicalAllowedValues([]any{"z", "a", "z"})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || canonicalValueKey(values[0]) != "String\x00a" || canonicalValueKey(values[1]) != "String\x00z" {
		t.Fatalf("canonical allowed-value set = %#v", values)
	}

	for datatype, input := range map[string]any{
		"Time":     "03:04:05.1200",
		"DateTime": "2026-01-02T03:04:05.1200",
	} {
		value, err := canonicalLiteral(datatype, input)
		if err != nil {
			t.Fatalf("canonical %s literal: %v", datatype, err)
		}
		if got := canonicalValueKey(value); !strings.HasPrefix(got, datatype+"\x00") {
			t.Fatalf("canonical %s literal key = %q", datatype, got)
		}
	}
	if _, err := canonicalLiteral("Float", json.Number("1.25")); err == nil || !strings.Contains(err.Error(), "approximate Float") {
		t.Fatalf("Float literal error = %v", err)
	}
	if _, err := canonicalLiteral("Opaque", "value"); err == nil || !strings.Contains(err.Error(), "no canonical") {
		t.Fatalf("Opaque literal error = %v", err)
	}
}

func TestCanonicalBytesNormalizesUnicodeAndRejectsControlCharacters(t *testing.T) {
	value := projectSource(t, bytes.Replace(sourceJSON(false), []byte(`"name":"customers"`), []byte(`"name":"Cafe\u0301"`), 1))
	canonical, err := CanonicalBytes(value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(canonical), `"name":"Café"`) {
		t.Fatalf("canonical string was not NFC: %s", canonical)
	}
	badName := bytes.Replace(sourceJSON(false), []byte(`"name":"customers"`), []byte(`"name":"bad\nname"`), 1)
	if _, err := ProjectSource(decodeSource(t, badName), testContract); err == nil || !strings.Contains(err.Error(), "control character") {
		t.Fatalf("control-character error = %v", err)
	}
}

func TestCanonicalBytesRejectsInvalidEnvelopeAndLossyIntegers(t *testing.T) {
	badEnvelope := bytes.Replace(sourceJSON(false), []byte(`"apiVersion":"leapview.dev/v1"`), []byte(`"apiVersion":"wrong"`), 1)
	if _, err := ProjectSource(decodeSource(t, badEnvelope), testContract); err == nil || !strings.Contains(err.Error(), "envelope") {
		t.Fatalf("invalid envelope error = %v", err)
	}

	large := bytes.Replace(sourceJSON(false), []byte(`"amount":2`), []byte(`"amount":9007199254740993`), 1)
	value := projectSource(t, large)
	if _, err := CanonicalBytes(value); err == nil || !strings.Contains(err.Error(), "RFC 8785 exact range") {
		t.Fatalf("lossy integer error = %v", err)
	}
}

func TestSealedProjectionRejectsBypassAndZeroValue(t *testing.T) {
	var zero Source
	if _, err := CanonicalBytes(zero); err == nil {
		t.Fatal("zero-value Source was accepted as a projection")
	}
	if err := json.Unmarshal([]byte(`{"profile":"leapview.contract/v1"}`), &zero); err == nil {
		t.Fatal("JSON unmarshalling manufactured a sealed Source")
	}
	if _, implements := any(SourceView{}).(Projection); implements {
		t.Fatal("generated SourceView unexpectedly satisfies Projection")
	}
	if _, implements := any(ModelView{}).(Projection); implements {
		t.Fatal("generated ModelView unexpectedly satisfies Projection")
	}
	if _, implements := any(SemanticModelView{}).(Projection); implements {
		t.Fatal("generated SemanticModelView unexpectedly satisfies Projection")
	}
}

func TestProjectedPayloadAndCanonicalBytesAreMutationSafe(t *testing.T) {
	authored := decodeSource(t, sourceJSON(false))
	projected, err := ProjectSource(authored, testContract)
	if err != nil {
		t.Fatal(err)
	}
	first, err := CanonicalBytes(projected)
	if err != nil {
		t.Fatal(err)
	}
	first[0] = 'x'
	authored.Metadata.Name = "mutated"
	authored.Spec.Connection = "connection:mutated"
	second, err := CanonicalBytes(projected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(second), "mutated") {
		t.Fatalf("authored mutation leaked into sealed projection: %s", second)
	}
	if second[0] != '{' {
		t.Fatalf("canonical bytes were not returned as a defensive copy: %s", second)
	}
}

func projectSource(t *testing.T, encoded []byte) Source {
	t.Helper()
	value, err := ProjectSource(decodeSource(t, encoded), testContract)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func decodeSource(t *testing.T, encoded []byte) projectcontracts.Source {
	t.Helper()
	var value projectcontracts.Source
	decodeContract(t, encoded, &value)
	return value
}

func decodeContract(t *testing.T, encoded []byte, output any) {
	t.Helper()
	if err := json.Unmarshal(encoded, output); err != nil {
		t.Fatalf("decode generated contract: %v", err)
	}
}

func sourceJSON(reverse bool) []byte {
	fields := `"customer_id":{"datatype":"String","nullable":false,"description":"INTERNAL DESCRIPTION","tags":["internal-tag"],"criticalDataElement":true,"classification":"restricted","authoritativeDefinitions":[{"type":"Business","url":"HTTPS://EXAMPLE.COM:443/a/../%7eglossary"}]},"amount":{"datatype":"Decimal"}`
	if reverse {
		fields = `"amount":{"datatype":"Decimal"},"customer_id":{"datatype":"String","nullable":false,"description":"INTERNAL DESCRIPTION","tags":["internal-tag"],"criticalDataElement":true,"classification":"restricted","authoritativeDefinitions":[{"type":"Business","url":"HTTPS://EXAMPLE.COM:443/a/../%7eglossary"}]}`
	}
	return []byte(`{
  "apiVersion":"leapview.dev/v1","kind":"Source",
  "metadata":{"id":"source:customers","name":"customers","owner":"secret-owner","description":"private provenance","contract":{"version":"1.2.3","compatibility":"backward"}},
  "spec":{"connection":"connection:private","location":{"type":"path","path":"/private/customer.csv","format":"csv"},"schema":{"mode":"strict","fields":{` + fields + `}},"freshness":{"basis":"revision","revision":"2026-01-02T03:04:05.1200+02:00","warningAfter":{"amount":2,"unit":"hour"}}}
}`)
}
