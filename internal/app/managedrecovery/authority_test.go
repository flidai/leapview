package managedrecovery

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
)

func TestManagedAuthorityUsesExplicitTLSAndRejectsOriginalCluster(t *testing.T) {
	h := postgrestest.StartTLS(t)
	role := h.EnsureRole(t, postgrestest.Role{Name: "managed_recovery_operator", Password: "authority-component-secret", Login: true})
	database := h.NewDatabase(t, "")
	h.GrantDatabase(t, database.Name, role, "CONNECT")
	admin, err := pgx.Connect(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if _, err := admin.Exec(t.Context(), "GRANT EXECUTE ON FUNCTION pg_control_system() TO managed_recovery_operator;CREATE TABLE authority_component(id integer);GRANT INSERT,SELECT ON authority_component TO managed_recovery_operator"); err != nil {
		t.Fatal(err)
	}
	var systemID string
	if err := admin.QueryRow(t.Context(), "SELECT system_identifier::text FROM pg_control_system()").Scan(&systemID); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(database.PrivateURL(role))
	if err != nil {
		t.Fatal(err)
	}
	parsed.RawQuery = "sslmode=verify-full"
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	input := AuthorityInput{URLFile: filepath.Join(root, "url"), RootCAFile: filepath.Join(root, "ca"), Role: role.Name}
	ca, err := os.ReadFile(h.RootCertPath())
	if err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string][]byte{input.URLFile: []byte(parsed.String()), input.RootCAFile: ca} {
		if err := os.WriteFile(path, value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	primaries := []providerrestore.PrimaryEnrollment{{SystemIdentifier: "1"}}
	pool, err := OpenManagedAuthority(t.Context(), input, primaries)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "INSERT INTO authority_component VALUES(1)"); err != nil {
		pool.Close()
		t.Fatal("explicit maintenance authority could not write its ledger")
	}
	pool.Close()
	primaries[0].SystemIdentifier = systemID
	if pool, err := OpenManagedAuthority(t.Context(), input, primaries); err == nil {
		pool.Close()
		t.Fatal("original writer supplied recovery authority")
	}
	wrong := input
	wrong.Role = "other_role"
	if pool, err := OpenManagedAuthority(t.Context(), wrong, primaries); err == nil {
		pool.Close()
		t.Fatal("ambient/foreign role accepted")
	}
	if err := os.WriteFile(input.RootCAFile, []byte("foreign CA"), 0600); err != nil {
		t.Fatal(err)
	}
	if pool, err := OpenManagedAuthority(t.Context(), input, primaries); err == nil {
		pool.Close()
		t.Fatal("foreign authority CA accepted")
	}
}
