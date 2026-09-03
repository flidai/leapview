package module

import (
	"context"
	"strings"
	"testing"

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
