package app

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestRefreshCandidateLocalEvidencePreservesCommittedPinWithoutDecrypting(t *testing.T) {
	factory, job, resource, reference, _, keys, _ := refreshCredentialFixture(t)
	connections, err := newRefreshCandidateConnections(t.Context(), candidateConnectionLeaser{}, factory, job)
	require.NoError(t, err)
	request := deploymentmodule.CandidateConnectionRequest{
		CandidateID: "candidate:refresh", Actor: job.PrincipalID, TargetID: resource.TargetID, Identity: job.Identity,
		Requirements: []deploymentmodule.CandidateConnectionRequirement{{ConnectionID: projectgraph.ResourceID(resource.ResourceID), ConnectorKind: "postgres"}},
	}
	request.Identity.GenerationID = "candidate-generation"
	evidence, err := connections.Resolve(t.Context(), request)
	require.NoError(t, err)
	require.Len(t, evidence, 1)
	require.Equal(t, reference.VersionID, evidence[0].CredentialVersionID)
	require.Empty(t, evidence[0].ProviderVersion)
	require.Equal(t, reference.Scope.Destination, evidence[0].EndpointConfigHash)
	require.Zero(t, keys.decryptCalls)
	require.ErrorIs(t, factory.checkBaseCredentials(t.Context(), job), errRefreshLocalCredentialUnsupported)
	module, err := analyticsmodule.Build(t.Context(), analyticsmodule.Config{CredentialTargetID: resource.TargetID, CredentialEnvironment: job.Identity.Environment, DisableProcessEnvironment: true, RuntimeCacheEntries: 4, RuntimeCacheBytes: 1 << 20, NodeCacheEntries: 8, NodeCacheBytes: 2 << 20})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, module.Close()) })
	connections.base.module = module
	leases, err := connections.Acquire(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, evidence, leases.Evidence(), "planning and acquisition must fingerprint the same exact local pin")
	require.Zero(t, keys.decryptCalls, "registration must not decrypt before a source callback")
	require.NoError(t, leases.Close())
}

func TestRefreshCandidateLocalConnectionUsesPinnedCredentialAndPreservesCleanup(t *testing.T) {
	for _, cleanupFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "cleanup failure"}[cleanupFailure], func(t *testing.T) {
			factory, job, resource, _, _, keys, _ := refreshCredentialFixture(t)
			connections, err := newRefreshCandidateConnections(t.Context(), candidateConnectionLeaser{}, factory, job)
			require.NoError(t, err)
			var closeErr error
			if cleanupFailure {
				closeErr = errors.New("private candidate close diagnostic")
			}
			session := newLocalCredentialUseSession(false, closeErr)
			target := newLocalCredentialUseFactory(t, session)
			connections.useLocal = target.WithLocalConnection
			pin := connections.pins[resource.ResourceID]
			resolver := candidateLocalConnectionResolver{connections: connections, pin: pin}
			var retained semanticmodel.ConnectionAuth
			calls := 0
			err = resolver.WithConnection(t.Context(), resource.ResourceID, semanticmodel.Connection{Kind: "postgres"}, func(connection semanticmodel.Connection) error {
				calls++
				retained = connection.Auth
				require.Equal(t, "queued-refresh-secret", retained["password"])
				require.Equal(t, pin.binding.Endpoint.Host, connection.Host)
				return nil
			})
			if cleanupFailure {
				require.ErrorIs(t, err, analyticsruntime.ErrConnectionCleanupFailed)
				require.NotContains(t, err.Error(), "private")
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, calls)
			require.Equal(t, 1, session.closeCalls)
			require.Empty(t, retained)
			require.Equal(t, 1, keys.decryptCalls)
			require.Equal(t, make([]byte, len(keys.lastPlaintext)), keys.lastPlaintext)
		})
	}
}

