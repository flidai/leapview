package platform

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	securejoin "github.com/cyphar/filepath-securejoin"
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/platform/filesystem"
)

const (
	instanceBackupManifestName  = "leapview-backup.json"
	instanceBackupDBName        = "leapview.db"
	instanceBackupLegacyVersion = 1
	instanceRestoreDirMode      = securefs.PrivateDirMode
	instanceRestoreFileMode     = securefs.PrivateFileMode
	instanceRestoreDBMode       = securefs.PrivateFileMode

	// InstanceRestoreCheckpointPattern reserves a private filename namespace
	// for the disposable current-state checkpoint created during restore.
	InstanceRestoreCheckpointPattern = ".leapview-current-backup-*.tar.gz"
	instanceOperationMarkerName      = ".leapview-target.json"
)

var interruptedRestoreCheckpointPrefixes = []string{
	".leapview-current-backup-",
	"leapview-current-backup-", // compatibility with pre-rc.1 interrupted restores
}

type InstanceBackupOptions struct {
	HomeDir              string
	DBPath               string
	OutPath              string
	ExcludeRelativePaths []string
	BackupID             string
	Now                  func() time.Time
	ReleaseIdentity      compatibility.ReleaseIdentity
	StorageTopology      InstanceBackupStorageTopology
	Environment          string
	TransitionPolicy     *compatibility.Policy
}

type InstanceRestoreOptions struct {
	TargetHomeDir         string
	BackupPath            string
	CurrentBackupOut      string
	DiscardCurrentBackup  bool
	ExpectedEnvironment   string
	PreserveRelativeFile  string
	ResetRelativePaths    []string
	TargetReleaseIdentity compatibility.ReleaseIdentity
	ExternalEvidence      map[string]string
	MinimumFreeBytes      uint64
	ExclusiveLockHeld     bool
	RequireExclusiveLock  bool
	TransitionPolicy      *compatibility.Policy
	ValidatedPlan         *InstanceRestorePreflightPlan
}

type instanceBackupManifest struct {
	Version   int       `json:"version"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"createdAt"`
	DBPath    string    `json:"dbPath"`
}

type instanceOperationMarker struct {
	Version int    `json:"version"`
	Target  string `json:"target"`
	ID      string `json:"id"`
}

func readInstanceOperationMarker(path string) (instanceOperationMarker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return instanceOperationMarker{}, err
	}
	var marker instanceOperationMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return instanceOperationMarker{}, err
	}
	return marker, nil
}

func syncInstanceOperationDir(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func instanceTargetIdentity(target string) (string, string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(target))
	if err != nil {
		return "", "", err
	}
	abs = filepath.Clean(abs)
	// Resolve symlinks in the existing portion of the path so aliases share an
	// identity, while still permitting a not-yet-created target.
	resolved := abs
	for candidate := abs; ; candidate = filepath.Dir(candidate) {
		if _, err := os.Lstat(candidate); err == nil {
			r, evalErr := filepath.EvalSymlinks(candidate)
			if evalErr != nil {
				return "", "", evalErr
			}
			resolved = filepath.Join(r, strings.TrimPrefix(abs, candidate))
			resolved = filepath.Clean(resolved)
			break
		} else if !os.IsNotExist(err) {
			return "", "", err
		} else if filepath.Dir(candidate) == candidate {
			break
		}
	}
	digest := sha256.Sum256([]byte(resolved))
	return resolved, fmt.Sprintf("%x", digest[:12]), nil
}

func writeInstanceOperationMarker(dir, target, id string) error {
	data, err := json.Marshal(instanceOperationMarker{Version: 1, Target: target, ID: id})
	if err != nil {
		return err
	}
	path := filepath.Join(dir, instanceOperationMarkerName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, instanceRestoreDBMode)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(data, '\n')); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = syncInstanceOperationDir(path)
	}
	return err
}

func verifyInstanceOperationMarker(dir, target, id string) error {
	marker, err := readInstanceOperationMarker(filepath.Join(dir, instanceOperationMarkerName))
	if err != nil {
		return fmt.Errorf("read instance operation marker: %w", err)
	}
	if marker.Version != 1 || marker.Target != target || marker.ID != id {
		return fmt.Errorf("instance operation marker does not match target %q", target)
	}
	return nil
}

func checkpointMarkerPath(checkpoint string) string { return checkpoint + instanceOperationMarkerName }

func scopedOperationName(name, prefix, id string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	rest := strings.TrimPrefix(name, prefix)
	return strings.HasPrefix(rest, id+"-")
}

