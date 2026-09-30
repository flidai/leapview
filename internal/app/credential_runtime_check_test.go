package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/credential"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

const runtimeCheckVersion = "fb1a93e4-c9e4-4f50-90a4-8c2f7e2bfa7d"

func TestLocalRuntimeCredentialCheckMatchesExactScopeAndPinInsideAdmission(t *testing.T) {
	check, binding, identity, reader, analytics := runtimeCheckFixture(t)
	got, err := check.check(t.Context(), identity, binding, runtimeCheckVersion)
	require.NoError(t, err)
	require.Equal(t, connectionbinding.CredentialIdentity{CredentialVersionID: runtimeCheckVersion}, got)
	require.Equal(t, identity, reader.identity)
	require.Equal(t, reader.reference.Scope.Resource, reader.resource)
	require.Equal(t, 1, analytics.preparations)
	require.Equal(t, "runtime-check-secret", analytics.password)
	require.Empty(t, reader.retainedFields)
}

func TestLocalRuntimeCredentialCheckRejectsReferenceSubstitutionBeforePreparation(t *testing.T) {
	for name, mutate := range map[string]func(*credential.RuntimeCredentialReference){
		"version":     func(r *credential.RuntimeCredentialReference) { r.VersionID = "0070a4f4-5dcb-4d26-a69a-dac930858573" },
		"owner":       func(r *credential.RuntimeCredentialReference) { r.Scope.OwnerID = "other-customer" },
		"kind":        func(r *credential.RuntimeCredentialReference) { r.Scope.Resource.ScopeKind = "agent" },
		"target":      func(r *credential.RuntimeCredentialReference) { r.Scope.Resource.TargetID = "other-target" },
		"project":     func(r *credential.RuntimeCredentialReference) { r.Scope.Resource.ProjectID = "other-project" },
		"environment": func(r *credential.RuntimeCredentialReference) { r.Scope.Resource.Environment = "other-env" },
		"connection":  func(r *credential.RuntimeCredentialReference) { r.Scope.Resource.ResourceID = "other-connection" },
		"purpose":     func(r *credential.RuntimeCredentialReference) { r.Scope.Purpose = "agent-provider" },
		"provider":    func(r *credential.RuntimeCredentialReference) { r.Scope.Provider = "mysql" },
		"destination": func(r *credential.RuntimeCredentialReference) {
			r.Scope.Destination = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		},
	} {
		t.Run(name, func(t *testing.T) {
			check, binding, identity, reader, analytics := runtimeCheckFixture(t)
			mutate(&reader.reference)
			got, err := check.check(t.Context(), identity, binding, runtimeCheckVersion)
			require.Error(t, err)
			require.Zero(t, got)
			require.Zero(t, analytics.preparations)
			require.Empty(t, reader.retainedFields)
		})
	}
}

func TestLocalRuntimeCredentialCheckRejectsUnscopedRequestsBeforeReader(t *testing.T) {
	for _, mode := range []string{"target", "environment", "project", "zero version", "missing reader", "missing owner", "missing bindings", "missing analytics"} {
		t.Run(mode, func(t *testing.T) {
			check, binding, identity, reader, analytics := runtimeCheckFixture(t)
			version := runtimeCheckVersion
			switch mode {
			case "target":
				binding.TargetID = "other-target"
			case "environment":
				identity.Environment = "other-env"
			case "project":
				identity.ProjectID = "other-project"
			case "zero version":
				version = "00000000-0000-0000-0000-000000000000"
			case "missing reader":
				check.reader = (*runtimeCheckReader)(nil)
			case "missing owner":
				check.owners = nil
			case "missing bindings":
				check.bindings = nil
			case "missing analytics":
				check.analytics = nil
			}
			_, err := check.check(t.Context(), identity, binding, version)
			require.Error(t, err)
			require.Zero(t, reader.calls)
			require.Zero(t, analytics.preparations)
		})
	}
}

func TestLocalRuntimeCredentialCheckRejectsBindingDriftAndOwnerFailure(t *testing.T) {
	for _, mode := range []string{"binding drift before read", "binding drift during read", "owner unavailable", "owner changed during read", "reader denied"} {
		t.Run(mode, func(t *testing.T) {
			check, binding, identity, reader, analytics := runtimeCheckFixture(t)
			lookup := check.bindings.(*testCredentialBindingLookup)
			owner := check.owners.(*runtimeCheckOwner)
			switch mode {
			case "binding drift before read":
				lookup.binding.Revision++
			case "binding drift during read":
				reader.beforeConsumer = func() { lookup.binding.Endpoint.Database = "another" }
			case "owner unavailable":
				owner.err = errors.New("database error with runtime-check-secret")
			case "owner changed during read":
				reader.beforeConsumer = func() { owner.owner = "replacement-owner" }
			case "reader denied":
				reader.err = errors.New("runtime-check-secret in provider diagnostics")
			}
			got, err := check.check(t.Context(), identity, binding, runtimeCheckVersion)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "runtime-check-secret")
			require.Zero(t, got)
			require.Zero(t, analytics.preparations)
		})
	}
}

