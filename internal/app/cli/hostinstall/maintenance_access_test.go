package hostinstall

import (
	"path/filepath"
	"testing"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/platform/buildinfo"
)

func accessTransitionRequestFixture(t *testing.T) NativeRequest {
	r := nativeRequestFixture(t)
	r.Plan.SourceBefore.PermissionProfile = "legacy-capabilities/v1"
	r.AccessTransition = &admincli.AccessTransitionIntent{
		TargetID: "target_demo", Environment: "production", ProjectID: "project_demo",
		ExpectedPolicyRevision: 32, ExpectedPolicyDigest: "sha256:" + hex64('d'),
		ExpectedServingGeneration: "generation_legacy", ExpectedServingPolicyDigest: "sha256:" + hex64('e'),
		PublisherPrincipalID: "publisher", ReviewerPrincipalID: "reviewer",
		Grants: []admincli.AccessTransitionGrantIntent{{GrantID: "viewer-dashboard", Principal: "viewer", ResourceID: "dashboard_demo", ResourceKind: "dashboard", Actions: []string{"dashboard.read"}}},
	}
	return r
}

func TestAccessTransitionFenceBindsIntentCandidateAndDetachedCopy(t *testing.T) {
	r := accessTransitionRequestFixture(t)
	id, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	state := DetachedRehearsalState{Version: 1, Identity: id, RecoveryDigest: "sha256:" + hex64('f'), Phase: DetachedRunning}
	if err := writeDetachedState(root, state); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, detachedStateName)
	binary := buildinfo.Identity{Revision: r.CandidateRevision}
	intent, err := VerifyAccessTransitionFence(r, path, state.RecoveryDigest, "detached", binary)
	if err != nil || intent.ProjectID != r.AccessTransition.ProjectID {
		t.Fatalf("%+v %v", intent, err)
	}
	for _, mode := range []string{"live", "rehearsal", "unknown"} {
		if _, err := VerifyAccessTransitionFence(r, path, state.RecoveryDigest, mode, binary); err == nil {
			t.Fatalf("accepted %s with a detached journal", mode)
		}
	}
	r.AccessTransition.ExpectedPolicyRevision++
	if _, err := VerifyAccessTransitionFence(r, path, state.RecoveryDigest, "detached", binary); err == nil {
		t.Fatal("accepted changed authority intent")
	}
	r.AccessTransition.ExpectedPolicyRevision--
	if _, err := VerifyAccessTransitionFence(r, path, "sha256:"+hex64('c'), "detached", binary); err == nil {
		t.Fatal("accepted another captured copy")
	}
	binary.Dirty = true
	if _, err := VerifyAccessTransitionFence(r, path, state.RecoveryDigest, "detached", binary); err == nil {
		t.Fatal("accepted dirty candidate")
	}
	binary.Dirty = false
	state.Phase = DetachedPassed
	if err := writeDetachedState(root, state); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAccessTransitionFence(r, path, state.RecoveryDigest, "detached", binary); err == nil {
		t.Fatal("replayed transition after rehearsal ended")
	}
}

func TestAccessIntentIsRequiredOnlyForTheDeclaredPermissionTransition(t *testing.T) {
	r := accessTransitionRequestFixture(t)
	r.AccessTransition = nil
	if _, err := r.Identity(); err == nil {
		t.Fatal("legacy authority converted without explicit intent")
	}
	r = accessTransitionRequestFixture(t)
	r.Plan.SourceBefore.PermissionProfile = "leapview.permissions/v1"
	if _, err := r.Identity(); err == nil {
		t.Fatal("arbitrary grant mutation attached to ordinary typed update")
	}
}
