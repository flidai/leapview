package compose

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNativePostgresQualificationPackagingContract(t *testing.T) {
	root := filepath.Join("..", "..")
	release := read(t, filepath.Join(root, ".github", "workflows", "release.yml"))
	installed := read(t, filepath.Join(root, ".github", "workflows", "installed-candidate.yml"))
	initScript := read(t, filepath.Join(root, "deploy", "postgres", "init.sh"))
	environment := read(t, filepath.Join(root, "deploy", "compose", "leapview.env.example"))

	for _, required := range []string{
		`canonical_postgres_init="deploy/postgres/init.sh"`,
		`canonical_postgres_init_sha256="$(sha256sum "$canonical_postgres_init" | awk '{print $1}')"`,
		`cp deploy/postgres/init.sh "dist/$package/qualification/postgres-init.sh"`,
		`chmod 0755 "dist/$package/qualification/postgres-init.sh"`,
		`test "$(stat -c '%a' "dist/$package/qualification/postgres-init.sh")" = "755"`,
		`test "$(sha256sum "dist/$package/qualification/postgres-init.sh" | awk '{print $1}')" = "$canonical_postgres_init_sha256"`,
		`grep -q '^LEAPVIEW_POSTGRES_REQUIRE_TLS=true$' "dist/$package/leapview.env.example"`,
		`chmod 0644 "dist/$package/Caddyfile"`,
		`test "$(stat -c '%a' "dist/$package/Caddyfile")" = "644"`,
		`test -s "dist/$package/Caddyfile"`,
		`test -s "dist/$package/compose.https.yaml"`,
		`grep -q 'reverse_proxy leapview:8080' "dist/$package/Caddyfile"`,
		`"dist/$package/leapview.env.example" "dist/$package/qualification"`,
		"grep -Rni 'sqlite' \\",
		"grep -RnE 'LEAPVIEW_(DB|DATABASE|DUCKDB)_' \\",
		"release package contains an SQLite or file-backed qualification fallback",
	} {
		if !strings.Contains(release, required) {
			t.Errorf("release workflow missing native PostgreSQL packaging contract %q", required)
		}
	}
	for _, required := range []string{
		"Verify native PostgreSQL qualification assets",
		"qualification/postgres-init.sh",
		`chmod 0755 "$init_script"`,
		`test "$(stat -c '%a' "$init_script")" = "755"`,
		`chmod 0644 "$PACKAGE_ROOT/Caddyfile"`,
		`test "$(stat -c '%a' "$PACKAGE_ROOT/Caddyfile")" = "644"`,
		"CREATE ROLE leapview_control_owner",
		"CREATE ROLE leapview_ducklake_owner",
		"Caddyfile",
		"compose.https.yaml",
		"reverse_proxy leapview:8080",
		`"$PACKAGE_ROOT/leapview.env.example" "$PACKAGE_ROOT/qualification"`,
		"^LEAPVIEW_POSTGRES_REQUIRE_TLS=true$",
		"grep -Rni 'sqlite' \\",
		"grep -RnE 'LEAPVIEW_(DB|DATABASE|DUCKDB)_' \\",
		"installed archive contains an SQLite or file-backed qualification fallback",
	} {
		if !strings.Contains(installed, required) {
			t.Errorf("installed-candidate workflow missing native PostgreSQL packaging contract %q", required)
		}
	}
	for _, required := range []string{
		"CREATE ROLE leapview_control_owner",
		"CREATE ROLE leapview_ducklake_owner",
		"CREATE DATABASE",
	} {
		if !strings.Contains(initScript, required) {
			t.Errorf("canonical PostgreSQL init script missing %q", required)
		}
	}
	if !strings.Contains(environment, "LEAPVIEW_POSTGRES_REQUIRE_TLS=true\n") {
		t.Fatal("packaged application environment must require PostgreSQL TLS")
	}
}