func hasOperationDigest(value string) bool {
	if len(value) < 25 || value[24] != '-' {
		return false
	}
	for _, c := range value[:24] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func writeCheckpointMarker(checkpoint, target, id string) error {
	data, err := json.Marshal(instanceOperationMarker{Version: 1, Target: target, ID: id})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(checkpointMarkerPath(checkpoint), os.O_WRONLY|os.O_CREATE|os.O_EXCL, instanceRestoreDBMode)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(data, '\n')); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = syncInstanceOperationDir(checkpointMarkerPath(checkpoint))
	}
	return err
}

func BackupInstance(ctx context.Context, options InstanceBackupOptions) error {
	outPath := strings.TrimSpace(options.OutPath)
	if outPath == "" {
		return fmt.Errorf("instance backup output path is required")
	}
	outAbs, err := filepath.Abs(outPath)
	if err != nil {
		return err
	}
	homeAbs, _, err := validateInstanceBackupSource(options.HomeDir, options.DBPath)
	if err != nil {
		return err
	}
	_, targetID, err := instanceTargetIdentity(homeAbs)
	if err != nil {
		return err
	}
	if pathWithin(homeAbs, outAbs) {
		return fmt.Errorf("instance backup output path must not be inside home dir")
	}
	if _, err := os.Stat(outAbs); err == nil {
		return fmt.Errorf("instance backup output path %q already exists", outPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	outParent := filepath.Dir(outAbs)
	if err := securefs.EnsurePrivateDir(outParent); err != nil {
		return err
	}
	homeParent := filepath.Dir(homeAbs)
	if err := removeInterruptedInstanceBackupWork(homeParent, targetID); err != nil {
		return err
	}
	if outParent != homeParent {
		if err := removeInterruptedInstanceBackupWork(outParent, targetID); err != nil {
			return err
		}
	}
	tmpArchive, err := os.CreateTemp(outParent, fmt.Sprintf(".leapview-instance-backup-%s-*.tar.gz", targetID))
	if err != nil {
		return err
	}
	if err := tmpArchive.Chmod(securefs.PrivateFileMode); err != nil {
		_ = tmpArchive.Close()
		_ = os.Remove(tmpArchive.Name())
		return err
	}
	tmpArchivePath := tmpArchive.Name()
	cleanupArchive := true
	defer func() {
		if cleanupArchive {
			_ = os.Remove(tmpArchivePath)
		}
	}()
	if err := writeInstanceBackup(ctx, options, tmpArchive); err != nil {
		_ = tmpArchive.Close()
		return err
	}
	if err := tmpArchive.Sync(); err != nil {
		_ = tmpArchive.Close()
		return err
	}
	if err := tmpArchive.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpArchivePath, outAbs); err != nil {
		return err
	}
	cleanupArchive = false
	return nil
}

// BackupInstanceToWriter writes a validated full-instance archive directly to
// out. Callers own atomic destination handling and must stop the serving process.
func BackupInstanceToWriter(ctx context.Context, options InstanceBackupOptions, out io.Writer) error {
	if out == nil {
		return fmt.Errorf("instance backup output is required")
	}
	homeAbs, _, err := validateInstanceBackupSource(options.HomeDir, options.DBPath)
	if err != nil {
		return err
	}
	_, targetID, err := instanceTargetIdentity(homeAbs)
	if err != nil {
		return err
	}
	if err := removeInterruptedInstanceBackupWork(filepath.Dir(homeAbs), targetID); err != nil {
		return err
	}
	return writeInstanceBackup(ctx, options, out)
}

func writeInstanceBackup(ctx context.Context, options InstanceBackupOptions, out io.Writer) error {
	homeAbs, dbAbs, err := validateInstanceBackupSource(options.HomeDir, options.DBPath)
	if err != nil {
		return err
	}
	excluded, err := normalizeInstanceRelativePaths(options.ExcludeRelativePaths)
	if err != nil {
		return fmt.Errorf("instance backup exclusions: %w", err)
	}
	parent := filepath.Dir(homeAbs)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	_, targetID, err := instanceTargetIdentity(homeAbs)
	if err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp(parent, fmt.Sprintf(".leapview-instance-backup-%s-*", targetID))
	if err != nil {
		return err
	}
	defer removeInstanceStateTree(tmpDir)
	dbCopy := filepath.Join(tmpDir, instanceBackupDBName)
	store, err := Open(ctx, dbAbs)
	if err != nil {
		return err
	}
	instanceID, environment, err := readBackupInstanceIdentity(ctx, store)
	if err != nil {
		_ = store.Close()
		return err
	}
	if err := store.Backup(ctx, dbCopy); err != nil {
		_ = store.Close()
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}
	if expected := strings.TrimSpace(options.Environment); expected != "" {
		if environment != "unbound" && environment != expected {
			return fmt.Errorf("instance backup environment %q does not match configured environment %q", environment, expected)
		}
		environment = expected
	}
	members, err := collectInstanceBackupMembers(homeAbs, dbAbs, dbCopy, excluded)
	if err != nil {
		return err
	}
	backupID := strings.TrimSpace(options.BackupID)
	if backupID == "" {
		backupID, err = newBackupID()
		if err != nil {
			return err
		}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	createdAt := now().UTC()
	policy, err := instanceTransitionPolicy(options.TransitionPolicy)
	if err != nil {
		return err
	}
	releaseIdentity, err := normalizeBackupReleaseIdentity(options.ReleaseIdentity)
	if err != nil {
		return err
	}
	inventory := make([]InstanceBackupMember, len(members))
	for index, member := range members {
		inventory[index] = member.manifest
	}
	inventoryDigest, err := inventorySHA256(inventory)
	if err != nil {
		return err
	}
	manifest := InstanceBackupManifestV2{
		SchemaVersion: InstanceBackupManifestVersion, Kind: "leapview-instance", BackupID: backupID,
		ReleaseIdentity: releaseIdentity,
		InstanceID:      instanceID, Environment: environment, CreatedAt: createdAt, CompletedAt: createdAt,
		ArchiveMode: "full-instance", StorageTopology: normalizeBackupStorageTopology(options.StorageTopology),
		RequiredTransitionPolicyVersion: policy.PolicyVersion, Members: inventory, InventorySHA256: inventoryDigest,
	}
	return writeManifestV2Archive(out, manifest, members)
}

func validateInstanceBackupSource(homeDir, dbPath string) (string, string, error) {
	homeDir = strings.TrimSpace(homeDir)
	dbPath = strings.TrimSpace(dbPath)
	if homeDir == "" {
		return "", "", fmt.Errorf("instance backup home dir is required")
	}
	if dbPath == "" {
		return "", "", fmt.Errorf("instance backup database path is required")
	}
	homeAbs, err := filepath.Abs(homeDir)
	if err != nil {
		return "", "", err
	}
	dbAbs, err := filepath.Abs(dbPath)
	if err != nil {
		return "", "", err
	}
	if !pathWithin(homeAbs, dbAbs) {
		return "", "", fmt.Errorf("instance backup database path must be inside home dir")
	}
	return homeAbs, dbAbs, nil
}

func normalizeInstanceRelativePaths(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		clean := path.Clean(filepath.ToSlash(strings.TrimSpace(value)))
		if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("path %q must be relative to the instance home", value)
		}
		if clean == instanceBackupDBName || clean == instanceBackupManifestName {
			return nil, fmt.Errorf("path %q is required instance state", value)
		}
		if _, duplicate := seen[clean]; duplicate {
			continue
		}
		seen[clean] = struct{}{}
		normalized = append(normalized, clean)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func instanceRelativePathMatches(value string, paths []string) bool {
	value = filepath.ToSlash(filepath.Clean(value))
	for _, candidate := range paths {
		if value == candidate || strings.HasPrefix(value, candidate+"/") {
			return true
		}
	}
	return false
}

func RestoreInstance(ctx context.Context, options InstanceRestoreOptions) error {
	backupPath := strings.TrimSpace(options.BackupPath)
	if backupPath == "" {
		return fmt.Errorf("instance restore backup path is required")
	}
	targetHome := strings.TrimSpace(options.TargetHomeDir)
	if targetHome == "" {
		return fmt.Errorf("instance restore target home dir is required")
	}
	targetAbs, err := filepath.Abs(targetHome)
	if err != nil {
		return err
	}
	backupAbs, err := filepath.Abs(backupPath)
	if err != nil {
		return err
	}
	if pathWithin(targetAbs, backupAbs) {
		return fmt.Errorf("instance restore backup path must not be inside target home dir")
	}
	validated, err := PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{
		ArchivePath: backupAbs, TargetHomeDir: targetAbs,
		ExpectedEnvironment:   options.ExpectedEnvironment,
		TargetReleaseIdentity: options.TargetReleaseIdentity,
		ExternalEvidence:      options.ExternalEvidence, MinimumFreeBytes: options.MinimumFreeBytes,
		PreserveRelativeFile: options.PreserveRelativeFile, ResetRelativePaths: options.ResetRelativePaths,
		ExclusiveLockHeld: options.ExclusiveLockHeld, RequireExclusiveLock: options.RequireExclusiveLock,
		CurrentBackupOut: options.CurrentBackupOut, DiscardCurrentBackup: options.DiscardCurrentBackup,
		TransitionPolicy: options.TransitionPolicy,
	})
	if err != nil {
		return err
	}
	if supplied := options.ValidatedPlan; supplied != nil && !sameRestorePreflightIdentity(*supplied, validated) {
		reason := RestorePreflightStaleArchive
		if supplied.ArchivePath == validated.ArchivePath && supplied.ArchiveSHA256 == validated.ArchiveSHA256 {
			reason = RestorePreflightStaleTarget
		}
		return &InstanceRestorePreflightError{ReasonCode: reason, Remediation: "rerun preflight and consume the resulting plan without substitution", Err: fmt.Errorf("validated restore plan no longer matches archive or target identity")}
	}
	plan := &validated
	if !plan.Allowed {
		return &InstanceRestorePreflightError{ReasonCode: plan.ReasonCode, Remediation: plan.Remediation, Err: fmt.Errorf("validated plan is denied")}
	}
	if err := verifyRestorePlanInputs(*plan, backupAbs, targetAbs, options.PreserveRelativeFile); err != nil {
		return err
	}
	file, err := os.Open(backupAbs)
	if err != nil {
		return err
	}
	defer file.Close()
	return restoreInstanceFromReader(ctx, options, file, *plan)
}

func validateInstanceRestoreDestination(targetHome, currentBackupOut string, discardCurrentBackup bool, preserveRelativeFile string) (string, string, bool, bool, error) {
	targetAbs, err := filepath.Abs(strings.TrimSpace(targetHome))
	if err != nil {
		return "", "", false, false, err
	}
	if info, statErr := os.Lstat(targetAbs); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", "", false, false, fmt.Errorf("instance restore target home must not be a symlink")
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", "", false, false, statErr
	}
	currentBackupOut = strings.TrimSpace(currentBackupOut)
	if discardCurrentBackup && currentBackupOut == "" {
		return "", "", false, false, fmt.Errorf("discarding the current instance backup requires a current backup path")
	}
	currentBackupAbs := ""
	if currentBackupOut != "" {
		currentBackupAbs, err = filepath.Abs(currentBackupOut)
		if err != nil {
			return "", "", false, false, err
		}
		if pathWithin(targetAbs, currentBackupAbs) {
			return "", "", false, false, fmt.Errorf("current instance backup path must not be inside target home dir")
		}
	}
	exists, nonEmpty, err := dirExistsNonEmptyExcept(targetAbs, preserveRelativeFile)
	if err != nil {
		return "", "", false, false, err
	}
	return targetAbs, currentBackupAbs, exists, nonEmpty, nil
}

