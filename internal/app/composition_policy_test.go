package app

import (
	"testing"

	"github.com/flidai/leapview/internal/deployment"
)

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
