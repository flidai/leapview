package connectionbinding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAdministrationAuditsSuccessfulMetadataMutationsWithoutCredentialMetadata(t *testing.T) {
	now := time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)
	binding := validTargetBinding(t)
	repository := &administrationRepository{binding: binding}
	audit := &recordingAdministrationAudit{}
	service, err := NewAdministration(AdministrationConfig{
		Repository: repository, Authorize: allowAdministration,
		Dependencies: staticDependencyInspector{}, Audit: audit,
		Now: func() time.Time {
			now = now.Add(time.Minute)
			return now
		},
	})
	require.NoError(t, err)
	key := BindingKey{
		Scope: binding.Scope, TargetID: binding.TargetID, ConnectionID: binding.ConnectionID,
	}

	configuration := binding.Configuration()
	configuration.Endpoint.Host = "warehouse-next.internal"
	updated, err := service.UpdateConfiguration(context.Background(), UpdateConfigurationRequest{
		ActorID: "operator-1", Key: key, Configuration: configuration,
		ExpectedRevision: binding.Revision,
	})
	require.NoError(t, err)
	if _, err := service.Disable(context.Background(), "operator-1", key); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enable(context.Background(), "operator-1", key); err != nil {
		t.Fatal(err)
	}

	if len(audit.events) != 3 {
		t.Fatalf("administration audit events = %#v", audit.events)
	}
	wantActions := []AdministrationAuditAction{
		AuditBindingUpdated, AuditBindingDisabled, AuditBindingEnabled,
	}
	for index, event := range audit.events {
		if event.Action != wantActions[index] || event.Actor != "operator-1" ||
			event.BindingID != binding.ID || event.TargetID != binding.TargetID ||
			event.ProjectID != binding.Scope.ProjectID ||
			event.ConnectionID != binding.ConnectionID ||
			event.Outcome != AdministrationAuditSucceeded {
			t.Fatalf("audit event[%d] = %#v", index, event)
		}
		encoded, err := json.Marshal(event)
		require.NoError(t, err)
		for _, forbidden := range []string{
			"source-secret", "connection_string", "project-1", "/leapview/sales",
		} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("audit event disclosed %q: %s", forbidden, encoded)
			}
		}
	}
	if audit.events[0].Revision != updated.Revision {
		t.Fatalf("update audit revision = %d, want %d", audit.events[0].Revision, updated.Revision)
	}
}

func TestAdministrationRequiresAuditRecorder(t *testing.T) {
	_, err := NewAdministration(AdministrationConfig{
		Repository: &administrationRepository{}, Authorize: allowAdministration,
		Dependencies: staticDependencyInspector{}, Now: time.Now,
	})
	if !errors.Is(err, ErrAdministrationAuditUnavailable) {
		t.Fatalf("NewAdministration() error = %v, want ErrAdministrationAuditUnavailable", err)
	}
}

func TestAdministrationPreservesMutationAndObservesBestEffortAuditFailure(t *testing.T) {
	binding := validTargetBinding(t)
	repository := &administrationRepository{binding: binding}
	var logs bytes.Buffer
	service, err := NewAdministration(AdministrationConfig{
		Repository: repository, Authorize: allowAdministration,
		Dependencies: staticDependencyInspector{},
		Audit:        failingAdministrationAudit{},
		Logger:       slog.New(slog.NewJSONHandler(&logs, nil)),
		Now:          func() time.Time { return binding.UpdatedAt.Add(time.Minute) },
	})
	require.NoError(t, err)
	disabled, err := service.Disable(context.Background(), "operator-1", BindingKey{
		Scope: binding.Scope, TargetID: binding.TargetID, ConnectionID: binding.ConnectionID,
	})
	if err != nil || disabled.Enabled || repository.binding.Enabled {
		t.Fatalf("Disable() result = %#v, repository = %#v, error = %v", disabled, repository.binding, err)
	}
	if output := logs.String(); !strings.Contains(output, "best-effort connection administration audit failed") ||
		!strings.Contains(output, string(AuditBindingDisabled)) || !strings.Contains(output, binding.ID.String()) {
		t.Fatalf("audit failure log = %s", output)
	}
}

func TestAdministrationAuditsBindingCreation(t *testing.T) {
	now := time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)
	input := validTargetBindingInput()
	input.Now = time.Time{}
	repository := &administrationRepository{}
	audit := &recordingAdministrationAudit{}
	service, err := NewAdministration(AdministrationConfig{
		Repository: repository, Authorize: allowAdministration,
		Dependencies: staticDependencyInspector{}, Audit: audit,
		Now: func() time.Time { return now },
	})
	require.NoError(t, err)
	created, err := service.Create(context.Background(), "operator-1", input)
	require.NoError(t, err)
	if len(audit.events) != 1 || audit.events[0].Action != AuditBindingCreated ||
		audit.events[0].Revision != created.Revision {
		t.Fatalf("create audit events = %#v", audit.events)
	}
}

type recordingAdministrationAudit struct {
	events []AdministrationAuditEvent
}

func (audit *recordingAdministrationAudit) RecordConnectionAdministration(
	_ context.Context,
	event AdministrationAuditEvent,
) error {
	audit.events = append(audit.events, event)
	return nil
}

type failingAdministrationAudit struct{}

func (failingAdministrationAudit) RecordConnectionAdministration(
	context.Context,
	AdministrationAuditEvent,
) error {
	return errors.New("audit unavailable")
}