func sameRestorePreflightIdentity(left, right InstanceRestorePreflightPlan) bool {
	return left.Allowed == right.Allowed && left.BackupID == right.BackupID &&
		left.ManifestVersion == right.ManifestVersion && left.ManifestSHA256 == right.ManifestSHA256 &&
		left.PolicyVersion == right.PolicyVersion && left.ArchivePath == right.ArchivePath &&
		left.ArchiveSHA256 == right.ArchiveSHA256 && left.TargetHome == right.TargetHome &&
		left.TargetTreeSHA256 == right.TargetTreeSHA256 && left.Environment == right.Environment &&
		left.ArchiveRelease == right.ArchiveRelease && left.TargetRelease == right.TargetRelease &&
		reflect.DeepEqual(left.Replace, right.Replace) && reflect.DeepEqual(left.Preserve, right.Preserve) &&
		reflect.DeepEqual(left.Reset, right.Reset) && reflect.DeepEqual(left.ExternalPrerequisites, right.ExternalPrerequisites)
}

// RestoreInstanceFromReader validates and restores a full-instance archive
// directly from in. The target is replaced only after extraction succeeds.
func RestoreInstanceFromReader(ctx context.Context, options InstanceRestoreOptions, in io.Reader) error {
	if in == nil {
		return fmt.Errorf("instance restore input is required")
	}
	targetHome := strings.TrimSpace(options.TargetHomeDir)
	if targetHome == "" {
		return fmt.Errorf("instance restore target home dir is required")
	}
	temporary, err := os.CreateTemp("", ".leapview-restore-preflight-*.tar.gz")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	_, copyErr := io.CopyBuffer(temporary, in, make([]byte, InstanceBackupValidationBufferSize))
	if copyErr == nil {
		copyErr = temporary.Sync()
	}
	if closeErr := temporary.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return copyErr
	}
	options.BackupPath = temporaryPath
	options.ValidatedPlan = nil
	return RestoreInstance(ctx, options)
}

