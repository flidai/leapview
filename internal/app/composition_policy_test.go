package app

import (
	"testing"

	"github.com/flidai/leapview/internal/deployment"
)

func TestProtectedPublishingTargetPolicy(t *testing.T) {
	for _, test := range []struct {
		name       string
		production bool
		evaluation bool
		want       bool
	}{
		{
			name:       "enterprise production target",
			production: true,
			want:       true,
		},
		{
			name: "development target",
		},
		{
			name:       "disposable local evaluation target",
			production: true,
			evaluation: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := protectedPublishingTarget(
				test.production,
				test.evaluation,
			); got != test.want {
				t.Fatalf("protected publishing = %t, want %t", got, test.want)
			}
		})
	}
}

func TestDeliveryApprovalPolicyExemptsAuthorizedOperationalRestatement(t *testing.T) {
	for _, test := range []struct {
		name       string
		production bool
		evaluation bool
		operation  deployment.DeliveryOperationKind
		want       bool
	}{
		{name: "production code change", production: true, operation: deployment.DeliveryOperationCodeChange, want: true},
		{name: "production binding change", production: true, operation: deployment.DeliveryOperationBindingChange, want: true},
		{name: "production policy change", production: true, operation: deployment.DeliveryOperationPolicyChange, want: true},
		{name: "production operational restatement", production: true, operation: deployment.DeliveryOperationRestatement},
		{name: "evaluation code change", production: true, evaluation: true, operation: deployment.DeliveryOperationCodeChange},
		{name: "development code change", operation: deployment.DeliveryOperationCodeChange},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := requiresDeliveryApproval(test.production, test.evaluation, test.operation); got != test.want {
				t.Fatalf("requires delivery approval = %t, want %t", got, test.want)
			}
		})
	}
}
