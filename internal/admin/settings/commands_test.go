package settings

import "testing"

func TestServiceAccountAuditActionUsesTypeSpecVocabulary(t *testing.T) {
	tests := map[string]string{
		"create":        "service_principal.created",
		"update":        "service_principal.updated",
		"delete":        "service_principal.deleted",
		"disable":       "service_principal.disabled",
		"enable":        "service_principal.enabled",
		"create_secret": "service_principal_secret.created",
		"revoke_secret": "service_principal_secret.revoked",
		"rotate_secret": "service_principal_secret.rotated",
		"revoke_all":    "service_principal_credentials.revoked_all",
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
