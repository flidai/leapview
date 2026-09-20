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
	event := AuditEventSignalFromDomain(access.AuditEvent{ID: "a1", ProjectID: "project:test", Action: "service_principal_secret.created", MetadataJSON: `{"secretId":"s1"}`})
	if event.ProjectID != "project:test" || event.Metadata["secretId"] != "s1" || event.Metadata == nil {
		t.Fatalf("metadata = %#v", event.Metadata)
	}
}

type auditReadRepository struct {
	access.Repository
	filter access.AuditEventFilter
	rows   []access.AuditEvent
}

func (r *auditReadRepository) ListAuditEvents(_ context.Context, filter access.AuditEventFilter) ([]access.AuditEvent, error) {
	r.filter = filter
	return append([]access.AuditEvent(nil), r.rows...), nil
}

func (*auditReadRepository) ListPrincipals(context.Context, access.PrincipalFilter) ([]access.Principal, error) {
	return nil, nil
}

func (*auditReadRepository) ListAllGroups(context.Context) ([]access.Group, error) {
	return nil, nil
}

func (*auditReadRepository) ListServicePrincipals(context.Context) ([]access.Principal, error) {
	return nil, nil
}

func TestLoadAuditLogBindsProjectAndPassesDateFilters(t *testing.T) {
	repository := &auditReadRepository{rows: []access.AuditEvent{{ID: "audit-1", ProjectID: "project:test", CreatedAt: "2026-09-16T00:00:00Z"}}}
	filters := AuditLogFilters{ProjectID: "project:foreign", From: "2026-09-01T00:00:00Z", To: "2026-10-01T00:00:00Z"}
	state, err := LoadAuditLog(t.Context(), repository, "project:test", filters, "", 25)
	if err != nil {
		t.Fatal(err)
	}
	if repository.filter.ProjectID != "project:test" || !repository.filter.IncludeUnscoped {
		t.Fatalf("repository project scope = %#v, want bound project plus unscoped events", repository.filter)
	}
	if repository.filter.From != filters.From || repository.filter.To != filters.To {
		t.Fatalf("repository date filters = %#v, want from/to preserved", repository.filter)
	}
	if state.Filters.ProjectID != "project:test" || len(state.Items) != 1 || state.Items[0].ProjectID != "project:test" {
		t.Fatalf("audit state = %#v, want bound project and project identity", state)
	}
}

func TestLoadAuditLogRequiresBoundProject(t *testing.T) {
	repository := &auditReadRepository{}
	if _, err := LoadAuditLog(t.Context(), repository, "", AuditLogFilters{}, "", 25); err == nil {
		t.Fatal("LoadAuditLog accepted an unbound project")
	}
	if repository.filter.ProjectID != "" {
		t.Fatalf("repository was read without a bound project: %#v", repository.filter)
	}
}

type testServiceAccountReader struct{}

func (testServiceAccountReader) ListServicePrincipals(context.Context) ([]access.Principal, error) {
	return []access.Principal{{ID: "svc-2", DisplayName: "Zulu", Kind: access.PrincipalKindServicePrincipal}, {ID: "svc-1", DisplayName: "Alpha", Kind: access.PrincipalKindServicePrincipal}}, nil
}
func (testServiceAccountReader) ListServicePrincipalSecrets(context.Context, string) ([]access.ServicePrincipalSecret, error) {
	return []access.ServicePrincipalSecret{{ID: "secret-1", ServicePrincipalID: "svc-1", Name: "ci"}}, nil
}
func (testServiceAccountReader) CountServicePrincipalSecrets(context.Context) (map[string]int, error) {
	return map[string]int{"svc-1": 1}, nil
}

func TestLoadServiceAccountsSortsAndSelectsMetadata(t *testing.T) {
	signal, err := LoadServiceAccounts(context.Background(), testServiceAccountReader{}, "svc-1")
	if err != nil {
		t.Fatal(err)
	}
	if signal.Items[0].ID != "svc-1" || signal.Items[0].SecretCount != 1 || signal.SelectedID != "svc-1" || len(signal.Secrets) != 1 {
		t.Fatalf("signal = %#v", signal)
	}
}