func TestLocalRuntimeCredentialCheckDoesNotReportSuccessAfterScopeDriftsDuringPoolUse(t *testing.T) {
	for _, mode := range []string{"owner", "binding"} {
		t.Run(mode, func(t *testing.T) {
			check, binding, identity, _, analytics := runtimeCheckFixture(t)
			analytics.afterPreparation = func() {
				if mode == "owner" {
					check.owners.(*runtimeCheckOwner).owner = "replacement-owner"
				} else {
					check.bindings.(*testCredentialBindingLookup).binding.Revision++
				}
			}
			got, err := check.check(t.Context(), identity, binding, runtimeCheckVersion)
			require.Error(t, err)
			require.Zero(t, got)
			require.Equal(t, 1, analytics.preparations)
		})
	}
}

func TestLocalRuntimeCredentialCheckPreservesOwnerReadCancellation(t *testing.T) {
	check, binding, identity, reader, analytics := runtimeCheckFixture(t)
	check.owners.(*runtimeCheckOwner).err = context.Canceled
	_, err := check.check(t.Context(), identity, binding, runtimeCheckVersion)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, reader.calls)
	require.Zero(t, analytics.preparations)
}

type runtimeCheckOwner struct {
	owner string
	err   error
}

func (o *runtimeCheckOwner) CustomerOwner(context.Context) (string, error) { return o.owner, o.err }

type runtimeCheckReader struct {
	reference      credential.RuntimeCredentialReference
	identity       projectgraph.ServingIdentity
	resource       credential.Resource
	admitted       *bool
	calls          int
	retainedFields map[string]string
	beforeConsumer func()
	err            error
}

func (r *runtimeCheckReader) WithCredential(ctx context.Context, identity projectgraph.ServingIdentity, resource credential.Resource, consumer func(credential.RuntimeCredentialReference, map[string]string) error) error {
	r.calls++
	if !*r.admitted {
		return errors.New("reader called outside module admission")
	}
	r.identity, r.resource = identity, resource
	if r.err != nil {
		return r.err
	}
	if r.beforeConsumer != nil {
		r.beforeConsumer()
	}
	r.retainedFields = map[string]string{"password": "runtime-check-secret"}
	defer clear(r.retainedFields)
	return consumer(r.reference, r.retainedFields)
}

type runtimeCheckAnalytics struct {
	admitted         bool
	preparations     int
	password         string
	afterPreparation func()
}

func (a *runtimeCheckAnalytics) CheckLocalRuntimeCredential(ctx context.Context, _ connectionbinding.TargetBinding, version string, read func(context.Context, func(map[string]string) error) error) (connectionbinding.CredentialIdentity, error) {
	a.admitted = true
	defer func() { a.admitted = false }()
	err := read(ctx, func(fields map[string]string) error {
		a.preparations++
		a.password = fields["password"]
		if a.afterPreparation != nil {
			a.afterPreparation()
		}
		return nil
	})
	if err != nil {
		return connectionbinding.CredentialIdentity{}, err
	}
	return connectionbinding.CredentialIdentity{CredentialVersionID: version}, nil
}

func runtimeCheckFixture(t *testing.T) (localRuntimeCredentialCheck, connectionbinding.TargetBinding, projectgraph.ServingIdentity, *runtimeCheckReader, *runtimeCheckAnalytics) {
	t.Helper()
	binding, err := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
		ID: "binding_one", TargetID: "target_one", ConnectionID: "warehouse", ConnectorKind: "postgres",
		AuthenticationMode:  connectionbinding.AuthenticationExternalBundle,
		Scope:               connectionbinding.BindingScope{ProjectID: "historical_project", Environment: "production"},
		Endpoint:            connectionbinding.EndpointConfig{Host: "warehouse.example", Port: 5432, Database: "warehouse", TLSMode: "require"},
		CredentialReference: connectionbinding.CredentialReference{ProjectID: "provider_project", Environment: "production", SecretPath: "/warehouse", SecretKey: "password"},
		Enabled:             true, Now: time.Now(),
	})
	require.NoError(t, err)
	identity, err := projectgraph.NewServingIdentity(binding.Scope.ProjectID, binding.Scope.Environment, "generation-retained")
	require.NoError(t, err)
	analytics := &runtimeCheckAnalytics{}
	reader := &runtimeCheckReader{admitted: &analytics.admitted, reference: credential.RuntimeCredentialReference{
		VersionID: runtimeCheckVersion,
		Scope: credential.Scope{
			Resource: credential.Resource{ScopeKind: "connection", TargetID: "target_one", ProjectID: "historical_project", Environment: "production", ResourceID: "warehouse"},
			OwnerID:  "customer-one", Purpose: "connection-authentication", Provider: "postgres", Destination: binding.Evidence().EndpointConfigHash,
		},
	}}
	check := localRuntimeCredentialCheck{
		targetID: binding.TargetID.String(), environment: binding.Scope.Environment,
		reader: reader, analytics: analytics, owners: &runtimeCheckOwner{owner: "customer-one"},
		bindings: &testCredentialBindingLookup{binding: binding},
	}
	return check, binding, identity, reader, analytics
}
