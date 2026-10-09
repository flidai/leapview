package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/credential"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func firstSourcePreparationFixture(t *testing.T, expiring ...bool) (*pgxpool.Pool, *pgxpool.Pool, *Repository, *FirstSourcePreparations, credential.FirstSourcePreparationIntent) {
	t.Helper()
	db, runtime, requests, stored := postgresActivationFixture(t)
	receipt, audit := postgresValidationReceipt(t, stored)
	receipt.BindingID = "binding:first"
	receipt.BindingRevision = 1
	if len(expiring) > 0 && expiring[0] {
		receipt.ValidatedAt = time.Now().UTC().Add(-5*time.Minute + 3*time.Second).Truncate(time.Microsecond)
		receipt.ExpiresAt = receipt.ValidatedAt.Add(5 * time.Minute)
	}
	audit, err := receipt.AuditIntent()
	if err != nil {
		t.Fatal(err)
	}
	if err := requests.SaveValidation(t.Context(), receipt, audit); err != nil {
		t.Fatal(err)
	}
	i := credential.FirstSourceAdmissionIntent{Version: 1, OperationID: uuid.NewString(), TargetID: receipt.Binding.TargetID,
		ProjectID: receipt.Binding.ProjectID, Environment: receipt.Binding.Environment, CustomerOwnerID: receipt.Binding.OwnerID,
		OperatorPrincipalID: receipt.ActorID, ConnectionID: receipt.Binding.ResourceID, BindingID: receipt.BindingID,
		ExpectedPolicyRevision: 1, ExpectedPolicyDigest: "sha256:" + strings.Repeat("a", 64),
		Endpoint:            credential.FirstSourceEndpoint{Host: "postgres.internal", TLSMode: "require"},
		CredentialReference: credential.FirstSourceCredentialReference{ProjectID: "sales", Environment: "prod", SecretPath: "/customer/warehouse", SecretKey: "password"}}
	digest, err := i.Digest()
	if err != nil {
		t.Fatal(err)
	}
	bindingDigest, err := i.BindingDigest()
	if err != nil {
		t.Fatal(err)
	}
	admission := credential.FirstSourceAdmission{Intent: i, IntentDigest: digest, BindingDigest: bindingDigest, PolicyRevision: 2, PolicyDigest: "sha256:" + strings.Repeat("b", 64), CreatedAt: time.Now().UTC()}
	auditor := credentialAuditAdapter{repository: accesspostgres.New()}
	admissions, err := NewFirstSourceAdmissions(runtime, auditor)
	if err != nil {
		t.Fatal(err)
	}
	tx := beginCredentialTx(t, runtime)
	if _, err := admissions.InsertAdmissionTx(t.Context(), tx, admission, func(context.Context, Tx, credential.FirstSourceAdmission) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	journal, err := NewFirstSourcePreparations(runtime, auditor)
	if err != nil {
		t.Fatal(err)
	}
	return db, runtime, requests, journal, credential.FirstSourcePreparationIntent{PreparationID: uuid.NewString(), AdmissionOperationID: i.OperationID, AdmissionDigest: digest,
		Receipt: receipt, PublisherID: uuid.NewString(), SourceOwnerID: uuid.NewString(), SourceDigest: "sha256:" + strings.Repeat("c", 64),
		SourceAttestationDigest: "sha256:" + strings.Repeat("d", 64), PlanOperation: "code_change", PlanIdempotencyKey: "initial-source-plan", PlanRequestDigest: "sha256:" + strings.Repeat("e", 64), ExpectedTargetRevision: 1}
}

func allowFirstSourcePreparation(context.Context, Tx, credential.FirstSourcePreparationIntent) error {
	return nil
}
func allowFirstSourcePlan(context.Context, Tx, credential.FirstSourcePreparationIntent, credential.FirstSourcePlanLink) error {
	return nil
}

