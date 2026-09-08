package contractprojection

import (
	"encoding/json"
	"testing"

	contracts "github.com/flidai/leapview/internal/project/contracts"
)

func governedSourceProjection(t *testing.T, field string) Source {
	t.Helper()
	var input contracts.Source
	raw := `{"apiVersion":"leapview.dev/v1","kind":"Source","metadata":{"id":"source:orders","name":"orders"},"spec":{"connection":"warehouse","location":{"type":"path","path":"orders.csv","format":"csv"},"schema":{"mode":"strict","fields":{"id":` + field + `}}}}`
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal(err)
	}
	value, err := ProjectSource(input, Contract{Version: "1.0.0", Compatibility: "backward"})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestGovernanceFieldsAffectCanonicalIdentity(t *testing.T) {
	baseline, err := Digest(governedSourceProjection(t, `{"datatype":"Integer"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`{"datatype":"Integer","nullable":false}`,
		`{"datatype":"Integer","criticalDataElement":false}`,
		`{"datatype":"Integer","classification":"restricted"}`,
		`{"datatype":"Integer","authoritativeDefinitions":[]}`,
		`{"datatype":"Integer","authoritativeDefinitions":[{"type":"businessDefinition","url":"https://example.test/id"}]}`,
		`{"datatype":"Integer","deprecation":{"since":"1.0.0","reason":"Use key","replacement":"key"}}`,
	} {
		t.Run(field, func(t *testing.T) {
			projection := governedSourceProjection(t, field)
			digest, err := Digest(projection)
			if err != nil || digest == baseline {
				t.Fatalf("governance field lost from identity: digest=%s err=%v", digest, err)
			}
			data, err := CanonicalBytes(projection)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSourcePublication(data); err != nil {
				t.Fatalf("canonical projection cannot replay: %v", err)
			}
		})
	}
}

func TestDefinitionOrderingMatchesReplayWithJSONEscaping(t *testing.T) {
	first := `{"type":"businessDefinition","url":"https://example.test/?a=1&b=2"}`
	second := `{"type":"businessDefinition","url":"https://example.test/?a=10"}`
	left := governedSourceProjection(t, `{"datatype":"Integer","authoritativeDefinitions":[`+first+`,`+second+`]}`)
	right := governedSourceProjection(t, `{"datatype":"Integer","authoritativeDefinitions":[`+second+`,`+first+`,`+first+`]}`)
	leftDigest, err := Digest(left)
	if err != nil {
		t.Fatal(err)
	}
	rightDigest, err := Digest(right)
	if err != nil || leftDigest != rightDigest {
		t.Fatalf("definition order/duplicates changed identity: %v", err)
	}
	data, err := CanonicalBytes(left)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSourcePublication(data); err != nil {
		t.Fatalf("replay ordering differs from projection ordering: %v", err)
	}
}
