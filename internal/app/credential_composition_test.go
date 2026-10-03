package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/credential"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type testCredentialBindingLookup struct {
	binding connectionbinding.TargetBinding
	scope   connectionbinding.BindingScope
	target  connectionbinding.TargetID
	conn    projectgraph.ResourceID
}

func (lookup *testCredentialBindingLookup) Binding(_ context.Context, scope connectionbinding.BindingScope, target connectionbinding.TargetID, connection projectgraph.ResourceID) (connectionbinding.TargetBinding, error) {
	lookup.scope, lookup.target, lookup.conn = scope, target, connection
	return lookup.binding, nil
}

func TestCredentialTargetBindingReaderProjectsOnlyExactNonSecretBindingEvidence(t *testing.T) {
	projectID := projectgraph.ResourceID("project_one")
	connectionID := projectgraph.ResourceID("connection_one")
	targetID := connectionbinding.TargetID("target_one")
	binding, err := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
		ID: "binding_one", TargetID: targetID, ConnectionID: connectionID, ConnectorKind: "postgres",
		AuthenticationMode:  connectionbinding.AuthenticationExternalBundle,
		Scope:               connectionbinding.BindingScope{ProjectID: projectID, Environment: "production"},
		Endpoint:            connectionbinding.EndpointConfig{Host: "database.example", Port: 5432, Database: "warehouse"},
		CredentialReference: connectionbinding.CredentialReference{ProjectID: projectID, Environment: "production", SecretPath: "/connections/warehouse", SecretKey: "password"},
		Enabled:             true, Now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	lookup := &testCredentialBindingLookup{binding: binding}
	reader := newCredentialTargetBindingReader(lookup)
	got, err := reader.ReadTargetConnectionBinding(t.Context(), targetID.String(), projectID.String(), "production", connectionID.String())
	if err != nil {
		t.Fatal(err)
	}
	if lookup.target != targetID || lookup.scope != (connectionbinding.BindingScope{ProjectID: projectID, Environment: "production"}) || lookup.conn != connectionID {
		t.Fatalf("lookup scope = %#v / %q / %q", lookup.scope, lookup.target, lookup.conn)
	}
	if got.TargetID != targetID.String() || got.ProjectID != projectID.String() || got.Environment != "production" ||
		got.ConnectionID != connectionID.String() || got.ConnectorKind != "postgres" ||
		got.AuthenticationMode != string(connectionbinding.AuthenticationExternalBundle) || got.EndpointConfigHash != binding.Evidence().EndpointConfigHash {
		t.Fatalf("binding projection = %#v", got)
	}
	if got.EndpointConfigHash == "" || got.EndpointConfigHash == "database.example" {
		t.Fatal("binding projection exposed endpoint configuration instead of only its digest")
	}
}

func TestCredentialTargetBindingReaderRejectsMismatchedLookupResult(t *testing.T) {
	projectID := projectgraph.ResourceID("project_one")
	connectionID := projectgraph.ResourceID("connection_one")
	targetID := connectionbinding.TargetID("target_one")
	binding, err := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
		ID: "binding_one", TargetID: "target_other", ConnectionID: connectionID, ConnectorKind: "postgres",
		AuthenticationMode:  connectionbinding.AuthenticationExternalBundle,
		Scope:               connectionbinding.BindingScope{ProjectID: projectID, Environment: "production"},
		Endpoint:            connectionbinding.EndpointConfig{Host: "database.example", Port: 5432, Database: "warehouse"},
		CredentialReference: connectionbinding.CredentialReference{ProjectID: projectID, Environment: "production", SecretPath: "/connections/warehouse", SecretKey: "password"},
		Enabled:             true, Now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := newCredentialTargetBindingReader(&testCredentialBindingLookup{binding: binding})
	if _, err := reader.ReadTargetConnectionBinding(t.Context(), targetID.String(), projectID.String(), "production", connectionID.String()); err != connectionbinding.ErrBindingNotFound {
		t.Fatalf("mismatched binding error = %v", err)
	}
}