func restoreInstanceFromReader(ctx context.Context, options InstanceRestoreOptions, in io.Reader, plan InstanceRestorePreflightPlan) error {
	targetHome := strings.TrimSpace(options.TargetHomeDir)
	currentBackupOut := strings.TrimSpace(options.CurrentBackupOut)
	preserveRelativeFile, err := validatePreservedRelativeFile(options.PreserveRelativeFile)
	if err != nil {
		return err
	}
	resetRelativePaths, err := normalizeInstanceRelativePaths(options.ResetRelativePaths)
	if err != nil {
		return fmt.Errorf("instance restore resets: %w", err)
	}
	if targetHome == "" {
		return fmt.Errorf("instance restore target home dir is required")
	}
	targetAbs, currentBackupAbs, exists, nonEmpty, err := validateInstanceRestoreDestination(targetHome, currentBackupOut, options.DiscardCurrentBackup, preserveRelativeFile)
	if err != nil {
		return err
	}
	canonicalTarget, targetID, err := instanceTargetIdentity(targetAbs)
	if err != nil {
		return err
	}
	if options.DiscardCurrentBackup {
		currentBackupAbs = filepath.Join(filepath.Dir(currentBackupAbs), fmt.Sprintf(".leapview-current-backup-%s-%d.tar.gz", targetID, time.Now().UnixNano()))
	}

	parent := filepath.Dir(targetAbs)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	if err := recoverInterruptedInstanceOperations(targetAbs); err != nil {
		return err
	}
	tmpRestore, err := os.MkdirTemp(parent, fmt.Sprintf(".leapview-restore-%s-*", targetID))
	if err != nil {
		return err
	}
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = removeInstanceStateTree(tmpRestore)
		}
	}()
	archiveDigest := sha256.New()
	if err := extractInstanceBackupReader(ctx, io.TeeReader(in, archiveDigest), tmpRestore); err != nil {
		return err
	}
	if _, err := io.CopyBuffer(archiveDigest, in, make([]byte, InstanceBackupValidationBufferSize)); err != nil {
		return err
	}
	if got := fmt.Sprintf("%x", archiveDigest.Sum(nil)); got != plan.ArchiveSHA256 {
		return &InstanceRestorePreflightError{ReasonCode: RestorePreflightStaleArchive, Remediation: "rerun preflight for the exact archive", Err: fmt.Errorf("archive checksum changed during restore")}
	}
	if environment := strings.TrimSpace(options.ExpectedEnvironment); environment != "" {
		restored, err := Open(ctx, filepath.Join(tmpRestore, instanceBackupDBName))
		if err != nil {
			return fmt.Errorf("open restored instance environment: %w", err)
		}
		bindErr := restored.BindInstanceEnvironment(ctx, environment)
		closeErr := restored.Close()
		if bindErr != nil {
			return fmt.Errorf("validate restored instance environment: %w", bindErr)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	for _, relativePath := range resetRelativePaths {
		resetPath, err := securejoin.SecureJoin(tmpRestore, filepath.FromSlash(relativePath))
		if err != nil {
			return fmt.Errorf("resolve instance restore reset path %q: %w", relativePath, err)
		}
		if err := removeInstanceStateTree(resetPath); err != nil {
			return fmt.Errorf("reset derived instance path %q: %w", relativePath, err)
		}
	}
	targetDigest, err := instanceTreeSHA256(targetAbs, preserveRelativeFile)
	if err != nil {
		return err
	}
	if targetDigest != plan.TargetTreeSHA256 {
		return &InstanceRestorePreflightError{ReasonCode: RestorePreflightStaleTarget, Remediation: "stop target changes and rerun preflight", Err: fmt.Errorf("target identity changed during restore preparation")}
	}
	if exists && nonEmpty {
		if currentBackupOut == "" {
			return fmt.Errorf("current instance backup path is required when restoring over an existing home dir")
		}
		if err := BackupInstance(ctx, InstanceBackupOptions{
			HomeDir:              targetAbs,
			DBPath:               filepath.Join(targetAbs, instanceBackupDBName),
			OutPath:              currentBackupAbs,
			ExcludeRelativePaths: resetRelativePaths,
			Environment:          options.ExpectedEnvironment,
			ReleaseIdentity:      options.TargetReleaseIdentity,
			TransitionPolicy:     options.TransitionPolicy,
		}); err != nil {
			return fmt.Errorf("backup current instance: %w", err)
		}
		if options.DiscardCurrentBackup {
			if err := writeCheckpointMarker(currentBackupAbs, canonicalTarget, targetID); err != nil {
				return fmt.Errorf("mark disposable current instance backup: %w", err)
			}
		}
	}
	if preserveRelativeFile != "" {
		if err := preserveFileAcrossRestore(targetAbs, tmpRestore, preserveRelativeFile); err != nil {
			return err
		}
	}

	oldTarget := ""
	if exists {
		oldTarget = filepath.Join(parent, fmt.Sprintf(".leapview-restore-old-%s-%s", targetID, time.Now().UTC().Format("20060102150405.000000000")))
		if err := writeInstanceOperationMarker(targetAbs, canonicalTarget, targetID); err != nil {
			return fmt.Errorf("mark rollback state: %w", err)
		}
		if err := os.Rename(targetAbs, oldTarget); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpRestore, targetAbs); err != nil {
		if oldTarget != "" {
			_ = os.Rename(oldTarget, targetAbs)
		}
		return err
	}
	cleanupTmp = false
	if oldTarget != "" {
		if err := removeInstanceStateTree(oldTarget); err != nil {
			return fmt.Errorf("remove replaced instance state: %w", err)
		}
	}
	if options.DiscardCurrentBackup {
		if err := os.Remove(currentBackupAbs); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove disposable current instance backup: %w", err)
		}
		_ = os.Remove(checkpointMarkerPath(currentBackupAbs))
	}
	return nil
}

