package module

import (
	"errors"
	"reflect"
	"testing"

	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
)

func TestConnectionBindingOperationClassifications(t *testing.T) {
	contracts := analyticsgen.GetAPIGenOperationContracts()
	commands := map[string]struct {
		auditAction string
		privilege   string
		idempotency string
		uiAction    string
	}{
		"createTargetConnectionBinding":  {auditAction: string(connectionbinding.AuditBindingCreated), privilege: "MANAGE_CONNECTION_METADATA", idempotency: "required", uiAction: "connection.binding.configure"},
		"updateTargetConnectionBinding":  {auditAction: string(connectionbinding.AuditBindingUpdated), privilege: "MANAGE_CONNECTION_METADATA", uiAction: "connection.binding.update"},
		"testTargetConnectionBinding":    {auditAction: string(connectionbinding.RefreshTest), privilege: "TEST_CONNECTION", idempotency: "required", uiAction: "connection.binding.test"},
		"refreshTargetConnectionBinding": {auditAction: string(connectionbinding.RefreshRequested), privilege: "TEST_CONNECTION", idempotency: "required", uiAction: "connection.binding.refresh"},
		"enableTargetConnectionBinding":  {auditAction: string(connectionbinding.AuditBindingEnabled), privilege: "MANAGE_CONNECTION_METADATA", idempotency: "required", uiAction: "connection.binding.enable"},
		"disableTargetConnectionBinding": {auditAction: string(connectionbinding.AuditBindingDisabled), privilege: "MANAGE_CONNECTION_METADATA", idempotency: "required", uiAction: "connection.binding.disable"},
	}
	for operationID, expected := range commands {
		contract, ok := contracts[operationID]
		if !ok || contract.Command == nil {
			t.Fatalf("command contract %q = %#v", operationID, contract)
		}
		if analyticsGeneratedOperationKind(contract) != "command" ||
			contract.Command.Owner != "LeapViewAPI.Analytics" ||
			contract.Command.Audit.SuccessAction != expected.auditAction ||
			!contract.Command.Audit.Required ||
			contract.Command.Target == nil ||
			contract.Command.Target.Parameter != "workspace" ||
			contract.Command.Target.Type != "workspace" ||
			contract.Command.Privilege != expected.privilege ||
			contract.Command.Idempotency != expected.idempotency ||
			contract.Command.UI == nil ||
			contract.Command.UI.ActionID != expected.uiAction {
			t.Errorf("command contract %q = %#v", operationID, contract)
		}
	}

	// Planning is an explicit POST query because it computes a confirmation
	// token without mutating durable state. Testing is deliberately absent from
	// this list because it can persist validation state and promote a pool.
	for _, operationID := range []string{
		"listTargetConnectionBindings",
		"getTargetConnectionBinding",
		"planTargetConnectionBindingChange",
		"getTargetConnectionBindingHealth",
	} {
		contract, ok := contracts[operationID]
		if !ok || contract.Command != nil || analyticsGeneratedOperationKind(contract) != "query" {
			t.Errorf("query contract %q = %#v", operationID, contract)
		}
	}
}

func TestGeneratedConnectionBindingCommandsRequireBothAuditSinks(t *testing.T) {
	module := &Module{connectionBindings: &moduleBindingCatalog{}}
	_, err := module.NewConnectionAdministration(ConnectionAdministrationConfig{})
	if !errors.Is(err, connectionbinding.ErrAdministrationAuditUnavailable) {
		t.Fatalf("missing administration audit error = %v", err)
	}
	_, err = module.NewConnectionAdministration(ConnectionAdministrationConfig{
		AdministrationAudit: moduleAdministrationAuditNoop{},
	})
	if !errors.Is(err, connectionbinding.ErrRotationAuditUnavailable) {
		t.Fatalf("missing rotation audit error = %v", err)
	}
	_, err = module.NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{})
	if !errors.Is(err, connectionbinding.ErrRotationAuditUnavailable) {
		t.Fatalf("runtime missing rotation audit error = %v", err)
	}
}

func analyticsGeneratedOperationKind(contract analyticsgen.GenOperationContract) string {
	field := reflect.ValueOf(contract).FieldByName("Kind")
	if !field.IsValid() {
		return ""
	}
	return field.String()
}