func TestCredentialValidationProbeUsesExactBindingAndPolicyDigest(t *testing.T) {
	projectID := projectgraph.ResourceID("project_one")
	connectionID := projectgraph.ResourceID("connection_one")
	targetID := connectionbinding.TargetID("target_one")
	binding, err := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
		ID: "binding_one", TargetID: targetID, ConnectionID: connectionID, ConnectorKind: "postgres",
		AuthenticationMode:  connectionbinding.AuthenticationExternalBundle,
		Scope:               connectionbinding.BindingScope{ProjectID: projectID, Environment: "production"},
		Endpoint:            connectionbinding.EndpointConfig{Host: "database.example", Port: 5432, Database: "warehouse", TLSMode: "verify-full"},
		CredentialReference: connectionbinding.CredentialReference{ProjectID: projectID, Environment: "production", SecretPath: "/connections/warehouse", SecretKey: "password"},
		Enabled:             true, Now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	lookup := &testCredentialBindingLookup{binding: binding}
	analytics := &testCredentialAnalyticsProbe{policy: "target-postgres-password-read-only-probe-v1/target-production/explicit-private-target-egress-v1"}
	probe := newCredentialValidationProbe(lookup, analytics)
	resource := credential.Resource{ScopeKind: "connection", TargetID: targetID.String(), ProjectID: projectID.String(), Environment: "production", ResourceID: connectionID.String()}
	scope := credential.Scope{
		Resource: resource, OwnerID: "customer_one", Purpose: "connection-authentication",
		Provider: "postgres", Destination: binding.Evidence().EndpointConfigHash,
	}
	target, err := probe.ResolveValidationTarget(t.Context(), resource, scope)
	if err != nil {
		t.Fatalf("resolve target: %v", err)
	}
	if target.BindingID != binding.ID.String() || target.BindingRevision != binding.Revision || target.Scope != scope || len(target.ConfigurationDigest) != 71 {
		t.Fatalf("validation target = %#v", target)
	}
	if err := probe.ProbeCredential(t.Context(), target, "22222222-2222-4222-8222-222222222222", map[string]string{"password": "draft-secret"}); err != nil {
		t.Fatalf("probe credential: %v", err)
	}
	if analytics.binding.ID != binding.ID || analytics.binding.Revision != binding.Revision ||
		analytics.version != "22222222-2222-4222-8222-222222222222" || analytics.fields["password"] != "draft-secret" {
		t.Fatalf("analytics probe input = %#v / %q / %#v", analytics.binding, analytics.version, analytics.fields)
	}
}

func TestCredentialValidationProbeRejectsBindingDriftBeforeUsingDraftSecret(t *testing.T) {
	projectID := projectgraph.ResourceID("project_one")
	connectionID := projectgraph.ResourceID("connection_one")
	targetID := connectionbinding.TargetID("target_one")
	created := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	baseInput := connectionbinding.TargetBindingInput{
		ID: "binding_one", TargetID: targetID, ConnectionID: connectionID, ConnectorKind: "postgres",
		AuthenticationMode:  connectionbinding.AuthenticationExternalBundle,
		Scope:               connectionbinding.BindingScope{ProjectID: projectID, Environment: "production"},
		Endpoint:            connectionbinding.EndpointConfig{Host: "database.example", Port: 5432, Database: "warehouse", TLSMode: "verify-full"},
		CredentialReference: connectionbinding.CredentialReference{ProjectID: projectID, Environment: "production", SecretPath: "/connections/warehouse", SecretKey: "password"},
		Enabled:             true, Now: created,
	}
	first, err := connectionbinding.NewTargetBinding(baseInput)
	if err != nil {
		t.Fatal(err)
	}
	changedInput := baseInput
	changedInput.Endpoint.Host = "other-database.example"
	second, err := connectionbinding.NewTargetBinding(changedInput)
	if err != nil {
		t.Fatal(err)
	}
	second.Revision = first.Revision + 1
	lookup := &sequencedCredentialBindingLookup{bindings: []connectionbinding.TargetBinding{first, second}}
	analytics := &testCredentialAnalyticsProbe{policy: "target-postgres-password-read-only-probe-v1/target-production/explicit-private-target-egress-v1"}
	probe := newCredentialValidationProbe(lookup, analytics)
	resource := credential.Resource{ScopeKind: "connection", TargetID: targetID.String(), ProjectID: projectID.String(), Environment: "production", ResourceID: connectionID.String()}
	scope := credential.Scope{
		Resource: resource, OwnerID: "customer_one", Purpose: "connection-authentication",
		Provider: "postgres", Destination: first.Evidence().EndpointConfigHash,
	}
	target, err := probe.ResolveValidationTarget(t.Context(), resource, scope)
	if err != nil {
		t.Fatalf("resolve initial target: %v", err)
	}
	err = probe.ProbeCredential(t.Context(), target, "22222222-2222-4222-8222-222222222222", map[string]string{"password": "must-not-reach-new-destination"})
	if !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("probe after target drift = %v, want conflict", err)
	}
	if analytics.calls != 0 {
		t.Fatalf("analytics probe calls = %d, want zero after target drift", analytics.calls)
	}
}

