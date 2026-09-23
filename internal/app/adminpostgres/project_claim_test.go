package adminpostgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/app/config"
)

type projectClaimAccessAuthority struct {
	principal        access.Principal
	principalErr     error
	admin            bool
	adminErr         error
	requestedEmail   string
	requestedAdminID string
}

func (a *projectClaimAccessAuthority) PrincipalByEmail(_ context.Context, email string) (access.Principal, error) {
	a.requestedEmail = email
	return a.principal, a.principalErr
}

func (a *projectClaimAccessAuthority) IsPlatformAdmin(_ context.Context, principalID string) (bool, error) {
	a.requestedAdminID = principalID
	return a.admin, a.adminErr
}

func TestLocalProjectClaimUsesInitializationBootstrapIdentity(t *testing.T) {
	if got, err := adminoffline.ResolveBootstrapEmail(false, config.Config{}.BootstrapEmail); err != nil || got != adminoffline.DefaultDevelopmentBootstrapEmail {
		t.Fatalf("default local Project-claim email = %q, want initialization identity %q", got, adminoffline.DefaultDevelopmentBootstrapEmail)
	}
	if got, err := adminoffline.ResolveBootstrapEmail(false, config.Config{BootstrapEmail: "Owner <owner@example.com>"}.BootstrapEmail); err != nil || got != "owner@example.com" {
		t.Fatalf("explicit local Project-claim email = %q, want normalized configured identity", got)
	}
}

func TestResolveLocalBootstrapAdministratorUsesPersistedPrincipalID(t *testing.T) {
	authority := &projectClaimAccessAuthority{
		principal: access.Principal{ID: "0199a09c-bef2-7d86-b3ec-d3c58616ce8e", Email: "admin@localhost"},
		admin:     true,
	}
	principal, err := resolveLocalBootstrapAdministrator(t.Context(), authority, "admin@localhost")
	if err != nil {
		t.Fatal(err)
	}
	if principal.ID != authority.principal.ID {
		t.Fatalf("resolved principal ID = %q, want %q", principal.ID, authority.principal.ID)
	}
	if authority.requestedEmail != "admin@localhost" {
		t.Fatalf("principal lookup email = %q", authority.requestedEmail)
	}
	if authority.requestedAdminID != authority.principal.ID {
		t.Fatalf("administrator lookup ID = %q, want persisted principal ID %q", authority.requestedAdminID, authority.principal.ID)
	}
	if authority.requestedAdminID == access.PrincipalIDForEmail("admin@localhost") {
		t.Fatal("administrator lookup used the legacy email-derived identity")
	}
}

func TestResolveLocalBootstrapAdministratorRejectsMissingOrInactivePrincipal(t *testing.T) {
	tests := []struct {
		name      string
		authority *projectClaimAccessAuthority
		want      string
	}{
		{name: "missing", authority: &projectClaimAccessAuthority{principalErr: errors.New("not found")}, want: "resolve local bootstrap administrator"},
		{name: "inactive", authority: &projectClaimAccessAuthority{principal: access.Principal{ID: "0199a09c-bef2-7d86-b3ec-d3c58616ce8e"}}, want: "not an active platform administrator"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveLocalBootstrapAdministrator(t.Context(), tt.authority, "admin@localhost")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}
