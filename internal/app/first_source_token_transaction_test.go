package app

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type firstSourceTokenTransactionProbe struct {
	*accesspostgres.Repository
	after func(access.Repository) error
}

func (r firstSourceTokenTransactionProbe) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	return r.Repository.RunAuditedMutation(ctx, func(native access.Repository) (access.AuditEventInput, error) {
		event, err := mutation(native)
		if err == nil {
			err = r.after(native)
		}
		return event, err
	})
}

func TestFirstSourceTokenIssuerKeepsAuthorityAndAuditAtomic(t *testing.T) {
	f := newFirstSourceAuthorityFixtureWithPublisher(t, true)
	delivery := deploymentpostgres.New(f.pool)
	_, err := delivery.CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: f.resource.TargetID, ProjectID: f.resource.ProjectID, Environment: f.resource.Environment})
	require.NoError(t, err)
	scope := firstSourceServiceScope(f)
	forced := errors.New("audit transaction rejected")
	probe := firstSourceTokenTransactionProbe{Repository: f.repository, after: func(access.Repository) error { return forced }}
	issuer := newFirstSourceTokenIssuer(&scope, probe)
	secret, handled, err := issuer.Create(f.ctx, firstSourceTokenInput(t, f))
	require.ErrorIs(t, err, forced)
	require.True(t, handled)
	require.True(t, secret == "")
	tokens, err := f.repository.ListAPITokens(t.Context(), f.actor)
	require.NoError(t, err)
	require.Empty(t, tokens)
	audits, err := f.repository.ListAuditEvents(t.Context(), access.AuditEventFilter{PrincipalID: f.actor, Action: "api_token.created", Limit: 100})
	require.NoError(t, err)
	require.Empty(t, audits)
	evidence, err := accessmodule.CredentialTransactionEvidence(f.ctx, f.actor)
	require.NoError(t, err)
	probe.after = func(access.Repository) error {
		tokens, err := f.repository.ListAPITokens(t.Context(), f.actor)
		require.NoError(t, err)
		require.Empty(t, tokens, "token is invisible before authority transaction commits")
		for _, query := range []struct {
			sql string
			id  string
		}{
			{"SELECT id FROM access.session WHERE id=$1::uuid FOR UPDATE", evidence.Credential.ID},
			{"SELECT target_id FROM delivery.delivery_target WHERE target_id=$1 FOR UPDATE", f.resource.TargetID},
		} {
			competing, err := f.pool.Begin(t.Context())
			require.NoError(t, err)
			_, err = competing.Exec(t.Context(), "SET LOCAL lock_timeout='100ms'")
			require.NoError(t, err)
			_, err = competing.Exec(t.Context(), query.sql, query.id)
			var lockErr *pgconn.PgError
			require.ErrorAs(t, err, &lockErr)
			require.Equal(t, "55P03", lockErr.Code)
			require.NoError(t, competing.Rollback(t.Context()))
		}
		return nil
	}
	issuer.repository = probe
	secret, handled, err = issuer.Create(f.ctx, firstSourceTokenInput(t, f))
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, secret != "")
	tokens, err = f.repository.ListAPITokens(t.Context(), f.actor)
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	audits, err = f.repository.ListAuditEvents(t.Context(), access.AuditEventFilter{PrincipalID: f.actor, Action: "api_token.created", Limit: 100})
	require.NoError(t, err)
	require.Len(t, audits, 1)
	require.NoError(t, f.repository.DeleteSession(t.Context(), f.session), "token commit releases the held session")
}