func removeInterruptedInstanceBackupWork(parent, targetID string) error {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		prefix := ".leapview-instance-backup-" + targetID + "-"
		if !strings.HasPrefix(entry.Name(), prefix) {
			if strings.HasPrefix(entry.Name(), ".leapview-instance-backup-") && !scopedOperationName(entry.Name(), ".leapview-instance-backup-", targetID) {
				if !entry.IsDir() && !strings.HasSuffix(entry.Name(), ".tar.gz") {
					continue
				}
				// A different target's digest is safe to leave alone; an
				// unscoped legacy name is ambiguous and must stop recovery.
				legacy := strings.TrimPrefix(entry.Name(), ".leapview-instance-backup-")
				if hasOperationDigest(legacy) {
					continue
				}
				return fmt.Errorf("ambiguous legacy instance backup artifact %q; refusing to delete", entry.Name())
			}
			continue
		}
		path := filepath.Join(parent, entry.Name())
		remove := os.Remove
		if entry.IsDir() {
			remove = removeInstanceStateTree
		}
		if err := remove(path); err != nil {
			return fmt.Errorf("remove interrupted instance backup %q: %w", entry.Name(), err)
		}
	}
	return nil
}

func recoverInterruptedInstanceOperations(target string) error {
	parent := filepath.Dir(target)
	canonicalTarget, targetID, err := instanceTargetIdentity(target)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	var oldTargets []string
	if info, statErr := os.Lstat(target); statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		markerPath := filepath.Join(target, instanceOperationMarkerName)
		if _, markerErr := os.Lstat(markerPath); markerErr == nil {
			if err := verifyInstanceOperationMarker(target, canonicalTarget, targetID); err != nil {
				return fmt.Errorf("refuse unverified target rollback marker: %w", err)
			}
			_ = os.Remove(markerPath)
		} else if !os.IsNotExist(markerErr) {
			return markerErr
		}
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".leapview-restore-old-") {
			prefix := ".leapview-restore-old-" + targetID + "-"
			if !strings.HasPrefix(entry.Name(), prefix) {
				legacy := strings.TrimPrefix(entry.Name(), ".leapview-restore-old-")
				if hasOperationDigest(legacy) {
					continue
				}
				return fmt.Errorf("ambiguous legacy restore rollback %q; refusing to adopt or delete", entry.Name())
			}
			if err := verifyInstanceOperationMarker(filepath.Join(parent, entry.Name()), canonicalTarget, targetID); err != nil {
				return fmt.Errorf("refuse unverified restore rollback %q: %w", entry.Name(), err)
			}
			oldTargets = append(oldTargets, filepath.Join(parent, entry.Name()))
		}
	}
	sort.Strings(oldTargets)
	if _, err := os.Lstat(target); os.IsNotExist(err) && len(oldTargets) > 0 {
		latest := oldTargets[len(oldTargets)-1]
		info, statErr := os.Lstat(latest)
		if statErr != nil {
			return statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("interrupted restore rollback %q is not a directory", latest)
		}
		if err := os.Rename(latest, target); err != nil {
			return fmt.Errorf("roll back interrupted instance restore: %w", err)
		}
		_ = os.Remove(filepath.Join(target, instanceOperationMarkerName))
		oldTargets = oldTargets[:len(oldTargets)-1]
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, stale := range oldTargets {
		if err := removeInstanceStateTree(stale); err != nil {
			return fmt.Errorf("remove interrupted restore rollback %q: %w", stale, err)
		}
	}
	if err := removeInterruptedInstanceBackupWork(parent, targetID); err != nil {
		return err
	}
	entries, err = os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".leapview-restore-") {
			if strings.HasPrefix(entry.Name(), ".leapview-restore-old-") {
				continue
			}
			prefix := ".leapview-restore-" + targetID + "-"
			if !strings.HasPrefix(entry.Name(), prefix) {
				legacy := strings.TrimPrefix(entry.Name(), ".leapview-restore-")
				if hasOperationDigest(legacy) {
					continue
				}
				return fmt.Errorf("ambiguous legacy restore artifact %q; refusing to delete", entry.Name())
			}
			if err := removeInstanceStateTree(filepath.Join(parent, entry.Name())); err != nil {
				return fmt.Errorf("remove interrupted instance operation %q: %w", entry.Name(), err)
			}
			continue
		}
		for _, prefix := range interruptedRestoreCheckpointPrefixes {
			if !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			if strings.HasSuffix(entry.Name(), instanceOperationMarkerName) {
				break
			}
			if entry.IsDir() {
				return fmt.Errorf("interrupted restore checkpoint %q is a directory", entry.Name())
			}
			checkpoint := filepath.Join(parent, entry.Name())
			marker, markerErr := readInstanceOperationMarker(checkpointMarkerPath(checkpoint))
			if markerErr != nil {
				return fmt.Errorf("refuse unverified interrupted restore checkpoint %q: %w", entry.Name(), markerErr)
			}
			if marker.ID != targetID {
				continue
			}
			if marker.Version != 1 || marker.Target != canonicalTarget {
				return fmt.Errorf("refuse unverified interrupted restore checkpoint %q: marker does not match target", entry.Name())
			}
			if err := os.Remove(checkpoint); err != nil {
				return fmt.Errorf("remove interrupted restore checkpoint %q: %w", entry.Name(), err)
			}
			_ = os.Remove(checkpointMarkerPath(checkpoint))
			break
		}
	}
	return nil
}

