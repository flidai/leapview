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
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

const (
	EvidenceTransitionQualification = "transition-qualification"
	EvidenceBackupManifestV2        = "backup-manifest-v2"
	EvidenceRestorePreflight        = "restore-preflight"
)

type ScenarioOutcome struct {
	// Timing fields are retained only to fail closed for older adapters. The
	// ledger derives measurements from its persisted execution phases and owner
	// evidence; adapters must leave these values zero.
	RecoveryPointAt    time.Time
	RestoreCompletedAt time.Time
	ReadyAt            time.Time
	Artifacts          []EvidenceArtifact
	cleanup            func() error
}

type EvidenceArtifact struct {
	Kind string
	Path string
}

type ScenarioAdapter interface {
	Execute(context.Context, Occurrence) (ScenarioOutcome, error)
}

type phaseRecorder func(string, string) error
type phaseRecorderContextKey struct{}

func recordQualificationPhase(ctx context.Context, phase, event string) error {
	recorder, _ := ctx.Value(phaseRecorderContextKey{}).(phaseRecorder)
	if recorder == nil {
		return fmt.Errorf("recovery qualification phase recorder is unavailable")
	}
	return recorder(phase, event)
}

// RecordQualificationPhase lets owner adapters delimit ledger-clocked phases
// without supplying or controlling their timestamps.
func RecordQualificationPhase(ctx context.Context, phase, event string) error {
	return recordQualificationPhase(ctx, phase, event)
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
	if _, err = lifecycle.Repository.Retain(ctx, RetentionPolicy{Now: lifecycle.now(), ComplianceWindow: lifecycle.ComplianceWindow}); err != nil {
		return err
	}
	return lifecycle.garbageCollectEvidence(ctx)
}

func (lifecycle Lifecycle) garbageCollectEvidence(ctx context.Context) error {
	lock, err := acquireEvidenceMutationLock(ctx, lifecycle.EvidenceRoot)
	if err != nil {
		return err
	}
	defer lock.Release()
	occurrences, err := lifecycle.Repository.Occurrences(ctx)
	if err != nil {
		return err
	}
	return GarbageCollectEvidence(lifecycle.EvidenceRoot, occurrences)
}

