package migrations

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const permissionValidatorDeclaration = "CREATE OR REPLACE FUNCTION access.valid_permission_pairs"

func normalizedPermissionValidator(t *testing.T, source string) string {
	t.Helper()
	start := strings.Index(source, permissionValidatorDeclaration)
	if start < 0 {
		t.Fatalf("permission validator declaration is missing")
	}
	end := strings.Index(source[start:], "END $$;")
	if end < 0 {
		t.Fatalf("permission validator terminator is missing")
	}
	end += start + len("END $$;")
	return strings.Join(strings.Fields(source[start:end]), " ")
}

func TestPermissionValidatorMigrationMatchesGeneratedContract(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate migration source")
	}
	root := filepath.Join(filepath.Dir(source), "../../../access/postgres")
	generated, err := os.ReadFile(filepath.Join(root, "permission_contract.sql"))
	if err != nil {
		t.Fatalf("read generated permission contract: %v", err)
	}
	migration, err := os.ReadFile(filepath.Join(filepath.Dir(source), "027_typed_permission_validation_hardening.sql"))
	if err != nil {
		t.Fatalf("read permission validation migration: %v", err)
	}
	if got, want := normalizedPermissionValidator(t, string(migration)), normalizedPermissionValidator(t, string(generated)); got != want {
		t.Fatalf("migration validator drifted from generated contract\n got: %s\nwant: %s", got, want)
	}
}