func removeInstanceStateTree(root string) error {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return os.Remove(root)
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if err := os.Chmod(path, instanceRestoreDirMode); err != nil {
			return fmt.Errorf("make instance state directory %q removable: %w", path, err)
		}
		return nil
	}); err != nil {
		return err
	}
	return os.RemoveAll(root)
}

func validatePreservedRelativeFile(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	clean := filepath.Clean(value)
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("preserved restore file must be a relative path inside the instance home")
	}
	return clean, nil
}

func preserveFileAcrossRestore(currentHome, restoredHome, relativePath string) error {
	currentPath := filepath.Join(currentHome, relativePath)
	info, err := os.Lstat(currentPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("preserved restore path %q is not a regular file", relativePath)
	}
	restoredPath := filepath.Join(restoredHome, relativePath)
	if err := os.Remove(restoredPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(restoredPath), instanceRestoreDirMode); err != nil {
		return err
	}
	if err := os.Link(currentPath, restoredPath); err != nil {
		return fmt.Errorf("preserve restore file %q: %w", relativePath, err)
	}
	return nil
}

func dirExistsNonEmptyExcept(path, ignoredRelativeFile string) (bool, bool, error) {
	exists, _, err := dirExistsNonEmpty(path)
	if err != nil || !exists {
		return exists, false, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, false, err
	}
	for _, entry := range entries {
		if ignoredRelativeFile != "" && entry.Name() == ignoredRelativeFile {
			continue
		}
		return true, true, nil
	}
	return true, false, nil
}