func acquireEvidenceMutationLock(ctx context.Context, root string) (*instancelock.Lock, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	for {
		lock, err := instancelock.AcquireNamed(root, ".recovery-evidence.lock")
		if err == nil {
			return lock, nil
		}
		if !strings.Contains(err.Error(), "another process") {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// GarbageCollectEvidence removes only unreferenced content-addressed evidence
// files. References are treated as a set so a digest shared by multiple
// occurrences survives until the final reference is gone.
func GarbageCollectEvidence(root string, occurrences []Occurrence) error {
	referenced := make(map[string]struct{})
	for _, occurrence := range occurrences {
		for _, evidence := range occurrence.Evidence {
			referenced[evidence.SHA256] = struct{}{}
		}
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	removed := false
	for _, entry := range entries {
		name := entry.Name()
		if len(name) != 64+len(".json") || !strings.HasSuffix(name, ".json") || !validSHA256(strings.TrimSuffix(name, ".json")) {
			continue
		}
		if _, keep := referenced[strings.TrimSuffix(name, ".json")]; keep {
			continue
		}
		if err := os.Remove(filepath.Join(root, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		removed = true
	}
	if removed {
		return (osEvidenceDurability{}).SyncDirectory(root)
	}
	return nil
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
	adapterContext := context.WithValue(executeCtx, phaseRecorderContextKey{}, phaseRecorder(func(phase, event string) error {
		return lifecycle.Repository.RecordPhase(executeCtx, occurrence.ID, occurrence.Fence, phase, event, lifecycle.now())
	}))
	outcome, runErr := adapter.Execute(adapterContext, occurrence)
	cancel()
	if heartbeatErr := <-heartbeatDone; heartbeatErr != nil {
		if outcome.cleanup != nil {
			_ = outcome.cleanup()
		}
		return heartbeatErr
	}
	completed := lifecycle.now()
	evidenceLock, lockErr := acquireEvidenceMutationLock(ctx, lifecycle.EvidenceRoot)
	if lockErr != nil {
		return lifecycle.Repository.Fail(ctx, occurrence.ID, occurrence.Fence, completed, Result{}, NewFailure("evidence_store_unavailable", lockErr.Error()))
	}
	defer evidenceLock.Release()
	result, resultErr := outcome.result(occurrence, started, completed, lifecycle.EvidenceRoot)
	if outcome.cleanup != nil {
		resultErr = errors.Join(resultErr, outcome.cleanup())
	}
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
	if !outcome.RecoveryPointAt.IsZero() || !outcome.RestoreCompletedAt.IsZero() || !outcome.ReadyAt.IsZero() {
		return Result{}, fmt.Errorf("qualification adapters must not provide ledger timing fields")
	}
	artifacts, err := validateScenarioArtifacts(occurrence, outcome.Artifacts)
	if err != nil {
		return Result{}, err
	}
	result := Result{RecoveryPointAt: recoveryPointFromArtifacts(artifacts)}
	if result.RecoveryPointAt.IsZero() || result.RecoveryPointAt.After(completed) {
		return Result{}, fmt.Errorf("owner evidence recovery point is missing or after ledger completion")
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
		artifact, err := readUBDRArtifact(input.Kind, input.Path, occurrence)
		if err != nil {
			return nil, err
		}
		seen[input.Kind] = true
		validated = append(validated, artifact)
	}
	if occurrence.Operation == OperationRestore {
		var manifestDocument, planDocument []byte
		for _, artifact := range validated {
			switch artifact.reference.Kind {
			case EvidenceBackupManifestV2:
				manifestDocument = artifact.contents
			case EvidenceRestorePreflight:
				planDocument = artifact.contents
			}
		}
		_, manifest, err := platform.ValidateInstanceRestorePreflightDocument(planDocument, manifestDocument, platform.InstanceBackupEvidenceExpectation{
			ArtifactIdentity: occurrence.ArtifactIdentity, PolicyVersion: occurrence.PolicyVersion,
			PolicySHA256: occurrence.PolicySHA256, TargetScope: occurrence.TargetScope,
		})
		if err != nil {
			return nil, err
		}
		for index := range validated {
			validated[index].recoveryPointAt = manifest.CompletedAt
		}
	}
	return validated, nil
}

func recoveryPointFromArtifacts(artifacts []validatedEvidenceArtifact) time.Time {
	var point time.Time
	for _, artifact := range artifacts {
		if !artifact.recoveryPointAt.IsZero() && (point.IsZero() || artifact.recoveryPointAt.Before(point)) {
			point = artifact.recoveryPointAt
		}
	}
	return point
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

// ValidateUBDRArtifact validates the existing UBDR owner report and returns a
// content-bound reference. It deliberately does not invent a parallel report.
func ValidateUBDRArtifact(kind, path string) (EvidenceReference, error) {
	artifact, err := readUBDRArtifact(kind, path, Occurrence{})
	return artifact.reference, err
}

type validatedEvidenceArtifact struct {
	reference       EvidenceReference
	contents        []byte
	recoveryPointAt time.Time
}

func readUBDRArtifact(kind, path string, expected Occurrence) (validatedEvidenceArtifact, error) {
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
		report, err := compatibility.ValidateTransitionQualificationEvidence(contents, compatibility.TransitionQualificationExpectation{
			CandidateImage: expected.ArtifactIdentity, PolicyVersion: expected.PolicyVersion,
			PolicySHA256: expected.PolicySHA256, TargetScope: expected.TargetScope,
		})
		if err != nil {
			return validatedEvidenceArtifact{}, err
		}
		sum := sha256.Sum256(contents)
		digest := hex.EncodeToString(sum[:])
		uri := (&url.URL{Scheme: "artifact", Host: "qualification", Path: "/" + kind + "/" + digest + ".json"}).String()
		return validatedEvidenceArtifact{reference: EvidenceReference{Kind: kind, URI: uri, SHA256: digest}, contents: contents, recoveryPointAt: report.RecoveryPointAt}, nil
	case EvidenceBackupManifestV2:
		manifest, err := platform.ValidateInstanceBackupManifestDocument(contents, platform.InstanceBackupEvidenceExpectation{
			ArtifactIdentity: expected.ArtifactIdentity, PolicyVersion: expected.PolicyVersion,
			PolicySHA256: expected.PolicySHA256, TargetScope: expected.TargetScope,
		})
		if err != nil {
			return validatedEvidenceArtifact{}, err
		}
		sum := sha256.Sum256(contents)
		digest := hex.EncodeToString(sum[:])
		uri := (&url.URL{Scheme: "artifact", Host: "qualification", Path: "/" + kind + "/" + digest + ".json"}).String()
		return validatedEvidenceArtifact{reference: EvidenceReference{Kind: kind, URI: uri, SHA256: digest}, contents: contents, recoveryPointAt: manifest.CompletedAt}, nil
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
	return retainUBDRArtifactWithDurability(artifact, root, osEvidenceDurability{})
}

type evidenceDurability interface {
	SyncFile(*os.File) error
	Rename(string, string) error
	SyncDirectory(string) error
}

type osEvidenceDurability struct{}

func (osEvidenceDurability) SyncFile(file *os.File) error { return file.Sync() }
func (osEvidenceDurability) Rename(source, destination string) error {
	return os.Rename(source, destination)
}
func (osEvidenceDurability) SyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func retainUBDRArtifactWithDurability(artifact validatedEvidenceArtifact, root string, durability evidenceDurability) (EvidenceReference, error) {
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
	if err := durability.SyncFile(temporary); err != nil {
		temporary.Close()
		return EvidenceReference{}, err
	}
	if err := temporary.Close(); err != nil {
		return EvidenceReference{}, err
	}
	if err := durability.Rename(temporaryPath, destination); err != nil {
		return EvidenceReference{}, err
	}
	if err := durability.SyncDirectory(root); err != nil {
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
