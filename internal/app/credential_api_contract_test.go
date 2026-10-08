package app

import (
	"testing"

	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	apigenapi "github.com/flidai/leapview/internal/app/api/gen"
	cligen "github.com/flidai/leapview/internal/app/cli/gen"
	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
)

func TestAPIGenCredentialCapabilityOwnsItsOperationSurface(t *testing.T) {
	contracts := credentialgen.GetAPIGenOperationContracts()
	if got, want := len(contracts), 8; got != want {
		t.Fatalf("Credential generated operations = %d, want %d", got, want)
	}
	for operationID, contract := range contracts {
		if contract.Namespace != "LeapViewAPI.Credential" || len(contract.Tags) != 1 || contract.Tags[0] != "Credentials" {
			t.Errorf("Credential operation %q ownership = namespace %q, tags %v", operationID, contract.Namespace, contract.Tags)
		}
		if _, exists := apigenapi.GetAPIGenOperationContracts()[operationID]; exists {
			t.Errorf("Credential operation %q is still emitted by the application package", operationID)
		}
	}
	list, ok := contracts["listConnectionCredentialDrafts"]
	if !ok || list.Authz == nil || list.Authz.Action != "connection.read" || list.Authz.Resolver != "connection" {
		t.Fatalf("credential draft list authz = %#v, want connection.read on connection", list.Authz)
	}
	get, ok := contracts["getConnectionCredentialDraft"]
	if !ok || get.Authz == nil || get.Authz.Action != "connection.read" || get.Authz.Resolver != "connection" {
		t.Fatalf("credential draft get authz = %#v, want connection.read on connection", get.Authz)
	}
	save, ok := contracts["saveCredentialDraft"]
	if !ok || save.Command == nil {
		t.Fatal("saveCredentialDraft is missing its generated command contract")
	}
	if save.Authz == nil || save.Authz.Action != "connection.manage" || save.Authz.Resolver != "connection" {
		t.Fatalf("credential draft save authz = %#v, want connection.manage on connection", save.Authz)
	}
	if save.Command.Owner != "LeapViewAPI.Credential" || save.Command.Idempotency != "forbidden" || save.Command.Audit.SuccessAction != "credential.draft.saved" || save.Command.Audit.Guarantee != "transactional" {
		t.Fatalf("credential draft save command contract = %#v", save.Command)
	}
	if save.Command.UI == nil || len(save.Command.AdditionalExposures) != 1 || save.Command.AdditionalExposures[0] != "ui" {
		t.Fatalf("credential draft save must expose only the audited UI surface: %#v", save.Command)
	}
	if payload := save.Command.Audit.Payload; payload == nil || payload.Schema != "CredentialDraftAuditPayload" {
		t.Fatalf("credential draft audit payload contract = %#v", payload)
	} else {
		fields := map[string]bool{}
		for _, field := range payload.Fields {
			fields[field.Name] = true
		}
		if !fields["version_id"] || !fields["purpose"] || fields["fields"] {
			t.Fatalf("credential draft audit fields = %#v, want only metadata fields", payload.Fields)
		}
	}
	for _, spec := range cligen.APIGeneratedCommandSpecs {
		if spec.OperationID == "saveCredentialDraft" || spec.OperationID == "listConnectionCredentialDrafts" || spec.OperationID == "getConnectionCredentialDraft" {
			t.Errorf("credential draft operation %q unexpectedly has a generated CLI command", spec.OperationID)
		}
	}
	if got, want := len(apiaggregate.GetAPIGenOperationContracts()), expectedAPIGenAggregateOperationCount; got != want {
		t.Fatalf("aggregate generated operations = %d, want %d", got, want)
	}
}

func TestCredentialActivationContractPreservesAuthorizationAndAtomicAudit(t *testing.T) {
	contracts := credentialgen.GetAPIGenOperationContracts()
	for operation, action := range map[string]string{
		"startCredentialActivation": "credential.activation.requested",
		"retryCredentialActivation": "credential.activation.retried",
		"abortCredentialActivation": "credential.activation.aborted",
	} {
		contract, ok := contracts[operation]
		if !ok || contract.Authz == nil || contract.Authz.Action != "connection.manage" || contract.Authz.Resolver != "connection" {
			t.Fatalf("%s must require exact connection management", operation)
		}
		command := contract.Command
		if command == nil || command.Idempotency != "forbidden" || command.Audit.Guarantee != "transactional" || command.Audit.SuccessAction != action || command.Owner != "LeapViewAPI.Credential" {
			t.Fatalf("%s activation/audit contract = %#v", operation, command)
		}
		if command.UI == nil || len(command.AdditionalExposures) != 1 || command.AdditionalExposures[0] != "ui" || command.Audit.Payload == nil || command.Audit.Payload.Schema != "CredentialActivationAuditPayload" {
			t.Fatalf("%s must expose the audited UI and retain metadata-only audit", operation)
		}
	}
	status, ok := contracts["getCredentialActivation"]
	if !ok || status.Authz == nil || status.Authz.Action != "connection.read" || status.Authz.Resolver != "connection" || status.Command != nil {
		t.Fatal("activation status must remain an authorized read")
	}
}

func TestCredentialValidationContractRequiresFreshExplicitProbe(t *testing.T) {
	contract, ok := credentialgen.GetAPIGenOperationContracts()["validateCredentialDraft"]
	if !ok || contract.Command == nil {
		t.Fatal("missing credential validation command")
	}
	if contract.Authz == nil || contract.Authz.Action != "connection.manage" || contract.Authz.Resolver != "connection" {
		t.Fatal("validation must require exact connection management")
	}
	command := contract.Command
	if command.Idempotency != "forbidden" || command.Audit.SuccessAction != "credential.draft.validated" || command.Audit.Guarantee != "transactional" {
		t.Fatalf("validation command: %#v", command)
	}
	if command.UI != nil || len(command.AdditionalExposures) != 0 {
		t.Fatal("validation must remain API only until saved draft selection is implemented")
	}
	if payload := command.Audit.Payload; payload == nil || payload.Schema != "CredentialValidationAuditPayload" || len(payload.Fields) != 6 {
		t.Fatalf("validation audit payload: %#v", payload)
	}
	if _, ok := apiaggregate.GetAPIGenOperationContracts()["testTargetConnectionBinding"]; ok {
		t.Fatal("pool-promoting Test must be removed")
	}
}
