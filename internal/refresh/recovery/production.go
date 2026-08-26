package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/compatibility"
)

type ProductionQualificationConfig struct {
	HomeDir          string
	DBPath           string
	InstanceID       string
	Environment      string
	ReleaseIdentity  compatibility.ReleaseIdentity
	StorageTopology  platform.InstanceBackupStorageTopology
	StorageEvidence  StorageEvidenceProvider
	TransitionPolicy *compatibility.Policy
	PolicySHA256     string
	WorkRoot         string
	EvidenceRoot     string
	ControllerPath   string
	BundleRoot       string
	PredecessorImage string
	Cron             string
	Timezone         string
	StaleAfter       time.Duration
	Command          QualificationCommand
}

type StorageQualificationEvidence struct {
	Topology         platform.InstanceBackupStorageTopology
	ExternalEvidence map[string]string
}

type StorageEvidenceProvider func(context.Context) (StorageQualificationEvidence, error)

type QualificationCommand interface {
	Run(context.Context, string, ...string) error
}

type OSQualificationCommand struct{}

func (OSQualificationCommand) Run(ctx context.Context, executable string, arguments ...string) error {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Stdout = ioDiscard{}
	command.Stderr = ioDiscard{}
	return command.Run()
}

// ioDiscard avoids retaining subprocess output that can contain topology or
// credentials. The command's reviewed evidence files are the durable output.
type ioDiscard struct{}

func (ioDiscard) Write(value []byte) (int, error) { return len(value), nil }

func (config ProductionQualificationConfig) validate() error {
	for label, value := range map[string]string{
		"home": config.HomeDir, "database": config.DBPath, "instance": config.InstanceID, "environment": config.Environment,
		"work root": config.WorkRoot, "evidence root": config.EvidenceRoot,
		"controller": config.ControllerPath, "bundle": config.BundleRoot, "predecessor": config.PredecessorImage,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("production recovery qualification %s is required", label)
		}
	}
	if err := ValidateArtifactIdentity(config.ReleaseIdentity.Image); err != nil {
		return err
	}
	if err := ValidateArtifactIdentity(config.PredecessorImage); err != nil {
		return fmt.Errorf("production recovery predecessor: %w", err)
	}
	if config.TransitionPolicy == nil {
		return fmt.Errorf("production recovery transition policy is required")
	}
	if err := config.TransitionPolicy.Validate(); err != nil {
		return err
	}
	if !validSHA256(config.PolicySHA256) {
		return fmt.Errorf("production recovery qualification policy SHA-256 is required")
	}
	for label, value := range map[string]string{"home": config.HomeDir, "database": config.DBPath, "work root": config.WorkRoot, "evidence root": config.EvidenceRoot, "bundle": config.BundleRoot} {
		absolute, err := filepath.Abs(value)
		if err != nil || absolute != filepath.Clean(value) {
			return fmt.Errorf("production recovery qualification %s path must be absolute and canonical", label)
		}
	}
	if pathWithinQualification(config.HomeDir, config.WorkRoot) {
		return fmt.Errorf("production recovery qualification work root must be outside the instance home")
	}
	return nil
}

func (config ProductionQualificationConfig) storageEvidence(ctx context.Context) (StorageQualificationEvidence, error) {
	if config.StorageEvidence != nil {
		evidence, err := config.StorageEvidence(ctx)
		if err != nil {
			return StorageQualificationEvidence{}, err
		}
		if evidence.ExternalEvidence == nil {
			evidence.ExternalEvidence = map[string]string{}
		}
		return evidence, nil
	}
	return StorageQualificationEvidence{Topology: config.StorageTopology, ExternalEvidence: map[string]string{}}, nil
}

