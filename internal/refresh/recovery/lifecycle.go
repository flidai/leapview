package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/compatibility"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

const (
	EvidenceTransitionQualification = "transition-qualification"
	EvidenceBackupManifestV2        = "backup-manifest-v2"
	EvidenceRestorePreflight        = "restore-preflight"
)

type ScenarioOutcome struct {
	RecoveryPointAt    time.Time
	RestoreCompletedAt time.Time
	ReadyAt            time.Time
	Artifacts          []EvidenceArtifact
}

type EvidenceArtifact struct {
	Kind string
	Path string
}

type ScenarioAdapter interface {
	Execute(context.Context, Occurrence) (ScenarioOutcome, error)
}

type ScenarioAdapterFunc func(context.Context, Occurrence) (ScenarioOutcome, error)

func (adapter ScenarioAdapterFunc) Execute(ctx context.Context, occurrence Occurrence) (ScenarioOutcome, error) {
	return adapter(ctx, occurrence)
}

type EvidencePublisher interface {
	Publish(context.Context, Occurrence) error
}

type DefinitionProvider func(context.Context) ([]Definition, error)

type Lifecycle struct {
	Repository       Repository
	Definitions      DefinitionProvider
	Adapters         map[string]ScenarioAdapter
	Publisher        EvidencePublisher
	Clock            refreshschedule.Clock
	WorkerID         string
	Actor            string
	Lease            time.Duration
	BatchSize        int
	ComplianceWindow time.Duration
	EvidenceRoot     string
}

func (lifecycle Lifecycle) Validate() error {
	if lifecycle.Repository == nil || lifecycle.Definitions == nil {
		return fmt.Errorf("recovery qualification repository and definition provider are required")
	}
	if err := validateCanonical("worker id", lifecycle.WorkerID, 256); err != nil {
		return err
	}
	if err := validateCanonical("actor", lifecycle.Actor, 256); err != nil {
		return err
	}
	if lifecycle.Lease <= 0 || lifecycle.ComplianceWindow <= 0 {
		return fmt.Errorf("recovery qualification lease and compliance window must be positive")
	}
	if lifecycle.BatchSize < 1 || lifecycle.BatchSize > 1000 {
		return fmt.Errorf("recovery qualification batch size must be between 1 and 1000")
	}
	if strings.TrimSpace(lifecycle.EvidenceRoot) == "" || !filepath.IsAbs(lifecycle.EvidenceRoot) {
		return fmt.Errorf("recovery qualification evidence root must be absolute")
	}
	return nil
}

func (lifecycle Lifecycle) now() time.Time {
	if lifecycle.Clock == nil {
		return time.Now().UTC()
	}
	return lifecycle.Clock.Now().UTC()
}

// RunOnce reconciles immutable schedule revisions, materializes due work,
// executes owner adapters, publishes exact evidence, and applies retention.
func (lifecycle Lifecycle) RunOnce(ctx context.Context) error {
	if err := lifecycle.Validate(); err != nil {
		return err
	}
	definitions, err := lifecycle.Definitions(ctx)
	if err != nil {
		return err
	}
	now := lifecycle.now()
	for _, definition := range definitions {
		if _, ok := lifecycle.Adapters[definition.Operation]; !ok {
			return fmt.Errorf("recovery qualification operation %s has no scenario adapter", definition.Operation)
		}
	}
	if err := lifecycle.Repository.ReconcileSchedules(ctx, definitions, now); err != nil {
		return err
	}
	if _, err := lifecycle.Repository.EnqueueDue(ctx, now, lifecycle.BatchSize); err != nil {
		return err
	}
	for count := 0; count < lifecycle.BatchSize; count++ {
		claimed, ok, err := lifecycle.Repository.ClaimNext(ctx, ClaimInput{
			WorkerID: lifecycle.WorkerID, Actor: lifecycle.Actor, Now: lifecycle.now(), Lease: lifecycle.Lease,
		})
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if err := lifecycle.execute(ctx, claimed); err != nil {
			return err
		}
	}
	if lifecycle.Publisher != nil {
		for count := 0; count < lifecycle.BatchSize; count++ {
			claimed, ok, err := lifecycle.Repository.ClaimEvidence(ctx, lifecycle.WorkerID+"-publisher", lifecycle.now(), lifecycle.Lease)
			if err != nil {
				return err
			}
			if !ok {
				break
			}
			if err := lifecycle.Publisher.Publish(ctx, claimed); err != nil {
				if failErr := lifecycle.Repository.FailEvidence(ctx, claimed.ID, claimed.EvidenceFence, lifecycle.now(), err); failErr != nil {
					return errors.Join(err, failErr)
				}
				continue
			}
			if err := lifecycle.Repository.PublishEvidence(ctx, claimed.ID, claimed.EvidenceFence, lifecycle.now()); err != nil {
				return err
			}
		}
	}
	_, err = lifecycle.Repository.Retain(ctx, RetentionPolicy{Now: lifecycle.now(), ComplianceWindow: lifecycle.ComplianceWindow})
	return err
}

