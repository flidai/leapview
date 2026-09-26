package postgres

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) initialPublisherOrigin(ctx context.Context, token access.APIToken) (*access.InitialPublisherOrigin, bool, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, false, err
	}
	tokenID, err := pgUUID(token.ID)
	if err != nil {
		return nil, false, err
	}
	principalID, err := pgUUID(token.PrincipalID)
	if err != nil {
		return nil, false, err
	}
	row, err := accessdb.New(db).InitialPublisherOrigin(ctx, accessdb.InitialPublisherOriginParams{PublisherCredentialID: tokenID, PrincipalID: principalID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	// The initialization record permanently binds claim/principal/instance.
	// ACK and retention may revoke and prune the parent claim; neither changes
	// this provenance. The publisher must still carry the exact issued scope.
	if !isExactInitialProjectPublisher(token, projectgraph.ResourceID(row.ProjectID)) {
		return nil, false, access.ErrForbidden
	}
	return &access.InitialPublisherOrigin{ClaimCredentialID: principalUUID(row.ClaimCredentialID), PrincipalID: token.PrincipalID, InstanceID: row.InstanceID, ProjectID: row.ProjectID}, !row.ClosedAt.Valid, nil
}

func (r *Repository) InitialPublisherPasswordSetupOpen(ctx context.Context, principalID, tokenID string) (bool, error) {
	db, err := r.requireDB()
	if err != nil {
		return false, err
	}
	now, err := accessdb.New(db).DatabaseNow(ctx)
	if err != nil {
		return false, err
	}
	token, err := r.APITokenAuthorityEvidence(ctx, principalID, tokenID, dbEpochMicros(now))
	if err != nil {
		return false, err
	}
	origin, open, err := r.initialPublisherOrigin(ctx, token)
	return origin != nil && open, err
}