func pathWithinQualification(parent, child string) bool {
	relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// ProductionDefinitions registers the four owner workflows against the exact
// running release, policy, and durable instance identity.
func (config ProductionQualificationConfig) ProductionDefinitions(_ context.Context) ([]Definition, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	cron := config.Cron
	if strings.TrimSpace(cron) == "" {
		cron = "0 3 * * *"
	}
	timezone := config.Timezone
	if strings.TrimSpace(timezone) == "" {
		timezone = "UTC"
	}
	staleAfter := config.StaleAfter
	if staleAfter == 0 {
		staleAfter = 36 * time.Hour
	}
	definitions := make([]Definition, 0, 4)
	for _, operation := range []string{OperationBackup, OperationRestore, OperationUpgrade, OperationRollback} {
		targetScope := "instance:" + config.InstanceID
		if operation == OperationUpgrade || operation == OperationRollback {
			targetScope = "release:" + config.ReleaseIdentity.ReleaseID
		}
		definitions = append(definitions, Definition{
			ScheduleID: "ubdr-production-" + operation, Scenario: "managed-instance-qualification", Operation: operation,
			PolicyVersion: config.TransitionPolicy.PolicyVersion, PolicySHA256: config.PolicySHA256, TargetScope: targetScope,
			ArtifactIdentity: config.ReleaseIdentity.Image, Cron: cron, Timezone: timezone,
			StaleAfter: staleAfter, Enabled: true,
		})
	}
	return definitions, nil
}

func (config ProductionQualificationConfig) ProductionAdapters() map[string]ScenarioAdapter {
	adapters := map[string]ScenarioAdapter{}
	for _, operation := range []string{OperationBackup, OperationRestore} {
		adapters[operation] = productionBackupRestoreAdapter{config: config, operation: operation}
	}
	for _, operation := range []string{OperationUpgrade, OperationRollback} {
		adapters[operation] = productionTransitionAdapter{config: config}
	}
	return adapters
}

type productionBackupRestoreAdapter struct {
	config    ProductionQualificationConfig
	operation string
}

func (adapter productionBackupRestoreAdapter) Execute(ctx context.Context, occurrence Occurrence) (ScenarioOutcome, error) {
	if err := adapter.config.validate(); err != nil {
		return ScenarioOutcome{}, NewFailure("qualification_configuration_invalid", err.Error())
	}
	work, err := prepareQualificationWorkspace(adapter.config.WorkRoot, occurrence)
	if err != nil {
		return ScenarioOutcome{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(work)
		}
	}()
	storage, err := adapter.config.storageEvidence(ctx)
	if err != nil {
		return ScenarioOutcome{}, NewFailure("external_recovery_evidence_invalid", err.Error())
	}
	archive := filepath.Join(work, "instance.tar.gz")
	excluded := []string{}
	if pathWithinQualification(adapter.config.HomeDir, adapter.config.EvidenceRoot) {
		relative, err := filepath.Rel(adapter.config.HomeDir, adapter.config.EvidenceRoot)
		if err != nil {
			return ScenarioOutcome{}, err
		}
		excluded = append(excluded, filepath.ToSlash(relative))
	}
	if err := platform.BackupInstance(ctx, platform.InstanceBackupOptions{
		HomeDir: adapter.config.HomeDir, DBPath: adapter.config.DBPath, OutPath: archive,
		ExcludeRelativePaths: excluded,
		Environment:          adapter.config.Environment, ReleaseIdentity: adapter.config.ReleaseIdentity,
		StorageTopology: storage.Topology, TransitionPolicy: adapter.config.TransitionPolicy,
		TransitionPolicySHA256: adapter.config.PolicySHA256,
	}); err != nil {
		return ScenarioOutcome{}, err
	}
	manifestDocument, _, err := platform.ReadInstanceBackupManifestDocument(archive)
	if err != nil {
		return ScenarioOutcome{}, err
	}
	manifestPath := filepath.Join(work, "leapview-backup.json")
	if err := os.WriteFile(manifestPath, manifestDocument, 0o600); err != nil {
		return ScenarioOutcome{}, err
	}
	outcome := ScenarioOutcome{Artifacts: []EvidenceArtifact{{Kind: EvidenceBackupManifestV2, Path: manifestPath}}, cleanup: func() error { return os.RemoveAll(work) }}
	if adapter.operation == OperationBackup {
		keep = true
		return outcome, nil
	}
	target := filepath.Join(work, "restored")
	preflightOptions := platform.InstanceRestorePreflightOptions{
		ArchivePath: archive, TargetHomeDir: target, ExpectedEnvironment: adapter.config.Environment,
		TargetReleaseIdentity: adapter.config.ReleaseIdentity, TargetStorageTopology: storage.Topology,
		ExternalEvidence:  storage.ExternalEvidence,
		ExclusiveLockHeld: true, RequireExclusiveLock: true, TransitionPolicy: adapter.config.TransitionPolicy,
		TransitionPolicySHA256: adapter.config.PolicySHA256,
	}
	plan, err := platform.PreflightInstanceRestore(ctx, preflightOptions)
	if err != nil {
		return ScenarioOutcome{}, err
	}
	planDocument, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return ScenarioOutcome{}, err
	}
	planDocument = append(planDocument, '\n')
	planPath := filepath.Join(work, "preflight-report.json")
	if err := os.WriteFile(planPath, planDocument, 0o600); err != nil {
		return ScenarioOutcome{}, err
	}
	if err := recordQualificationPhase(ctx, PhaseRestore, PhaseStarted); err != nil {
		return ScenarioOutcome{}, err
	}
	if err := platform.RestoreInstance(ctx, platform.InstanceRestoreOptions{
		TargetHomeDir: target, BackupPath: archive, ExpectedEnvironment: adapter.config.Environment,
		TargetReleaseIdentity: adapter.config.ReleaseIdentity, TargetStorageTopology: storage.Topology,
		ExternalEvidence:  storage.ExternalEvidence,
		ExclusiveLockHeld: true, RequireExclusiveLock: true, TransitionPolicy: adapter.config.TransitionPolicy,
		TransitionPolicySHA256: adapter.config.PolicySHA256,
		ValidatedPlan:          &plan,
	}); err != nil {
		return ScenarioOutcome{}, err
	}
	restored, err := platform.Open(ctx, filepath.Join(target, "leapview.db"))
	if err != nil {
		return ScenarioOutcome{}, err
	}
	restoredID, identityErr := restored.InstanceID(ctx)
	closeErr := restored.Close()
	expectedTarget := strings.TrimPrefix(occurrence.TargetScope, "instance:")
	if identityErr != nil || closeErr != nil || restoredID != expectedTarget {
		return ScenarioOutcome{}, fmt.Errorf("restored qualification instance identity does not match scheduled target: %w", errorsJoin(identityErr, closeErr))
	}
	if err := recordQualificationPhase(ctx, PhaseRestore, PhaseCompleted); err != nil {
		return ScenarioOutcome{}, err
	}
	outcome.Artifacts = append(outcome.Artifacts, EvidenceArtifact{Kind: EvidenceRestorePreflight, Path: planPath})
	keep = true
	return outcome, nil
}

