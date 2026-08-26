package recovery

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/flidai/leapview/internal/platform/compatibility"
)

type ProductionQualificationConfig struct {
	HomeDir          string
	DBPath           string
	InstanceID       string
	Environment      string
	BuildIdentity    buildinfo.Identity
	ReleaseIdentity  compatibility.ReleaseIdentity
	StorageTopology  platform.InstanceBackupStorageTopology
	StorageEvidence  StorageEvidenceProvider
	TransitionPolicy *compatibility.Policy
	PolicySHA256     string
	WorkRoot         string
	EvidenceRoot     string
	ControllerPath   string
	ContainerRuntime string
	BundleRoot       string
	PredecessorImage string
	Cron             string
	Timezone         string
	StaleAfter       time.Duration
}

type StorageQualificationEvidence struct {
	Topology         platform.InstanceBackupStorageTopology
	ExternalEvidence map[string]string
}

type StorageEvidenceProvider func(context.Context) (StorageQualificationEvidence, error)

type qualificationPhaseEvent struct {
	Operation string `json:"operation"`
	Event     string `json:"event"`
}

type OSQualificationCommand struct{}

func (OSQualificationCommand) RunWithPhases(
	ctx context.Context,
	executable string,
	observe func(string, string) error,
	arguments ...string,
) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Stdout = ioDiscard{}
	command.Stderr = ioDiscard{}
	command.ExtraFiles = []*os.File{writer}
	command.Env = append(os.Environ(), "LEAPVIEW_QUALIFICATION_PHASE_FD=3")
	if err := command.Start(); err != nil {
		writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return err
	}
	readErr := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(bufio.NewReader(reader))
		for {
			var event qualificationPhaseEvent
			if err := decoder.Decode(&event); err != nil {
				if err == io.EOF {
					readErr <- nil
				} else {
					readErr <- fmt.Errorf("decode qualification phase event: %w", err)
				}
				return
			}
			if observe == nil {
				readErr <- fmt.Errorf("qualification phase observer is required")
				return
			}
			if err := observe(event.Operation, event.Event); err != nil {
				readErr <- err
				return
			}
		}
	}()
	waitErr := command.Wait()
	phaseErr := <-readErr
	if waitErr != nil || phaseErr != nil {
		return errorsJoin(waitErr, phaseErr)
	}
	return nil
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
	if config.BuildIdentity.Development || config.BuildIdentity.Dirty ||
		strings.TrimSpace(config.BuildIdentity.Version) == "" || strings.TrimSpace(config.BuildIdentity.Revision) == "" ||
		strings.TrimPrefix(config.BuildIdentity.Version, "v") != strings.TrimPrefix(config.ReleaseIdentity.Version, "v") ||
		config.BuildIdentity.Revision != config.ReleaseIdentity.SourceRevision {
		return fmt.Errorf("production recovery qualification requires exact released build identity")
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

func (config ProductionQualificationConfig) validateRuntime(ctx context.Context) error {
	if err := config.validate(); err != nil {
		return err
	}
	controller, err := os.Stat(config.ControllerPath)
	if err != nil || !controller.Mode().IsRegular() || controller.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("production recovery qualification controller %s must be a readable executable", config.ControllerPath)
	}
	identityCommand := exec.CommandContext(ctx, config.ControllerPath, "version", "--json")
	var identityOutput strings.Builder
	identityCommand.Stdout = &identityOutput
	identityCommand.Stderr = ioDiscard{}
	if err := identityCommand.Run(); err != nil {
		return fmt.Errorf("production recovery qualification controller identity is unavailable: %w", err)
	}
	controllerVersion := struct {
		Product string `json:"product"`
		buildinfo.Identity
	}{}
	decoder := json.NewDecoder(strings.NewReader(identityOutput.String()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&controllerVersion); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		(controllerVersion.Product != "" && controllerVersion.Product != "leapviewctl") || controllerVersion.Identity != config.BuildIdentity {
		return fmt.Errorf("production recovery qualification controller identity does not match the admitted release")
	}
	bundle, err := os.Stat(config.BundleRoot)
	if err != nil || !bundle.IsDir() {
		return fmt.Errorf("production recovery qualification release bundle %s is unavailable", config.BundleRoot)
	}
	for _, relative := range transitionQualificationBundleFiles {
		info, err := os.Stat(filepath.Join(config.BundleRoot, relative))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("production recovery qualification release bundle is missing %s", filepath.ToSlash(relative))
		}
	}
	for label, root := range map[string]string{"work root": config.WorkRoot, "evidence root": config.EvidenceRoot} {
		probe, err := os.CreateTemp(root, ".qualification-permission-*")
		if err != nil {
			return fmt.Errorf("production recovery qualification %s is not writable: %w", label, err)
		}
		name := probe.Name()
		closeErr := probe.Close()
		removeErr := os.Remove(name)
		if err := errorsJoin(closeErr, removeErr); err != nil {
			return fmt.Errorf("production recovery qualification %s cannot create and remove files: %w", label, err)
		}
	}
	runtimePath := strings.TrimSpace(config.ContainerRuntime)
	if runtimePath == "" {
		runtimePath = "docker"
	}
	command := exec.CommandContext(ctx, runtimePath, "version", "--format", "{{.Server.Version}}")
	command.Stdout = ioDiscard{}
	command.Stderr = ioDiscard{}
	if err := command.Run(); err != nil {
		return fmt.Errorf("production recovery qualification requires an accessible container runtime %s: %w", runtimePath, err)
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
func (config ProductionQualificationConfig) ProductionDefinitions(ctx context.Context) ([]Definition, error) {
	if err := config.validateRuntime(ctx); err != nil {
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
	if err := adapter.config.validateRuntime(ctx); err != nil {
		return ScenarioOutcome{}, NewFailure("qualification_configuration_invalid", err.Error())
	}
	work, err := prepareQualificationRunDirectory(adapter.config.WorkRoot, occurrence)
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
	if err := adapter.config.validateRuntime(ctx); err != nil {
		return ScenarioOutcome{}, NewFailure("qualification_configuration_invalid", err.Error())
	}
	policyDigest, err := digestFile(filepath.Join(adapter.config.BundleRoot, "release-transition-policy.json"))
	if err != nil {
		return ScenarioOutcome{}, NewFailure("qualification_policy_unavailable", "candidate transition policy is unavailable")
	}
	if policyDigest != adapter.config.PolicySHA256 || occurrence.PolicySHA256 != adapter.config.PolicySHA256 {
		return ScenarioOutcome{}, NewFailure("qualification_policy_mismatch", "candidate transition policy does not match the scheduled immutable policy")
	}
	work, err := prepareQualificationRunDirectory(adapter.config.WorkRoot, occurrence)
	if err != nil {
		return ScenarioOutcome{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(work)
		}
	}()
	runner := OSQualificationCommand{}
	phaseRunner := runner
	bundle, err := prepareTransitionQualificationBundle(work, adapter.config)
	if err != nil {
		return ScenarioOutcome{}, NewFailure("qualification_bundle_invalid", err.Error())
	}
	observed := map[string]map[string]bool{
		OperationUpgrade:  {},
		OperationRollback: {},
	}
	err = phaseRunner.RunWithPhases(ctx, adapter.config.ControllerPath, func(operation, event string) error {
		if operation != OperationUpgrade && operation != OperationRollback {
			return fmt.Errorf("unexpected transition qualification operation %q", operation)
		}
		if event != PhaseStarted && event != PhaseCompleted {
			return fmt.Errorf("unexpected transition qualification phase event %q", event)
		}
		if observed[operation][event] {
			return fmt.Errorf("duplicate transition qualification phase %s/%s", operation, event)
		}
		if event == PhaseCompleted && !observed[operation][PhaseStarted] {
			return fmt.Errorf("transition qualification phase %s completed before start", operation)
		}
		observed[operation][event] = true
		if operation != occurrence.Operation {
			return nil
		}
		return recordQualificationPhase(ctx, PhaseReadiness, event)
	},
		"qualify", "installed-candidate", "--bundle", bundle,
		"--evidence-dir", work, "--previous-image", adapter.config.PredecessorImage,
		"--require-release-transition",
	)
	if err != nil {
		return ScenarioOutcome{}, err
	}
	for _, operation := range []string{OperationUpgrade, OperationRollback} {
		if !observed[operation][PhaseStarted] || !observed[operation][PhaseCompleted] {
			return ScenarioOutcome{}, NewFailure("qualification_phase_incomplete", "transition qualification omitted required owner phase boundaries")
		}
	}
	keep = true
	return ScenarioOutcome{Artifacts: []EvidenceArtifact{{
		Kind: EvidenceTransitionQualification, Path: filepath.Join(work, "transition-qualification.json"),
	}}, cleanup: func() error { return os.RemoveAll(work) }}, nil
}

var transitionQualificationBundleFiles = []string{
	"Caddyfile",
	"README.md",
	"QUALIFICATION.md",
	"compose.https.yaml",
	"compose.yaml",
	"deployment.env.example",
	"leapview.env.example",
	"leapviewctl",
	"release-transition-policy.json",
	filepath.Join("qualification", "Dockerfile.authoring-client"),
	filepath.Join("qualification", "authoring-worker.mjs"),
	filepath.Join("qualification", "browser.mjs"),
	filepath.Join("qualification", "bun.lock"),
	filepath.Join("qualification", "package.json"),
	filepath.Join("qualification", "performance-policy.json"),
	filepath.Join("qualification", "performance.mjs"),
}

func prepareTransitionQualificationBundle(work string, config ProductionQualificationConfig) (string, error) {
	bundle := filepath.Join(work, "release-bundle")
	if err := os.Mkdir(bundle, 0o700); err != nil {
		return "", err
	}
	for _, relative := range transitionQualificationBundleFiles {
		source := filepath.Join(config.BundleRoot, relative)
		destination := filepath.Join(bundle, relative)
		info, err := os.Stat(source)
		if err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("required qualification bundle file %s is unavailable", filepath.ToSlash(relative))
		}
		if err := copyQualificationBundleFile(source, destination, info.Mode().Perm()); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(bundle, "image-reference.txt"), []byte(config.ReleaseIdentity.Image+"\n"), 0o600); err != nil {
		return "", err
	}
	identity, err := json.MarshalIndent(config.BuildIdentity, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(bundle, "release-identity.json"), append(identity, '\n'), 0o600); err != nil {
		return "", err
	}
	if err := writeQualificationBundleChecksums(bundle); err != nil {
		return "", err
	}
	return bundle, nil
}

func copyQualificationBundleFile(source, destination string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	return errorsJoin(copyErr, syncErr, closeErr)
}

func writeQualificationBundleChecksums(root string) error {
	var paths []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() == "SHA256SUMS" {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("qualification bundle contains an unsupported entry")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	}); err != nil {
		return err
	}
	sort.Strings(paths)
	var document strings.Builder
	for _, relative := range paths {
		digest, err := digestFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return err
		}
		fmt.Fprintf(&document, "%s  ./%s\n", digest, relative)
	}
	return os.WriteFile(filepath.Join(root, "SHA256SUMS"), []byte(document.String()), 0o600)
}

