package operation

import (
	"encoding/json"
	"testing"

	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

func TestGeneratedAccessAuditPayloadEnvelopeAndRedaction(t *testing.T) {
	durable, err := accessgen.EncodeGenCreatePrincipalAuditPayload(accessgen.GenSchemaPrincipalCreatedAuditPayload{Email: "alice@example.com"})
	if err != nil {
		t.Fatalf("encode durable principal audit: %v", err)
	}
	logSafe, err := accessgen.EncodeGenCreatePrincipalAuditPayloadForLog(accessgen.GenSchemaPrincipalCreatedAuditPayload{Email: "alice@example.com"})
	if err != nil {
		t.Fatalf("encode log principal audit: %v", err)
	}
	assertAuditPayloadValue(t, durable, "PrincipalCreatedAuditPayload", "email", "alice@example.com")
	assertAuditPayloadValue(t, logSafe, "PrincipalCreatedAuditPayload", "email", "[REDACTED]")
}

func assertAuditPayloadValue(t *testing.T, encoded, schema, field, want string) {
	t.Helper()
	var envelope struct {
		SchemaVersion int                    `json:"schemaVersion"`
		Retention     string                 `json:"retention"`
		PayloadSchema string                 `json:"payloadSchema"`
		Payload       map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
		t.Fatalf("decode audit envelope: %v", err)
	}
	if envelope.SchemaVersion != 1 || envelope.Retention != "security" || envelope.PayloadSchema != schema || envelope.Payload[field] != want {
		t.Fatalf("audit envelope = %#v, want %s/%s=%q", envelope, schema, field, want)
	}
}
