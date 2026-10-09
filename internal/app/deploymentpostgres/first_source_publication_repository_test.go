package deploymentpostgres

import (
	"context"
	"errors"
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	lineagepostgres "github.com/flidai/leapview/internal/lineage/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/stretchr/testify/require"
)

func TestFirstSourcePublicationCommitsBothJournalsWithNativeCAS(t *testing.T) {
	f := newActivationPublicationFixtureWithOptions(t, activationPublicationFixtureOptions{candidateVersionMatches: true, prepareCredential: true, localPin: true, reserveCredentialRequest: true})
	row, err := f.credentials.GetActivationRequest(t.Context(), f.publication.TargetID, f.operationID)
	require.NoError(t, err)
	require.Equal(t, "switching", row.State)
	lineage, err := NewActivationLineageVerifier(lineagepostgres.New(f.db))
	require.NoError(t, err)
	repository, err := NewFirstSourcePublicationRepository(f.db, Authorities{Access: accesspostgres.New(), Events: eventspostgres.New(), Lineage: lineage}, f.credentials, row, activationPublicationCurrentAuthority(f))
	require.NoError(t, err)
	interrupted := errors.New("first-source semantic fence interrupted")
	tx, err := f.db.Begin(t.Context())
	require.NoError(t, err)
	_, err = repository.ActivateTxWithPreCommitHook(t.Context(), tx, f.activation, func(context.Context, deploymentpostgres.Tx, deploymentpostgres.DeliveryPublication) error {
		return interrupted
	})
	require.ErrorIs(t, err, interrupted)
	// Deliberately commit the caller transaction: the activation savepoint must
	// already have rolled back both credential journals and the serving pointer.
	require.NoError(t, tx.Commit(t.Context()))
	after, err := f.credentials.GetActivationRequest(t.Context(), f.publication.TargetID, f.operationID)
	require.NoError(t, err)
	require.Equal(t, row, after)
	assertActivationPublicationUnchanged(t, f, false)
	assertActivationCommitAuditCount(t, f, 0)
	tx, err = f.db.Begin(t.Context())
	require.NoError(t, err)
	_, err = repository.ActivateTx(t.Context(), tx, f.activation)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(t.Context()))
	after, err = f.credentials.GetActivationRequest(t.Context(), f.publication.TargetID, f.operationID)
	require.NoError(t, err)
	require.Equal(t, "committed", after.State)
	require.Equal(t, row.Revision+1, after.Revision)
	assertActivationCommitAuditCount(t, f, 1)
	tx, err = f.db.Begin(t.Context())
	require.NoError(t, err)
	replayed, err := repository.ActivateTx(t.Context(), tx, f.activation)
	require.NoError(t, err)
	require.True(t, replayed.Replay)
	require.NoError(t, tx.Commit(t.Context()))
	latest, err := f.credentials.GetActivationRequest(t.Context(), f.publication.TargetID, f.operationID)
	require.NoError(t, err)
	require.Equal(t, after, latest)
	assertActivationCommitAuditCount(t, f, 1)
}
