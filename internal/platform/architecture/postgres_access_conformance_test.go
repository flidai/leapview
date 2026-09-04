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

// TestFAI616LiveControlAuthorityBoundary protects the focused control-plane
// cutover: production reuses the capability-owned PostgreSQL access
// repository, local SQLite is not promoted to a competing authority, and
// durable authored-resource references remain behind the access port rather
// than a cross-capability identity-ledger import.
func TestFAI616LiveControlAuthorityBoundary(t *testing.T) {
	root := repoRoot(t)
	required := map[string][]string{
		"internal/access/control.go": {
			"type ControlStore interface",
			"InitializeControlState(context.Context, ControlStateSeed, graph.ProjectGraph)",
			"ControlState(context.Context, string)",
		},
		"internal/access/module/persistence.go": {
			"Control     access.ControlStore",
			"p.Control = repository",
			"PostgreSQL live access control authority is required",
		},
		"internal/access/postgres/control.go": {
			"var _ access.ControlStore = (*Repository)(nil)",
			"project.durable_resource_reference",
			"lockControlInstance",
		},
		"internal/access/snapshot/control.go": {
			"ControlSeedFromSnapshot",
			"FromControlState",
			"DataPolicy is intentionally",
		},
	}
	for relative, fragments := range required {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		for _, fragment := range fragments {
			if !strings.Contains(source, fragment) {
				t.Errorf("%s is missing live-control authority evidence %q", relative, fragment)
			}
		}
		for _, forbidden := range []string{
			`internal/project/identityledger`,
			`"crypto/sha256"`,
			`"crypto/sha512"`,
			"jsoncanonicalizer",
		} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s introduces forbidden control-plane authority %q", relative, forbidden)
			}
		}
	}

	sqliteFiles, err := filepath.Glob(filepath.Join(root, "internal", "access", "sqlite", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range sqliteFiles {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "ControlStore") || strings.Contains(string(body), "InitializeControlState") {
			t.Errorf("%s introduces a SQLite live-control authority", path)
		}
	}
}

// TestFAI617LiveControlLifecycleProjection proves the production activation
// path consumes the PostgreSQL live control authority without reviving the
// retired authored-grant path. Candidate previews stay immutable and private;
// only active generation preparation installs the live projection.
func TestFAI617LiveControlLifecycleProjection(t *testing.T) {
	root := repoRoot(t)
	required := map[string][]string{
		"internal/app/composition.go": {
			"AuthorizationSnapshotProjector:",
			"accesssnapshot.ProjectLiveAuthorizationSnapshot",
			"accessBundle.Control",
		},
		"internal/app/runtimefactory/factory.go": {
			"type AuthorizationSnapshotProjector func",
			"if input.Candidate != nil || f.authorizationSnapshotProjector == nil",
		},
		"internal/access/snapshot/control.go": {
			"ProjectLiveAuthorizationSnapshot",
			"compatibility.DataPolicies()",
			"store.ReactivateGrant",
		},
		"internal/app/identity_lifecycle.go": {
			"Live grants belong to",
			"reference.OwnerKind == identityledger.ReferenceOwnerKindGrant",
		},
		"internal/deployment/module/jobs.go": {
			"PublishWithActivation",
			"RollbackWithActivation",
			"activateSealedGeneration",
		},
	}
	for relative, fragments := range required {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		for _, fragment := range fragments {
			if !strings.Contains(source, fragment) {
				t.Errorf("%s is missing FAI-617 lifecycle evidence %q", relative, fragment)
			}
		}
		if relative == "internal/access/snapshot/control.go" || relative == "internal/app/identity_lifecycle.go" {
			for _, forbidden := range []string{`"crypto/sha256"`, `"crypto/sha512"`, "jsoncanonicalizer"} {
				if strings.Contains(source, forbidden) {
					t.Errorf("%s introduces a second identity/canonicalization authority %q", relative, forbidden)
				}
			}
		}
	}

	identityLifecycle, err := os.ReadFile(filepath.Join(root, "internal", "app", "identity_lifecycle.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(identityLifecycle), "artifacts.Compiler.Manifest.Access.Grants") {
		t.Fatal("production identity lifecycle still projects authored manifest grants")
	}
	jobs, err := os.ReadFile(filepath.Join(root, "internal", "deployment", "module", "jobs.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"sealedCoordinator.Publish(ctx, request)", "sealedCoordinator.Rollback(ctx, request)", "sealedReconcile"} {
		if strings.Contains(string(jobs), forbidden) {
			t.Errorf("sealed deployment jobs retain post-CAS runtime reconciliation path %q", forbidden)
		}
	}
}