func TestRefreshCandidateLocalConnectionRejectsDriftBeforeSecretUse(t *testing.T) {
	for _, change := range []string{"version", "endpoint", "disabled", "token", "outside scope"} {
		t.Run(change, func(t *testing.T) {
			factory, job, resource, _, _, keys, tokens := refreshCredentialFixture(t)
			connections, err := newRefreshCandidateConnections(t.Context(), candidateConnectionLeaser{}, factory, job)
			require.NoError(t, err)
			pin := connections.pins[resource.ResourceID]
			switch change {
			case "version":
				pin.reference.VersionID = "0070a4f4-5dcb-4d26-a69a-dac930858573"
			case "endpoint":
				pin.binding.Endpoint.Host = "other.example"
			case "disabled":
				pin.binding.Enabled = false
				pin.binding.Health = connectionbinding.HealthDisabled
			case "token":
				tokens.revoked = true
			case "outside scope":
				connections.authority.queued.Permissions = connections.authority.queued.Permissions[:1]
			}
			opened := false
			connections.useLocal = func(context.Context, connectionbinding.TargetBinding, connectionbinding.CredentialSnapshot, semanticmodel.Connection, func(semanticmodel.Connection) error) error {
				opened = true
				return nil
			}
			resolver := candidateLocalConnectionResolver{connections: connections, pin: pin}
			err = resolver.WithConnection(t.Context(), resource.ResourceID, semanticmodel.Connection{Kind: "postgres"}, func(semanticmodel.Connection) error { t.Fatal("drift reached consumer"); return nil })
			require.Error(t, err)
			require.False(t, opened)
			require.Zero(t, keys.decryptCalls)
		})
	}
}

func TestRefreshCandidateRejectsDroppedOrUncapturedConnection(t *testing.T) {
	factory, job, resource, _, _, keys, _ := refreshCredentialFixture(t)
	connections, err := newRefreshCandidateConnections(t.Context(), candidateConnectionLeaser{}, factory, job)
	require.NoError(t, err)
	request := deploymentmodule.CandidateConnectionRequest{CandidateID: "candidate:refresh", Actor: job.PrincipalID, TargetID: resource.TargetID, Identity: job.Identity}
	_, err = connections.Resolve(t.Context(), request)
	require.ErrorIs(t, err, credentialmodule.ErrRuntimeConflict)
	request.Requirements = []deploymentmodule.CandidateConnectionRequirement{{ConnectionID: "other:connection", ConnectorKind: "postgres"}}
	_, err = connections.Resolve(t.Context(), request)
	require.ErrorIs(t, err, credentialmodule.ErrRuntimeForbidden)
	require.Zero(t, keys.decryptCalls)
}

func TestRefreshCandidateRejectsBindingLookupTupleSubstitution(t *testing.T) {
	for _, field := range []string{"target", "project", "environment", "connection"} {
		t.Run(field, func(t *testing.T) {
			factory, job, _, _, _, keys, _ := refreshCredentialFixture(t)
			binding := factory.authority.bindings.(*testCredentialBindingLookup).binding
			other := binding
			switch field {
			case "target":
				other.TargetID = "other-target"
			case "project":
				other.Scope.ProjectID = "other-project"
			case "environment":
				other.Scope.Environment = "other-environment"
			case "connection":
				other.ConnectionID = "other-connection"
			}
			require.NoError(t, other.Validate())
			factory.authority.bindings = &candidateTupleSubstitutionLookup{binding: binding, other: other}
			_, err := newRefreshCandidateConnections(t.Context(), candidateConnectionLeaser{}, factory, job)
			require.ErrorIs(t, err, credentialmodule.ErrRuntimeConflict)
			require.Zero(t, keys.decryptCalls)
		})
	}
}

type candidateTupleSubstitutionLookup struct {
	binding connectionbinding.TargetBinding
	other   connectionbinding.TargetBinding
	calls   int
}

func (lookup *candidateTupleSubstitutionLookup) Binding(context.Context, connectionbinding.BindingScope, connectionbinding.TargetID, projectgraph.ResourceID) (connectionbinding.TargetBinding, error) {
	lookup.calls++
	if lookup.calls > 1 {
		return lookup.other, nil
	}
	return lookup.binding, nil
}
