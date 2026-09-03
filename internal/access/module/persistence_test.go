package module

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestBuildProductionRequiresInjectedPostgreSQLPersistence(t *testing.T) {
	for name, config := range map[string]Config{
		"missing authority": {Production: true},
		"sqlite database":   {Production: true, Database: &sql.DB{}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Build(context.Background(), config)
			if err == nil || !strings.Contains(err.Error(), "PostgreSQL persistence") {
				t.Fatalf("Build() error = %v, want PostgreSQL persistence failure", err)
			}
		})
	}
}
