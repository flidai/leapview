package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/credential"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	"github.com/stretchr/testify/require"
)

func TestForegroundRuntimeCredentialCheckComposesExactRuntimeReader(t *testing.T) {
	for _, scenario := range []string{"authorized", "revoked after storage"} {
		t.Run(scenario, func(t *testing.T) {
			authority, ctx, _, _, binding, reference := newForegroundRuntimeCredentialAuthorityFixture(t)
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
			if scenario == "revoked after storage" {
				repository.afterRead = func() { tokens.token.RevokedAt = time.Now().UTC().Format(time.RFC3339Nano) }
			}
			keys := &runtimeReaderKeyring{deploymentID: encryptionBinding.DeploymentID}
			provider := foregroundCheckProviderWithLeases(authority, 5)
			authority.provider = provider
			var authorityUsed credentialmodule.RuntimeUseAuthority
			factory := runtimeCredentialReaderFactoryFunc(func(use credentialmodule.RuntimeUseAuthority) (credentialmodule.RuntimeCredentialReader, error) {
				authorityUsed = use
				return credential.NewRuntimeResolver(repository, keys, use)
			})
			analytics := &runtimeReaderAnalytics{}
			check := foregroundRuntimeCredentialCheck{authority: authority, readers: factory, analytics: analytics}

			got, err := check.check(ctx, binding.ConnectionID)
			if scenario == "authorized" {
				require.NoError(t, err)
				require.Equal(t, connectionbinding.CredentialIdentity{CredentialVersionID: reference.VersionID}, got)
				require.Equal(t, 3, tokens.calls, "the initial pin and both reader checks use current request authority")
				require.Equal(t, 1, repository.calls, "the reader fetches the exact committed version once")
				require.Equal(t, reference.VersionID, repository.versionID)
				require.Equal(t, reference.Scope.OwnerID, repository.ownerID)
				require.Equal(t, 1, analytics.preparations)
				require.Equal(t, "foreground-check-secret", analytics.password)
				require.Empty(t, analytics.retainedFields)
				usedAuthority, ok := authorityUsed.(foregroundRuntimeCredentialAuthority)
				require.True(t, ok, "the factory must receive the request-local authority")
				require.Equal(t, "principal_credential", usedAuthority.principalID)
				require.Len(t, provider.leases, 5)
				for _, lease := range provider.leases {
					require.True(t, lease.released)
				}
				return
			}
			require.ErrorIs(t, err, credential.ErrForbidden)
			require.Zero(t, got)
			require.Zero(t, analytics.preparations, "revocation after storage stops before local pool preparation")
			require.Equal(t, 1, repository.calls)
			require.Len(t, provider.leases, 5)
			for _, lease := range provider.leases {
				require.True(t, lease.released)
			}
		})
	}
}

func TestForegroundRuntimeCredentialCheckOrdersLeasesAroundSourceWork(t *testing.T) {
	for _, phase := range []string{"admission", "cleanup"} {
		t.Run(phase, func(t *testing.T) {
			authority, ctx, _, _, binding, reference := newForegroundRuntimeCredentialAuthorityFixture(t)
			provider := foregroundCheckProviderWithLeases(authority, 3)
			authority.provider = provider
			started := make(chan struct{}, 1)
			proceed := make(chan struct{})
			analytics := foregroundCheckBlockingAnalytics{phase: phase, started: started, proceed: proceed}
			factory := runtimeCredentialReaderFactoryFunc(func(credentialmodule.RuntimeUseAuthority) (credentialmodule.RuntimeCredentialReader, error) {
				return &foregroundCheckReader{reference: reference}, nil
			})
			check := foregroundRuntimeCredentialCheck{authority: authority, readers: factory, analytics: analytics}
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			done := make(chan error, 1)
			go func() {
				_, err := check.check(ctx, binding.ConnectionID)
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("analytics check did not reach the blocked phase")
			}
			require.True(t, provider.leases[0].released, "the short selection lease is released before source-work admission")
			require.True(t, provider.leases[1].released, "the committed-pin lookup lease is also released before admission")
			if phase == "admission" {
				require.Equal(t, 2, provider.calls, "no long serving lease is acquired while admission is blocked")
			} else {
				require.Equal(t, 3, provider.calls, "the reader acquires a fresh generation lease inside the admitted callback")
				require.False(t, provider.leases[2].released, "the per-read lease spans callback cleanup")
			}
			cancel()
			if phase == "cleanup" {
				require.False(t, provider.leases[2].released, "cancellation cannot release a lease while callback cleanup is active")
			}
			close(proceed)
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(3 * time.Second):
				t.Fatal("foreground credential check did not return after the blocked phase was released")
			}
			if phase == "cleanup" {
				require.True(t, provider.leases[2].released, "normal cancellation return releases the per-read lease")
			}
		})
	}
}

