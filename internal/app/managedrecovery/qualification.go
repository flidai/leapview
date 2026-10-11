package managedrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/refresh/recovery"
)

// ManagedQualificationEvidence records only the bounded stopped-provider
// exercise. It supplies no activation, protected-run or full-profile authority.
type ManagedQualificationEvidence struct {
	SchemaVersion                 int                      `json:"schemaVersion"`
	Kind                          string                   `json:"kind"`
	Scope                         string                   `json:"scope"`
	Admission                     ManagedAdmissionEvidence `json:"admission"`
	ArtifactSourceRevision        string                   `json:"artifactSourceRevision"`
	ReplacementMachineIDDigest    string                   `json:"replacementMachineIdDigest"`
	StartedAt                     time.Time                `json:"startedAt"`
	CompletedAt                   time.Time                `json:"completedAt"`
	RestoreDurationMillis         int64                    `json:"restoreDurationMillis"`
	CompletedReplayDurationMillis int64                    `json:"completedReplayDurationMillis"`
	AdmissionDurationMillis       int64                    `json:"admissionDurationMillis"`
	ActivationQualified           bool                     `json:"activationQualified"`
	FullManagedProfileQualified   bool                     `json:"fullManagedProfileQualified"`
}

type qualificationOperations struct {
	restore   func(context.Context) (providerrestore.Report, bool, error)
	replay    func(context.Context) (providerrestore.Report, bool, error)
	admit     func(context.Context) (ManagedAdmissionEvidence, error)
	machineID func() (string, error)
	now       func() time.Time
}

// QualifyManagedRecovery performs a fresh exact restore, its completed retry,
// and fresh admission on the replacement. The caller holds the instance home
// lock; source preparation/enrollment and explicit original fencing precede it.
func QualifyManagedRecovery(ctx context.Context, input ManagedInput, config ManagedConfig, authority ManagedAuthorities) (ManagedQualificationEvidence, error) {
	return qualifyManagedRecovery(ctx, input, config, authority, qualificationOperations{
		restore: func(ctx context.Context) (providerrestore.Report, bool, error) {
			return executeManagedRestore(ctx, input, config, authority, func() error {
				if err := requireFreshManagedDestinations(config); err != nil {
					return err
				}
				if config.PostgresProvider == "module-owned" {
					return requireFreshModulePostgres(ctx, config)
				}
				return nil
			})
		},
		replay: func(ctx context.Context) (providerrestore.Report, bool, error) {
			return executeManagedRestore(ctx, input, config, authority, nil)
		},
		admit: func(ctx context.Context) (ManagedAdmissionEvidence, error) {
			return AdmitManagedRecovery(ctx, config, authority)
		},
		machineID: func() (string, error) { return readQualificationMachineID("/etc/machine-id") },
		now:       time.Now,
	})
}

