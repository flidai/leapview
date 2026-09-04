package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFAI617PostgresAccessComposition keeps the production access authority
// on the identity-owned PostgreSQL handle. The source assertions intentionally
// guard the composition seam: local SQLite remains an explicit branch, while
// production cannot accidentally inherit the process store.
func TestFAI617PostgresAccessComposition(t *testing.T) {
	root := repoRoot(t)
	files := map[string][]string{
		"internal/platform/postgres/postgres.go": {
			"type DBTX interface",
			"type PoolHandle interface",
			"func (p *Pool) Query(",
			"func (p *Pool) QueryRow(",
		},
		"internal/app/identity_authority.go": {
			"Pool    platformpostgres.PoolHandle",
			"any(pool).(platformpostgres.PoolHandle)",
			"identity authority pool does not expose shared PostgreSQL access handle",
		},
		"internal/app/runtime_capabilities.go": {
			"PostgresDB     platformpostgres.DBTX",
			"accessmodule.BuildPostgres(ctx, accessConfig",
			"accessConfig.LegacySQLite = true",
		},
		"internal/access/module/postgres.go": {
			"accesspostgres.NewAccess(postgres.Database",
			"mcpoauth.NewPostgres(postgres.Database",
			"NewPostgresPersistence(repository, oauth)",
			"config.Persistence = &persistence",
			"config.ExistingAuth = auth",
		},
		"internal/app/composition.go": {
			"accessDatabase = nil",
			"PostgresDB: identityAuthority.Pool",
		},
	}
	for relative, required := range files {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		for _, fragment := range required {
			if !strings.Contains(source, fragment) {
				t.Errorf("%s is missing PostgreSQL access composition fragment %q", relative, fragment)
			}
		}
	}
	composition, err := os.ReadFile(filepath.Join(root, "internal", "app", "composition.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(composition), "PostgresDB: store.SQLDB()") {
		t.Error("production access composition passes SQLite as its PostgreSQL database")
	}
	runtimeCapabilities, err := os.ReadFile(filepath.Join(root, "internal", "app", "runtime_capabilities.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"internal/access/postgres", "internal/access/http/mcpoauth", "accessConfig.PostgresRepository"} {
		if strings.Contains(string(runtimeCapabilities), forbidden) {
			t.Errorf("access capability config leaks concrete adapter %q", forbidden)
		}
	}
}

func TestFAI609NativeAdminKeepsPostgresAdaptersCapabilityOwned(t *testing.T) {
	root := repoRoot(t)
	admin, err := os.ReadFile(filepath.Join(root, "internal", "app", "adminpostgres", "operations.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(admin)
	for _, required := range []string{
		"accessmodule.NewPostgresInstanceInitializer",
		"migrations.Apply",
		"migrations.Verify",
		"bootstrappostgres.New",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("native Admin composition is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"internal/access/postgres",
		"platform.Open(",
		"store.SQLDB()",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("native Admin composition owns forbidden adapter/fallback %q", forbidden)
		}
	}
	composition, err := os.ReadFile(filepath.Join(root, "internal", "app", "composition.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"if production {",
		"instanceBootstrap = bootstrappostgres.New(identityAuthority.Pool)",
		"instanceBootstrap.BindInstanceEnvironment(ctx, string(environment))",
		"instanceBootstrap.InstanceID(ctx)",
	} {
		if !strings.Contains(string(composition), required) {
			t.Errorf("production composition is missing PostgreSQL bootstrap selection %q", required)
		}
	}
}
