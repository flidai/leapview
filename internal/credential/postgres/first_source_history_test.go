package postgres

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/credential"
	"github.com/google/uuid"
)

// These are real PostgreSQL journal transitions, not native publication proof.
// Stored identity is intentionally read without granting runtime readiness.
func TestFirstSourceStoredIdentitySurvivesTerminalStatesWithoutRevival(t *testing.T) {
	for _, state := range []string{"completed", "aborted"} {
		t.Run(state, func(t *testing.T) {
			_, runtime, requests, journal, i := firstSourcePreparationFixture(t)
			tx := beginCredentialTx(t, runtime)
			prepared, err := journal.PrepareTx(t.Context(), tx, i, allowFirstSourcePreparation)
			if err != nil {
				t.Fatal(err)
			}
			planID, err := uuid.NewV7()
			if err != nil {
				t.Fatal(err)
			}
			link := credential.FirstSourcePlanLink{TargetID: i.Receipt.Binding.TargetID, PreparationID: i.PreparationID, PlanID: planID.String(), RequestDigest: i.PlanRequestDigest, BindingDigest: "sha256:" + strings.Repeat("f", 64)}
			if err := journal.LinkPlanTx(t.Context(), tx, link, allowFirstSourcePlan); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			reserved, err := requests.GetActivationRequest(t.Context(), link.TargetID, i.PreparationID)
			if err != nil {
				t.Fatal(err)
			}
			phases := []string{"aborted"}
			if state == "completed" {
				phases = []string{"prepared", "switching", "committed", "completed"}
			}
			for _, phase := range phases {
				next := reserved
				next.State = phase
				if phase == "prepared" {
					next.PlanID = link.PlanID
					next.CandidateID = uuid.NewString()
					next.GenerationID = uuid.NewString()
					next.PublicationID = uuid.NewString()
				}
				tx = beginCredentialTx(t, runtime)
				reserved, err = requests.TransitionActivationRequestTx(t.Context(), tx, reserved, next, i.Receipt.ActorID, "", allowActivationRequest)
				if err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			tx = beginCredentialTx(t, runtime)
			stored, err := journal.StoredPreparationTx(t.Context(), tx, link.TargetID, i.PreparationID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored.Preparation, prepared) || stored.Reservation.State != state || stored.Admission.Intent.OperationID != i.AdmissionOperationID {
				t.Fatal("historical preparation identity/state changed")
			}
			selected, err := journal.StoredPlanLinkTx(t.Context(), tx, link.TargetID, link.PlanID)
			if err != nil || selected.Link != link || !reflect.DeepEqual(selected.Preparation, stored) {
				t.Fatalf("historical exact plan link: %v", err)
			}
			if _, err := journal.PreparationTx(t.Context(), tx, link.TargetID, i.PreparationID); !errors.Is(err, credential.ErrConflict) {
				t.Fatalf("terminal active preparation allowed: %v", err)
			}
			if _, err := journal.PlanLinkTx(t.Context(), tx, link.TargetID, link.PlanID); !errors.Is(err, credential.ErrConflict) {
				t.Fatalf("terminal active link allowed: %v", err)
			}
			if _, err := journal.PrepareTx(t.Context(), tx, i, allowFirstSourcePreparation); !errors.Is(err, credential.ErrConflict) {
				t.Fatalf("terminal reservation revived: %v", err)
			}
			if _, err := journal.RenewReceiptTx(t.Context(), tx, link.TargetID, i.PreparationID, i.Receipt.ReceiptID, allowFirstSourcePreparation); !errors.Is(err, credential.ErrConflict) {
				t.Fatalf("terminal proof renewed: %v", err)
			}
			if err := journal.LinkPlanTx(t.Context(), tx, link, allowFirstSourcePlan); !errors.Is(err, credential.ErrConflict) {
				t.Fatalf("terminal plan relinked: %v", err)
			}
			after, err := requests.GetActivationRequest(t.Context(), link.TargetID, i.PreparationID)
			if err != nil || !reflect.DeepEqual(reserved, after) {
				t.Fatalf("read changed terminal reservation: %v", err)
			}
		})
	}
}

func TestFirstSourceStoredIdentityRefusesMismatchedReservationPlan(t *testing.T) {
	_, runtime, requests, journal, i := firstSourcePreparationFixture(t)
	tx := beginCredentialTx(t, runtime)
	if _, err := journal.PrepareTx(t.Context(), tx, i, allowFirstSourcePreparation); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	reserved, err := requests.GetActivationRequest(t.Context(), i.Receipt.Binding.TargetID, i.PreparationID)
	if err != nil {
		t.Fatal(err)
	}
	next := reserved
	next.State = "aborted"
	next.PlanID = uuid.NewString()
	tx = beginCredentialTx(t, runtime)
	if _, err := requests.TransitionActivationRequestTx(t.Context(), tx, reserved, next, i.Receipt.ActorID, "", allowActivationRequest); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx = beginCredentialTx(t, runtime)
	if _, err := journal.StoredPreparationTx(t.Context(), tx, i.Receipt.Binding.TargetID, i.PreparationID); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("historical mismatch accepted: %v", err)
	}
}
