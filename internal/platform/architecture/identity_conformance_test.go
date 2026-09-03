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
// delivery operations auditable in source. Forward publication must enter the
// identity coordinator from sealedcontrol's post-approval activation callback,
// and rollback must retain the identity-first recovery fence established by
// FAI-617.
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
		`return c.publishSealed(ctx, request, func(sealedCtx context.Context, targetCommit func() error) error {`,
		`run, runErr := c.identity.Run(sealedCtx, transition, func(commitCtx context.Context) error {`,
		`return activate(commitCtx, targetCommit)`,
		`return targetCommit()`,
		`c.published.PublishedBundleTransition(ctx, c.instanceID, request.Request.GenerationID)`,
		`transition, err := IdentityRollbackTransition(request, published)`,
		`result, err = c.rollbackSealed(commitCtx, request, activate)`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("canonical identity lifecycle is missing fenced operation %q", required)
		}
	}
	if strings.Contains(source, `c.sealed.Publish(ctx, request)`) {
		t.Error("forward publication bypasses the sealed post-approval activation callback")
	}
	if strings.Count(source, `c.sealed.Rollback(ctx, request)`) != 1 {
		t.Error("sealed rollback must be called only by the identity lifecycle delivery helper")
	}
}

// TestFAI663RestoreReusesIdentityApprovalAndDigestAuthorities prevents the
// explicit restore path from growing a standalone mutation API, approval
// store, or hashing implementation. Restore evidence is carried by the
// existing candidate and delivery-plan contracts and executed by the existing
// identity/sealed coordinators.
func TestFAI663RestoreReusesIdentityApprovalAndDigestAuthorities(t *testing.T) {
	root := repoRoot(t)
	required := map[string][]string{
		"internal/app/identity_lifecycle.go": {
			`PrepareIdentityRestoreTransition`,
			`identityledger.OperationRestore`,
			`c.publishSealed(ctx, request, func(sealedCtx context.Context, targetCommit func() error) error {`,
		},
		"internal/deployment/plan_delivery_plan.go": {
			`*RestoreIntent`,
			`canonicalJSONDigest(deliveryPlanCanonical{`,
		},
		"internal/project/identityledger/coordinator.go": {
			`repository.RestoreAndActivate(ctx, Restore{`,
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
				t.Errorf("%s is missing FAI-663 authority reuse %q", relative, fragment)
			}
		}
		for _, forbidden := range []string{`"crypto/sha256"`, `"crypto/sha512"`, "sha256.Sum", "sha512.Sum"} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s introduces a restore-specific hashing authority %q", relative, forbidden)
			}
		}
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
