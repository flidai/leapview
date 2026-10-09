package app

import (
	"testing"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

func sourcePinFixture() (credential.ActivationRequestRecord, connectionbinding.RuntimeBindingRequest, connectionbinding.TargetBinding) {
	binding := connectionbinding.TargetBinding{ID: "binding", TargetID: "target", ConnectionID: "connection:source", ConnectorKind: "postgres", AuthenticationMode: connectionbinding.AuthenticationExternalBundle, Enabled: true, Revision: 3, Scope: connectionbinding.BindingScope{ProjectID: "project:test", Environment: "prod"}, Endpoint: connectionbinding.EndpointConfig{Host: "postgres", Port: 5432, Database: "source"}}
	version := uuid.NewString()
	hash := binding.Evidence().EndpointConfigHash
	request := connectionbinding.RuntimeBindingRequest{Actor: "operator", TargetID: binding.TargetID, Identity: projectgraph.ServingIdentity{ProjectID: binding.Scope.ProjectID, Environment: binding.Scope.Environment, GenerationID: uuid.NewString()}}
	row := credential.ActivationRequestRecord{State: "preparing", Request: credential.ActivationRequest{VersionID: version, ExpectedBindingRevision: 3}, Receipt: credential.ValidationReceipt{ActorID: request.Actor, BindingID: binding.ID.String(), BindingRevision: 3, ConfigurationDigest: hash, Binding: encryption.Binding{VersionID: version, DeploymentID: "target", ScopeKind: "connection", TargetID: "target", ProjectID: "project:test", Environment: "prod", ResourceID: "connection:source", Provider: "postgres", Destination: hash}}}
	return row, request, binding
}
func TestSourceCredentialOperationPinCannotEscapeItsReceipt(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*credential.ActivationRequestRecord, *connectionbinding.RuntimeBindingRequest, *connectionbinding.TargetBinding)
	}{
		{"actor", func(_ *credential.ActivationRequestRecord, r *connectionbinding.RuntimeBindingRequest, _ *connectionbinding.TargetBinding) {
			r.Actor = "other"
		}},
		{"target", func(_ *credential.ActivationRequestRecord, r *connectionbinding.RuntimeBindingRequest, _ *connectionbinding.TargetBinding) {
			r.TargetID = "other"
		}},
		{"project", func(_ *credential.ActivationRequestRecord, r *connectionbinding.RuntimeBindingRequest, _ *connectionbinding.TargetBinding) {
			r.Identity.ProjectID = "other"
		}},
		{"environment", func(_ *credential.ActivationRequestRecord, r *connectionbinding.RuntimeBindingRequest, _ *connectionbinding.TargetBinding) {
			r.Identity.Environment = "other"
		}},
		{"state", func(r *credential.ActivationRequestRecord, _ *connectionbinding.RuntimeBindingRequest, _ *connectionbinding.TargetBinding) {
			r.State = "aborted"
		}},
		{"version", func(r *credential.ActivationRequestRecord, _ *connectionbinding.RuntimeBindingRequest, _ *connectionbinding.TargetBinding) {
			r.Request.VersionID = uuid.NewString()
		}},
		{"binding", func(_ *credential.ActivationRequestRecord, _ *connectionbinding.RuntimeBindingRequest, b *connectionbinding.TargetBinding) {
			b.ID = "other"
		}},
		{"revision", func(_ *credential.ActivationRequestRecord, _ *connectionbinding.RuntimeBindingRequest, b *connectionbinding.TargetBinding) {
			b.Revision++
		}},
		{"endpoint", func(_ *credential.ActivationRequestRecord, _ *connectionbinding.RuntimeBindingRequest, b *connectionbinding.TargetBinding) {
			b.Endpoint.Database = "other"
		}},
		{"disabled", func(_ *credential.ActivationRequestRecord, _ *connectionbinding.RuntimeBindingRequest, b *connectionbinding.TargetBinding) {
			b.Enabled = false
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			row, request, binding := sourcePinFixture()
			test.change(&row, &request, &binding)
			if version, err := sourceReceiptPin(row, request, binding); err == nil {
				t.Fatalf("changed %s accepted version %q", test.name, version)
			}
		})
	}
	row, request, binding := sourcePinFixture()
	if got, err := sourceReceiptPin(row, request, binding); err != nil || got != row.Request.VersionID {
		t.Fatalf("exact pin = %q, %v", got, err)
	}
}
func TestSourceCredentialCommittedPinRetainsExactConfiguration(t *testing.T) {
	row, _, binding := sourcePinFixture()
	pin := analyticsmodule.ActiveRuntimeBindingEvidence{BindingID: binding.ID, ConnectionID: binding.ConnectionID, ConnectorKind: binding.ConnectorKind, Revision: binding.Revision, EndpointConfigHash: binding.Evidence().EndpointConfigHash, CredentialVersionID: row.Request.VersionID}
	if got, err := sourceCommittedPin([]analyticsmodule.ActiveRuntimeBindingEvidence{pin}, binding); err != nil || got != row.Request.VersionID {
		t.Fatalf("committed pin = %q, %v", got, err)
	}
	for _, change := range []func(*analyticsmodule.ActiveRuntimeBindingEvidence){func(p *analyticsmodule.ActiveRuntimeBindingEvidence) { p.Revision++ }, func(p *analyticsmodule.ActiveRuntimeBindingEvidence) { p.BindingID = "other" }, func(p *analyticsmodule.ActiveRuntimeBindingEvidence) { p.EndpointConfigHash = "other" }, func(p *analyticsmodule.ActiveRuntimeBindingEvidence) { p.ValidatedVersion = "provider-v1" }} {
		bad := pin
		change(&bad)
		if _, err := sourceCommittedPin([]analyticsmodule.ActiveRuntimeBindingEvidence{bad}, binding); err == nil {
			t.Fatal("changed committed configuration accepted")
		}
	}
	if _, err := sourceCommittedPin([]analyticsmodule.ActiveRuntimeBindingEvidence{pin, pin}, binding); err == nil {
		t.Fatal("duplicate committed pin accepted")
	}
}