func TestCredentialValidationProbeUsesSameBindingSnapshotForDigestAndProbe(t *testing.T) {
	projectID := projectgraph.ResourceID("project_one")
	connectionID := projectgraph.ResourceID("connection_one")
	targetID := connectionbinding.TargetID("target_one")
	created := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	baseInput := connectionbinding.TargetBindingInput{
		ID: "binding_one", TargetID: targetID, ConnectionID: connectionID, ConnectorKind: "postgres",
		AuthenticationMode:  connectionbinding.AuthenticationExternalBundle,
		Scope:               connectionbinding.BindingScope{ProjectID: projectID, Environment: "production"},
		Endpoint:            connectionbinding.EndpointConfig{Host: "database.example", Port: 5432, Database: "warehouse", TLSMode: "verify-full"},
		CredentialReference: connectionbinding.CredentialReference{ProjectID: projectID, Environment: "production", SecretPath: "/connections/warehouse", SecretKey: "password"},
		Enabled:             true, Now: created,
	}
	first, err := connectionbinding.NewTargetBinding(baseInput)
	if err != nil {
		t.Fatal(err)
	}
	changedInput := baseInput
	changedInput.Endpoint.Host = "other-database.example"
	second, err := connectionbinding.NewTargetBinding(changedInput)
	if err != nil {
		t.Fatal(err)
	}
	second.Revision = first.Revision + 1
	resource := credential.Resource{ScopeKind: "connection", TargetID: targetID.String(), ProjectID: projectID.String(), Environment: "production", ResourceID: connectionID.String()}
	scope := credential.Scope{
		Resource: resource, OwnerID: "customer_one", Purpose: "connection-authentication",
		Provider: "postgres", Destination: first.Evidence().EndpointConfigHash,
	}
	policy := "target-postgres-password-read-only-probe-v1/target-production/explicit-private-target-egress-v1"
	expected, err := credentialValidationTargetForBinding(first, resource, scope, policy)
	if err != nil {
		t.Fatalf("build expected target: %v", err)
	}
	lookup := &sequencedCredentialBindingLookup{bindings: []connectionbinding.TargetBinding{first, second}}
	analytics := &testCredentialAnalyticsProbe{policy: policy}
	probe := newCredentialValidationProbe(lookup, analytics)
	if err := probe.ProbeCredential(t.Context(), expected, "22222222-2222-4222-8222-222222222222", map[string]string{"password": "draft-secret"}); err != nil {
		t.Fatalf("probe exact target: %v", err)
	}
	if lookup.calls != 1 || analytics.calls != 1 || analytics.binding.ID != first.ID ||
		analytics.binding.Evidence().EndpointConfigHash != expected.Scope.Destination {
		t.Fatalf("binding reads=%d analytics calls=%d used binding=%#v", lookup.calls, analytics.calls, analytics.binding)
	}
}

