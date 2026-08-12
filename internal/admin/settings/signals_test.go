package settings

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/workspace"
)

func TestWorkspaceSignalFromSummaryEscapesLinks(t *testing.T) {
	item := WorkspaceSignalFromSummary(workspace.Summary{ID: "sales team", Title: "Sales", Description: "Revenue"}, "prod")
	if item.Href != "/workspaces/sales%20team" || item.Links.Self != "/api/v1/workspaces/sales%20team" {
		t.Fatalf("links = %#v", item)
	}
}

func TestNormalizeAuditLogCommandResetsCursorAndBoundsLimit(t *testing.T) {
	command := NormalizeAuditLogCommand(AuditLogCommand{Action: "filter", PageToken: "stale", Limit: 500, Filters: AuditLogFilters{Action: " create "}})
	if command.PageToken != "" || command.Limit != 100 || command.Filters.Action != "create" {
		t.Fatalf("normalized command = %#v", command)
	}
}

func TestAuditEventSignalParsesMetadataWithoutRawSecret(t *testing.T) {
	event := AuditEventSignalFromDomain(access.AuditEvent{ID: "a1", Action: "service_principal_secret.created", MetadataJSON: `{"secretId":"s1"}`})
	if event.Metadata["secretId"] != "s1" || event.Metadata == nil {
		t.Fatalf("metadata = %#v", event.Metadata)
	}
}

type testServiceAccountReader struct{}

func (testServiceAccountReader) ListServicePrincipals(context.Context) ([]access.Principal, error) {
	return []access.Principal{{ID: "svc-2", DisplayName: "Zulu", Kind: access.PrincipalKindServicePrincipal}, {ID: "svc-1", DisplayName: "Alpha", Kind: access.PrincipalKindServicePrincipal}}, nil
}
func (testServiceAccountReader) ListServicePrincipalSecrets(context.Context, string) ([]access.ServicePrincipalSecret, error) {
	return []access.ServicePrincipalSecret{{ID: "secret-1", ServicePrincipalID: "svc-1", Name: "ci"}}, nil
}

func TestLoadServiceAccountsSortsAndSelectsMetadata(t *testing.T) {
	signal, err := LoadServiceAccounts(context.Background(), testServiceAccountReader{}, "svc-1")
	if err != nil {
		t.Fatal(err)
	}
	if signal.Items[0].ID != "svc-1" || signal.SelectedID != "svc-1" || len(signal.Secrets) != 1 {
		t.Fatalf("signal = %#v", signal)
	}
}
