package app

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/credential"
	"github.com/stretchr/testify/require"
)

func TestForegroundRuntimeCredentialAuthorityWithRealReader(t *testing.T) {
	for _, scenario := range []string{"authorized", "already revoked", "revoked after storage", "attenuated after storage"} {
		t.Run(scenario, func(t *testing.T) {
			authority, ctx, identity, resource, binding, reference := newForegroundRuntimeCredentialAuthorityFixture(t)
			requestCredential, ok := accessmodule.APICredentialFromContext(ctx)
			require.True(t, ok)
			requestCredential.Principal = access.Principal{ID: requestCredential.Token.PrincipalID}
			requestCredential.Token.TokenFingerprint = "foreground-token-fingerprint"
			requestCredential.Token.ExpiresAt = time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
			ctx = accessmodule.WithAPICredential(ctx, requestCredential)
			tokens := &foregroundRuntimeTokenEvidence{token: requestCredential.Token}
			authority.recheck = accessmodule.CredentialAuthorityRechecker(tokens, nil)

			encryptionBinding := runtimeReaderEncryptionBinding(reference.Scope, reference.VersionID)
			repository := &foregroundRuntimeHookRepository{runtimeReaderRepository: runtimeReaderRepository{
				versions: map[string]credential.StoredVersion{
					reference.VersionID: runtimeReaderStoredVersion(encryptionBinding, `{"password":"foreground-check-secret"}`),
				},
			}}
			switch scenario {
			case "already revoked":
				tokens.token.RevokedAt = time.Now().UTC().Format(time.RFC3339Nano)
			case "revoked after storage":
				repository.afterRead = func() { tokens.token.RevokedAt = time.Now().UTC().Format(time.RFC3339Nano) }
			case "attenuated after storage":
				repository.afterRead = func() { tokens.token.Permissions = []access.PermissionPair{} }
			}
			keys := &runtimeReaderKeyring{deploymentID: encryptionBinding.DeploymentID}
			reader, err := credential.NewRuntimeResolver(repository, keys, authority)
			require.NoError(t, err)
			analytics := &runtimeReaderAnalytics{}
			check := localRuntimeCredentialCheck{
				targetID: resource.TargetID, environment: resource.Environment,
				reader: reader, owners: authority.owners, bindings: authority.bindings, analytics: analytics,
			}

			got, err := check.check(ctx, identity, binding, reference.VersionID)
			if scenario == "authorized" {
				require.NoError(t, err)
				require.Equal(t, connectionbinding.CredentialIdentity{CredentialVersionID: reference.VersionID}, got)
				require.Equal(t, 2, tokens.calls, "the actual reader rechecks current request authority after storage")
				require.Equal(t, 1, repository.calls)
				require.Equal(t, reference.Scope.OwnerID, repository.ownerID)
				require.Equal(t, reference.Scope.Resource, repository.resource)
				require.Equal(t, reference.VersionID, repository.versionID)
				require.Equal(t, 1, keys.decryptCalls)
				require.Equal(t, 1, analytics.preparations)
				require.Equal(t, "foreground-check-secret", analytics.password)
				require.Empty(t, analytics.retainedFields)
				require.Equal(t, make([]byte, len(keys.lastPlaintext)), keys.lastPlaintext)
				return
			}
			require.ErrorIs(t, err, credential.ErrForbidden)
			require.Zero(t, got)
			require.Zero(t, keys.decryptCalls, "denied current authority must stop before decryption")
			require.Zero(t, analytics.preparations, "denied current authority must stop before any pool work")
			if scenario == "already revoked" {
				require.Zero(t, repository.calls, "initial denial must not read encrypted credential storage")
			} else {
				require.Equal(t, 1, repository.calls)
				require.Equal(t, 2, tokens.calls)
			}
		})
	}
}

type foregroundRuntimeTokenEvidence struct {
	token access.APIToken
	calls int
}

func (reader *foregroundRuntimeTokenEvidence) APITokenAuthorityEvidence(_ context.Context, principalID, tokenID string, _ time.Time) (access.APIToken, error) {
	reader.calls++
	if principalID != reader.token.PrincipalID || tokenID != reader.token.ID {
		return access.APIToken{}, access.ErrForbidden
	}
	return reader.token, nil
}

type foregroundRuntimeHookRepository struct {
	runtimeReaderRepository
	afterRead func()
	calls     int
}

func (repository *foregroundRuntimeHookRepository) GetStoredDraft(ctx context.Context, deploymentID, ownerID string, resource credential.Resource, versionID string) (credential.StoredVersion, error) {
	repository.calls++
	stored, err := repository.runtimeReaderRepository.GetStoredDraft(ctx, deploymentID, ownerID, resource, versionID)
	if repository.afterRead != nil {
		repository.afterRead()
	}
	return stored, err
}
