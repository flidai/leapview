package composectl

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/flidai/leapview/internal/platform/compatibility"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
)

func TestReleasedHostRecoveryCompositionExecutesDueTransitionOwnersWithValidatedEvidence(t *testing.T) {
	basePolicy, err := compatibility.EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	template, err := compatibility.EmbeddedCandidateTransitionTemplate()
	if err != nil {
		t.Fatal(err)
	}
	identity := compatibility.ReleaseIdentity{
		ReleaseID: "v0.2.0-rc.2", Version: "0.2.0-rc.2", SourceRevision: strings.Repeat("2", 40),
		Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("c", 64), Distribution: "public", Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
	policy, err := basePolicy.BindCandidateWithTemplate(identity, template.Platforms, template)
	if err != nil {
		t.Fatal(err)
	}
	predecessorRelease, ok := policy.ReleaseByID(template.PredecessorRelease)
	if !ok {
		t.Fatal("embedded predecessor release is unavailable")
	}
	predecessor := predecessorRelease.IdentityForPlatform(runtime.GOOS + "/" + runtime.GOARCH)
	build := buildinfo.Identity{
		Version: identity.Version, Revision: identity.SourceRevision, BuildTime: "2026-08-26T00:00:00Z",
	}
	root := t.TempDir()
	stateRoot := filepath.Join(root, "docker-volume")
	home := filepath.Join(stateRoot, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := platform.Open(t.Context(), filepath.Join(home, "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindInstanceEnvironment(t.Context(), "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InstanceID(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	policyDocument, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeScheduledQualificationBundle(root, policyDocument); err != nil {
		t.Fatal(err)
	}
	policyDigest := fmt.Sprintf("%x", sha256.Sum256(policyDocument))
	evidencePath := filepath.Join(root, "owner-transition-evidence.json")
	if err := writeScheduledTransitionEvidence(evidencePath, predecessor, identity, policy.PolicyVersion, policyDigest); err != nil {
		t.Fatal(err)
	}
	identityDocument, err := json.Marshal(build)
	if err != nil {
		t.Fatal(err)
	}
	controllerScript := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "version" ]; then printf '%%s\n' '%s'; exit 0; fi
evidence_dir=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--evidence-dir" ]; then evidence_dir="$2"; shift 2; else shift; fi
done
printf '{"operation":"upgrade","event":"started"}\n' >&3
printf '{"operation":"upgrade","event":"completed"}\n' >&3
printf '{"operation":"rollback","event":"started"}\n' >&3
printf '{"operation":"rollback","event":"completed"}\n' >&3
cp '%s' "$evidence_dir/transition-qualification.json"
`, identityDocument, evidencePath)
	if err := os.WriteFile(filepath.Join(root, "leapviewctl"), []byte(controllerScript), 0o700); err != nil {
		t.Fatal(err)
	}
	dockerPath := filepath.Join(root, "docker")
	dockerScript := fmt.Sprintf(`#!/bin/sh
case " $* " in
  *" ps -q leapview "*) printf 'container-id\n' ;;
  *" inspect --format "*) printf '%%s\n' '%s' ;;
  *" version --format "*) printf '27.0.0\n' ;;
  *) exit 1 ;;
esac
`, stateRoot)
	if err := os.WriteFile(dockerPath, []byte(dockerScript), 0o700); err != nil {
		t.Fatal(err)
	}
	appEnvironment := strings.Join([]string{
		"LEAPVIEW_PRODUCTION=true",
		"LEAPVIEW_HOME=/var/lib/leapview/home",
		"LEAPVIEW_ENVIRONMENT=prod",
		"LEAPVIEW_MANAGED_DATA_BACKEND=local",
		"LEAPVIEW_RECOVERY_QUALIFICATION_ENABLED=true",
		"LEAPVIEW_RECOVERY_QUALIFICATION_EXECUTION_ENVIRONMENT=host",
		"LEAPVIEW_RECOVERY_QUALIFICATION_CRON=@hourly",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, appEnvName), []byte(appEnvironment), 0o600); err != nil {
		t.Fatal(err)
	}
	deploymentEnvironment := "COMPOSE_HTTPS=0\nLEAPVIEW_IMAGE=" + identity.Image + "\n"
	if err := os.WriteFile(filepath.Join(root, deploymentEnvName), []byte(deploymentEnvironment), 0o600); err != nil {
		t.Fatal(err)
	}
	controller, err := New(Options{Root: root, DockerBin: dockerPath})
	if err != nil {
		t.Fatal(err)
	}
	// The first canonical host run installs immutable schedule revisions.
	if err := controller.runScheduledRecoveryQualification(t.Context(), build); err != nil {
		t.Fatal(err)
	}
	store, err = platform.Open(t.Context(), filepath.Join(home, "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `
		UPDATE recovery_qualification_schedules
		SET next_run_at = ?
		WHERE operation IN ('upgrade', 'rollback') AND closed_at IS NULL
	`, time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05.000000000Z")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// A normal timer invocation now claims and executes both FAI-514 owners.
	if err := controller.runScheduledRecoveryQualification(t.Context(), build); err != nil {
		t.Fatal(err)
	}
	store, err = platform.Open(t.Context(), filepath.Join(home, "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	occurrences, err := refreshmodule.NewRecoveryRepository(store.SQLDB()).Occurrences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	transitionCount := 0
	transitionOperations := map[string]bool{}
	for _, occurrence := range occurrences {
		if occurrence.Operation != refreshmodule.OperationUpgrade && occurrence.Operation != refreshmodule.OperationRollback {
			continue
		}
		transitionCount++
		transitionOperations[occurrence.Operation] = true
		if occurrence.Status != refreshmodule.StatusSucceeded || occurrence.EvidenceStatus != "published" || len(occurrence.Evidence) != 1 {
			t.Fatalf("owner-validated transition occurrence = %#v", occurrence)
		}
		if occurrence.ReadinessCompletedAt.Before(occurrence.ReadinessStartedAt) || occurrence.ReadinessStartedAt.IsZero() {
			t.Fatalf("owner readiness phase was not persisted: %#v", occurrence)
		}
	}
	if transitionCount < 2 || !transitionOperations[refreshmodule.OperationUpgrade] || !transitionOperations[refreshmodule.OperationRollback] {
		t.Fatalf("transition occurrences = %d (%v), want due upgrade and rollback", transitionCount, transitionOperations)
	}
}

func writeScheduledQualificationBundle(root string, policy []byte) error {
	files := []string{
		"Caddyfile", "README.md", "QUALIFICATION.md", "compose.https.yaml", "compose.yaml",
		"deployment.env.example", "leapview.env.example", "release-transition-policy.json",
		filepath.Join("qualification", "Dockerfile.authoring-client"), filepath.Join("qualification", "authoring-worker.mjs"),
		filepath.Join("qualification", "browser.mjs"), filepath.Join("qualification", "bun.lock"), filepath.Join("qualification", "package.json"),
		filepath.Join("qualification", "performance-policy.json"), filepath.Join("qualification", "performance.mjs"),
	}
	for _, relative := range files {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		contents := []byte(relative + "\n")
		if relative == "release-transition-policy.json" {
			contents = policy
		}
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func writeScheduledTransitionEvidence(path string, predecessor, candidate compatibility.ReleaseIdentity, policyVersion, policyDigest string) error {
	state := compatibility.TransitionQualificationState{
		InstanceID: "lvinst_scheduled_transition", Environment: "qualification", CanonicalOrigin: "https://qualification.example",
		PrincipalID: "principal_qualification", PrincipalKind: "user", PrincipalEmail: "qualification@example.com", PrincipalName: "Qualification",
	}
	evidence := compatibility.TransitionQualificationEvidence{
		SchemaVersion: 1, PolicyVersion: policyVersion, PolicySHA256: policyDigest,
		RecoveryPointAt: time.Now().UTC().Add(-time.Minute), Predecessor: predecessor, Candidate: candidate,
		StateBeforeUpgrade: strings.Repeat("d", 64), StateAfterUpgrade: strings.Repeat("d", 64), StateAfterRollback: strings.Repeat("d", 64),
		InventoryBefore: state, InventoryAfterUpgrade: state, InventoryAfterRollback: state,
		UpgradeResult: "success", RollbackResult: "success", PreservationVerified: true,
	}
	document, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	return os.WriteFile(path, document, 0o600)
}
