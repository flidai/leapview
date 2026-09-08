package compose

import (
	"os"
	"os/exec"
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
		`chmod 0755 "dist/$package/qualification/validate-bundle.sh"`,
		`chmod 0644 "dist/$package/Caddyfile"`,
		`test "$(stat -c '%a' "dist/$package/Caddyfile")" = "644"`,
		`"dist/$package/qualification/validate-bundle.sh" "dist/$package"`,
	} {
		if !strings.Contains(release, required) {
			t.Errorf("release workflow missing native PostgreSQL packaging contract %q", required)
		}
	}
	for _, required := range []string{
		"Verify native PostgreSQL qualification assets",
		"qualification/postgres-init.sh",
		`chmod 0755 "$PACKAGE_ROOT/qualification/validate-bundle.sh"`,
		`chmod 0755 "$init_script"`,
		`chmod 0644 "$PACKAGE_ROOT/Caddyfile"`,
		`"$PACKAGE_ROOT/qualification/validate-bundle.sh" "$PACKAGE_ROOT"`,
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

func TestQualificationBundleValidatorBehavior(t *testing.T) {
	root := filepath.Join("..", "..")
	validator := filepath.Join(root, "deploy", "compose", "qualification", "validate-bundle.sh")

	tests := []struct {
		name   string
		valid  bool
		mutate func(t *testing.T, bundle string)
	}{
		{name: "valid bundle", valid: true},
		{
			name: "missing required path",
			mutate: func(t *testing.T, bundle string) {
				t.Helper()
				if err := os.Remove(filepath.Join(bundle, "Caddyfile")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "invalid init mode",
			mutate: func(t *testing.T, bundle string) {
				t.Helper()
				if err := os.Chmod(filepath.Join(bundle, "qualification", "postgres-init.sh"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "invalid proxy mode",
			mutate: func(t *testing.T, bundle string) {
				t.Helper()
				if err := os.Chmod(filepath.Join(bundle, "Caddyfile"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "TLS requirement missing",
			mutate: func(t *testing.T, bundle string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(bundle, "leapview.env.example"), []byte("LEAPVIEW_POSTGRES_REQUIRE_TLS=false\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "SQLite fallback content",
			mutate: func(t *testing.T, bundle string) {
				t.Helper()
				path := filepath.Join(bundle, "compose.yaml")
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(contents, []byte("\n# sqlite fallback\n")...), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "file-backed environment fallback",
			mutate: func(t *testing.T, bundle string) {
				t.Helper()
				path := filepath.Join(bundle, "leapview.env.example")
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				legacyDatabaseURL := "LEAP" + "VIEW_DATABASE_URL=file:///tmp/leapview.db\n"
				if err := os.WriteFile(path, append(contents, []byte(legacyDatabaseURL)...), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle := writeQualificationBundleFixture(t)
			if test.mutate != nil {
				test.mutate(t, bundle)
			}
			err := exec.Command(validator, bundle).Run()
			if test.valid {
				if err != nil {
					t.Fatalf("valid bundle rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid bundle accepted")
			}
		})
	}
}

func writeQualificationBundleFixture(t *testing.T) string {
	t.Helper()
	bundle := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bundle, "qualification"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]struct {
		contents string
		mode     os.FileMode
	}{
		"compose.yaml": {
			contents: "services:\n  leapview:\n    image: example/leapview\n",
			mode:     0o600,
		},
		"compose.https.yaml": {
			contents: "services:\n  caddy:\n    image: example/caddy\n",
			mode:     0o600,
		},
		"Caddyfile": {
			contents: "{$CADDY_DOMAIN} {\n  reverse_proxy leapview:8080\n}\n",
			mode:     0o644,
		},
		"leapview.env.example": {
			contents: "LEAPVIEW_POSTGRES_REQUIRE_TLS=true\n",
			mode:     0o600,
		},
		"qualification/postgres-init.sh": {
			contents: "#!/usr/bin/env bash\nCREATE ROLE leapview_control_owner;\nCREATE ROLE leapview_ducklake_owner;\nCREATE DATABASE leapview;\n",
			mode:     0o755,
		},
		"qualification/validate-bundle.sh": {
			contents: "#!/usr/bin/env bash\n",
			mode:     0o755,
		},
	}
	for relativePath, file := range files {
		path := filepath.Join(bundle, relativePath)
		if err := os.WriteFile(path, []byte(file.contents), file.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, file.mode); err != nil {
			t.Fatal(err)
		}
	}
	return bundle
}