func TestFirstSourcePreparationAtomicReservationReplayAndImmutablePlanLink(t *testing.T) {
	db, runtime, requests, journal, i := firstSourcePreparationFixture(t)
	tx := beginCredentialTx(t, runtime)
	if _, err := journal.PrepareTx(t.Context(), tx, i, allowFirstSourcePreparation); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.first_source_preparation`).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("caller transaction leaked: %d %v", visible, err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.activation_request`).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("reservation survived rollback: %d %v", visible, err)
	}
	tx = beginCredentialTx(t, runtime)
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
	tx = beginCredentialTx(t, runtime)
	again, err := journal.PrepareTx(t.Context(), tx, i, allowFirstSourcePreparation)
	if err != nil || !reflect.DeepEqual(again, prepared) {
		t.Fatalf("exact replay: %v", err)
	}
	if err := journal.LinkPlanTx(t.Context(), tx, link, allowFirstSourcePlan); err != nil {
		t.Fatal(err)
	}
	read, err := journal.PlanLinkTx(t.Context(), tx, link.TargetID, link.PlanID)
	if err != nil || read != link {
		t.Fatalf("exact plan read: %v", err)
	}
	changed := link
	changed.BindingDigest = "sha256:" + strings.Repeat("a", 64)
	if err := journal.LinkPlanTx(t.Context(), tx, changed, allowFirstSourcePlan); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("changed link: %v", err)
	}
	changedIntent := i
	changedIntent.SourceAttestationDigest = "sha256:" + strings.Repeat("a", 64)
	if _, err := journal.PrepareTx(t.Context(), tx, changedIntent, allowFirstSourcePreparation); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("changed source: %v", err)
	}
	for _, mutate := range []func(*credential.FirstSourcePreparationIntent){
		func(p *credential.FirstSourcePreparationIntent) { p.PublisherID = uuid.NewString() },
		func(p *credential.FirstSourcePreparationIntent) { p.SourceOwnerID = uuid.NewString() },
		func(p *credential.FirstSourcePreparationIntent) { p.SourceDigest = "sha256:" + strings.Repeat("0", 64) },
		func(p *credential.FirstSourcePreparationIntent) { p.PlanIdempotencyKey = "another-key" },
		func(p *credential.FirstSourcePreparationIntent) {
			p.PlanRequestDigest = "sha256:" + strings.Repeat("0", 64)
		},
	} {
		changed := i
		mutate(&changed)
		if _, err := journal.PrepareTx(t.Context(), tx, changed, allowFirstSourcePreparation); !errors.Is(err, credential.ErrConflict) {
			t.Fatalf("changed immutable intent accepted: %v", err)
		}
	}
	other := i
	other.PreparationID = uuid.NewString()
	if _, err := journal.PrepareTx(t.Context(), tx, other, allowFirstSourcePreparation); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("receipt reserved twice: %v", err)
	}
	if err := journal.LinkPlanTx(t.Context(), tx, link, func(context.Context, Tx, credential.FirstSourcePreparationIntent, credential.FirstSourcePlanLink) error {
		return credential.ErrForbidden
	}); !errors.Is(err, credential.ErrForbidden) {
		t.Fatalf("revoked plan replay authority: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{`UPDATE credential.first_source_preparation SET intent_digest='sha256:'||repeat('0',64)`, `DELETE FROM credential.first_source_preparation`, `UPDATE credential.first_source_plan_link SET binding_digest='sha256:'||repeat('0',64)`, `DELETE FROM credential.first_source_plan_link`} {
		if _, err := db.Exec(t.Context(), command); err == nil {
			t.Fatal("immutable preparation/link mutation accepted")
		}
	}
	reserved, err := requests.GetActivationRequest(t.Context(), i.Receipt.Binding.TargetID, i.PreparationID)
	if err != nil {
		t.Fatal(err)
	}
	next := reserved
	next.PlanID = uuid.NewString()
	tx = beginCredentialTx(t, runtime)
	if _, err := requests.TransitionActivationRequestTx(t.Context(), tx, reserved, next, i.Receipt.ActorID, "", allowActivationRequest); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx = beginCredentialTx(t, runtime)
	if _, err := journal.PlanLinkTx(t.Context(), tx, link.TargetID, link.PlanID); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("mismatched underlying plan revived: %v", err)
	}
}

