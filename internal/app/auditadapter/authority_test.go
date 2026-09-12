package auditadapter

import (
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
)

func TestAuthorityRetainsExactRepositoryIdentity(t *testing.T) {
	repository := accesspostgres.New()
	authority := New(repository)
	if !authority.Configured() || !authority.Matches(repository) {
		t.Fatal("authority did not retain the configured Access repository")
	}
	if authority.Matches(accesspostgres.New()) {
		t.Fatal("authority matched a distinct Access repository")
	}
	if New(nil).Configured() || New(nil).Matches(repository) {
		t.Fatal("nil authority reported configured")
	}
}
