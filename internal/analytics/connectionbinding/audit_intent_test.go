package connectionbinding

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
)

func TestBuildConnectionAdministrationAuditIntentIsStableAndRedacted(t *testing.T) {
	binding := validTargetBinding(t)
	event := AdministrationAuditEvent{
		ProjectID: binding.Scope.ProjectID, BindingID: binding.ID, TargetID: binding.TargetID,
		ConnectionID: binding.ConnectionID, Actor: "principal-1", Action: AuditBindingUpdated,
		Outcome: AdministrationAuditSucceeded, Revision: binding.Revision + 1,
	}
	invocation := AdministrationAuditInvocation{
		OperationID: "updateTargetConnectionBinding", PrincipalID: "principal-1",
		RequestID: "request-1", CorrelationID: "correlation-1", IdempotencyKey: "retry-key",
	}
	first, err := BuildConnectionAdministrationAuditIntent(invocation, event)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildConnectionAdministrationAuditIntent(invocation, event)
	if err != nil {
		t.Fatal(err)
	}
	if first.EventID != second.EventID || first.AggregateKey != second.AggregateKey ||
		first.AggregateSequence != event.Revision {
		t.Fatalf("intent identity changed across retries: first=%#v second=%#v", first, second)
	}
	if first.ResourceKind != "connection" || first.ResourceID != binding.ConnectionID.String() ||
		first.Capability != access.CapabilityResourceManage || first.Action != string(AuditBindingUpdated) {
		t.Fatalf("intent resource contract = %#v", first)
	}
	canonical, err := first.Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(canonical.MetadataJSON), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["payloadSchema"] != "TargetConnectionAdministrationAuditPayload" {
		t.Fatalf("metadata envelope = %#v", envelope)
	}
	encoded := canonical.MetadataJSON
	for _, forbidden := range []string{"source-secret", "connection_string", "/leapview/sales", "credentialReference", "secretPath"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("audit metadata disclosed %q: %s", forbidden, encoded)
		}
	}
}

func TestBuildConnectionAdministrationAuditIntentMapsEveryAdministrativeAction(t *testing.T) {
	binding := validTargetBinding(t)
	actions := map[AdministrationAuditAction]string{
		AuditBindingCreated: "createTargetConnectionBinding", AuditBindingUpdated: "updateTargetConnectionBinding",
		AuditBindingEnabled: "enableTargetConnectionBinding", AuditBindingDisabled: "disableTargetConnectionBinding",
	}
	for action, operation := range actions {
		t.Run(string(action), func(t *testing.T) {
			intent, err := BuildAdministrationAuditIntent(AdministrationAuditInvocation{OperationID: operation}, AdministrationAuditEvent{
				ProjectID: binding.Scope.ProjectID, BindingID: binding.ID, TargetID: binding.TargetID,
				ConnectionID: binding.ConnectionID, Actor: "operator-1", Action: action,
				Outcome: AdministrationAuditSucceeded, Revision: binding.Revision,
			})
			if err != nil {
				t.Fatal(err)
			}
			if intent.Operation != operation || intent.Action != string(action) {
				t.Fatalf("intent = %#v", intent)
			}
		})
	}
}

func TestConnectionAdministrationAuditIntentContextRoundTrip(t *testing.T) {
	var want access.AuditIntent
	want.EventID = "connection-binding:test"
	ctx := WithAuditIntent(context.Background(), want)
	got, ok := AuditIntentFromContext(ctx)
	if !ok || got.EventID != want.EventID {
		t.Fatalf("AuditIntentFromContext() = %#v, %t", got, ok)
	}
	if _, ok := AuditIntentFromContext(context.Background()); ok {
		t.Fatal("AuditIntentFromContext() found an intent in a clean context")
	}
}

func TestAdministrationRequiresContextIntentBeforeCommandMutation(t *testing.T) {
	binding := validTargetBinding(t)
	repository := &administrationRepository{binding: binding}
	service, err := NewAdministration(AdministrationConfig{
		Repository: repository, Authorize: allowAdministration, Dependencies: staticDependencyInspector{},
		RequireAuditIntent: true, Now: func() time.Time { return binding.UpdatedAt.Add(time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Disable(context.Background(), "operator-1", BindingKey{
		Scope: binding.Scope, TargetID: binding.TargetID, ConnectionID: binding.ConnectionID,
	})
	if err != ErrAdministrationAuditUnavailable {
		t.Fatalf("Disable() error = %v, want ErrAdministrationAuditUnavailable", err)
	}
	if repository.binding.Enabled != binding.Enabled || repository.binding.Revision != binding.Revision {
		t.Fatalf("binding mutated without an audit intent: %#v", repository.binding)
	}
}

func TestBuildConnectionAdministrationAuditIntentRejectsActionOperationMismatch(t *testing.T) {
	binding := validTargetBinding(t)
	_, err := BuildConnectionAdministrationAuditIntent(AdministrationAuditInvocation{
		OperationID: "disableTargetConnectionBinding",
	}, AdministrationAuditEvent{
		ProjectID: binding.Scope.ProjectID, BindingID: binding.ID, TargetID: binding.TargetID,
		ConnectionID: binding.ConnectionID, Action: AuditBindingEnabled,
		Outcome: AdministrationAuditSucceeded, Revision: binding.Revision,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match action") {
		t.Fatalf("mismatched operation error = %v", err)
	}
}