func (lifecycle Lifecycle) execute(ctx context.Context, occurrence Occurrence) error {
	adapter := lifecycle.Adapters[occurrence.Operation]
	if adapter == nil {
		return lifecycle.Repository.Fail(ctx, occurrence.ID, occurrence.Fence, lifecycle.now(), Result{}, NewFailure("adapter_unavailable", "scheduled qualification adapter is unavailable"))
	}
	started := lifecycle.now()
	if err := lifecycle.Repository.Start(ctx, occurrence.ID, occurrence.Fence, started); err != nil {
		return err
	}
	executeCtx, cancel := context.WithCancel(ctx)
	heartbeatDone := make(chan error, 1)
	go func() {
		interval := lifecycle.Lease / 3
		if interval <= 0 {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-executeCtx.Done():
				heartbeatDone <- nil
				return
			case <-ticker.C:
				if err := lifecycle.Repository.Heartbeat(executeCtx, occurrence.ID, occurrence.Fence, lifecycle.now(), lifecycle.Lease); err != nil {
					heartbeatDone <- err
					cancel()
					return
				}
			}
		}
	}()
	outcome, runErr := adapter.Execute(executeCtx, occurrence)
	cancel()
	if heartbeatErr := <-heartbeatDone; heartbeatErr != nil {
		return heartbeatErr
	}
	completed := lifecycle.now()
	result, resultErr := outcome.result(occurrence, started, completed, lifecycle.EvidenceRoot)
	if resultErr != nil {
		runErr = errors.Join(runErr, NewFailure("invalid_measurement", resultErr.Error()))
		result = Result{}
	}
	if runErr != nil {
		return lifecycle.Repository.Fail(ctx, occurrence.ID, occurrence.Fence, completed, result, runErr)
	}
	return lifecycle.Repository.Complete(ctx, occurrence.ID, occurrence.Fence, completed, result)
}

func (outcome ScenarioOutcome) result(occurrence Occurrence, started, completed time.Time, evidenceRoot string) (Result, error) {
	if completed.Before(started) {
		return Result{}, fmt.Errorf("qualification completion precedes start")
	}
	result := Result{RecoveryPointAt: outcome.RecoveryPointAt}
	if !outcome.RestoreCompletedAt.IsZero() {
		if outcome.RestoreCompletedAt.Before(started) || outcome.RestoreCompletedAt.After(completed) {
			return Result{}, fmt.Errorf("restore completion is outside the persisted execution interval")
		}
		result.RestoreDuration = outcome.RestoreCompletedAt.Sub(started)
	}
	if !outcome.ReadyAt.IsZero() {
		readinessStart := started
		if !outcome.RestoreCompletedAt.IsZero() {
			readinessStart = outcome.RestoreCompletedAt
		}
		if outcome.ReadyAt.Before(readinessStart) || outcome.ReadyAt.After(completed) {
			return Result{}, fmt.Errorf("readiness completion is outside the persisted execution interval")
		}
		result.ReadinessDuration = outcome.ReadyAt.Sub(readinessStart)
	}
	artifacts, err := validateScenarioArtifacts(occurrence, outcome.Artifacts)
	if err != nil {
		return Result{}, err
	}
	for _, artifact := range artifacts {
		reference, err := retainUBDRArtifact(artifact, evidenceRoot)
		if err != nil {
			return Result{}, err
		}
		result.Evidence = append(result.Evidence, reference)
	}
	return result, nil
}

// FileEvidencePublisher verifies that the exact bytes referenced by a
// qualification still exist before the ledger records publication.
type FileEvidencePublisher struct{ Root string }

func (publisher FileEvidencePublisher) Publish(_ context.Context, occurrence Occurrence) error {
	if !filepath.IsAbs(publisher.Root) {
		return NewFailure("evidence_store_invalid", "qualification evidence store is not configured")
	}
	if len(occurrence.Evidence) == 0 {
		return NewFailure("evidence_missing", "qualification produced no publishable evidence")
	}
	for _, reference := range occurrence.Evidence {
		parsed, err := url.Parse(reference.URI)
		if err != nil || parsed.Scheme != "artifact" || parsed.Host != "qualification" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return NewFailure("evidence_uri_invalid", "qualification evidence is not a content-addressed artifact reference")
		}
		name := filepath.Base(parsed.Path)
		if name != reference.SHA256+".json" {
			return NewFailure("evidence_uri_invalid", "qualification evidence artifact name does not match its digest")
		}
		digest, err := digestFile(filepath.Join(publisher.Root, name))
		if err != nil {
			return NewFailure("evidence_unavailable", "qualification evidence file is unavailable")
		}
		if digest != reference.SHA256 {
			return NewFailure("evidence_digest_mismatch", "qualification evidence bytes changed before publication")
		}
	}
	return nil
}