func TestFirstSourcePreparationPlanAuditFailureRollsBackSelection(t *testing.T) {
	db, runtime, _, journal, i := firstSourcePreparationFixture(t)
	tx := beginCredentialTx(t, runtime)
	if _, err := journal.PrepareTx(t.Context(), tx, i, allowFirstSourcePreparation); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `ALTER TABLE audit.audit_event ADD CONSTRAINT reject_first_source_selection CHECK (action <> 'credential.first_source.plan_selected')`); err != nil {
		t.Fatal(err)
	}
	planID, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	link := credential.FirstSourcePlanLink{TargetID: i.Receipt.Binding.TargetID, PreparationID: i.PreparationID, PlanID: planID.String(), RequestDigest: i.PlanRequestDigest, BindingDigest: "sha256:" + strings.Repeat("f", 64)}
	tx = beginCredentialTx(t, runtime)
	if err := journal.LinkPlanTx(t.Context(), tx, link, allowFirstSourcePlan); err == nil {
		t.Fatal("audit rejection accepted selection")
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.first_source_plan_link`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("selection survived audit rollback: %d %v", count, err)
	}
	tx = beginCredentialTx(t, runtime)
	if _, err := journal.PlanLinkTx(t.Context(), tx, link.TargetID, link.PlanID); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("missing exact plan link: %v", err)
	}
}

func TestFirstSourcePreparationRejectsExistingGenericRequest(t *testing.T) {
	_, runtime, requests, journal, i := firstSourcePreparationFixture(t)
	tx := beginCredentialTx(t, runtime)
	if _, err := requests.ReserveActivationRequestTx(t.Context(), tx, i.Receipt, firstSourceReservation(i), allowActivationRequest); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx = beginCredentialTx(t, runtime)
	if _, err := journal.PrepareTx(t.Context(), tx, i, allowFirstSourcePreparation); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("generic request adopted as first source: %v", err)
	}
}

func TestFirstSourcePreparationAuditFailureAndLiveRevocationRollback(t *testing.T) {
	db, runtime, _, _, i := firstSourcePreparationFixture(t)
	if _, err := db.Exec(t.Context(), `ALTER TABLE audit.audit_event ADD CONSTRAINT reject_first_source_preparation_audit CHECK (action <> 'credential.first_source.prepared')`); err != nil {
		t.Fatal(err)
	}
	journal, err := NewFirstSourcePreparations(runtime, credentialAuditAdapter{repository: accesspostgres.New()})
	if err != nil {
		t.Fatal(err)
	}
	tx := beginCredentialTx(t, runtime)
	if _, err := journal.PrepareTx(t.Context(), tx, i, allowFirstSourcePreparation); err == nil {
		t.Fatalf("audit rejection: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.activation_request`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("audit failure retained reservation: %d %v", count, err)
	}
	if _, err := db.Exec(t.Context(), `ALTER TABLE audit.audit_event DROP CONSTRAINT reject_first_source_preparation_audit`); err != nil {
		t.Fatal(err)
	}
	journal, err = NewFirstSourcePreparations(runtime, credentialAuditAdapter{repository: accesspostgres.New()})
	if err != nil {
		t.Fatal(err)
	}
	tx = beginCredentialTx(t, runtime)
	if _, err := journal.PrepareTx(t.Context(), tx, i, nil); !errors.Is(err, credential.ErrUnavailable) {
		t.Fatalf("missing authority: %v", err)
	}
	if _, err := journal.PrepareTx(t.Context(), tx, i, func(context.Context, Tx, credential.FirstSourcePreparationIntent) error {
		return credential.ErrForbidden
	}); !errors.Is(err, credential.ErrForbidden) {
		t.Fatalf("revoked authority: %v", err)
	}
}

func TestFirstSourcePreparationExplicitRenewalPreservesIntentAndRejectsTerminal(t *testing.T) {
	_, runtime, requests, journal, i := firstSourcePreparationFixture(t, true)
	tx := beginCredentialTx(t, runtime)
	before, err := journal.PrepareTx(t.Context(), tx, i, allowFirstSourcePreparation)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(i.Receipt.ExpiresAt) + 20*time.Millisecond)
	tx = beginCredentialTx(t, runtime)
	if _, err := journal.PrepareTx(t.Context(), tx, i, allowFirstSourcePreparation); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("expired proof replay: %v", err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	fresh := i.Receipt
	fresh.ReceiptID = uuid.NewString()
	fresh.ValidatedAt = time.Now().UTC().Truncate(time.Microsecond)
	fresh.ExpiresAt = fresh.ValidatedAt.Add(5 * time.Minute)
	audit, err := fresh.AuditIntent()
	if err != nil {
		t.Fatal(err)
	}
	if err := requests.SaveValidation(t.Context(), fresh, audit); err != nil {
		t.Fatal(err)
	}
	foreign := fresh
	foreign.ReceiptID = uuid.NewString()
	foreign.ActorID = uuid.NewString()
	audit, err = foreign.AuditIntent()
	if err != nil {
		t.Fatal(err)
	}
	if err := requests.SaveValidation(t.Context(), foreign, audit); err != nil {
		t.Fatal(err)
	}
	tx = beginCredentialTx(t, runtime)
	if _, err := journal.RenewReceiptTx(t.Context(), tx, i.Receipt.Binding.TargetID, i.PreparationID, foreign.ReceiptID, allowFirstSourcePreparation); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("foreign operator renewal: %v", err)
	}
	if _, err := journal.RenewReceiptTx(t.Context(), tx, i.Receipt.Binding.TargetID, i.PreparationID, fresh.ReceiptID, allowFirstSourcePreparation); err != nil {
		t.Fatal(err)
	}
	after, err := journal.PrepareTx(t.Context(), tx, i, allowFirstSourcePreparation)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("renewal rewrote original intent: %v", err)
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
	tx = beginCredentialTx(t, runtime)
	if _, err := requests.TransitionActivationRequestTx(t.Context(), tx, reserved, next, i.Receipt.ActorID, "credential.activation.aborted", allowActivationRequest); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx = beginCredentialTx(t, runtime)
	if _, err := journal.PreparationTx(t.Context(), tx, i.Receipt.Binding.TargetID, i.PreparationID); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("aborted preparation read: %v", err)
	}
	if _, err := journal.RenewReceiptTx(t.Context(), tx, i.Receipt.Binding.TargetID, i.PreparationID, fresh.ReceiptID, allowFirstSourcePreparation); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("aborted preparation revived: %v", err)
	}
}
