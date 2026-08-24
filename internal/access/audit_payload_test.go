package access

import (
	"strings"
	"testing"
)

const testGeneratedAuditEnvelope = `{"schemaVersion":1,"retention":"security","payloadSchema":"ExampleAuditPayload","payload":{"existing":"value"}}`

func TestRewriteGeneratedAuditEnvelopePayloadRejectsMalformedOrAmbiguousEnvelope(t *testing.T) {
	for _, raw := range []string{
		`{"schemaVersion":1,"retention":"security","payloadSchema":"Example","payload":{"added":""}} trailing`,
		`{"schemaVersion":1,"retention":"security","payloadSchema":"Example","payload":{},"payload":{}}`,
		`{"schemaVersion":1,"retention":"security","payloadSchema":"Example"}`,
		`{"schemaVersion":1,"retention":"security","payloadSchema":"Example","payload":null}`,
		`{"schemaVersion":1,"retention":"security","payloadSchema":"Example","payload":[]}`,
	} {
		if _, err := RewriteGeneratedAuditEnvelopePayload(raw, map[string]any{"existing": "value"}); err == nil {
			t.Fatalf("accepted malformed envelope %q", raw)
		}
	}
}

func TestRewriteGeneratedAuditEnvelopePayloadRejectsTopLevelDrift(t *testing.T) {
	if _, err := RewriteGeneratedAuditEnvelopePayload(`{"schemaVersion":1,"retention":"security","payloadSchema":"Example","payload":{},"drift":true}`, nil); err == nil {
		t.Fatal("accepted undeclared top-level envelope field")
	}
	if _, err := RewriteGeneratedAuditEnvelopePayload(testGeneratedAuditEnvelope, map[string]any{"payload": "drift"}); err == nil {
		t.Fatal("accepted payload rewrite targeting top-level envelope field")
	}
}

func TestRewriteGeneratedAuditEnvelopePayloadRejectsNilFieldValue(t *testing.T) {
	if _, err := RewriteGeneratedAuditEnvelopePayload(testGeneratedAuditEnvelope, map[string]any{"existing": nil}); err == nil {
		t.Fatal("accepted nil payload field value")
	}
}

func TestRewriteGeneratedAuditEnvelopePayloadRejectsForbiddenNestedFields(t *testing.T) {
	for key := range map[string]struct{}{"secret": {}, "rawSql": {}, "authorization": {}} {
		if _, err := RewriteGeneratedAuditEnvelopePayload(testGeneratedAuditEnvelope, map[string]any{"existing": map[string]any{key: "forbidden"}}); err == nil {
			t.Fatalf("accepted forbidden nested field %q", key)
		}
	}
}

func TestRewriteGeneratedAuditEnvelopePayloadEnrichesNestedPayload(t *testing.T) {
	raw := `{"schemaVersion":1,"retention":"security","payloadSchema":"ExampleAuditPayload","payload":{"existing":"value","sizeBytes":0,"digest":""}}`
	got, err := RewriteGeneratedAuditEnvelopePayload(raw, map[string]any{"sizeBytes": int64(42), "digest": "sha256:test"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"payload":{"digest":"sha256:test","existing":"value","sizeBytes":42}`) {
		t.Fatalf("rewritten envelope = %s", got)
	}
	if strings.Contains(got, `"sizeBytes":42,"payload"`) || strings.Contains(got, `"digest":"sha256:test","payload"`) {
		t.Fatalf("enrichment drifted outside payload: %s", got)
	}
}
