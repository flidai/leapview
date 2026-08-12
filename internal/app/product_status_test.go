package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/platform/buildinfo"
)

func TestProductAdministrationStatusIsRedacted(t *testing.T) {
	configuration := config.Config{
		LocalAuth: true, OIDCProviderID: "corporate", OIDCIssuerURL: "https://issuer.secret.test",
		OIDCClientID: "secret-client-id", OIDCSecret: "secret-client-secret", OIDCCallbackURL: "https://app.test/callback",
		AzureClientID: "azure-secret-id", AzureSecret: "azure-secret-value", AzureCallbackURL: "https://app.test/azure-callback", AzureTenant: "secret-tenant",
		SCIMBearerToken: "secret-scim-bearer", AgentAPIKey: "secret-agent-key", AgentModel: "model-name",
		ManagedDataBackend: "s3", ManagedDataS3Bucket: "secret-bucket", QueryResultMaxRows: 1000, QueryResultMaxBytes: 1024,
		ManagedDataMaxFiles: 25, ManagedDataMaxFileBytes: 2048, ManagedDataMaxRevisionBytes: 4096,
	}
	status := productAdministrationStatus(configuration, "lvinst_1", "https://app.test", "prod", buildinfo.Identity{Version: "1.2.3"})
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{
		"issuer.secret.test", "secret-client-id", "secret-client-secret", "azure-secret-id", "azure-secret-value",
		"secret-tenant", "secret-scim-bearer", "secret-agent-key", "secret-bucket", "model-name",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("product status leaked %q: %s", forbidden, text)
		}
	}
	if !status.Authentication.OIDC.Enabled || status.Authentication.OIDC.Provider != "corporate" || !status.API.ServicePrincipals.Enabled || status.System.StorageBackend != "s3" {
		t.Fatalf("safe status fields = %#v", status)
	}
	if status.System.Limits.QueryResultMaxRows != 1000 || status.System.Limits.QueryResultMaxBytes != 1024 ||
		status.System.Limits.ManagedDataMaxFiles != 25 || status.System.Limits.ManagedDataMaxFileBytes != 2048 ||
		status.System.Limits.ManagedDataMaxRevisionBytes != 4096 {
		t.Fatalf("system limits = %#v", status.System.Limits)
	}
}
