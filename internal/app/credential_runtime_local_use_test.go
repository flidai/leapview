package app

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/credential"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/stretchr/testify/require"
)

func TestRuntimeCredentialLeaseReaderHoldsCommittedLocalCredentialThroughConnectionUse(t *testing.T) {
	t.Run("lease spans synchronous consumer and blocking pool close", func(t *testing.T) {
		reader, provider, requestContext, identity, resource, binding, reference, keys := newRealLocalCredentialUseReader(t)
		session := newLocalCredentialUseSession(true, nil)
		factory := newLocalCredentialUseFactory(t, session)
		ctx, cancel := context.WithCancel(requestContext)
		defer cancel()

		var gotReference credentialmodule.RuntimeCredentialReference
		var fields map[string]string
		var callbackAuth semanticmodel.ConnectionAuth
		var callbackPassword string
		done := make(chan error, 1)
		go func() {
			done <- reader.WithCredential(ctx, identity, resource, func(got credentialmodule.RuntimeCredentialReference, values map[string]string) error {
				gotReference, fields = got, values
				snapshot, err := connectionbinding.NewLocalCredentialSnapshot(values, got.VersionID, time.Now().UTC(), time.Time{})
				if err != nil {
					return err
				}
				defer snapshot.Destroy()
				return factory.WithLocalConnection(ctx, binding, snapshot, semanticmodel.Connection{Kind: "postgres"}, func(connection semanticmodel.Connection) error {
					callbackAuth = connection.Auth
					callbackPassword, _ = connection.Auth["password"].(string)
					return nil
				})
			})
		}()

		select {
		case <-session.closeStarted:
		case <-time.After(3 * time.Second):
			t.Fatal("local pool did not reach synchronous close")
		}
		require.Equal(t, reference, gotReference)
		require.True(t, hasLocalPasswordStatement(session), "the actual target pool must prepare the committed password")
		require.Equal(t, "lease-reader-secret", callbackPassword)
		require.Equal(t, "lease-reader-secret", fields["password"], "the decrypted fields must remain available until pool cleanup completes")
		require.False(t, provider.leases[0].released, "serving-generation lease must span the consumer and pool close")

		cancel()
		require.False(t, provider.leases[0].released, "cancellation must not release the generation lease before cleanup returns")
		session.releaseClose()
		require.ErrorIs(t, <-done, context.Canceled)
		require.True(t, provider.leases[0].released, "confirmed synchronous cleanup permits releasing the lease")
		require.Empty(t, fields, "RuntimeResolver must clear decrypted fields after the consumer returns")
		require.Empty(t, callbackAuth, "TargetRuntimePool must clear callback-owned auth after WithLocalConnection")
		require.Equal(t, make([]byte, len(keys.lastPlaintext)), keys.lastPlaintext, "RuntimeResolver must clear plaintext after the consumer returns")
		require.Equal(t, 1, session.closeCalls)
	})

	t.Run("uncertain cleanup marker retains serving lease", func(t *testing.T) {
		reader, provider, requestContext, identity, resource, binding, _, keys := newRealLocalCredentialUseReader(t)
		session := newLocalCredentialUseSession(false, errors.New("private driver close detail"))
		factory := newLocalCredentialUseFactory(t, session)
		var fields map[string]string
		var factoryErr error
		err := reader.WithCredential(requestContext, identity, resource, func(reference credentialmodule.RuntimeCredentialReference, values map[string]string) error {
			fields = values
			snapshot, err := connectionbinding.NewLocalCredentialSnapshot(values, reference.VersionID, time.Now().UTC(), time.Time{})
			if err != nil {
				return err
			}
			defer snapshot.Destroy()
			factoryErr = factory.WithLocalConnection(requestContext, binding, snapshot, semanticmodel.Connection{Kind: "postgres"}, func(semanticmodel.Connection) error { return nil })
			return factoryErr
		})
		require.ErrorIs(t, factoryErr, analyticsruntime.ErrConnectionCleanupFailed)
		require.ErrorIs(t, err, credentialmodule.ErrRuntimeUnavailable)
		require.NotContains(t, err.Error(), "private driver close detail")
		require.False(t, provider.leases[0].released, "uncertain pool cleanup must retain the exact serving-generation lease")
		require.Empty(t, fields, "RuntimeResolver must clear fields after cleanup failure")
		require.Equal(t, make([]byte, len(keys.lastPlaintext)), keys.lastPlaintext)
	})

	t.Run("consumer panic cleanup marker retains serving lease", func(t *testing.T) {
		reader, provider, requestContext, identity, resource, binding, _, keys := newRealLocalCredentialUseReader(t)
		session := newLocalCredentialUseSession(false, nil)
		factory := newLocalCredentialUseFactory(t, session)
		var fields map[string]string
		var factoryErr error
		err := reader.WithCredential(requestContext, identity, resource, func(reference credentialmodule.RuntimeCredentialReference, values map[string]string) error {
			fields = values
			snapshot, err := connectionbinding.NewLocalCredentialSnapshot(values, reference.VersionID, time.Now().UTC(), time.Time{})
			if err != nil {
				return err
			}
			defer snapshot.Destroy()
			factoryErr = factory.WithLocalConnection(requestContext, binding, snapshot, semanticmodel.Connection{Kind: "postgres"}, func(semanticmodel.Connection) error {
				panic("private native consumer detail")
			})
			return factoryErr
		})
		require.ErrorIs(t, factoryErr, analyticsruntime.ErrConnectionCleanupFailed)
		require.ErrorIs(t, err, credentialmodule.ErrRuntimeUnavailable)
		require.NotContains(t, err.Error(), "private native consumer detail")
		require.False(t, provider.leases[0].released, "consumer panic leaves cleanup uncertain and retains the generation lease")
		require.Empty(t, fields)
		require.Equal(t, make([]byte, len(keys.lastPlaintext)), keys.lastPlaintext)
	})
}