func prepareQualificationRunDirectory(root string, occurrence Occurrence) (string, error) {
	if occurrence.ID == "" || occurrence.Fence.Generation <= 0 {
		return "", fmt.Errorf("production recovery qualification requires an occurrence-owned fenced run directory")
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
	runDirectory := filepath.Join(root, fmt.Sprintf("%s%d", prefix, occurrence.Fence.Generation))
	if err := os.Mkdir(runDirectory, 0o700); err != nil {
		return "", err
	}
	return runDirectory, nil
}

// ReclaimQualificationRunDirectories removes only ledger-owned directories whose
// occurrence has no live execution lease. Unknown entries are retained so the
// sweep cannot delete operator or future-version data.
func ReclaimQualificationRunDirectories(root string, occurrences []Occurrence, now time.Time) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	owned := make(map[string]bool)
	for _, occurrence := range occurrences {
		if occurrence.ID == "" || occurrence.Fence.Generation <= 0 {
			continue
		}
		name := fmt.Sprintf("%s-generation-%d", occurrence.ID, occurrence.Fence.Generation)
		active := (occurrence.Status == StatusClaimed || occurrence.Status == StatusRunning) &&
			occurrence.LeaseExpiresAt.After(now)
		owned[name] = active
		prefix := occurrence.ID + "-generation-"
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			if _, err := strconv.ParseInt(strings.TrimPrefix(entry.Name(), prefix), 10, 64); err == nil {
				if entry.Name() != name {
					owned[entry.Name()] = false
				}
			}
		}
	}
	removed := false
	for _, entry := range entries {
		active, recognized := owned[entry.Name()]
		if !recognized || active || !entry.IsDir() {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
		removed = true
	}
	if !removed {
		return nil
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
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
