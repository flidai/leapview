package settings

import "testing"

func TestServiceAccountAuditActionUsesTypeSpecVocabulary(t *testing.T) {
	tests := map[string]string{
		"create":        "service_principal.created",
		"update":        "service_principal.updated",
		"delete":        "service_principal.deleted",
		"create_secret": "service_principal_secret.created",
		"revoke_secret": "service_principal_secret.revoked",
	}
	for command, want := range tests {
		got, ok := serviceAccountAuditAction(command)
		if !ok || got != want {
			t.Errorf("serviceAccountAuditAction(%q) = %q, %t; want %q, true", command, got, ok, want)
		}
	}
	if got, ok := serviceAccountAuditAction("service_account.create"); ok || got != "" {
		t.Fatalf("legacy action unexpectedly accepted: %q, %t", got, ok)
	}
}
