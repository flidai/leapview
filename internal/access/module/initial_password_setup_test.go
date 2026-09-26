package module

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

type initialSetupGuardRepository struct {
	access.Repository
	mustChange bool
	open       bool
	err        error
}

func (r *initialSetupGuardRepository) LocalCredential(context.Context, string) (access.LocalCredential, error) {
	return access.LocalCredential{MustChangePassword: r.mustChange}, nil
}
func (r *initialSetupGuardRepository) InitialPublisherPasswordSetupOpen(context.Context, string, string) (bool, error) {
	return r.open, r.err
}

func TestInitialPublisherPasswordGateRefreshesEligibility(t *testing.T) {
	repo := &initialSetupGuardRepository{mustChange: true, open: true}
	auth := &Auth{repo: repo, localAuth: true}
	request := httptest.NewRequest("GET", "/api/v1/projects/project:setup/role-bindings", nil)
	credential := &access.APICredential{Token: access.APIToken{ID: "publisher"}, InitialPublisher: &access.InitialPublisherOrigin{}}
	if auth.mustChangeLocalPassword(request, "principal", credential) {
		t.Fatal("open initial setup denied")
	}
	// Reuse the same credential object, as a cache could, after password reset.
	repo.open = false
	if !auth.mustChangeLocalPassword(request, "principal", credential) {
		t.Fatal("stale credential retained closed exception")
	}
	repo.open = true
	repo.err = errors.New("database unavailable")
	if !auth.mustChangeLocalPassword(request, "principal", credential) {
		t.Fatal("lookup error bypassed password policy")
	}
	repo.err = nil
	repo.mustChange = false
	repo.open = false
	if auth.mustChangeLocalPassword(request, "principal", credential) {
		t.Fatal("closed exception blocked ordinary use after password change")
	}
	repo.mustChange = true
	if !auth.mustChangeLocalPassword(request, "principal", nil) {
		t.Fatal("browser bypassed temporary password")
	}
	if !auth.mustChangeLocalPassword(request, "principal", &access.APICredential{Token: access.APIToken{Name: access.InitialProjectClaimPublisherTokenName("claim")}}) {
		t.Fatal("name spoof bypassed temporary password")
	}
}
