package module

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access/http/mcpoauth"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
)

func TestNewPostgresPersistenceRejectsUnconfiguredRepository(t *testing.T) {
	if _, err := NewPostgresPersistence(&accesspostgres.Repository{}, nil); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured PostgreSQL repository error = %v", err)
	}
}

func TestBuildProductionRequiresInjectedPostgreSQLPersistence(t *testing.T) {
	for name, config := range map[string]Config{
		"missing authority": {Production: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Build(context.Background(), config)
			if err == nil || !strings.Contains(err.Error(), "PostgreSQL persistence") {
				t.Fatalf("Build() error = %v, want PostgreSQL persistence failure", err)
			}
		})
	}
}

type profileOAuthResourceStub struct{}

func (profileOAuthResourceStub) Authenticate(context.Context, string) (mcpoauth.Credential, error) {
	return mcpoauth.Credential{}, context.Canceled
}
func (profileOAuthResourceStub) ProtectedResourceMetadata(http.ResponseWriter, *http.Request) {}
func (profileOAuthResourceStub) Challenge(http.ResponseWriter)                                {}

func TestBuildProductionRejectsProfileOnlyAuthorityInjection(t *testing.T) {
	_, err := Build(context.Background(), Config{Production: true, ProfileOAuthResource: profileOAuthResourceStub{}})
	if err == nil || !strings.Contains(err.Error(), "profile-only authority injection") {
		t.Fatalf("Build() error = %v, want profile-only authority rejection", err)
	}
}

func TestBuildPersistenceRejectsIgnoredProfileOnlyAuthorityInjection(t *testing.T) {
	_, err := Build(context.Background(), Config{Persistence: &Persistence{}, ProfileOAuthResource: profileOAuthResourceStub{}})
	if err == nil || !strings.Contains(err.Error(), "requires persistence-free access build") {
		t.Fatalf("Build() error = %v, want mixed authority rejection", err)
	}
}
