package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFAI617ProductionIdentityAuthorityBoundary keeps the instance identity
// authority on the PostgreSQL control-plane path. Local/development
// composition intentionally receives no identity repository; it must not
// manufacture an in-memory or SQLite substitute that could diverge from the
// production fence.
func TestFAI617ProductionIdentityAuthorityBoundary(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "app", "identity_authority.go")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, required := range []string{
		`platformpostgres "github.com/flidai/leapview/internal/platform/postgres"`,
		`identitymodule "github.com/flidai/leapview/internal/project/identityledger/module"`,
		`return identitymodule.OpenControl(ctx, cfg)`,
		`if !cfg.Production`,
		`return identityAuthorityBundle{}, nil`,
		`identitymodule.NewPostgresRepository(pool)`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("production identity authority is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		`internal/project/identityledger/postgres`,
		`internal/project/identityledger"`,
		"internal/project/identityledger/sqlite",
		"internal/project/identityledger/memory",
		"sqlite.Open",
		"NewSQLite",
		"NewCoordinator",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("identity authority retains local/fake ledger path %q", forbidden)
		}
	}
}

// TestFAI617CanonicalLifecycleHasNoIdentityBypass makes the two canonical
// delivery operations auditable in source. The app lifecycle must load
// durable publish evidence and pass the sealed operation only as the delivery
// commit after identityledger.Coordinator has fenced the transition. Direct
// sealed calls are confined to the small helper methods used by those commit
// callbacks.
func TestFAI617CanonicalLifecycleHasNoIdentityBypass(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "app", "identity_lifecycle.go")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, required := range []string{
		`c.publish.BundlePublishTransition(ctx, c.instanceID, request.Generation.ID)`,
		`_, runErr := c.identity.Run(ctx, transition, func(commitCtx context.Context) error {`,
		`publication, err = c.publishSealed(commitCtx, request, activate)`,
		`c.published.PublishedBundleTransition(ctx, c.instanceID, request.Request.GenerationID)`,
		`transition, err := IdentityRollbackTransition(request, published)`,
		`result, err = c.rollbackSealed(commitCtx, request, activate)`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("canonical identity lifecycle is missing fenced operation %q", required)
		}
	}
	if strings.Count(source, `c.sealed.Publish(ctx, request)`) != 1 {
		t.Error("sealed publish must be called only by the identity lifecycle delivery helper")
	}
	if strings.Count(source, `c.sealed.Rollback(ctx, request)`) != 1 {
		t.Error("sealed rollback must be called only by the identity lifecycle delivery helper")
	}
}

// TestFAI617CompositionUsesIdentityAdmissionAndCoordinator keeps production
// planning, readiness, publication, rollback, and startup reconciliation on
// the identity authority composed by the application root.
func TestFAI617CompositionUsesIdentityAdmissionAndCoordinator(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "internal", "app", "composition.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, required := range []string{
		`identityAuthority, err := buildIdentityAuthority(ctx, cfg)`,
		`sealedCoordinator = identityCoordinator`,
		`PlanIdentityCandidate(planCtx, identityAuthority.Repository`,
		`PrepareIdentityPublishTransition(readyCtx, identityAuthority.Repository`,
		`ListInFlightTransitions(ctx, instanceID)`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("production identity composition is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		`sealedControlCoordinator.PublishWithActivation(publishCtx`,
		`sealedControlCoordinator.RollbackWithActivation(rollbackCtx`,
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("canonical delivery bypasses identity coordinator via %q", forbidden)
		}
	}
}

// TestFAI617IdentityLifecycleDoesNotCreateHashAuthority protects the existing
// graph/artifact/release digest boundaries. Identity lifecycle code may carry
// GraphDigest evidence, but it must not introduce a second SHA or hash
// implementation for transition or reference identity.
func TestFAI617IdentityLifecycleDoesNotCreateHashAuthority(t *testing.T) {
	root := repoRoot(t)
	paths := []string{
		"internal/app/identity_authority.go",
		"internal/app/identity_lifecycle.go",
		"internal/project/identityledger/transition.go",
		"internal/project/identityledger/postgres/transition_repository.go",
	}
	for _, relative := range paths {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		for _, forbidden := range []string{
			`"crypto/sha256"`,
			`"crypto/sha512"`,
			`"hash/`,
			"sha256.Sum",
			"sha512.Sum",
			"hex.Encode",
		} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s introduces a second transition/reference hash authority %q", relative, forbidden)
			}
		}
	}
}
