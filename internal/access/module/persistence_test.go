package module

import (
	"context"
	"strings"
	"testing"
)

func TestNewSQLiteAuditStoreRequiresDatabase(t *testing.T) {
	if store := NewSQLiteAuditStore(nil); store != nil {
		t.Fatal("NewSQLiteAuditStore(nil) returned a store")
	}
}

func TestBuildProductionRequiresInjectedPostgreSQLPersistence(t *testing.T) {
	for name, config := range map[string]Config{
		"missing authority":  {Production: true},
		"sqlite persistence": {Production: true, Persistence: &Persistence{backend: backendSQLiteLegacy}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Build(context.Background(), config)
			if err == nil || !strings.Contains(err.Error(), "PostgreSQL persistence") {
				t.Fatalf("Build() error = %v, want PostgreSQL persistence failure", err)
			}
		})
	}
}
