package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/flidai/leapview/internal/credential"
	credentialdb "github.com/flidai/leapview/internal/credential/postgres/internal/db"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type FirstSourcePreparationAuthorizer func(context.Context, Tx, credential.FirstSourcePreparationIntent) error
type FirstSourcePlanAuthorizer func(context.Context, Tx, credential.FirstSourcePreparationIntent, credential.FirstSourcePlanLink) error

// FirstSourcePreparations augments the existing exclusive receipt reservation
// with source/publisher intent. All writes use a caller-owned transaction and
// a mandatory live authorizer; no ordinary role or publication is inferred.
type FirstSourcePreparations struct {
	db       *pgxpool.Pool
	requests *Repository
	audit    AuditRepository
}

func NewFirstSourcePreparations(db *pgxpool.Pool, audit AuditRepository) (*FirstSourcePreparations, error) {
	requests, err := New(db, audit)
	if err != nil {
		return nil, err
	}
	return &FirstSourcePreparations{db: db, requests: requests, audit: audit}, nil
}

func (r *FirstSourcePreparations) PreparationTx(ctx context.Context, tx Tx, target, id string) (credential.FirstSourcePreparation, error) {
	if r == nil || ctx == nil || typednil.IsNil(tx) || !canonical(target, 255) || !canonicalPreparationUUID(id) {
		return credential.FirstSourcePreparation{}, credential.ErrInvalid
	}
	return r.read(ctx, tx, target, id)
}

func (r *FirstSourcePreparations) Preparation(ctx context.Context, target, id string) (credential.FirstSourcePreparation, error) {
	if r == nil || r.db == nil || ctx == nil || !canonical(target, 255) || !canonicalPreparationUUID(id) {
		return credential.FirstSourcePreparation{}, credential.ErrInvalid
	}
	return r.read(ctx, r.db, target, id)
}

func (r *FirstSourcePreparations) read(ctx context.Context, db FirstSourceAdmissionDB, target, id string) (credential.FirstSourcePreparation, error) {
	stored, err := r.readStored(ctx, db, target, id)
	if err != nil {
		return credential.FirstSourcePreparation{}, err
	}
	if !firstSourceReservationActive(stored.Reservation.State) {
		return credential.FirstSourcePreparation{}, credential.ErrConflict
	}
	return stored.Preparation, nil
}

func firstSourceReservationActive(state string) bool {
	return state == "preparing" || state == "prepared" || state == "switching"
}

// StoredPreparationTx returns exact historical identity without inferring
// active authority, receipt freshness or successful publication.
func (r *FirstSourcePreparations) StoredPreparationTx(ctx context.Context, tx Tx, target, id string) (credential.FirstSourceStoredPreparation, error) {
	if r == nil || ctx == nil || typednil.IsNil(tx) || !canonical(target, 255) || !canonicalPreparationUUID(id) {
		return credential.FirstSourceStoredPreparation{}, credential.ErrInvalid
	}
	return r.readStored(ctx, tx, target, id)
}

