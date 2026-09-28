package app

import (
	"context"
	"strings"
	"testing"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
)

type transitionSnapshotDigests map[[3]string]string

func (s transitionSnapshotDigests) AuthorizationSnapshotDigest(_ context.Context, projectID, environment, generationID string) (string, bool, error) {
	digest, ok := s[[3]string{projectID, environment, generationID}]
	return digest, ok, nil
}

func TestAccessTransitionSnapshotKeepsStoredLegacyDigestAndRecapturesTypedCandidate(t *testing.T) {
	const (
		targetID    = "target_demo"
		projectID   = "project_demo"
		environment = "production"
		legacyID    = "generation_legacy"
		candidateID = "generation_typed"
	)
	legacyDigest := "sha256:" + strings.Repeat("a", 64)
	planDigest := "sha256:" + strings.Repeat("b", 64)
	typedDigest := "sha256:" + strings.Repeat("c", 64)
	stored := transitionSnapshotDigests{
		{projectID, environment, legacyID}: legacyDigest,
	}
	compileCalls := 0
	compile := func(_ context.Context, generationID string) (deploymentmodule.AccessTransitionSnapshotDigests, error) {
		compileCalls++
		if generationID != candidateID {
			t.Fatalf("compiled generation = %q, want typed candidate", generationID)
		}
		return deploymentmodule.AccessTransitionSnapshotDigests{
			PlanPolicySnapshotDigest: planDigest, ServingPolicySnapshotDigest: typedDigest,
		}, nil
	}

	legacy, err := resolveAccessTransitionSnapshotDigests(
		t.Context(), targetID, projectID, environment, legacyID, legacyID,
		legacyID, targetID, stored, compile,
	)
	if err != nil || legacy.ServingPolicySnapshotDigest != legacyDigest || legacy.PlanPolicySnapshotDigest != "" {
		t.Fatalf("legacy digest = %q, err=%v; want original stored schema-32 digest %q", legacy, err, legacyDigest)
	}
	if compileCalls != 0 {
		t.Fatalf("legacy snapshot was recompiled %d times", compileCalls)
	}

	candidate, err := resolveAccessTransitionSnapshotDigests(
		t.Context(), targetID, projectID, environment, legacyID, legacyID,
		candidateID, targetID, stored, compile,
	)
	if err != nil || candidate.ServingPolicySnapshotDigest != typedDigest || candidate.PlanPolicySnapshotDigest != planDigest {
		t.Fatalf("typed candidate digests = %+v, err=%v; want plan %q and serving %q", candidate, err, planDigest, typedDigest)
	}
	if compileCalls != 1 {
		t.Fatalf("candidate compiler calls = %d, want 1", compileCalls)
	}

	stored[[3]string{projectID, environment, candidateID}] = typedDigest
	if _, err := resolveAccessTransitionSnapshotDigests(
		t.Context(), targetID, projectID, environment, legacyID, candidateID,
		candidateID, targetID, stored, compile,
	); err != nil {
		t.Fatalf("already persisted candidate snapshot was rejected: %v", err)
	}
	stored[[3]string{projectID, environment, candidateID}] = "sha256:" + strings.Repeat("d", 64)
	if _, err := resolveAccessTransitionSnapshotDigests(
		t.Context(), targetID, projectID, environment, legacyID, legacyID,
		candidateID, targetID, stored, compile,
	); err == nil {
		t.Fatal("candidate with a conflicting immutable stored snapshot was accepted")
	}
}

func TestAccessTransitionSnapshotRequiresExactTargetAndGenerationScope(t *testing.T) {
	digest := "sha256:" + strings.Repeat("d", 64)
	reader := transitionSnapshotDigests{{"project_demo", "production", "generation_old"}: digest}
	compile := func(context.Context, string) (deploymentmodule.AccessTransitionSnapshotDigests, error) {
		return deploymentmodule.AccessTransitionSnapshotDigests{PlanPolicySnapshotDigest: digest, ServingPolicySnapshotDigest: digest}, nil
	}
	_, err := resolveAccessTransitionSnapshotDigests(
		t.Context(), "target_other", "project_demo", "production", "generation_old", "generation_old",
		"generation_old", "target_demo", reader, compile,
	)
	if err == nil {
		t.Fatalf("foreign generation target result = %v, want identity rejection", err)
	}
	_, err = resolveAccessTransitionSnapshotDigests(
		t.Context(), "target_demo", "project_demo", "production", "generation_old", "generation_other",
		"generation_old", "target_demo", reader, compile,
	)
	if err == nil {
		t.Fatal("legacy digest was returned after the active generation changed")
	}
}