func TestForegroundRuntimeCredentialCheckQuarantinesReadLeaseOnCleanupFailureOrPanic(t *testing.T) {
	for _, scenario := range []string{"cleanup failure", "panic"} {
		t.Run(scenario, func(t *testing.T) {
			authority, ctx, _, _, binding, reference := newForegroundRuntimeCredentialAuthorityFixture(t)
			provider := foregroundCheckProviderWithLeases(authority, 3)
			authority.provider = provider
			factory := runtimeCredentialReaderFactoryFunc(func(credentialmodule.RuntimeUseAuthority) (credentialmodule.RuntimeCredentialReader, error) {
				return &foregroundCheckReader{reference: reference}, nil
			})
			var analytics localRuntimeCredentialAnalytics = foregroundCheckAnalyticsFunc(func(ctx context.Context, _ connectionbinding.TargetBinding, _ string, read func(context.Context, func(map[string]string) error) error) (connectionbinding.CredentialIdentity, error) {
				if scenario == "panic" {
					_ = read(ctx, func(map[string]string) error { panic("native cleanup state unknown") })
					return connectionbinding.CredentialIdentity{}, nil
				}
				_ = read(ctx, func(map[string]string) error { return analyticsmodule.ErrLocalRuntimeCredentialCleanupFailed })
				return connectionbinding.CredentialIdentity{}, analyticsmodule.ErrLocalRuntimeCredentialCleanupFailed
			})
			check := foregroundRuntimeCredentialCheck{authority: authority, readers: factory, analytics: analytics}

			if scenario == "panic" {
				func() {
					defer func() { require.NotNil(t, recover(), "panic must propagate to the caller") }()
					_, _ = check.check(ctx, binding.ConnectionID)
				}()
			} else {
				got, err := check.check(ctx, binding.ConnectionID)
				require.ErrorIs(t, err, analyticsmodule.ErrLocalRuntimeCredentialCleanupFailed)
				require.Zero(t, got)
			}
			require.True(t, provider.leases[0].released, "the short selection lease is released before local work")
			require.True(t, provider.leases[1].released, "the short authority metadata lease is released before local work")
			require.False(t, provider.leases[2].released, "uncertain pool cleanup must quarantine the per-read serving lease")
		})
	}
}

func TestForegroundRuntimeCredentialCheckFailsClosedOnCutoverAndRedactsFactoryErrors(t *testing.T) {
	t.Run("cutover before per-read lease stops credential access", func(t *testing.T) {
		authority, ctx, identity, _, binding, _ := newForegroundRuntimeCredentialAuthorityFixture(t)
		initialSnapshot := authority.provider.(*foregroundRuntimeAuthorityProvider).lease.(*foregroundRuntimeAuthorityLease).snapshot
		cutover, err := projectgraph.NewServingIdentity(identity.ProjectID, identity.Environment, "generation_after_cutover")
		require.NoError(t, err)
		cutoverSnapshot := foregroundAuthoritySnapshot(t, cutover, identity.ProjectID, "principal_credential", true)
		provider := &foregroundCheckLeaseProvider{leases: []*foregroundRuntimeAuthorityLease{
			&foregroundRuntimeAuthorityLease{identity: identity, snapshot: initialSnapshot},
			&foregroundRuntimeAuthorityLease{identity: identity, snapshot: initialSnapshot},
			&foregroundRuntimeAuthorityLease{identity: cutover, snapshot: cutoverSnapshot},
		}}
		authority.provider = provider
		factoryCalls := 0
		reader := &foregroundCheckReader{}
		factory := runtimeCredentialReaderFactoryFunc(func(credentialmodule.RuntimeUseAuthority) (credentialmodule.RuntimeCredentialReader, error) {
			factoryCalls++
			return reader, nil
		})
		check := foregroundRuntimeCredentialCheck{authority: authority, readers: factory, analytics: &runtimeReaderAnalytics{}}

		got, err := check.check(ctx, binding.ConnectionID)
		require.ErrorIs(t, err, credentialmodule.ErrRuntimeConflict)
		require.Zero(t, got)
		require.Equal(t, 1, factoryCalls)
		require.Zero(t, reader.calls, "the decorator must reject a changed full identity before credential access")
		require.Equal(t, 3, provider.calls)
		require.True(t, provider.leases[0].released)
		require.True(t, provider.leases[1].released)
		require.True(t, provider.leases[2].released)
	})

	t.Run("reader factory diagnostic is redacted", func(t *testing.T) {
		authority, ctx, _, _, binding, _ := newForegroundRuntimeCredentialAuthorityFixture(t)
		provider := foregroundCheckProviderWithLeases(authority, 2)
		authority.provider = provider
		factory := runtimeCredentialReaderFactoryFunc(func(credentialmodule.RuntimeUseAuthority) (credentialmodule.RuntimeCredentialReader, error) {
			return nil, errors.New("secret keyring path detail")
		})
		check := foregroundRuntimeCredentialCheck{authority: authority, readers: factory, analytics: &runtimeReaderAnalytics{}}

		got, err := check.check(ctx, binding.ConnectionID)
		require.ErrorIs(t, err, credentialmodule.ErrRuntimeUnavailable)
		require.NotContains(t, err.Error(), "secret keyring")
		require.Zero(t, got)
		require.True(t, provider.leases[0].released)
		require.True(t, provider.leases[1].released)
	})
}