func (r *FirstSourcePreparations) readStored(ctx context.Context, db FirstSourceAdmissionDB, target, id string) (credential.FirstSourceStoredPreparation, error) {
	q := credentialdb.New(db)
	row, err := q.GetFirstSourcePreparation(ctx, credentialdb.GetFirstSourcePreparationParams{TargetID: target, OperationID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.FirstSourceStoredPreparation{}, credential.ErrNotFound
	}
	if err != nil {
		return credential.FirstSourceStoredPreparation{}, normalizeDatabaseError(err)
	}
	var intent credential.FirstSourcePreparationIntent
	if err := strictjson.DecodeWithOptions(row.IntentDocument, &intent, strictjson.Options{MaxBytes: 32 << 10, MaxDepth: 16, DuplicateKeys: strictjson.CaseFoldedKeys}); err != nil {
		return credential.FirstSourceStoredPreparation{}, credential.ErrInvalid
	}
	result := credential.FirstSourceStoredPreparation{Preparation: credential.FirstSourcePreparation{Intent: intent, IntentDigest: row.IntentDigest, CreatedAt: row.CreatedAt}}
	if result.Preparation.Validate() != nil || intent.PreparationID != row.OperationID || intent.Receipt.ReceiptID != row.OriginalReceiptID || intent.Receipt.Binding.TargetID != row.TargetID {
		return result, credential.ErrConflict
	}
	request, err := q.GetActivationRequest(ctx, credentialdb.GetActivationRequestParams{DeploymentID: target, OperationID: id})
	if err != nil {
		return result, normalizeDatabaseError(err)
	}
	reserved, err := r.requests.requestFromRow(ctx, q, request)
	if err != nil {
		return result, err
	}
	if !reserved.Receipt.Equal(intent.Receipt) || reserved.Request != firstSourceReservation(intent) {
		return result, credential.ErrConflict
	}
	if reserved.PlanID != "" {
		link, err := q.GetFirstSourcePlanLinkForPreparation(ctx, credentialdb.GetFirstSourcePlanLinkForPreparationParams{TargetID: target, PreparationID: id})
		if err != nil || link.PlanID != reserved.PlanID || link.RequestDigest != intent.PlanRequestDigest {
			return result, credential.ErrConflict
		}
	}
	admission, err := readFirstSourceAdmission(ctx, db, target, intent.AdmissionOperationID)
	if err != nil {
		return result, err
	}
	if !intent.MatchesAdmission(admission) {
		return result, credential.ErrConflict
	}
	result.Admission, result.Reservation = admission, reserved
	return result, nil
}

func firstSourceReservation(i credential.FirstSourcePreparationIntent) credential.ActivationRequest {
	return credential.ActivationRequest{OperationID: i.PreparationID, VersionID: i.Receipt.Binding.VersionID, ReceiptID: i.Receipt.ReceiptID, ExpectedBindingRevision: i.Receipt.BindingRevision}
}

func (r *FirstSourcePreparations) PrepareTx(ctx context.Context, tx Tx, i credential.FirstSourcePreparationIntent, authorize FirstSourcePreparationAuthorizer) (result credential.FirstSourcePreparation, err error) {
	if i.Validate() != nil {
		return result, credential.ErrInvalid
	}
	err = r.write(ctx, tx, i, authorize, func(tx Tx, q *credentialdb.Queries) error {
		existing, err := r.read(ctx, tx, i.Receipt.Binding.TargetID, i.PreparationID)
		digest, e := i.Digest()
		if e != nil {
			return e
		}
		if err == nil {
			if existing.IntentDigest != digest {
				return credential.ErrConflict
			}
			if e := r.requests.CheckActivationReceiptFreshTx(ctx, tx, i.Receipt.Binding.TargetID, i.PreparationID); e != nil {
				return e
			}
			result = existing
			return nil
		}
		if !errors.Is(err, credential.ErrNotFound) {
			return err
		}
		admission, err := readFirstSourceAdmission(ctx, tx, i.Receipt.Binding.TargetID, i.AdmissionOperationID)
		if err != nil {
			return err
		}
		if !i.MatchesAdmission(admission) {
			return credential.ErrConflict
		}
		// Only this atomic creation may associate a first-source intent with
		// its generic receipt reservation. Never adopt an interrupted rotation.
		_, requestErr := q.GetActivationRequest(ctx, credentialdb.GetActivationRequestParams{DeploymentID: i.Receipt.Binding.TargetID, OperationID: i.PreparationID})
		if requestErr == nil {
			return credential.ErrConflict
		}
		if !errors.Is(requestErr, pgx.ErrNoRows) {
			return normalizeDatabaseError(requestErr)
		}
		if _, err := r.requests.ReserveActivationRequestTx(ctx, tx, i.Receipt, firstSourceReservation(i), func(ctx context.Context, tx Tx) error { return authorize(ctx, tx, i) }); err != nil {
			return err
		}
		intent, err := i.PreparationAuditIntent()
		if err != nil {
			return err
		}
		if err := r.audit.RecordAuditEvent(ctx, tx, intent); err != nil {
			return err
		}
		document, err := json.Marshal(i)
		if err != nil {
			return err
		}
		_, err = q.InsertFirstSourcePreparation(ctx, credentialdb.InsertFirstSourcePreparationParams{OperationID: i.PreparationID, TargetID: i.Receipt.Binding.TargetID, OriginalReceiptID: i.Receipt.ReceiptID, IntentDigest: digest, IntentDocument: document})
		if err != nil {
			return normalizeDatabaseError(err)
		}
		result, err = r.read(ctx, tx, i.Receipt.Binding.TargetID, i.PreparationID)
		return err
	})
	return result, err
}

func (r *FirstSourcePreparations) RenewReceiptTx(ctx context.Context, tx Tx, target, id, receiptID string, authorize FirstSourcePreparationAuthorizer) (result credential.ValidationReceipt, err error) {
	if !canonicalPreparationUUID(receiptID) {
		return result, credential.ErrInvalid
	}
	prepared, err := r.PreparationTx(ctx, tx, target, id)
	if err != nil {
		return result, err
	}
	i := prepared.Intent
	err = r.write(ctx, tx, i, authorize, func(tx Tx, q *credentialdb.Queries) error {
		// Re-read active state after the live target/principal/binding fences.
		if _, err := r.read(ctx, tx, target, id); err != nil {
			return err
		}
		row, err := q.GetActivationRequest(ctx, credentialdb.GetActivationRequestParams{DeploymentID: target, OperationID: id})
		if err != nil {
			return normalizeDatabaseError(err)
		}
		reserved, err := r.requests.requestFromRow(ctx, q, row)
		if err != nil {
			return err
		}
		owner, readErr := q.GetActivationReceiptReservation(ctx, credentialdb.GetActivationReceiptReservationParams{DeploymentID: target, ReceiptID: receiptID})
		if readErr != nil && !errors.Is(readErr, pgx.ErrNoRows) {
			return normalizeDatabaseError(readErr)
		}
		result, err = r.requests.RefreshActivationRequestReceiptTx(ctx, tx, reserved, receiptID, i.Receipt.ActorID, func(ctx context.Context, tx Tx) error { return authorize(ctx, tx, i) })
		if err != nil {
			return err
		}
		if readErr == nil && owner == id {
			return nil
		}
		intent, err := i.RenewalAuditIntent(receiptID)
		if err != nil {
			return err
		}
		return r.audit.RecordAuditEvent(ctx, tx, intent)
	})
	return result, err
}

func (r *FirstSourcePreparations) LinkPlanTx(ctx context.Context, tx Tx, link credential.FirstSourcePlanLink, authorize FirstSourcePlanAuthorizer) error {
	if link.Validate() != nil {
		return credential.ErrInvalid
	}
	if authorize == nil {
		return credential.ErrUnavailable
	}
	prepared, err := r.PreparationTx(ctx, tx, link.TargetID, link.PreparationID)
	if err != nil {
		return err
	}
	i := prepared.Intent
	return r.write(ctx, tx, i, func(ctx context.Context, tx Tx, i credential.FirstSourcePreparationIntent) error {
		return authorize(ctx, tx, i, link)
	}, func(tx Tx, q *credentialdb.Queries) error {
		if _, err := r.read(ctx, tx, link.TargetID, link.PreparationID); err != nil {
			return err
		}
		if link.RequestDigest != i.PlanRequestDigest {
			return credential.ErrConflict
		}
		if err := r.requests.CheckActivationReceiptFreshTx(ctx, tx, link.TargetID, link.PreparationID); err != nil {
			return err
		}
		existing, err := q.GetFirstSourcePlanLinkForPreparation(ctx, credentialdb.GetFirstSourcePlanLinkForPreparationParams{TargetID: link.TargetID, PreparationID: link.PreparationID})
		if err == nil {
			if firstSourceLinkFromRow(existing) != link {
				return credential.ErrConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return normalizeDatabaseError(err)
		}
		intent, err := i.PlanAuditIntent(link)
		if err != nil {
			return err
		}
		if err := r.audit.RecordAuditEvent(ctx, tx, intent); err != nil {
			return err
		}
		_, err = q.InsertFirstSourcePlanLink(ctx, credentialdb.InsertFirstSourcePlanLinkParams{PreparationID: link.PreparationID, TargetID: link.TargetID, PlanID: link.PlanID, RequestDigest: link.RequestDigest, BindingDigest: link.BindingDigest})
		return normalizeDatabaseError(err)
	})
}

func (r *FirstSourcePreparations) PlanLinkTx(ctx context.Context, tx Tx, target, planID string) (credential.FirstSourcePlanLink, error) {
	stored, err := r.StoredPlanLinkTx(ctx, tx, target, planID)
	if err != nil {
		return credential.FirstSourcePlanLink{}, err
	}
	if !firstSourceReservationActive(stored.Preparation.Reservation.State) {
		return credential.FirstSourcePlanLink{}, credential.ErrConflict
	}
	return stored.Link, nil
}

// StoredPlanLinkTx resolves the immutable selection by exact normal plan ID,
// retaining terminal state explicitly. It neither renews nor revives it.
func (r *FirstSourcePreparations) StoredPlanLinkTx(ctx context.Context, tx Tx, target, planID string) (credential.FirstSourceStoredPlanLink, error) {
	if r == nil || ctx == nil || typednil.IsNil(tx) || !canonical(target, 255) || !canonicalPreparationUUID(planID) {
		return credential.FirstSourceStoredPlanLink{}, credential.ErrInvalid
	}
	row, err := credentialdb.New(tx).GetFirstSourcePlanLink(ctx, credentialdb.GetFirstSourcePlanLinkParams{TargetID: target, PlanID: planID})
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.FirstSourceStoredPlanLink{}, credential.ErrNotFound
	}
	if err != nil {
		return credential.FirstSourceStoredPlanLink{}, normalizeDatabaseError(err)
	}
	link := firstSourceLinkFromRow(row)
	prepared, err := r.readStored(ctx, tx, target, link.PreparationID)
	if err != nil {
		return credential.FirstSourceStoredPlanLink{}, err
	}
	if link.Validate() != nil || prepared.Preparation.Intent.PlanRequestDigest != link.RequestDigest {
		return credential.FirstSourceStoredPlanLink{}, credential.ErrConflict
	}
	return credential.FirstSourceStoredPlanLink{Link: link, Preparation: prepared}, nil
}

func firstSourceLinkFromRow(row credentialdb.CredentialFirstSourcePlanLink) credential.FirstSourcePlanLink {
	return credential.FirstSourcePlanLink{TargetID: row.TargetID, PreparationID: row.PreparationID, PlanID: row.PlanID, RequestDigest: row.RequestDigest, BindingDigest: row.BindingDigest}
}

func (r *FirstSourcePreparations) write(ctx context.Context, tx Tx, i credential.FirstSourcePreparationIntent, authorize FirstSourcePreparationAuthorizer, mutate func(Tx, *credentialdb.Queries) error) error {
	if r == nil || r.requests == nil || typednil.IsNil(r.audit) || ctx == nil || typednil.IsNil(tx) || authorize == nil {
		return credential.ErrUnavailable
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return normalizeDatabaseError(err)
	}
	defer sp.Rollback(context.WithoutCancel(ctx))
	if err := requireReadCommitted(ctx, sp, i.Receipt.Binding.TargetID); err != nil {
		return err
	}
	if err := authorize(ctx, sp, i); err != nil {
		return err
	}
	if err := mutate(sp, credentialdb.New(sp)); err != nil {
		return err
	}
	return normalizeDatabaseError(sp.Commit(ctx))
}