type productionTransitionAdapter struct{ config ProductionQualificationConfig }

func (adapter productionTransitionAdapter) Execute(ctx context.Context, occurrence Occurrence) (ScenarioOutcome, error) {
	if err := adapter.config.validate(); err != nil {
		return ScenarioOutcome{}, NewFailure("qualification_configuration_invalid", err.Error())
	}
	policyDigest, err := digestFile(filepath.Join(adapter.config.BundleRoot, "release-transition-policy.json"))
	if err != nil {
		return ScenarioOutcome{}, NewFailure("qualification_policy_unavailable", "candidate transition policy is unavailable")
	}
	if policyDigest != adapter.config.PolicySHA256 || occurrence.PolicySHA256 != adapter.config.PolicySHA256 {
		return ScenarioOutcome{}, NewFailure("qualification_policy_mismatch", "candidate transition policy does not match the scheduled immutable policy")
	}
	work, err := prepareQualificationWorkspace(adapter.config.WorkRoot, occurrence)
	if err != nil {
		return ScenarioOutcome{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(work)
		}
	}()
	runner := adapter.config.Command
	if runner == nil {
		runner = OSQualificationCommand{}
	}
	if err := recordQualificationPhase(ctx, PhaseReadiness, PhaseStarted); err != nil {
		return ScenarioOutcome{}, err
	}
	err = runner.Run(ctx, adapter.config.ControllerPath,
		"qualify", "installed-candidate", "--bundle", adapter.config.BundleRoot,
		"--evidence-dir", work, "--previous-image", adapter.config.PredecessorImage,
		"--require-release-transition",
	)
	if err != nil {
		return ScenarioOutcome{}, err
	}
	if err := recordQualificationPhase(ctx, PhaseReadiness, PhaseCompleted); err != nil {
		return ScenarioOutcome{}, err
	}
	keep = true
	return ScenarioOutcome{Artifacts: []EvidenceArtifact{{
		Kind: EvidenceTransitionQualification, Path: filepath.Join(work, "transition-qualification.json"),
	}}, cleanup: func() error { return os.RemoveAll(work) }}, nil
}

func prepareQualificationWorkspace(root string, occurrence Occurrence) (string, error) {
	if occurrence.ID == "" || occurrence.Fence.Generation <= 0 {
		return "", fmt.Errorf("production recovery qualification requires an occurrence-owned fenced workspace")
	}
	prefix := occurrence.ID + "-generation-"
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		generation, err := strconv.ParseInt(strings.TrimPrefix(entry.Name(), prefix), 10, 64)
		if err != nil || generation >= occurrence.Fence.Generation {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return "", err
		}
	}
	workspace := filepath.Join(root, fmt.Sprintf("%s%d", prefix, occurrence.Fence.Generation))
	if err := os.Mkdir(workspace, 0o700); err != nil {
		return "", err
	}
	return workspace, nil
}

func errorsJoin(values ...error) error {
	var messages []string
	for _, value := range values {
		if value != nil {
			messages = append(messages, value.Error())
		}
	}
	if len(messages) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(messages, "; "))
}
