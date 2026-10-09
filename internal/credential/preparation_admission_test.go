package credential

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type preparingAuthority struct {
	*activationAuthorityFake
	gate     *ProviderAdmission
	retained context.Context
}

func (a *preparingAuthority) Switch(ctx context.Context, actor string, record ActivationRecord, receipt string) (ActivationRecord, error) {
	if a.gate.Ready() {
		return ActivationRecord{}, errors.New("ordinary work reopened during preparation")
	}
	work, release, err := a.gate.Acquire(ctx)
	if err != nil {
		return ActivationRecord{}, err
	}
	a.retained = work
	release()
	return a.activationAuthorityFake.Switch(ctx, actor, record, receipt)
}
func TestActivationRetryCanPrepareWhileOrdinaryProviderWorkStaysClosed(t *testing.T) {
	events := []string{}
	resource := Resource{ScopeKind: "agent", ResourceID: "instance"}
	request := ActivationRequest{OperationID: uuid.NewString(), VersionID: uuid.NewString(), ReceiptID: uuid.NewString()}
	base := &activationAuthorityFake{events: &events}
	_, _ = base.Prepare(t.Context(), "admin", resource, request)
	base.record.Status.State = "preparing"
	gate := NewProviderAdmission()
	authority := &preparingAuthority{activationAuthorityFake: base, gate: gate}
	runtime := &activationRuntimeFake{events: &events}
	coordinator, err := NewActivationCoordinator(authority, runtime, gate, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err = coordinator.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, err = gate.Acquire(t.Context()); !errors.Is(err, ErrProviderPaused) {
		t.Fatal("ordinary startup work admitted")
	}
	status, err := coordinator.RetryActivation(t.Context(), "admin", resource, request.OperationID, request.ReceiptID)
	if err != nil || !status.RuntimeReady {
		t.Fatalf("preparation recovery=%v ready=%v", err, status.RuntimeReady)
	}
	if authority.retained.Err() == nil {
		t.Fatal("preparation context remained usable after scope ended")
	}
}
