package module

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/google/uuid"
)

func TestActiveRuntimeResolverNeverTreatsLocalCredentialPinAsExternalVersion(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		binding := activeTestBinding(t)
		binding.ConnectorKind = "postgres"
		evidence := binding.Evidence()
		versioned := &activeVersionedResolver{}
		pin := ActiveRuntimeBindingEvidence{
			BindingID: evidence.BindingID, ConnectionID: evidence.ConnectionID, ConnectorKind: "postgres",
			Revision: evidence.BindingRevision, EndpointConfigHash: evidence.EndpointConfigHash,
			CredentialVersionID: uuid.NewString(),
		}
		if mixed {
			pin.ValidatedVersion = "external:v1"
		}
		module := activeTestModule(binding, versioned, pin)
		resolver := &activeRuntimeConnectionResolver{module: module, servingStateID: "state_sales", projectID: "sales", environment: "prod"}
		want := connectionbinding.ErrProviderUnavailable
		if mixed {
			want = connectionbinding.ErrIncompatibleBinding
		}
		if _, err := resolveTestConnection(resolver, t.Context(), "quack", semanticmodel.Connection{Kind: "postgres"}); !errors.Is(err, want) {
			t.Fatalf("local credential pin error = %v, want %v", err, want)
		}
		if versioned.calls != 0 || module.connectionFactory.(*activePoolFactory).healthChecks != 0 {
			t.Fatal("local credential pin reached an external resolver or runtime pool")
		}
	}
}
