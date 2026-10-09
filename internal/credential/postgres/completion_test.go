package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/credential"
)

func TestActivationCompletionRequiresReadinessAuthorityAndReleasesPendingFence(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input, _ := prepareActivationForSwitching(t, repository, runtimeDB, stored)
	switchActivation(t, repository, runtimeDB, input)
	tx := beginCredentialTx(t, runtimeDB)
	committed, err := repository.CommitActivationPublicationTx(t.Context(), tx, input.OperationID, activationPublication(input), allowActivationCommit)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNoPendingActivationTransaction(t, runtimeDB, input.Receipt.Binding.DeploymentID, credential.ErrConflict)
	tx = beginCredentialTx(t, runtimeDB)
	_, err = repository.CompleteActivationTx(t.Context(), tx, committed, func(context.Context, Tx, credential.PreparedActivation) error { return credential.ErrUnavailable })
	if !errors.Is(err, credential.ErrUnavailable) {
		t.Fatalf("unready completion=%v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNoPendingActivationTransaction(t, runtimeDB, input.Receipt.Binding.DeploymentID, credential.ErrConflict)
	tx = beginCredentialTx(t, runtimeDB)
	completed, err := repository.CompleteActivationTx(t.Context(), tx, committed, allowActivationCommit)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if completed.CompletedAt.IsZero() || completed.CompletedAt.Before(committed.CommittedAt) {
		t.Fatal("completion ordering is invalid")
	}
	assertNoPendingActivationTransaction(t, runtimeDB, input.Receipt.Binding.DeploymentID, nil)
	if _, err := repository.GetPendingActivation(t.Context(), input.Receipt.Binding.DeploymentID); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("completed remains pending=%v", err)
	}
	recovered, err := repository.GetActivationPreparation(t.Context(), input.Receipt.Binding.DeploymentID, input.OperationID)
	if err != nil || !recovered.CompletedAt.Equal(completed.CompletedAt) {
		t.Fatalf("lost completion acknowledgment recovery=%v", err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE credential.activation_preparation SET completed_at=completed_at+interval '1 second' WHERE operation_id=$1`, input.OperationID); err == nil {
		t.Fatal("completion was mutable")
	}
	assertActivationLifecycleAudit(t, db, input.OperationID, []string{"credential.activation.prepared", "credential.activation.switching", "credential.activation.committed", "credential.activation.completed"}, []int64{1, 2, 3, 4})
}
