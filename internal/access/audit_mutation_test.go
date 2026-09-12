package access

import "testing"

func TestValidateAuditedMutationInputs(t *testing.T) {
	if err := ValidateAuditedMutationInputs(nil); err == nil {
		t.Fatal("empty audit input must be rejected")
	}
	if err := ValidateAuditedMutationInputs([]AuditEventInput{{Action: "project.updated"}}); err != nil {
		t.Fatalf("non-empty audit input rejected: %v", err)
	}
}
