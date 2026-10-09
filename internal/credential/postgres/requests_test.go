package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/credential"
	"github.com/google/uuid"
)

func allowActivationRequest(context.Context, Tx) error { return nil }

func TestActivationRequestSurvivesPreparingInterruptionAndFencesPublication(t *testing.T) {
	_, runtimeDB, repository, stored := postgresActivationFixture(t)
	preparation := postgresActivationPreparation(t, repository, stored)
	receipt := preparation.Receipt
	request := credential.ActivationRequest{OperationID: uuid.NewString(), ReceiptID: receipt.ReceiptID, VersionID: receipt.Binding.VersionID, ExpectedBindingRevision: receipt.BindingRevision}
	tx := beginCredentialTx(t, runtimeDB)
	reserved, err := repository.ReserveActivationRequestTx(t.Context(), tx, receipt, request, allowActivationRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if reserved.State != "preparing" || reserved.Revision != 1 {
		t.Fatalf("reservation=%+v", reserved)
	}
	assertNoPendingActivationTransaction(t, runtimeDB, receipt.Binding.DeploymentID, credential.ErrConflict)
	recovered, err := repository.GetPendingActivationRequest(t.Context(), receipt.Binding.DeploymentID)
	if err != nil || recovered.Request != request {
		t.Fatalf("recover preparing=%v", err)
	}
	tx = beginCredentialTx(t, runtimeDB)
	if _, err := repository.ReserveActivationRequestTx(t.Context(), tx, receipt, request, allowActivationRequest); err != nil {
		t.Fatalf("exact lost-ack replay=%v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	conflicting := request
	conflicting.OperationID = uuid.NewString()
	tx = beginCredentialTx(t, runtimeDB)
	if _, err := repository.ReserveActivationRequestTx(t.Context(), tx, receipt, conflicting, allowActivationRequest); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("second operation=%v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	aborted := recovered
	aborted.State = "aborted"
	tx = beginCredentialTx(t, runtimeDB)
	if _, err := repository.TransitionActivationRequestTx(t.Context(), tx, recovered, aborted, receipt.ActorID, "credential.activation.aborted", allowActivationRequest); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNoPendingActivationTransaction(t, runtimeDB, receipt.Binding.DeploymentID, nil)
}

func TestActivationRequestAuditFailureRollsBackReservation(t *testing.T) {
	_, runtimeDB, repository, stored := postgresActivationFixture(t)
	prepared := postgresActivationPreparation(t, repository, stored)
	receipt := prepared.Receipt
	request := credential.ActivationRequest{OperationID: uuid.NewString(), ReceiptID: receipt.ReceiptID, VersionID: receipt.Binding.VersionID, ExpectedBindingRevision: receipt.BindingRevision}
	sentinel := errors.New("audit down")
	failing, err := New(runtimeDB, credentialFailingAudit{err: sentinel})
	if err != nil {
		t.Fatal(err)
	}
	tx := beginCredentialTx(t, runtimeDB)
	if _, err := failing.ReserveActivationRequestTx(t.Context(), tx, receipt, request, allowActivationRequest); !errors.Is(err, sentinel) {
		t.Fatalf("audit error=%v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetActivationRequest(t.Context(), receipt.Binding.DeploymentID, request.OperationID); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("failed request retained=%v", err)
	}
}
