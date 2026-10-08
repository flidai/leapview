package module

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
)

func TestCredentialBrowserCommandsKeepAuditedNonReplayableContracts(t *testing.T) {
	bindings := CredentialBrowserBindings()
	for _, action := range []string{"save", "validate", "prepare", "retry", "abort"} {
		binding := bindings[action]
		contract, ok := credentialgen.GetAPIGenCommandRuntimeContract(binding.OperationID())
		if !ok || !contract.Exposes(apigencommand.SurfaceUI) || contract.Guarantee != apigencommand.GuaranteeTransactional || contract.Idempotency != apigencommand.IdempotencyForbidden {
			t.Fatalf("%s lost its generated audited non-replayable contract", action)
		}
		called := false
		want := errors.New("transaction refused")
		err := executeCredentialBrowserCommand(context.Background(), action, "warehouse", "", "", func(context.Context) error {
			called = true
			return want
		})
		if !called || !errors.Is(err, want) {
			t.Fatalf("%s did not preserve transaction failure: %v", action, err)
		}
	}
}

func TestCredentialBrowserRejectsUnknownActionBeforeService(t *testing.T) {
	if err := executeCredentialBrowserCommand(context.Background(), "unknown", "warehouse", "", "", func(context.Context) error {
		t.Fatal("unknown action executed")
		return nil
	}); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestCredentialBrowserUsesSharedDraftValidationAndActivationServices(t *testing.T) {
	f := newValidationTransportFixture(t)
	f.versionID = activationVersion
	f.repository.stored.Metadata.Binding.VersionID = activationVersion
	activation := &activationTransportFake{state: "prepared"}
	f.config.Activation = activation
	run := func(input BrowserInput) (BrowserState, error) {
		return RunCredentialBrowser(t.Context(), f.config, "principal_test", f.project, f.targetID, f.connection, input)
	}
	saved, err := run(BrowserInput{Action: "save", Password: f.secret})
	if err != nil || saved.VersionID == "" || len(saved.Drafts) != 1 || activation.calls != 0 || saved.RuntimeReady {
		t.Fatalf("draft save failed or activated it: %+v %v", saved, err)
	}
	listed, err := run(BrowserInput{Action: "list"})
	if err != nil || len(listed.Drafts) != 1 || listed.Drafts[0].VersionID != saved.VersionID {
		t.Fatalf("saved metadata missing from list: %+v %v", listed, err)
	}
	validated, err := run(BrowserInput{Action: "validate", VersionID: f.versionID, ExpectedRevision: f.target.BindingRevision})
	if err != nil || validated.ReceiptID == "" || validated.BindingRevision != f.target.BindingRevision || activation.calls != 0 || validated.RuntimeReady {
		t.Fatalf("validation lost authority or activated draft: %+v %v", validated, err)
	}
	prepared, err := run(BrowserInput{Action: "prepare", VersionID: f.versionID, ReceiptID: validated.ReceiptID, OperationID: activationOperation, ExpectedRevision: validated.BindingRevision})
	if err != nil || prepared.Phase != "prepared" || prepared.RuntimeReady || activation.input.VersionID != f.versionID || activation.input.ReceiptID != validated.ReceiptID || activation.input.OperationID != activationOperation {
		t.Fatalf("preparation changed intent or claimed activation: %+v %v", prepared, err)
	}
	renewed, err := run(BrowserInput{Action: "validate", VersionID: f.versionID, OperationID: activationOperation, ExpectedRevision: f.target.BindingRevision})
	if err != nil || renewed.ReceiptID == "" || renewed.Phase != "prepared" || renewed.OperationID != activationOperation || renewed.RuntimeReady {
		t.Fatalf("fresh validation lost the pending operation state: %+v %v", renewed, err)
	}
	for _, state := range []BrowserState{saved, listed, validated, prepared} {
		encoded, _ := json.Marshal(state)
		if strings.Contains(string(encoded), f.secret) {
			t.Fatal("browser state disclosed credential")
		}
	}
	f.authority.allowed = false
	if _, err := run(BrowserInput{Action: "save", Password: f.secret}); err == nil {
		t.Fatal("revoked principal saved a draft")
	}
	if _, err := run(BrowserInput{Action: "list"}); err == nil {
		t.Fatal("revoked principal listed drafts")
	}
}
