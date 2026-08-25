package app

import (
	"context"
	"testing"

	accessmodule "github.com/flidai/leapview/internal/access/module"
)

func TestAuthoringDevelopmentBypassIsRequestLocalAndIdentityBound(t *testing.T) {
	tests := []struct {
		name        string
		ctx         context.Context
		principalID string
		want        bool
	}{
		{name: "missing principal", ctx: context.Background(), principalID: "dev"},
		{name: "ordinary principal", ctx: accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "dev"}), principalID: "dev"},
		{name: "different principal", ctx: accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "dev", DevBypass: true}), principalID: "other"},
		{name: "development principal", ctx: accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "dev", DevBypass: true}), principalID: "dev", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := authoringDevelopmentBypass(test.ctx, test.principalID); got != test.want {
				t.Fatalf("authoringDevelopmentBypass() = %v, want %v", got, test.want)
			}
		})
	}
}
