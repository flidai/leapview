package personalsettings

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/stretchr/testify/require"
)

type tokenIssuerResult struct {
	handled bool
	err     error
}

func (i tokenIssuerResult) PermissionOptions(context.Context, string) ([]access.PermissionPair, bool, error) {
	return nil, i.handled, i.err
}
func (i tokenIssuerResult) Create(context.Context, access.ScopedAPITokenInput) (string, bool, error) {
	return "", i.handled, i.err
}

func TestBoundedTokenIssuerPreservesNormalErrorsAndDenials(t *testing.T) {
	pair, err := access.NewProjectPermissionPair(access.ActionProjectSettingsRead, "project:first")
	require.NoError(t, err)
	normalFailure := errors.New("active snapshot unavailable")
	boundedFailure := errors.New("admission provider unavailable")
	for _, scenario := range []string{"ordinary", "bounded"} {
		t.Run(scenario, func(t *testing.T) {
			repo := &fakeRepository{principal: access.Principal{ID: "principal-1", Kind: access.PrincipalKindUser}}
			service := testService(t, repo)
			service.TokenIssuer = tokenIssuerResult{}
			normalCalls := 0
			service.CurrentEffectivePermissionOptions = func(context.Context, string) ([]access.PermissionPair, error) {
				normalCalls++
				return nil, normalFailure
			}
			want := normalFailure
			if scenario == "bounded" {
				service.TokenIssuer = tokenIssuerResult{handled: true, err: boundedFailure}
				want = boundedFailure
			}
			_, err := service.Load(t.Context(), repo.principal.ID, "", true)
			require.ErrorIs(t, err, want)
			secret, err := service.ApplyToken(t.Context(), repo.principal.ID, TokenCommand{Action: "create", Name: "bounded", Permissions: []PermissionPairSignal{permissionPairSignal(pair)}})
			require.ErrorIs(t, err, want)
			require.Nil(t, secret)
			require.False(t, repo.createdToken)
			require.Empty(t, repo.audits)
			if scenario == "bounded" {
				require.Zero(t, normalCalls)
			} else {
				require.Equal(t, 2, normalCalls)
			}
		})
	}
}
