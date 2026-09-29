//go:build linux

package hostinstall

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func transitionResultFixture(t *testing.T, e *NativeEffects) string {
	t.Helper()
	plan, err := e.request.AccessTransition.Plan()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(transitionActivation{
		OperationID:  "access-transition:" + strings.TrimPrefix(e.id.ArtifactAdmissionDigest, "sha256:"),
		IntentDigest: plan.IntentDigest, PlanID: "33333333-3333-4333-8333-333333333333",
		GenerationID:             "11111111-1111-4111-8111-111111111111",
		PublicationID:            "22222222-2222-4222-8222-222222222222",
		PlanPolicySnapshotDigest: "sha256:" + hex64('a'), ServingPolicySnapshotDigest: "sha256:" + hex64('b'),
		Status: "pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestTransitionActivationRequiresExactDurablePublicationBeforeBrowserGate(t *testing.T) {
	e, _, _ := transitionEffectsFixture(t)
	if err := e.recordTransitionResult(transitionResultFixture(t, e), true); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.execute = func(_ context.Context, args ...string) (string, error) {
		calls++
		if args[0] != "exec" || args[1] != e.clonePrefix()+"-pg" {
			t.Fatalf("activation check was not clone-only read: %v", args)
		}
		query := args[len(args)-1]
		for _, expected := range []string{"SELECT EXISTS", "delivery.delivery_active_pointer", "delivery.delivery_target", "access.authorization_snapshot", "p.state = 'committed'", "plan_document->'authorization'->>'snapshotDigest'", "s.digest =", "11111111-1111-4111-8111-111111111111", hex.EncodeToString([]byte(e.request.AccessTransition.TargetID)), hex.EncodeToString([]byte(e.request.AccessTransition.ProjectID)), hex.EncodeToString([]byte(e.request.AccessTransition.Environment))} {
			if !strings.Contains(query, expected) {
				t.Fatalf("missing exact activation predicate %q: %s", expected, query)
			}
		}
		return "t", nil
	}
	if err := e.waitTransitionActivation(t.Context(), e.clonePrefix()+"-pg", true); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected one exact-pointer verification, got %d", calls)
	}
	if err := e.waitTransitionActivation(t.Context(), e.request.Profile.Postgres, false); err == nil {
		t.Fatal("clone receipt authorized the live gate")
	}
	if calls != 1 {
		t.Fatal("missing live receipt was used for a database query")
	}
}

func TestTransitionActivationRejectsDriftAndCancellation(t *testing.T) {
	e, _, _ := transitionEffectsFixture(t)
	var result transitionActivation
	if err := json.Unmarshal([]byte(transitionResultFixture(t, e)), &result); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*transitionActivation){
		func(r *transitionActivation) { r.OperationID = "another operation" },
		func(r *transitionActivation) { r.IntentDigest = "sha256:" + hex64('b') },
		func(r *transitionActivation) { r.PlanID = "not a uuid" },
		func(r *transitionActivation) { r.GenerationID = "not a uuid" },
		func(r *transitionActivation) { r.PublicationID = "00000000-0000-0000-0000-000000000000" },
		func(r *transitionActivation) { r.PlanPolicySnapshotDigest = "sha256:invalid" },
		func(r *transitionActivation) { r.ServingPolicySnapshotDigest = "sha256:invalid" },
		func(r *transitionActivation) { r.Status = "rejected" },
	} {
		changed := result
		mutate(&changed)
		raw, _ := json.Marshal(changed)
		if err := e.recordTransitionResult(string(raw), false); err == nil {
			t.Fatalf("accepted unbound receipt: %+v", changed)
		}
	}
	if _, err := os.Stat(e.transitionResultPath(false)); !os.IsNotExist(err) {
		t.Fatalf("invalid result was persisted: %v", err)
	}
	if err := e.recordTransitionResult(transitionResultFixture(t, e), false); err != nil {
		t.Fatal(err)
	}
	e.execute = func(context.Context, ...string) (string, error) { return "f", nil }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := e.waitTransitionActivation(ctx, e.request.Profile.Postgres, false); err == nil {
		t.Fatal("uncommitted publication passed activation after cancellation")
	}
}
