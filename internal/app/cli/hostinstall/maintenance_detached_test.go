package hostinstall

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCaptureForRehearsalReopensWithoutMigration(t *testing.T) {
	c, j, e := setup()
	digest, err := c.CaptureForRehearsal(t.Context(), identity(), func(context.Context, Identity) (string, error) {
		if j.state.Phase != Capturing || j.state.RestoreRequired {
			t.Fatal("capture not fenced")
		}
		e.calls = append(e.calls, "capture-only")
		return "sha256:" + hex64('d'), nil
	})
	if err != nil || digest != "sha256:"+hex64('d') {
		t.Fatalf("capture: %s %v", digest, err)
	}
	if j.state.Phase != Recovered || j.state.RestoreRequired {
		t.Fatalf("live transaction unfinished: %+v", j.state)
	}
	want := []string{"admit", "quiesce", "capture-only", "stop", "verify-predecessor", "expose-predecessor"}
	if !reflect.DeepEqual(e.calls, want) {
		t.Fatalf("live effects: %v", e.calls)
	}
	e.calls = nil
	if _, err := c.CaptureForRehearsal(t.Context(), identity(), nil); err == nil {
		t.Fatal("replayed completed capture")
	}
	if len(e.calls) != 0 {
		t.Fatal(e.calls)
	}
}

func TestCaptureForRehearsalFailureNeverAuthorizesMigration(t *testing.T) {
	c, j, _ := setup()
	if _, err := c.CaptureForRehearsal(t.Context(), identity(), func(context.Context, Identity) (string, error) {
		return "", errors.New("snapshot failed")
	}); err == nil {
		t.Fatal("accepted failed capture")
	}
	if j.state.Phase != Capturing || j.state.RestoreRequired {
		t.Fatal(j.state)
	}
	if err := c.Recover(t.Context(), identity()); err != nil {
		t.Fatal(err)
	}
	if j.state.Phase != Recovered {
		t.Fatal(j.state)
	}
}

type detachedTestEffects struct {
	calls     []string
	fail      bool
	liveValue *string
}

func (e *detachedTestEffects) Rehearse(context.Context, string) error {
	e.calls = append(e.calls, "clone-rehearse")
	// A public write happens after the completed capture has reopened the site.
	*e.liveValue = "write after capture"
	if e.fail {
		return errors.New("candidate denied viewer")
	}
	return nil
}
func (e *detachedTestEffects) Cleanup(context.Context) error {
	e.calls = append(e.calls, "clone-cleanup")
	return nil
}

func TestDetachedFailureAndRetryPreserveNewLiveWrites(t *testing.T) {
	root := t.TempDir()
	state := DetachedRehearsalState{Version: 1, Identity: identity(), RecoveryDigest: "sha256:" + hex64('d'), Phase: DetachedReady}
	if err := writeDetachedState(root, state); err != nil {
		t.Fatal(err)
	}
	live := "old value"
	e := &detachedTestEffects{fail: true, liveValue: &live}
	if err := runDetachedRehearsal(t.Context(), root, identity(), e); err == nil {
		t.Fatal("accepted failed rehearsal")
	}
	if live != "write after capture" {
		t.Fatal("restored live state")
	}
	got, err := readDetachedState(root)
	if err != nil || got.Phase != DetachedFailed {
		t.Fatalf("state: %+v %v", got, err)
	}
	e.fail = false
	if err := runDetachedRehearsal(t.Context(), root, identity(), e); err != nil {
		t.Fatal(err)
	}
	got, _ = readDetachedState(root)
	if got.Phase != DetachedPassed || live != "write after capture" {
		t.Fatal(got, live)
	}
	for _, call := range e.calls {
		if call != "clone-rehearse" && call != "clone-cleanup" {
			t.Fatal(call)
		}
	}
}

func TestDetachedRejectsUnreleasedCaptureAndWrongIdentity(t *testing.T) {
	for _, phase := range []DetachedPhase{DetachedCaptured, DetachedReady} {
		root := t.TempDir()
		s := DetachedRehearsalState{Version: 1, Identity: identity(), RecoveryDigest: "sha256:" + hex64('d'), Phase: phase}
		if err := writeDetachedState(root, s); err != nil {
			t.Fatal(err)
		}
		id := identity()
		if phase == DetachedReady {
			id.ArtifactAdmissionDigest = "sha256:" + hex64('f')
		}
		live := "new writes"
		e := &detachedTestEffects{liveValue: &live}
		if err := runDetachedRehearsal(t.Context(), root, id, e); err == nil {
			t.Fatal("accepted invalid capture")
		}
		if len(e.calls) != 0 || live != "new writes" {
			t.Fatal("effects before admission")
		}
	}
}

func TestDetachedInterruptedRunOnlyCleansClone(t *testing.T) {
	root := t.TempDir()
	live := "write after crash"
	s := DetachedRehearsalState{Version: 1, Identity: identity(), RecoveryDigest: "sha256:" + hex64('d'), Phase: DetachedRunning}
	if err := writeDetachedState(root, s); err != nil {
		t.Fatal(err)
	}
	e := &detachedTestEffects{liveValue: &live}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runDetachedRehearsal(ctx, root, identity(), e); err == nil {
		t.Fatal("accepted canceled run")
	}
	if live != "write after crash" {
		t.Fatal("cancellation changed live state")
	}
	if err := runDetachedRehearsal(t.Context(), root, identity(), e); err != nil {
		t.Fatal(err)
	}
	if e.calls[0] != "clone-cleanup" {
		t.Fatal(e.calls)
	}
}