type runtimeCredentialReaderFactoryFunc func(credentialmodule.RuntimeUseAuthority) (credentialmodule.RuntimeCredentialReader, error)

func (factory runtimeCredentialReaderFactoryFunc) RuntimeReader(authority credentialmodule.RuntimeUseAuthority) (credentialmodule.RuntimeCredentialReader, error) {
	return factory(authority)
}

type foregroundCheckLeaseProvider struct {
	leases []*foregroundRuntimeAuthorityLease
	err    error
	calls  int
}

func (provider *foregroundCheckLeaseProvider) Acquire(context.Context) (runtimehostmodule.Lease, error) {
	provider.calls++
	index := provider.calls - 1
	if index < len(provider.leases) {
		return provider.leases[index], provider.err
	}
	return nil, provider.err
}

func foregroundCheckProviderWithLeases(authority foregroundRuntimeCredentialAuthority, count int) *foregroundCheckLeaseProvider {
	base := authority.provider.(*foregroundRuntimeAuthorityProvider).lease.(*foregroundRuntimeAuthorityLease)
	provider := &foregroundCheckLeaseProvider{}
	for range count {
		provider.leases = append(provider.leases, &foregroundRuntimeAuthorityLease{identity: base.identity, snapshot: base.snapshot})
	}
	return provider
}

type foregroundCheckReader struct {
	reference credentialmodule.RuntimeCredentialReference
	calls     int
}

func (reader *foregroundCheckReader) WithCredential(_ context.Context, _ projectgraph.ServingIdentity, _ credentialmodule.RuntimeResource, consume func(credentialmodule.RuntimeCredentialReference, map[string]string) error) error {
	reader.calls++
	if err := consume(reader.reference, map[string]string{"password": "test-secret"}); err != nil {
		return credentialmodule.ErrRuntimeUnavailable
	}
	return nil
}

type foregroundCheckBlockingAnalytics struct {
	phase   string
	started chan<- struct{}
	proceed <-chan struct{}
}

func (analytics foregroundCheckBlockingAnalytics) CheckLocalRuntimeCredential(
	ctx context.Context,
	_ connectionbinding.TargetBinding,
	versionID string,
	read func(context.Context, func(map[string]string) error) error,
) (connectionbinding.CredentialIdentity, error) {
	if analytics.phase == "admission" {
		analytics.started <- struct{}{}
		<-analytics.proceed
	}
	err := read(ctx, func(map[string]string) error {
		if analytics.phase == "cleanup" {
			analytics.started <- struct{}{}
			<-analytics.proceed
		}
		return nil
	})
	if err != nil {
		return connectionbinding.CredentialIdentity{}, err
	}
	if err := ctx.Err(); err != nil {
		return connectionbinding.CredentialIdentity{}, err
	}
	return connectionbinding.CredentialIdentity{CredentialVersionID: versionID}, nil
}

type foregroundCheckAnalyticsFunc func(context.Context, connectionbinding.TargetBinding, string, func(context.Context, func(map[string]string) error) error) (connectionbinding.CredentialIdentity, error)

func (analytics foregroundCheckAnalyticsFunc) CheckLocalRuntimeCredential(ctx context.Context, binding connectionbinding.TargetBinding, versionID string, read func(context.Context, func(map[string]string) error) error) (connectionbinding.CredentialIdentity, error) {
	return analytics(ctx, binding, versionID, read)
}

var _ runtimehostmodule.Provider = (*foregroundCheckLeaseProvider)(nil)
