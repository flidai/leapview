package app

import (
	"testing"

	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/deployment"
)

func TestAccessAuthConfigRequiresExplicitDevelopmentBypass(t *testing.T) {
	ordinary := accessAuthConfig(config.Config{}, false, false)
	if ordinary.DevBypass {
		t.Fatal("development authentication bypass was enabled without explicit configuration")
	}
	explicit := accessAuthConfig(config.Config{DevAuthBypass: true, DevAPIToken: "local-token"}, false, false)
	if !explicit.DevBypass || explicit.DevAPIToken != "local-token" {
		t.Fatalf("explicit development authentication config = %#v", explicit)
	}
	production := accessAuthConfig(config.Config{DevAuthBypass: true}, true, true)
	if production.DevBypass {
		t.Fatal("production authentication config accepted development bypass")
	}
}

func TestDeliveryApprovalPolicyExemptsAuthorizedOperationalRestatement(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation deployment.DeliveryOperationKind
		want      bool
	}{
		{name: "code change", operation: deployment.DeliveryOperationCodeChange, want: true},
		{name: "binding change", operation: deployment.DeliveryOperationBindingChange, want: true},
		{name: "policy change", operation: deployment.DeliveryOperationPolicyChange, want: true},
		{name: "operational restatement", operation: deployment.DeliveryOperationRestatement},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := requiresDeliveryApproval(test.operation); got != test.want {
				t.Fatalf("requires delivery approval = %t, want %t", got, test.want)
			}
		})
	}
}
