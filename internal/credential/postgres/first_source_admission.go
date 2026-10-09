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
	"github.com/jackc/pgx/v5/pgconn"
)

type FirstSourceAdmissionDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// FirstSourceAdmissions owns immutable admission records over the existing
// control pool. Mutation accepts only a caller-owned transaction and a live
// authority check; the offline composition owns fencing, binding and policy.
type FirstSourceAdmissions struct {
	db    FirstSourceAdmissionDB
	audit AuditRepository
}

func NewFirstSourceAdmissions(db FirstSourceAdmissionDB, audit AuditRepository) (*FirstSourceAdmissions, error) {
	if typednil.IsNil(db) || typednil.IsNil(audit) {
		return nil, credential.ErrUnavailable
	}
	return &FirstSourceAdmissions{db: db, audit: audit}, nil
}

func (r *FirstSourceAdmissions) Admission(ctx context.Context, targetID, operationID string) (credential.FirstSourceAdmission, error) {
	if r == nil || typednil.IsNil(r.db) {
		return credential.FirstSourceAdmission{}, credential.ErrUnavailable
	}
	return readFirstSourceAdmission(ctx, r.db, targetID, operationID)
}

func (r *FirstSourceAdmissions) AdmissionTx(ctx context.Context, tx pgx.Tx, targetID, operationID string) (credential.FirstSourceAdmission, error) {
	if r == nil || typednil.IsNil(tx) {
		return credential.FirstSourceAdmission{}, credential.ErrUnavailable
	}
	return readFirstSourceAdmission(ctx, tx, targetID, operationID)
}

// AdmissionForTarget resolves the server-bound initial operation. Request data
// cannot guess an operation identity or select a credential version through it.
func (r *FirstSourceAdmissions) AdmissionForTarget(ctx context.Context, targetID string) (credential.FirstSourceAdmission, error) {
	if r == nil || typednil.IsNil(r.db) {
		return credential.FirstSourceAdmission{}, credential.ErrUnavailable
	}
	return readFirstSourceAdmissionForTarget(ctx, r.db, targetID)
}

func (r *FirstSourceAdmissions) AdmissionForTargetTx(ctx context.Context, tx pgx.Tx, targetID string) (credential.FirstSourceAdmission, error) {
	if r == nil || typednil.IsNil(tx) {
		return credential.FirstSourceAdmission{}, credential.ErrUnavailable
	}
	return readFirstSourceAdmissionForTarget(ctx, tx, targetID)
}

func readFirstSourceAdmission(ctx context.Context, db FirstSourceAdmissionDB, targetID, operationID string) (credential.FirstSourceAdmission, error) {
	if ctx == nil || !canonical(targetID, 255) || !canonicalPreparationUUID(operationID) {
		return credential.FirstSourceAdmission{}, credential.ErrInvalid
	}
	admission, err := readFirstSourceAdmissionForTarget(ctx, db, targetID)
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	if admission.Intent.OperationID != operationID {
		return credential.FirstSourceAdmission{}, credential.ErrNotFound
	}
	return admission, nil
}

func readFirstSourceAdmissionForTarget(ctx context.Context, db FirstSourceAdmissionDB, targetID string) (credential.FirstSourceAdmission, error) {
	if ctx == nil || !canonical(targetID, 255) {
		return credential.FirstSourceAdmission{}, credential.ErrInvalid
	}
	row, err := credentialdb.New(db).GetFirstSourceAdmission(ctx, targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.FirstSourceAdmission{}, credential.ErrNotFound
	}
	if err != nil {
		return credential.FirstSourceAdmission{}, normalizeDatabaseError(err)
	}
	return firstSourceAdmissionFromRow(row)
}

type FirstSourceAdmissionAuthorizer func(context.Context, pgx.Tx, credential.FirstSourceAdmission) error

// InsertAdmissionTx appends audit and authority atomically. Exact retries
// remain reauthorized and return the original receipt; it never commits tx.
func (r *FirstSourceAdmissions) InsertAdmissionTx(ctx context.Context, tx pgx.Tx, admission credential.FirstSourceAdmission, authorize FirstSourceAdmissionAuthorizer) (credential.FirstSourceAdmission, error) {
	if r == nil || ctx == nil || typednil.IsNil(tx) || typednil.IsNil(r.audit) || authorize == nil {
		return credential.FirstSourceAdmission{}, credential.ErrUnavailable
	}
	if err := admission.Validate(); err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	if err := authorize(ctx, tx, admission); err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	queries := credentialdb.New(tx)
	row, err := queries.GetFirstSourceAdmission(ctx, admission.Intent.TargetID)
	if err == nil {
		stored, err := firstSourceAdmissionFromRow(row)
		if err != nil {
			return credential.FirstSourceAdmission{}, err
		}
		if stored.IntentDigest != admission.IntentDigest || stored.Intent.OperationID != admission.Intent.OperationID || stored.BindingDigest != admission.BindingDigest || stored.PolicyRevision != admission.PolicyRevision || stored.PolicyDigest != admission.PolicyDigest {
			return credential.FirstSourceAdmission{}, credential.ErrConflict
		}
		return stored, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return credential.FirstSourceAdmission{}, normalizeDatabaseError(err)
	}
	intent, err := admission.AuditIntent()
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	if err := r.audit.RecordAuditEvent(ctx, tx, intent); err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	document, err := json.Marshal(admission.Intent)
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	row, err = queries.InsertFirstSourceAdmission(ctx, credentialdb.InsertFirstSourceAdmissionParams{
		TargetID: admission.Intent.TargetID, OperationID: admission.Intent.OperationID, IntentDigest: admission.IntentDigest,
		IntentDocument: document, BindingDigest: admission.BindingDigest, PolicyRevision: admission.PolicyRevision, PolicyDigest: admission.PolicyDigest,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.FirstSourceAdmission{}, credential.ErrConflict
	}
	if err != nil {
		return credential.FirstSourceAdmission{}, normalizeDatabaseError(err)
	}
	return firstSourceAdmissionFromRow(row)
}

func firstSourceAdmissionFromRow(row credentialdb.CredentialFirstSourceAdmission) (credential.FirstSourceAdmission, error) {
	var intent credential.FirstSourceAdmissionIntent
	if err := strictjson.DecodeWithOptions(row.IntentDocument, &intent, strictjson.Options{MaxBytes: 32 << 10, MaxDepth: 16, DuplicateKeys: strictjson.CaseFoldedKeys, AllowUnknownFields: false}); err != nil {
		return credential.FirstSourceAdmission{}, credential.ErrInvalid
	}
	admission := credential.FirstSourceAdmission{Intent: intent, IntentDigest: row.IntentDigest, BindingDigest: row.BindingDigest,
		PolicyRevision: row.PolicyRevision, PolicyDigest: row.PolicyDigest, CreatedAt: row.CreatedAt}
	return admission, admission.Validate()
}
