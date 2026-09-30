package app

import (
	"testing"

	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/credential"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestRuntimeCredentialLeaseReaderUsesRealResolverAndRetainsUncertainCleanup(t *testing.T) {
	t.Run("successful callback releases the read lease", func(t *testing.T) {
		reader, provider, keys, identity, resource, reference := newRealRuntimeCredentialLeaseReader(t)
		var gotReference credentialmodule.RuntimeCredentialReference
		var retainedFields map[string]string
		err := reader.WithCredential(t.Context(), identity, resource, func(got credentialmodule.RuntimeCredentialReference, fields map[string]string) error {
			gotReference = got
			retainedFields = fields
			return nil
		})
		require.NoError(t, err)
		require.True(t, provider.leases[0].released)
		require.Empty(t, retainedFields, "RuntimeResolver must clear fields after a normal consumer return")
		require.Equal(t, reference, gotReference)
		require.Equal(t, make([]byte, len(keys.lastPlaintext)), keys.lastPlaintext, "RuntimeResolver must clear decrypted bytes after a normal consumer return")
	})

	t.Run("runtime cleanup sentinel is redacted and retains the lease", func(t *testing.T) {
		reader, provider, keys, identity, resource, _ := newRealRuntimeCredentialLeaseReader(t)
		var retainedFields map[string]string
		err := reader.WithCredential(t.Context(), identity, resource, func(_ credentialmodule.RuntimeCredentialReference, fields map[string]string) error {
			retainedFields = fields
			return analyticsruntime.ErrConnectionCleanupFailed
		})
		require.ErrorIs(t, err, credentialmodule.ErrRuntimeUnavailable)
		require.NotErrorIs(t, err, analyticsruntime.ErrConnectionCleanupFailed, "reader diagnostics must remain redacted")
		require.False(t, provider.leases[0].released, "uncertain connection cleanup must retain the generation lease")
		require.Empty(t, retainedFields, "RuntimeResolver must clear fields after a consumer error")
		require.Equal(t, make([]byte, len(keys.lastPlaintext)), keys.lastPlaintext, "RuntimeResolver must clear decrypted bytes after a consumer error")
	})

	t.Run("local cleanup sentinel remains retained", func(t *testing.T) {
		reader, provider, keys, identity, resource, _ := newRealRuntimeCredentialLeaseReader(t)
		var retainedFields map[string]string
		err := reader.WithCredential(t.Context(), identity, resource, func(_ credentialmodule.RuntimeCredentialReference, fields map[string]string) error {
			retainedFields = fields
			return analyticsmodule.ErrLocalRuntimeCredentialCleanupFailed
		})
		require.ErrorIs(t, err, credentialmodule.ErrRuntimeUnavailable)
		require.NotErrorIs(t, err, analyticsmodule.ErrLocalRuntimeCredentialCleanupFailed, "reader diagnostics must remain redacted")
		require.False(t, provider.leases[0].released)
		require.Empty(t, retainedFields)
		require.Equal(t, make([]byte, len(keys.lastPlaintext)), keys.lastPlaintext)
	})

	t.Run("callback panic is redacted and retains the lease", func(t *testing.T) {
		reader, provider, keys, identity, resource, _ := newRealRuntimeCredentialLeaseReader(t)
		var retainedFields map[string]string
		var panicValue any
		func() {
			defer func() { panicValue = recover() }()
			_ = reader.WithCredential(t.Context(), identity, resource, func(_ credentialmodule.RuntimeCredentialReference, fields map[string]string) error {
				retainedFields = fields
				panic("sensitive native cleanup detail")
			})
		}()
		panicErr, ok := panicValue.(error)
		require.True(t, ok, "RuntimeResolver must replace the consumer panic with a fixed error")
		require.EqualError(t, panicErr, "credential runtime consumer panicked")
		require.NotContains(t, panicErr.Error(), "sensitive native cleanup detail")
		require.False(t, provider.leases[0].released, "a consumer panic leaves cleanup uncertain")
		require.Empty(t, retainedFields, "RuntimeResolver must clear fields while unwinding a panic")
		require.Equal(t, make([]byte, len(keys.lastPlaintext)), keys.lastPlaintext, "RuntimeResolver must clear decrypted bytes while unwinding a panic")
	})
}

func newRealRuntimeCredentialLeaseReader(t *testing.T) (runtimeCredentialLeaseReader, *foregroundCheckLeaseProvider, *runtimeReaderKeyring, projectgraph.ServingIdentity, credentialmodule.RuntimeResource, credentialmodule.RuntimeCredentialReference) {
	t.Helper()
	authority, _, identity, resource, _, reference := newForegroundRuntimeCredentialAuthorityFixture(t)
	provider := foregroundCheckProviderWithLeases(authority, 1)
	encryptionBinding := runtimeReaderEncryptionBinding(reference.Scope, reference.VersionID)
	repository := &runtimeReaderRepository{versions: map[string]credential.StoredVersion{
		reference.VersionID: runtimeReaderStoredVersion(encryptionBinding, `{"password":"lease-reader-secret"}`),
	}}
	keys := &runtimeReaderKeyring{deploymentID: encryptionBinding.DeploymentID}
	resolver, err := credential.NewRuntimeResolver(repository, keys, &runtimeReaderAuthority{
		references: []credential.RuntimeCredentialReference{reference, reference},
	})
	require.NoError(t, err)
	return runtimeCredentialLeaseReader{provider: provider, identity: identity, reader: resolver}, provider, keys, identity, resource, reference
}
