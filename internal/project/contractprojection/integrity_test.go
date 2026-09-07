package contractprojection

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	contracts "github.com/flidai/leapview/internal/project/contracts"
)

func testSource(t *testing.T) contracts.Source {
	t.Helper()
	var source contracts.Source
	if err := json.Unmarshal([]byte(`{"apiVersion":"leapview.dev/v1","kind":"Source","metadata":{"id":"source:orders","name":"orders"},"spec":{"connection":"warehouse","location":{"type":"path","path":"orders.csv","format":"csv"},"schema":{"mode":"strict","fields":{"id":{"datatype":"Integer"}}}}}`), &source); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestSealedProjectionIntegrity(t *testing.T) {
	input := testSource(t)
	projection, err := ProjectSource(input, Contract{Version: "1.2.3", Compatibility: "backward"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := CanonicalBytes(projection)
	if err != nil {
		t.Fatal(err)
	}
	input.Metadata.Name = "mutated"
	after, err := CanonicalBytes(projection)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("input mutation changed sealed projection: %s %v", after, err)
	}
	view, err := DecodeSourcePublication(before)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := any(view).(Projection); ok {
		t.Fatal("read view can manufacture authority")
	}
	var sealed Source
	if err := json.Unmarshal(before, &sealed); err == nil {
		t.Fatal("JSON manufactured sealed authority")
	}
	for _, invalid := range []Projection{nil, Source{}, Model{}, SemanticModel{}} {
		if _, err := CanonicalBytes(invalid); err == nil {
			t.Fatalf("accepted unsealed %T", invalid)
		}
	}
	digest, err := Digest(projection)
	if err != nil {
		t.Fatal(err)
	}
	readDigest, err := DigestSourcePublication(before)
	if err != nil || digest != readDigest {
		t.Fatalf("replay identity mismatch %s %s %v", digest, readDigest, err)
	}
	for _, invalid := range [][]byte{
		bytes.Replace(before, []byte(`"profile":`), []byte(`"unknown":true,"profile":`), 1),
		bytes.Replace(before, []byte(`"datatype":"Integer"`), []byte(`"datatype":"Imaginary"`), 1),
		append(append([]byte{}, before...), []byte(` {}`)...),
	} {
		if _, err := DecodeSourcePublication(invalid); err == nil {
			t.Fatalf("accepted invalid publication %s", invalid)
		}
	}
}

func TestAuthoredEncodingFailsBeforeJSONReplacement(t *testing.T) {
	input := testSource(t)
	input.Metadata.Name = string([]byte{'x', 0xff})
	if _, err := ProjectSource(input, Contract{Version: "1.0.0", Compatibility: "backward"}); err == nil {
		t.Fatal("invalid UTF-8 silently replaced")
	}
	for _, input := range []any{map[string]any{"value": string([]byte{0xff})}, map[string]string{string([]byte{0xff}): "value"}} {
		if err := validateInputEncoding(input); err == nil {
			t.Fatal("nested invalid UTF-8 accepted")
		}
	}
	if err := validateInputEncoding("\uFFFD"); err != nil {
		t.Fatalf("valid replacement character rejected: %v", err)
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if err := validateInputEncoding(cycle); err == nil {
		t.Fatal("cyclic input accepted")
	}
}

func TestAuthoredContractMetadataIsAuthoritative(t *testing.T) {
	input := testSource(t)
	input.Metadata.Contract = &contracts.ContractMetadata{Version: "2.1.0", Compatibility: "backward"}

	projected, err := ProjectSource(input, Contract{Version: "2.1.0", Compatibility: "backward"})
	if err != nil {
		t.Fatalf("matching authored contract rejected: %v", err)
	}
	canonical, err := CanonicalBytes(projected)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(canonical, []byte(`"version":"2.1.0"`)) {
		t.Fatalf("canonical bytes omitted authored contract: %s", canonical)
	}
	if _, err := ProjectSource(input, Contract{Version: "2.1.1", Compatibility: "backward"}); err == nil {
		t.Fatal("mismatched constructor contract silently overrode authored metadata")
	}
	if _, err := ProjectSource(input, Contract{}); err != nil {
		t.Fatalf("authored contract did not provide constructor fallback: %v", err)
	}
}

func TestGovernanceFieldsAreProjectedAndAnnotationsExcluded(t *testing.T) {
	var input contracts.Source
	const raw = `{"apiVersion":"leapview.dev/v1","kind":"Source","metadata":{"id":"source:orders","name":"orders","contract":{"version":"1.2.3","compatibility":"backward"}},"spec":{"connection":"warehouse","location":{"type":"path","path":"orders.csv","format":"csv"},"schema":{"mode":"strict","fields":{"order_id":{"datatype":"String","nullable":false,"tags":["identifier"],"criticalDataElement":true,"classification":"restricted","authoritativeDefinitions":[{"type":"transformationImplementation","url":"https://CATALOG.example:443/definitions/../customer%2Did"},{"type":"transformationImplementation","url":"https://catalog.example/customer-id"},{"type":"businessDefinition","url":"https://catalog.example/customer-id"}],"deprecation":{"since":"1.0.0","reason":"Use customer_key instead","replacement":"customer_key"}}}}}}`
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal(err)
	}
	projected, err := ProjectSource(input, Contract{})
	if err != nil {
		t.Fatalf("project governed source: %v", err)
	}
	canonical, err := CanonicalBytes(projected)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"criticalDataElement":true`, `"classification":"restricted"`, `"deprecation"`, `"replacement":"customer_key"`, `"url":"https://catalog.example/customer-id"`} {
		if !bytes.Contains(canonical, []byte(want)) {
			t.Fatalf("canonical bytes omitted %s: %s", want, canonical)
		}
	}
	if bytes.Contains(canonical, []byte(`"tags"`)) {
		t.Fatalf("descriptive tags entered canonical bytes: %s", canonical)
	}
	if _, err := DecodeSourcePublication(canonical); err != nil {
		t.Fatalf("canonical governed source rejected on replay: %v", err)
	}
	if strings.Count(string(canonical), `"url":"https://catalog.example/customer-id"`) != 2 {
		t.Fatalf("authoritative definitions were not deduplicated: %s", canonical)
	}
	unnormalized := bytes.Replace(canonical, []byte(`"url":"https://catalog.example/customer-id"`), []byte(`"url":"HTTPS://catalog.example/customer-id"`), 1)
	if _, err := DecodeSourcePublication(unnormalized); err == nil {
		t.Fatal("replay accepted a non-canonical authoritative definition URL")
	}
}

func TestProjectionRejectsInvalidContractVersion(t *testing.T) {
	for _, version := range []string{"", "1", "1.0", "1.0.0-01", "v1.0.0"} {
		if _, err := ProjectSource(testSource(t), Contract{Version: version, Compatibility: "backward"}); err == nil {
			t.Errorf("accepted version %q", version)
		}
	}
}

func TestSemanticLiteralSetAndRangeIdentity(t *testing.T) {
	unicodeSet, err := canonicalAllowedValuesTyped([]any{"é", "e\u0301"}, "String", true)
	if err != nil || len(unicodeSet) != 1 {
		t.Fatalf("normalization must precede set deduplication: %v %v", unicodeSet, err)
	}
	left, err := canonicalAllowedValuesTyped([]any{"z", "\\", "a", "a"}, "String", true)
	if err != nil {
		t.Fatal(err)
	}
	right, err := canonicalAllowedValuesTyped([]any{"a", "z", "\\"}, "String", true)
	if err != nil {
		t.Fatal(err)
	}
	leftBytes, _ := json.Marshal(left)
	rightBytes, _ := json.Marshal(right)
	if !bytes.Equal(leftBytes, rightBytes) || len(left) != 3 {
		t.Fatal("set ordering/deduplication unstable")
	}
	for i := 1; i < len(left); i++ {
		if canonicalValueSortKey(left[i-1]) >= canonicalValueSortKey(left[i]) {
			t.Fatal("set not sorted by canonical element bytes")
		}
	}
	rangeValues, err := canonicalAllowedValuesTyped([]any{json.Number("2"), json.Number("10")}, "Integer", false)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalValueKey(rangeValues[0]) != "Integer\x002" || canonicalValueKey(rangeValues[1]) != "Integer\x0010" {
		t.Fatal("range endpoints reordered")
	}
	for _, values := range [][]any{{"a", true}, {nil}, {1.5}} {
		if _, err := canonicalAllowedValues(values); err == nil {
			t.Fatalf("accepted invalid literals %#v", values)
		}
	}
	precise, err := canonicalLiteral("Decimal", json.Number("9007199254740993.2500"))
	if err != nil || canonicalValueKey(precise) != "Decimal\x009007199254740993.25" {
		t.Fatalf("exact numeric identity lost: %#v %v", precise, err)
	}
}
