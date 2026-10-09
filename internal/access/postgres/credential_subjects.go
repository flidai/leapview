package postgres

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/jackc/pgx/v5"
)

// LockCredentialAuthorizationSubjectsTx holds the principal and every current
// group membership used by a credential mutation until commit. Newly added
// grants cannot make an already-denied request pass; revocation of any subject
// used by an allowed request must wait for this authority transaction.
func LockCredentialAuthorizationSubjectsTx(ctx context.Context, tx pgx.Tx, actor string) ([]access.SubjectRef, error) {
	if ctx == nil || typednil.IsNil(tx) {
		return nil, access.ErrForbidden
	}
	principal, err := pgUUID(actor)
	if err != nil || !principal.Valid {
		return nil, access.ErrForbidden
	}
	q := accessdb.New(tx)
	if _, err = q.LockCurrentCredentialPrincipal(ctx, principal); err != nil {
		return nil, access.ErrForbidden
	}
	groups, err := q.LockCredentialAuthorizationGroups(ctx, principal)
	if err != nil {
		return nil, access.ErrForbidden
	}
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, actor)
	if err != nil {
		return nil, access.ErrForbidden
	}
	subjects := []access.SubjectRef{subject}
	for _, group := range groups {
		subject, err := access.NewSubjectRef(access.SubjectKindGroup, principalUUID(group))
		if err != nil {
			return nil, access.ErrForbidden
		}
		subjects = append(subjects, subject)
	}
	return subjects, nil
}