func TestCredentialValidationConfigurationDigestTracksBindingPolicyAndDisabledState(t *testing.T) {
	projectID := projectgraph.ResourceID("project_one")
	binding, err := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
		ID: "binding_one", TargetID: "target_one", ConnectionID: "connection_one", ConnectorKind: "postgres",
		AuthenticationMode:  connectionbinding.AuthenticationExternalBundle,
		Scope:               connectionbinding.BindingScope{ProjectID: projectID, Environment: "production"},
		Endpoint:            connectionbinding.EndpointConfig{Host: "database.example", Port: 5432, Database: "warehouse", TLSMode: "verify-full"},
		CredentialReference: connectionbinding.CredentialReference{ProjectID: projectID, Environment: "production", SecretPath: "/connections/warehouse", SecretKey: "password"},
		Enabled:             true, Now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := "target-postgres-password-read-only-probe-v1/target-production/explicit-private-target-egress-v1"
	base, err := credentialValidationConfigurationDigest(binding, policy)
	if err != nil {
		t.Fatal(err)
	}
	changed := []struct {
		name    string
		binding connectionbinding.TargetBinding
		policy  string
	}{
		{name: "binding identity", binding: func() connectionbinding.TargetBinding { v := binding; v.ID = "binding_two"; return v }(), policy: policy},
		{name: "binding revision", binding: func() connectionbinding.TargetBinding { v := binding; v.Revision++; return v }(), policy: policy},
		{name: "endpoint", binding: func() connectionbinding.TargetBinding { v := binding; v.Endpoint.TLSMode = "require"; return v }(), policy: policy},
		{name: "credential reference", binding: func() connectionbinding.TargetBinding {
			v := binding
			v.CredentialReference.SecretPath = "/connections/next"
			return v
		}(), policy: policy},
		{name: "authentication mode", binding: func() connectionbinding.TargetBinding {
			v, _ := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{ID: binding.ID, TargetID: binding.TargetID, ConnectionID: binding.ConnectionID, ConnectorKind: binding.ConnectorKind, AuthenticationMode: connectionbinding.AuthenticationNone, Scope: binding.Scope, Endpoint: binding.Endpoint, Enabled: binding.Enabled, Now: binding.CreatedAt})
			return v
		}(), policy: policy},
		{name: "enabled state", binding: func() connectionbinding.TargetBinding {
			v, _ := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
				ID: binding.ID, TargetID: binding.TargetID, ConnectionID: binding.ConnectionID,
				ConnectorKind: binding.ConnectorKind, AuthenticationMode: binding.AuthenticationMode,
				Scope: binding.Scope, Endpoint: binding.Endpoint, CredentialReference: binding.CredentialReference,
				Enabled: false, Now: binding.CreatedAt,
			})
			return v
		}(), policy: policy},
		{name: "probe policy", binding: binding, policy: policy + "/revision-2"},
	}
	for _, test := range changed {
		t.Run(test.name, func(t *testing.T) {
			digest, err := credentialValidationConfigurationDigest(test.binding, test.policy)
			if err != nil {
				t.Fatal(err)
			}
			if digest == base {
				t.Fatal("configuration change left the validation digest unchanged")
			}
		})
	}
}

type testCredentialAnalyticsProbe struct {
	policy  string
	binding connectionbinding.TargetBinding
	version string
	fields  map[string]string
	calls   int
}

func (probe *testCredentialAnalyticsProbe) CredentialProbePolicyIdentity() string {
	return probe.policy
}

func (probe *testCredentialAnalyticsProbe) ProbeCredential(_ context.Context, binding connectionbinding.TargetBinding, version string, fields map[string]string) error {
	probe.calls++
	probe.binding, probe.version = binding, version
	probe.fields = make(map[string]string, len(fields))
	for name, value := range fields {
		probe.fields[name] = value
	}
	return nil
}

type sequencedCredentialBindingLookup struct {
	bindings []connectionbinding.TargetBinding
	calls    int
}

func (lookup *sequencedCredentialBindingLookup) Binding(context.Context, connectionbinding.BindingScope, connectionbinding.TargetID, projectgraph.ResourceID) (connectionbinding.TargetBinding, error) {
	if lookup.calls >= len(lookup.bindings) {
		return lookup.bindings[len(lookup.bindings)-1], nil
	}
	binding := lookup.bindings[lookup.calls]
	lookup.calls++
	return binding, nil
}