type qualificationState struct {
	InstanceID      string `json:"instanceId"`
	Environment     string `json:"environment"`
	CanonicalOrigin string `json:"canonicalOrigin"`
	PrincipalID     string `json:"principalId"`
	PrincipalKind   string `json:"principalKind"`
	PrincipalEmail  string `json:"principalEmail"`
	PrincipalName   string `json:"principalDisplayName"`
}

func validateScenarioArtifacts(occurrence Occurrence, inputs []EvidenceArtifact) ([]validatedEvidenceArtifact, error) {
	required := map[string]int{}
	switch occurrence.Operation {
	case OperationBackup:
		required[EvidenceBackupManifestV2] = 1
	case OperationRestore:
		required[EvidenceBackupManifestV2] = 1
		required[EvidenceRestorePreflight] = 1
	case OperationUpgrade, OperationRollback:
		required[EvidenceTransitionQualification] = 1
	default:
		return nil, fmt.Errorf("unsupported recovery qualification operation %q", occurrence.Operation)
	}
	if len(inputs) != len(required) {
		return nil, fmt.Errorf("recovery qualification operation %s requires its complete owner evidence set", occurrence.Operation)
	}
	validated := make([]validatedEvidenceArtifact, 0, len(inputs))
	seen := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		if required[input.Kind] != 1 || seen[input.Kind] {
			return nil, fmt.Errorf("recovery qualification operation %s received an unexpected or duplicate %s artifact", occurrence.Operation, input.Kind)
		}
		artifact, err := readUBDRArtifact(input.Kind, input.Path, occurrence.ArtifactIdentity)
		if err != nil {
			return nil, err
		}
		seen[input.Kind] = true
		validated = append(validated, artifact)
	}
	if occurrence.Operation == OperationRestore {
		var manifest platform.InstanceBackupManifestV2
		var plan platform.InstanceRestorePreflightPlan
		for _, artifact := range validated {
			switch artifact.reference.Kind {
			case EvidenceBackupManifestV2:
				if err := json.Unmarshal(artifact.contents, &manifest); err != nil {
					return nil, err
				}
			case EvidenceRestorePreflight:
				if err := json.Unmarshal(artifact.contents, &plan); err != nil {
					return nil, err
				}
			}
		}
		manifestDigest := sha256.Sum256(artifactContents(validated, EvidenceBackupManifestV2))
		if plan.BackupID != manifest.BackupID || plan.ManifestSHA256 != hex.EncodeToString(manifestDigest[:]) {
			return nil, fmt.Errorf("restore preflight evidence does not bind the supplied backup manifest")
		}
	}
	return validated, nil
}