func extractInstanceBackupReader(ctx context.Context, archive io.Reader, targetDir string) error {
	gzr, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("open instance backup gzip: %w", err)
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	seenManifest := false
	seenDB := false
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read instance backup: %w", err)
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == "." || filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) || name == ".." {
			return fmt.Errorf("instance backup contains unsafe path %q", header.Name)
		}
		if strings.HasPrefix(filepath.ToSlash(name), ".leapview-restore-old-") {
			return fmt.Errorf("instance backup contains reserved path %q", header.Name)
		}
		target, err := securejoin.SecureJoin(targetDir, name)
		if err != nil {
			return fmt.Errorf("resolve instance backup path %q: %w", header.Name, err)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, instanceRestoreDirMode); err != nil {
				return err
			}
			if err := os.Chmod(target, instanceRestoreDirMode); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), instanceRestoreDirMode); err != nil {
				return err
			}
			fileMode := instanceRestoreModeForFile(header.Name)
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fileMode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if err := os.Chmod(target, fileMode); err != nil {
				return err
			}
		case tar.TypeSymlink:
			return fmt.Errorf("instance backup symlink entries are not supported: %s", header.Name)
		default:
			return fmt.Errorf("instance backup contains unsupported entry %q", header.Name)
		}
		if header.Name == instanceBackupManifestName {
			seenManifest = true
			if err := validateInstanceBackupManifest(target); err != nil {
				return err
			}
		}
		if header.Name == instanceBackupDBName {
			seenDB = true
		}
	}
	if !seenManifest {
		return fmt.Errorf("instance backup is missing %s", instanceBackupManifestName)
	}
	if !seenDB {
		return fmt.Errorf("instance backup is missing %s", instanceBackupDBName)
	}
	return validateBackupDatabase(ctx, filepath.Join(targetDir, instanceBackupDBName))
}