func newRealLocalCredentialUseReader(t *testing.T) (runtimeCredentialLeaseReader, *foregroundCheckLeaseProvider, context.Context, projectgraph.ServingIdentity, credentialmodule.RuntimeResource, connectionbinding.TargetBinding, credentialmodule.RuntimeCredentialReference, *runtimeReaderKeyring) {
	t.Helper()
	authority, requestContext, identity, resource, binding, reference := newForegroundRuntimeCredentialAuthorityFixture(t)
	binding.Endpoint.SourceIdentity = "warehouse_source"
	require.NoError(t, binding.Validate())
	updatedDigest := binding.Evidence().EndpointConfigHash
	if reference.Scope.Destination != updatedDigest {
		stub := authority.evidence.releases.(sourceSchemaProvenanceStub)
		provenance := stub.provenance
		pin := provenance.Plan.Bindings[0]
		pin.EndpointConfigHash = updatedDigest
		plan := provenance.Plan
		plan.Bindings = []release.BindingEvidence{pin}
		if plan.GateEvidence != nil {
			gate := *plan.GateEvidence
			gate.BindingGeneration = release.BindingFingerprint(plan.Bindings)
			canonical, err := gate.Canonical()
			require.NoError(t, err)
			plan.GateEvidence = &canonical
		}
		updated, err := release.NewProvenance(release.ProvenanceInput{
			Artifact: provenance.Artifact, Candidate: provenance.Candidate,
			SourceRevision: provenance.SourceRevision, Plan: plan,
		})
		require.NoError(t, err)
		authority.evidence.releases = sourceSchemaProvenanceStub{provenance: updated}
		authority.evidence.commitments.(*committedCredentialGenerationStub).evidence.BindingFingerprint = release.BindingFingerprint(plan.Bindings)
		authority.bindings = &testCredentialBindingLookup{binding: binding}
		reference.Scope.Destination = updatedDigest
	}

	encryptionBinding := runtimeReaderEncryptionBinding(reference.Scope, reference.VersionID)
	repository := &runtimeReaderRepository{versions: map[string]credential.StoredVersion{
		reference.VersionID: runtimeReaderStoredVersion(encryptionBinding, `{"password":"lease-reader-secret"}`),
	}}
	keys := &runtimeReaderKeyring{deploymentID: encryptionBinding.DeploymentID}
	resolver, err := credential.NewRuntimeResolver(repository, keys, authority)
	require.NoError(t, err)
	provider := foregroundCheckProviderWithLeases(authority, 1)
	return runtimeCredentialLeaseReader{provider: provider, identity: identity, reader: resolver}, provider, requestContext, identity, resource, binding, reference, keys
}

func newLocalCredentialUseFactory(t *testing.T, session *localCredentialUseTargetSession) *analyticsduckdb.TargetRuntimePoolFactory {
	t.Helper()
	factory, err := analyticsduckdb.NewTargetRuntimePoolFactory(analyticsduckdb.TargetRuntimePoolFactoryConfig{
		Open:       func(context.Context) (analyticsduckdb.TargetRuntimeSession, error) { return session, nil },
		Limits:     analyticsduckdb.TargetRuntimeLimits{MemoryMaxBytes: 64 << 20, TempMaxBytes: 16 << 20, MaxThreads: 1},
		RequireTLS: true, ExtensionAdmission: postgresCredentialAdmission{},
	})
	require.NoError(t, err)
	return factory
}

func hasLocalPasswordStatement(session *localCredentialUseTargetSession) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	for _, statement := range session.statements {
		if strings.Contains(statement, "PASSWORD 'lease-reader-secret'") {
			return true
		}
	}
	return false
}

type localCredentialUseTargetSession struct {
	mu           sync.Mutex
	statements   []string
	closeCalls   int
	closeErr     error
	closeStarted chan struct{}
	closeRelease chan struct{}
	startOnce    sync.Once
	releaseOnce  sync.Once
}

func newLocalCredentialUseSession(blockClose bool, closeErr error) *localCredentialUseTargetSession {
	session := &localCredentialUseTargetSession{closeErr: closeErr}
	if blockClose {
		session.closeStarted = make(chan struct{})
		session.closeRelease = make(chan struct{})
	}
	return session
}

func (session *localCredentialUseTargetSession) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	session.mu.Lock()
	session.statements = append(session.statements, query)
	session.mu.Unlock()
	return driver.RowsAffected(1), nil
}

func (session *localCredentialUseTargetSession) Close() error {
	session.mu.Lock()
	session.closeCalls++
	session.mu.Unlock()
	if session.closeStarted != nil {
		session.startOnce.Do(func() { close(session.closeStarted) })
		<-session.closeRelease
	}
	return session.closeErr
}

func (session *localCredentialUseTargetSession) releaseClose() {
	if session.closeRelease != nil {
		session.releaseOnce.Do(func() { close(session.closeRelease) })
	}
}

var _ analyticsduckdb.TargetRuntimeSession = (*localCredentialUseTargetSession)(nil)