func qualifyManagedRecovery(ctx context.Context, input ManagedInput, config ManagedConfig, authority ManagedAuthorities, operations qualificationOperations) (ManagedQualificationEvidence, error) {
	fail := func(message string) (ManagedQualificationEvidence, error) {
		return ManagedQualificationEvidence{}, errors.New(message)
	}
	if ctx.Err() != nil || typednil.IsNil(authority.Ledger) || typednil.IsNil(authority.Sets) || operations.restore == nil || operations.replay == nil || operations.admit == nil || operations.machineID == nil || operations.now == nil {
		return fail("managed qualification requires live exact authorities and execution")
	}
	set, err := authority.Sets.ReadExact(ctx, config.RecoverySetID)
	if err != nil || set.Status != recoveryset.StatusPrepared || set.PublishedValidationAttemptID != "" {
		return fail("managed qualification requires a newly prepared authoritative frontier")
	}
	occurrence, err := authority.Ledger.Occurrence(ctx, config.OccurrenceID)
	if err != nil || occurrence.Status != recovery.StatusPending || occurrence.AttemptCount != 0 || occurrence.Fence.Generation != 0 || !occurrence.ExpiresAt.After(operations.now()) {
		return fail("managed qualification requires a fresh pending unattempted occurrence")
	}
	if VerifyManagedEnrollment(config.Enrollment, set, occurrence, config.InstanceHome) != nil || authority.AuthoritySystemIdentifier != config.Enrollment.Request.AuthoritySystemID || input.Authority.SystemIdentifier != authority.AuthoritySystemIdentifier || input.RecoverySetID != config.RecoverySetID || input.OccurrenceID != config.OccurrenceID || input.InstanceHome != config.InstanceHome || input.Artifact != config.Artifact || config.Artifact.Image != occurrence.ArtifactIdentity || config.Credentials.TargetID != set.Delivery.TargetID {
		return fail("managed qualification inputs differ from exact enrollment and independent authority")
	}
	machineID, err := operations.machineID()
	if err != nil || !validQualificationMachineID(machineID) || len(config.PrimaryFence.Primaries) != 1 || machineID == config.PrimaryFence.Primaries[0].MachineID || config.PrimaryFence.Primaries[0].SystemIdentifier != config.Enrollment.Request.SourceSystemID {
		return fail("managed qualification requires a distinct observed replacement host")
	}
	if requireFreshManagedDestinations(config) != nil {
		return fail("managed qualification requires absent restore destinations with private existing parents")
	}
	started := operations.now()
	report, restored, err := operations.restore(ctx)
	restoredAt := operations.now()
	expected := providerrestore.HandoffExpectations{OccurrenceID: config.OccurrenceID, TargetID: set.Delivery.TargetID, RecoverySetID: set.ID, FrontierDigest: set.FrontierDigest, ArtifactIdentity: config.Artifact.Image}
	if err != nil || ctx.Err() != nil || !restored || providerrestore.ValidateManagedLocalHandoffReport(report, set, expected) != nil || !report.Verification.Ready || !report.Verification.ObjectsConsistent || report.Admission.ValidationAttemptID != input.ValidationAttemptID || report.Admission.PublishedSetID != set.ID || report.Admission.PublishedStatus != recoveryset.StatusPublished || !validContentDigest(report.Admission.ValidationDigest) || report.Admission.PublishedAt.IsZero() || restoredAt.Before(started) || report.StartedAt.Before(started) || report.CompletedAt.Before(report.StartedAt) || report.CompletedAt.After(restoredAt) {
		return fail("managed qualification did not complete a fresh exact restore within its measured interval")
	}
	replayed, restoredAgain, err := operations.replay(ctx)
	replayedAt := operations.now()
	original, marshalErr := json.Marshal(report)
	retry, retryMarshalErr := json.Marshal(replayed)
	if err != nil || ctx.Err() != nil || restoredAgain || marshalErr != nil || retryMarshalErr != nil || string(original) != string(retry) || replayedAt.Before(restoredAt) {
		return fail("managed qualification completed retry differs or performed another restore")
	}
	admission, err := operations.admit(ctx)
	completed := operations.now()
	// Bind admission to the exact bytes retained by FileEvidenceStore.Save,
	// rather than accepting a syntactically valid substituted report digest.
	retained, retainedErr := json.MarshalIndent(report, "", "  ")
	retainedDigest := digestBytes(append(retained, '\n'))
	if err != nil || ctx.Err() != nil || retainedErr != nil || admission.SchemaVersion != 1 || admission.Kind != "leapview/managed-local-recovery-admission" || admission.Status != "verified-before-activation" || admission.OccurrenceID != config.OccurrenceID || admission.RecoverySetID != set.ID || admission.TargetID != set.Delivery.TargetID || admission.FrontierDigest != set.FrontierDigest || admission.ArtifactIdentity != config.Artifact.Image || admission.ValidationAttemptID != report.Admission.ValidationAttemptID || admission.ValidationDigest != report.Admission.ValidationDigest || "sha256:"+admission.ReportSHA256 != retainedDigest || !admission.OriginalsFenced || admission.ActivationQualified || admission.VerifiedAt.Before(replayedAt) || admission.VerifiedAt.After(completed) || completed.Before(replayedAt) {
		return fail("managed qualification fresh admission differs from the restored frontier")
	}
	currentMachine, err := operations.machineID()
	if err != nil || currentMachine != machineID {
		return fail("managed qualification replacement host changed during the exercise")
	}
	return ManagedQualificationEvidence{SchemaVersion: 1, Kind: "leapview/managed-recovery-preactivation-qualification", Scope: "fresh-managed-coordinator-replay-admission", Admission: admission, ArtifactSourceRevision: config.Artifact.SourceRevision, ReplacementMachineIDDigest: digestBytes([]byte("leapview-managed-recovery-qualification:" + machineID)), StartedAt: started.UTC(), CompletedAt: completed.UTC(), RestoreDurationMillis: restoredAt.Sub(started).Milliseconds(), CompletedReplayDurationMillis: replayedAt.Sub(restoredAt).Milliseconds(), AdmissionDurationMillis: completed.Sub(replayedAt).Milliseconds()}, nil
}

func requireFreshManagedDestinations(config ManagedConfig) error {
	if len(config.Roots) == 0 {
		return errors.New("exact managed object roots required")
	}
	paths := []string{config.Postgres.Destination}
	for _, root := range config.Roots {
		paths = append(paths, root.Destination)
	}
	for i, path := range paths {
		moduleData := i == 0 && config.PostgresProvider == "module-owned"
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || (!moduleData && validatePrivateSecretRoot(filepath.Dir(path)) != nil) {
			return errors.New("canonical absent managed destination with private existing parent required")
		}
		if !moduleData {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				return errors.New("managed qualification cannot reuse existing restored state")
			}
		}
		for _, other := range paths[:i] {
			if path == other || strings.HasPrefix(path, other+string(filepath.Separator)) || strings.HasPrefix(other, path+string(filepath.Separator)) {
				return errors.New("managed qualification restore destinations overlap")
			}
		}
	}
	return nil
}

// ValidateManagedQualificationOutput refuses stale receipts and destinations
// whose publication would modify the provider state being qualified.
func ValidateManagedQualificationOutput(path string, config ManagedConfig) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || validatePrivateSecretRoot(filepath.Dir(path)) != nil {
		return errors.New("new canonical qualification output with private existing parent required")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("managed qualification output already exists or is unavailable")
	}
	for _, destination := range append([]string{config.Postgres.Destination}, qualificationRootDestinations(config.Roots)...) {
		if destination != "" && (path == destination || strings.HasPrefix(path, destination+string(filepath.Separator))) {
			return errors.New("managed qualification output overlaps restored provider state")
		}
	}
	return nil
}

func qualificationRootDestinations(roots []ResticConfig) []string {
	result := make([]string, 0, len(roots))
	for _, root := range roots {
		result = append(result, root.Destination)
	}
	return result
}

func validQualificationMachineID(value string) bool {
	return regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(value) && value != strings.Repeat("0", 32)
}

func readQualificationMachineID(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 32 || info.Size() > 33 {
		return "", errors.New("actual replacement machine identity unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("actual replacement machine identity unavailable")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("actual replacement machine identity changed")
	}
	value, err := io.ReadAll(io.LimitReader(file, 34))
	id := strings.TrimSuffix(string(value), "\n")
	if err != nil || !validQualificationMachineID(id) {
		return "", errors.New("actual replacement machine identity invalid")
	}
	return id, nil
}
