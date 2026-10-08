package postgres

import (
	"testing"
	"time"

	"github.com/flidai/leapview/internal/credential"
	"github.com/google/uuid"
)

func TestActivationSourceRetryConsumesFreshReservedReceiptWithoutRewritingIntent(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	receipt, _ := postgresValidationReceipt(t, stored)
	receipt.ValidatedAt = time.Now().UTC().Add(-5*time.Minute + 2*time.Second).Truncate(time.Microsecond)
	receipt.ExpiresAt = receipt.ValidatedAt.Add(5 * time.Minute)
	seedActivationReceipt(t, db, receipt)
	input := activationPreparationForReceipt(receipt)
	request := credential.ActivationRequest{OperationID: input.OperationID, VersionID: receipt.Binding.VersionID, ReceiptID: receipt.ReceiptID, ExpectedBindingRevision: receipt.BindingRevision}
	tx := beginCredentialTx(t, runtimeDB)
	reserved, err := repository.ReserveActivationRequestTx(t.Context(), tx, receipt, request, allowActivationRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(receipt.ExpiresAt) + 20*time.Millisecond)
	fresh := receipt
	fresh.ReceiptID = uuid.NewString()
	fresh.ValidatedAt = time.Now().UTC().Truncate(time.Microsecond)
	fresh.ExpiresAt = fresh.ValidatedAt.Add(5 * time.Minute)
	seedActivationReceipt(t, db, fresh)
	tx = beginCredentialTx(t, runtimeDB)
	if _, err = repository.RefreshActivationRequestReceiptTx(t.Context(), tx, reserved, fresh.ReceiptID, receipt.ActorID, allowActivationRequest); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx = beginCredentialTx(t, runtimeDB)
	if _, err = repository.PrepareActivationTx(t.Context(), tx, input, allowActivationPreparation); err != nil {
		t.Fatalf("prepare immutable intent after renewed proof: %v", err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx = beginCredentialTx(t, runtimeDB)
	if _, err = repository.BeginActivationSwitchingTx(t.Context(), tx, receipt.Binding.DeploymentID, input.OperationID, receipt.ActorID, allowActivationSwitching); err != nil {
		t.Fatalf("switch renewed proof: %v", err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx = beginCredentialTx(t, runtimeDB)
	committed, err := repository.CommitActivationPublicationTx(t.Context(), tx, input.OperationID, activationPublication(input), allowActivationCommit)
	if err != nil {
		t.Fatalf("commit renewed proof: %v", err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if committed.Preparation.Receipt.ReceiptID != receipt.ReceiptID || committed.CommittedAt.IsZero() {
		t.Fatal("commit rewrote immutable intent or lost durable completion")
	}
}