func artifactContents(artifacts []validatedEvidenceArtifact, kind string) []byte {
	for _, artifact := range artifacts {
		if artifact.reference.Kind == kind {
			return artifact.contents
		}
	}
	return nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

// ValidateUBDRArtifact validates the existing UBDR owner report and returns a
// content-bound reference. It deliberately does not invent a parallel report.
func ValidateUBDRArtifact(kind, path string) (EvidenceReference, error) {
	artifact, err := readUBDRArtifact(kind, path, "")
	return artifact.reference, err
}

type validatedEvidenceArtifact struct {
	reference EvidenceReference
	contents  []byte
}

func readUBDRArtifact(kind, path, expectedArtifact string) (validatedEvidenceArtifact, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil || !filepath.IsAbs(abs) {
		return validatedEvidenceArtifact{}, fmt.Errorf("resolve qualification evidence path: %w", err)
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1<<20 {
		return validatedEvidenceArtifact{}, fmt.Errorf("qualification evidence must be a regular file no larger than 1 MiB")
	}
	file, err := os.Open(abs)
	if err != nil {
		return validatedEvidenceArtifact{}, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Size() > 1<<20 || !os.SameFile(info, openedInfo) {
		return validatedEvidenceArtifact{}, fmt.Errorf("qualification evidence path changed before validation")
	}
	contents, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return validatedEvidenceArtifact{}, err
	}
	if len(contents) > 1<<20 {
		return validatedEvidenceArtifact{}, fmt.Errorf("qualification evidence must be no larger than 1 MiB")
	}
	switch kind {
	case EvidenceTransitionQualification:
		var report struct {
			SchemaVersion        int                           `json:"schemaVersion"`
			Predecessor          compatibility.ReleaseIdentity `json:"predecessor"`
			Candidate            compatibility.ReleaseIdentity `json:"candidate"`
			PolicySHA256         string                        `json:"policySha256"`
			StateBeforeUpgrade   string                        `json:"stateBeforeUpgradeSha256"`
			StateAfterUpgrade    string                        `json:"stateAfterUpgradeSha256"`
			StateAfterRollback   string                        `json:"stateAfterRollbackSha256"`
			UpgradeResult        string                        `json:"upgradeResult"`
			RollbackResult       string                        `json:"rollbackResult"`
			PreservationVerified bool                          `json:"preservationVerified"`
			InventoryBefore      qualificationState            `json:"inventoryBeforeUpgrade"`
			InventoryAfter       qualificationState            `json:"inventoryAfterUpgrade"`
			InventoryRollback    qualificationState            `json:"inventoryAfterRollback"`
		}
		if err := json.Unmarshal(contents, &report); err != nil || report.SchemaVersion != 1 ||
			report.UpgradeResult != "success" || report.RollbackResult != "success" || !report.PreservationVerified ||
			!validSHA256(report.PolicySHA256) || !validSHA256(report.StateBeforeUpgrade) ||
			report.StateBeforeUpgrade != report.StateAfterUpgrade || report.StateBeforeUpgrade != report.StateAfterRollback ||
			report.InventoryBefore != report.InventoryAfter || report.InventoryBefore != report.InventoryRollback ||
			report.InventoryBefore.InstanceID == "" || report.Predecessor.Image == report.Candidate.Image ||
			ValidateArtifactIdentity(report.Predecessor.Image) != nil || ValidateArtifactIdentity(report.Candidate.Image) != nil ||
			(expectedArtifact != "" && report.Candidate.Image != expectedArtifact) {
			return validatedEvidenceArtifact{}, fmt.Errorf("transition qualification evidence is incomplete or does not qualify the scheduled artifact")
		}
	case EvidenceBackupManifestV2:
		var manifest platform.InstanceBackupManifestV2
		if err := json.Unmarshal(contents, &manifest); err != nil || manifest.SchemaVersion != platform.InstanceBackupManifestVersion ||
			manifest.Kind != "leapview-instance" || manifest.BackupID == "" || manifest.InstanceID == "" ||
			!validSHA256(manifest.InventorySHA256) || len(manifest.Members) == 0 ||
			ValidateArtifactIdentity(manifest.ReleaseIdentity.Image) != nil ||
			(expectedArtifact != "" && manifest.ReleaseIdentity.Image != expectedArtifact) {
			return validatedEvidenceArtifact{}, fmt.Errorf("backup manifest v2 evidence is incomplete or does not bind the scheduled artifact")
		}
	case EvidenceRestorePreflight:
		var plan platform.InstanceRestorePreflightPlan
		if err := json.Unmarshal(contents, &plan); err != nil || plan.SchemaVersion != 1 || !plan.Allowed ||
			plan.ReasonCode != platform.RestorePreflightAllowed || plan.BackupID == "" ||
			!validSHA256(plan.ManifestSHA256) || !validSHA256(plan.ArchiveSHA256) || !plan.ExclusiveLockVerified {
			return validatedEvidenceArtifact{}, fmt.Errorf("restore preflight evidence is incomplete")
		}
	default:
		return validatedEvidenceArtifact{}, fmt.Errorf("unsupported recovery qualification evidence kind %q", kind)
	}
	sum := sha256.Sum256(contents)
	digest := hex.EncodeToString(sum[:])
	uri := (&url.URL{Scheme: "artifact", Host: "qualification", Path: "/" + kind + "/" + digest + ".json"}).String()
	return validatedEvidenceArtifact{
		reference: EvidenceReference{Kind: kind, URI: uri, SHA256: digest}, contents: contents,
	}, nil
}

func retainUBDRArtifact(artifact validatedEvidenceArtifact, root string) (EvidenceReference, error) {
	reference, contents := artifact.reference, artifact.contents
	if !filepath.IsAbs(root) {
		return EvidenceReference{}, fmt.Errorf("qualification evidence root must be absolute")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return EvidenceReference{}, err
	}
	destination := filepath.Join(root, reference.SHA256+".json")
	if existing, readErr := os.ReadFile(destination); readErr == nil {
		sum := sha256.Sum256(existing)
		if hex.EncodeToString(sum[:]) != reference.SHA256 {
			return EvidenceReference{}, fmt.Errorf("content-addressed qualification evidence conflicts with retained bytes")
		}
		return reference, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return EvidenceReference{}, readErr
	}
	temporary, err := os.CreateTemp(root, ".qualification-evidence-*")
	if err != nil {
		return EvidenceReference{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return EvidenceReference{}, err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return EvidenceReference{}, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return EvidenceReference{}, err
	}
	if err := temporary.Close(); err != nil {
		return EvidenceReference{}, err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return EvidenceReference{}, err
	}
	return reference, nil
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
