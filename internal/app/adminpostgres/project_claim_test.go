package adminpostgres

import (
	"testing"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/app/config"
)

func TestLocalProjectClaimUsesInitializationBootstrapIdentity(t *testing.T) {
	if got, err := adminoffline.ResolveBootstrapEmail(false, config.Config{}.BootstrapEmail); err != nil || got != adminoffline.DefaultDevelopmentBootstrapEmail {
		t.Fatalf("default local Project-claim email = %q, want initialization identity %q", got, adminoffline.DefaultDevelopmentBootstrapEmail)
	}
	if got, err := adminoffline.ResolveBootstrapEmail(false, config.Config{BootstrapEmail: "Owner <owner@example.com>"}.BootstrapEmail); err != nil || got != "owner@example.com" {
		t.Fatalf("explicit local Project-claim email = %q, want normalized configured identity", got)
	}
}
