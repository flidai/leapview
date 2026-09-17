package settings

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestNormalizeAuditLogCommandResetsCursorAndBoundsLimit(t *testing.T) {
	command := NormalizeAuditLogCommand(AuditLogCommand{Action: "filter", PageToken: "stale", Limit: 500, Filters: AuditLogFilters{Action: " create "}})
	if command.PageToken != "" || command.Limit != 100 || command.Filters.Action != "create" {
		t.Fatalf("normalized command = %#v", command)
	}
}

func TestAuditEventSignalParsesMetadataWithoutRawSecret(t *testing.T) {
	event := AuditEventSignalFromDomain(access.AuditEvent{ID: "a1", ProjectID: "sales", Action: "service_principal_secret.created", MetadataJSON: `{"secretId":"s1"}`})
	if event.Metadata["secretId"] != "s1" || event.Metadata == nil {
		t.Fatalf("metadata = %#v", event.Metadata)
	}
	if event.ProjectID != "sales" {
		t.Fatalf("project id = %q, want sales", event.ProjectID)
	}
}

type testAuditLogReader struct{}

func (testAuditLogReader) ListAuditEvents(context.Context, access.AuditEventFilter) ([]access.AuditEvent, error) {
	return []access.AuditEvent{{
		ID: "event-1", PrincipalID: "principal-1", Action: "agent_tool.called", ResourceKind: "agent_tool",
		ResourceID: "querySemanticModel", Status: "success", CreatedAt: "2026-09-17T14:17:04Z",
	}}, nil
}

func (testAuditLogReader) ListPrincipals(context.Context, access.PrincipalFilter) ([]access.Principal, error) {
	return []access.Principal{{ID: "principal-1", DisplayName: "Platform Admin", Email: "admin@example.com"}}, nil
}

func TestLoadAuditLogEnrichesActorIdentity(t *testing.T) {
	signal, err := LoadAuditLog(context.Background(), testAuditLogReader{}, AuditLogFilters{}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(signal.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(signal.Items))
	}
	event := signal.Items[0]
	if event.PrincipalName != "Platform Admin" || event.PrincipalEmail != "admin@example.com" {
		t.Fatalf("actor identity = %q <%s>", event.PrincipalName, event.PrincipalEmail)
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
