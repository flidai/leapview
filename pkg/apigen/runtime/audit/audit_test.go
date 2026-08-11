package audit

import (
	"strings"
	"testing"
)

type fixturePayload struct {
	Action string `json:"action"`
	Actor  string `json:"actor"`
	Note   string `json:"note"`
	Token  string `json:"token"`
}

func TestEncodeAppliesAuditAndLogSensitivity(t *testing.T) {
	contract := Contract{
		Schema: "FixtureAuditPayload", SchemaVersion: 2, Retention: RetentionSecurity,
		Fields: []FieldContract{
			{Name: "action", Sensitivity: SensitivityPublic},
			{Name: "actor", Sensitivity: SensitivityPII},
			{Name: "note", Sensitivity: SensitivityInternal},
			{Name: "token", Sensitivity: SensitivitySecret},
		},
	}
	payload := fixturePayload{Action: "created", Actor: "principal-1", Note: "reviewed", Token: "do-not-store"}

	auditJSON, err := EncodeForAudit(contract, payload)
	if err != nil {
		t.Fatalf("EncodeForAudit() error = %v", err)
	}
	for _, want := range []string{`"schemaVersion":2`, `"retention":"security"`, `"actor":"principal-1"`, `"note":"reviewed"`, `"token":"[REDACTED]"`} {
		if !strings.Contains(auditJSON, want) {
			t.Fatalf("audit JSON = %s, want %s", auditJSON, want)
		}
	}
	if strings.Contains(auditJSON, payload.Token) {
		t.Fatalf("audit JSON leaked secret: %s", auditJSON)
	}

	logJSON, err := EncodeForLog(contract, payload)
	if err != nil {
		t.Fatalf("EncodeForLog() error = %v", err)
	}
	if !strings.Contains(logJSON, `"action":"created"`) || strings.Contains(logJSON, payload.Actor) || strings.Contains(logJSON, payload.Note) || strings.Contains(logJSON, payload.Token) {
		t.Fatalf("safe log JSON = %s", logJSON)
	}
}

func TestEncodeRejectsPayloadDrift(t *testing.T) {
	contract := Contract{
		Schema: "Fixture", SchemaVersion: 1, Retention: RetentionStandard,
		Fields: []FieldContract{{Name: "action", Sensitivity: SensitivityPublic}},
	}
	if _, err := EncodeForAudit(contract, fixturePayload{}); err == nil || !strings.Contains(err.Error(), "is not declared") {
		t.Fatalf("EncodeForAudit() error = %v", err)
	}
}
