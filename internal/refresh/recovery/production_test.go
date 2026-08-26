package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/flidai/leapview/internal/platform/compatibility"
)

func TestProductionRecoveryQualificationAdaptersExecuteOwnerWorkflows(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "instance")
	work := filepath.Join(root, "work")
	evidenceRoot := filepath.Join(home, "artifacts", "recovery-qualification")
	for _, directory := range []string{home, work, evidenceRoot, filepath.Join(root, "bundle")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := platform.Open(t.Context(), filepath.Join(home, "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindInstanceEnvironment(t.Context(), "qualification"); err != nil {
		t.Fatal(err)
	}
	instanceID, err := store.InstanceID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	candidate := compatibility.ReleaseIdentity{
		ReleaseID: "v0.2.0-rc.2", Version: "0.2.0-rc.2", SourceRevision: strings.Repeat("2", 40),
		Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64), Distribution: "public", Platform: "linux/amd64",
	}
	base, err := compatibility.EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	template, err := compatibility.EmbeddedCandidateTransitionTemplate()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := base.BindCandidateWithTemplate(candidate, []string{"linux/amd64", "linux/arm64"}, template)
	if err != nil {
		t.Fatal(err)
	}
	predecessor, ok := policy.ReleaseByID(template.PredecessorRelease)
	if !ok {
		t.Fatal("bound policy lost predecessor")
	}
	policyDocument, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyDigest := fmt.Sprintf("%x", sha256.Sum256(policyDocument))
	if err := os.WriteFile(filepath.Join(root, "bundle", "release-transition-policy.json"), policyDocument, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, relative := range transitionQualificationBundleFiles {
		path := filepath.Join(root, "bundle", relative)
		if relative == "release-transition-policy.json" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(relative+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	transitionEvidencePath := filepath.Join(root, "transition-evidence.json")
	if err := writeProductionTransitionEvidence(transitionEvidencePath, candidate, predecessor.IdentityForPlatform("linux/amd64"), policy.PolicyVersion, policyDigest); err != nil {
		t.Fatal(err)
	}
	build := buildinfo.Identity{Version: candidate.Version, Revision: candidate.SourceRevision, BuildTime: "2026-08-26T00:00:00Z"}
	buildDocument, err := json.Marshal(build)
	if err != nil {
		t.Fatal(err)
	}
	controllerPath := filepath.Join(root, "leapviewctl")
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
`, string(buildDocument), transitionEvidencePath)
	if err := os.WriteFile(controllerPath, []byte(controllerScript), 0o700); err != nil {
		t.Fatal(err)
	}
	dockerPath := filepath.Join(root, "docker")
	if err := os.WriteFile(dockerPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := ProductionQualificationConfig{
		HomeDir: home, DBPath: filepath.Join(home, "leapview.db"), InstanceID: instanceID, Environment: "qualification",
		BuildIdentity:   build,
		ReleaseIdentity: candidate, StorageTopology: platform.InstanceBackupStorageTopology{
			ControlPlane: "local", ManagedData: "local", DuckLake: "local", ExternalStores: []platform.InstanceBackupExternalStoreReference{},
		},
		TransitionPolicy: policy, PolicySHA256: policyDigest, WorkRoot: work, EvidenceRoot: evidenceRoot,
		ControllerPath: controllerPath, ContainerRuntime: dockerPath, BundleRoot: filepath.Join(root, "bundle"),
		PredecessorImage: predecessor.IdentityForPlatform("linux/amd64").Image,
		Cron:             "@hourly", Timezone: "UTC", StaleAfter: 24 * time.Hour,
	}
	definitions, err := config.ProductionDefinitions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 4 {
		t.Fatalf("production definitions = %d", len(definitions))
	}
	unavailableRuntime := config
	unavailableRuntime.ContainerRuntime = filepath.Join(root, "missing-docker")
	if _, err := unavailableRuntime.ProductionDefinitions(t.Context()); err == nil || !strings.Contains(err.Error(), "accessible container runtime") {
		t.Fatalf("missing container capability was not rejected clearly: %v", err)
	}
	mismatchedController := config
	mismatchedController.ControllerPath = filepath.Join(root, "wrong-leapviewctl")
	if err := os.WriteFile(mismatchedController.ControllerPath, []byte("#!/bin/sh\nprintf '{}\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := mismatchedController.ProductionDefinitions(t.Context()); err == nil || !strings.Contains(err.Error(), "identity does not match") {
		t.Fatalf("mismatched leapviewctl identity was not rejected clearly: %v", err)
	}
	adapters := config.ProductionAdapters()
	for index, definition := range definitions {
		occurrence := Occurrence{
			ID: fmt.Sprintf("qualification-%d", index), Fence: Fence{Owner: "test", Generation: 1},
			Operation: definition.Operation, ArtifactIdentity: definition.ArtifactIdentity,
			PolicyVersion: definition.PolicyVersion, PolicySHA256: definition.PolicySHA256, TargetScope: definition.TargetScope,
		}
		ctx := context.WithValue(t.Context(), phaseRecorderContextKey{}, phaseRecorder(func(_, _ string) error { return nil }))
		outcome, err := adapters[definition.Operation].Execute(ctx, occurrence)
		if err != nil {
			t.Fatalf("execute %s owner: %v", definition.Operation, err)
		}
		validated, err := validateScenarioArtifacts(Occurrence{
			Operation: definition.Operation, ArtifactIdentity: definition.ArtifactIdentity,
			PolicyVersion: definition.PolicyVersion, PolicySHA256: definition.PolicySHA256, TargetScope: definition.TargetScope,
		}, outcome.Artifacts)
		if err != nil {
			t.Fatalf("validate %s owner evidence: %v", definition.Operation, err)
		}
		if len(validated) == 0 || recoveryPointFromArtifacts(validated).IsZero() {
			t.Fatalf("%s owner did not produce recovery evidence", definition.Operation)
		}
		if definition.Operation == OperationBackup {
			original, err := os.ReadFile(outcome.Artifacts[0].Path)
			if err != nil {
				t.Fatal(err)
			}
			var invalidTopology platform.InstanceBackupManifestV2
			if err := json.Unmarshal(original, &invalidTopology); err != nil {
				t.Fatal(err)
			}
			invalidTopology.StorageTopology.ManagedData = "s3"
			invalidDocument, err := json.Marshal(invalidTopology)
			if err != nil {
				t.Fatal(err)
			}
			invalidPath := filepath.Join(filepath.Dir(outcome.Artifacts[0].Path), "invalid-topology.json")
			if err := os.WriteFile(invalidPath, invalidDocument, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := validateScenarioArtifacts(Occurrence{
				Operation: definition.Operation, ArtifactIdentity: definition.ArtifactIdentity,
				PolicyVersion: definition.PolicyVersion, PolicySHA256: definition.PolicySHA256, TargetScope: definition.TargetScope,
			}, []EvidenceArtifact{{Kind: EvidenceBackupManifestV2, Path: invalidPath}}); err == nil {
				t.Fatal("ledger accepted owner evidence with invalid external topology")
			}
			malformedPath := filepath.Join(filepath.Dir(outcome.Artifacts[0].Path), "malformed-manifest.json")
			if err := os.WriteFile(malformedPath, append(original, []byte(`{"unexpected":true}`)...), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := validateScenarioArtifacts(Occurrence{
				Operation: definition.Operation, ArtifactIdentity: definition.ArtifactIdentity,
				PolicyVersion: definition.PolicyVersion, PolicySHA256: definition.PolicySHA256, TargetScope: definition.TargetScope,
			}, []EvidenceArtifact{{Kind: EvidenceBackupManifestV2, Path: malformedPath}}); err == nil {
				t.Fatal("ledger accepted malformed owner manifest")
			}
		}
		if outcome.cleanup != nil {
			if err := outcome.cleanup(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if definitions[0].TargetScope != "instance:"+instanceID {
		t.Fatalf("backup target = %q", definitions[0].TargetScope)
	}
}

func TestQualificationWorkspaceReclaimsOnlySupersededOccurrenceGeneration(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		"occurrence-a-generation-1",
		"occurrence-a-generation-3",
		"occurrence-b-generation-1",
	} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	workspace, err := prepareQualificationWorkspace(root, Occurrence{
		ID: "occurrence-a", Fence: Fence{Owner: "worker", Generation: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if workspace != filepath.Join(root, "occurrence-a-generation-2") {
		t.Fatalf("workspace = %q", workspace)
	}
	if _, err := os.Stat(filepath.Join(root, "occurrence-a-generation-1")); !os.IsNotExist(err) {
		t.Fatalf("superseded crash workspace remains: %v", err)
	}
	for _, name := range []string{"occurrence-a-generation-2", "occurrence-a-generation-3", "occurrence-b-generation-1"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("active or unrelated workspace %s was removed: %v", name, err)
		}
	}
}

func TestQualificationWorkspaceSweepReclaimsCrashedAndTerminalButPreservesLiveLease(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{
		"active-generation-1", "crashed-generation-2", "terminal-generation-3", "operator-data",
	} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	occurrences := []Occurrence{
		{ID: "active", Status: StatusRunning, Fence: Fence{Owner: "worker", Generation: 1}, LeaseExpiresAt: now.Add(time.Minute)},
		{ID: "crashed", Status: StatusRunning, Fence: Fence{Owner: "worker", Generation: 2}, LeaseExpiresAt: now.Add(-time.Second)},
		{ID: "terminal", Status: StatusSucceeded, Fence: Fence{Owner: "worker", Generation: 3}, LeaseExpiresAt: now.Add(time.Hour)},
	}
	// A restarted lifecycle performs this sweep before claiming new work.
	if err := ReclaimQualificationWorkspaces(root, occurrences, now); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"active-generation-1", "operator-data"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("live or unowned workspace %s was removed: %v", name, err)
		}
	}
	for _, name := range []string{"crashed-generation-2", "terminal-generation-3"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("abandoned workspace %s remains: %v", name, err)
		}
	}
}

func writeProductionTransitionEvidence(path string, candidate, predecessor compatibility.ReleaseIdentity, policyVersion, policySHA256 string) error {
	state := compatibility.TransitionQualificationState{
		InstanceID: "lvinst_isolated_transition", Environment: "qualification", CanonicalOrigin: "https://qualification.example",
		PrincipalID: "principal_qualification", PrincipalKind: "user", PrincipalEmail: "qualification@example.com", PrincipalName: "Qualification",
	}
	report := compatibility.TransitionQualificationEvidence{
		SchemaVersion: 1, PolicyVersion: policyVersion, RecoveryPointAt: time.Now().UTC().Add(-time.Minute),
		Predecessor: predecessor, Candidate: candidate, PolicySHA256: policySHA256,
		StateBeforeUpgrade: strings.Repeat("d", 64), StateAfterUpgrade: strings.Repeat("d", 64), StateAfterRollback: strings.Repeat("d", 64),
		InventoryBefore: state, InventoryAfterUpgrade: state, InventoryAfterRollback: state,
		UpgradeResult: "success", RollbackResult: "success", PreservationVerified: true,
	}
	document, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return os.WriteFile(path, document, 0o600)
}