func instanceRestoreModeForFile(name string) os.FileMode {
	if filepath.ToSlash(filepath.Clean(name)) == instanceBackupDBName {
		return instanceRestoreDBMode
	}
	return instanceRestoreFileMode
}

func validateInstanceBackupSymlink(name, linkname string) error {
	cleanLink := path.Clean(filepath.ToSlash(linkname))
	if filepath.IsAbs(linkname) || cleanLink == ".." || strings.HasPrefix(cleanLink, "../") {
		return fmt.Errorf("instance backup contains unsafe symlink %q", name)
	}
	return nil
}

func validateInstanceBackupManifest(path string) error {
	document, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var probe struct {
		SchemaVersion int `json:"schemaVersion"`
		Version       int `json:"version"`
	}
	if err := json.Unmarshal(document, &probe); err != nil {
		return fmt.Errorf("read instance backup manifest: %w", err)
	}
	if probe.SchemaVersion == InstanceBackupManifestVersion {
		var manifest InstanceBackupManifestV2
		return validateManifestV2Document(document, &manifest)
	}
	var manifest instanceBackupManifest
	if err := json.Unmarshal(document, &manifest); err != nil {
		return fmt.Errorf("read legacy instance backup manifest: %w", err)
	}
	if manifest.Kind != "leapview-instance" {
		return fmt.Errorf("instance backup manifest kind = %q", manifest.Kind)
	}
	if manifest.Version != instanceBackupLegacyVersion {
		return fmt.Errorf("unsupported instance backup version %d", manifest.Version)
	}
	if manifest.DBPath != instanceBackupDBName {
		return fmt.Errorf("instance backup manifest database path = %q", manifest.DBPath)
	}
	return nil
}

func addJSONToTar(tw *tar.Writer, name string, value any) error {
	bytes, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	bytes = append(bytes, '\n')
	header := &tar.Header{
		Name:    name,
		Mode:    0o644,
		Size:    int64(len(bytes)),
		ModTime: time.Now().UTC(),
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err = tw.Write(bytes)
	return err
}

func addFileToTar(tw *tar.Writer, sourcePath, name string) error {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = name
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(tw, file)
	return err
}

func closeArchiveStreamWriters(tw *tar.Writer, gzw *gzip.Writer) error {
	if err := tw.Close(); err != nil {
		_ = gzw.Close()
		return err
	}
	if err := gzw.Close(); err != nil {
		return err
	}
	return nil
}

func dirExistsNonEmpty(path string) (bool, bool, error) {
	entries, err := os.ReadDir(path)
	if err == nil {
		return true, len(entries) > 0, nil
	}
	if os.IsNotExist(err) {
		return false, false, nil
	}
	return false, false, err
}

func pathWithin(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if samePath(parent, child) {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
