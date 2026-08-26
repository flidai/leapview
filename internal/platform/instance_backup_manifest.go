package platform

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/platform/ociref"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/mod/semver"
)

const (
	InstanceBackupManifestVersion      = 2
	InstanceBackupValidationBufferSize = 64 << 10
	instanceBackupManifestMaxBytes     = 1 << 20

	RestorePreflightAllowed             = "restore.preflight.allowed"
	RestorePreflightArchiveInvalid      = "restore.preflight.denied.archive_invalid"
	RestorePreflightCheckpointInvalid   = "restore.preflight.denied.checkpoint_invalid"
	RestorePreflightChecksumMismatch    = "restore.preflight.denied.checksum_mismatch"
	RestorePreflightDuplicatePath       = "restore.preflight.denied.duplicate_path"
	RestorePreflightExternalEvidence    = "restore.preflight.denied.external_evidence_missing"
	RestorePreflightIncompatibleRelease = "restore.preflight.denied.incompatible_release"
	RestorePreflightInsufficientDisk    = "restore.preflight.denied.insufficient_disk"
	RestorePreflightMemberMissing       = "restore.preflight.denied.member_missing"
	RestorePreflightMemberUnexpected    = "restore.preflight.denied.member_unexpected"
	RestorePreflightStaleArchive        = "restore.preflight.denied.stale_archive"
	RestorePreflightStaleTarget         = "restore.preflight.denied.stale_target"
	RestorePreflightStoppedRequired     = "restore.preflight.denied.exclusive_lock_required"
	RestorePreflightStorageTopology     = "restore.preflight.denied.storage_topology_mismatch"
	RestorePreflightUnsafePath          = "restore.preflight.denied.unsafe_path"
	RestorePreflightUnsupportedEntry    = "restore.preflight.denied.unsupported_entry"
	RestorePreflightUnsupportedManifest = "restore.preflight.denied.unsupported_manifest"
	RestorePreflightWrongEnvironment    = "restore.preflight.denied.wrong_environment"
)

//go:embed instance_backup_manifest_v2.schema.json
var instanceBackupManifestV2SchemaJSON []byte

type InstanceBackupStorageTopology struct {
	ControlPlane   string                                 `json:"controlPlane"`
	ManagedData    string                                 `json:"managedData"`
	DuckLake       string                                 `json:"duckLake"`
	ExternalStores []InstanceBackupExternalStoreReference `json:"externalStores"`
}

type InstanceBackupExternalStoreReference struct {
	Role          string `json:"role"`
	Provider      string `json:"provider"`
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
	Bucket        string `json:"bucket"`
	Prefix        string `json:"prefix"`
	RecoveryPoint string `json:"recoveryPoint"`
	EvidenceKey   string `json:"evidenceKey"`
}

type InstanceBackupMember struct {
	Path   string `json:"path"`
	Role   string `json:"role"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	SHA256 string `json:"sha256"`
}

type InstanceBackupManifestV2 struct {
	SchemaVersion                   int                           `json:"schemaVersion"`
	Kind                            string                        `json:"kind"`
	BackupID                        string                        `json:"backupId"`
	ReleaseIdentity                 compatibility.ReleaseIdentity `json:"releaseIdentity"`
	InstanceID                      string                        `json:"instanceId"`
	Environment                     string                        `json:"environment"`
	CreatedAt                       time.Time                     `json:"createdAt"`
	CompletedAt                     time.Time                     `json:"completedAt"`
	ArchiveMode                     string                        `json:"archiveMode"`
	StorageTopology                 InstanceBackupStorageTopology `json:"storageTopology"`
	RequiredTransitionPolicyVersion string                        `json:"requiredTransitionPolicyVersion"`
	Members                         []InstanceBackupMember        `json:"members"`
	InventorySHA256                 string                        `json:"inventorySha256"`
}

type instanceBackupStagedMember struct {
	manifest InstanceBackupMember
	source   string
}

type InstanceRestorePreflightOptions struct {
	ArchivePath            string
	TargetHomeDir          string
	ExpectedEnvironment    string
	TargetReleaseIdentity  compatibility.ReleaseIdentity
	TargetStorageTopology  InstanceBackupStorageTopology
	CurrentStorageTopology InstanceBackupStorageTopology
	ExternalEvidence       map[string]string
	MinimumFreeBytes       uint64
	PreserveRelativeFile   string
	ResetRelativePaths     []string
	ExclusiveLockHeld      bool
	RequireExclusiveLock   bool
	CurrentBackupOut       string
	DiscardCurrentBackup   bool
	TransitionPolicy       *compatibility.Policy
}

type InstanceRestorePreflightPlan struct {
	SchemaVersion            int                                    `json:"schemaVersion"`
	Allowed                  bool                                   `json:"allowed"`
	ReasonCode               string                                 `json:"reasonCode"`
	Remediation              string                                 `json:"remediation,omitempty"`
	BackupID                 string                                 `json:"backupId,omitempty"`
	ManifestVersion          int                                    `json:"manifestVersion"`
	ManifestSHA256           string                                 `json:"manifestSha256,omitempty"`
	PolicyVersion            string                                 `json:"transitionPolicyVersion,omitempty"`
	ArchivePath              string                                 `json:"archivePath"`
	ArchiveSHA256            string                                 `json:"archiveSha256,omitempty"`
	ArchiveSize              int64                                  `json:"archiveSize,omitempty"`
	TargetHome               string                                 `json:"targetHome"`
	CheckpointPath           string                                 `json:"checkpointPath,omitempty"`
	TargetTreeSHA256         string                                 `json:"targetTreeSha256,omitempty"`
	Environment              string                                 `json:"environment,omitempty"`
	ArchiveRelease           compatibility.ReleaseIdentity          `json:"archiveRelease,omitempty"`
	TargetRelease            compatibility.ReleaseIdentity          `json:"targetRelease,omitempty"`
	Replace                  []string                               `json:"replace"`
	Preserve                 []string                               `json:"preserve"`
	Reset                    []string                               `json:"reset"`
	ExternalPrerequisites    []InstanceBackupExternalStoreReference `json:"externalPrerequisites"`
	TargetStorageTopology    InstanceBackupStorageTopology          `json:"targetStorageTopology"`
	CheckpointTopology       InstanceBackupStorageTopology          `json:"checkpointStorageTopology"`
	RequiredBytes            uint64                                 `json:"requiredBytes"`
	AvailableBytes           uint64                                 `json:"availableBytes"`
	CheckpointRequiredBytes  uint64                                 `json:"checkpointRequiredBytes"`
	CheckpointAvailableBytes uint64                                 `json:"checkpointAvailableBytes"`
	ValidationBufferBytes    int                                    `json:"validationBufferBytes"`
	ExclusiveLockVerified    bool                                   `json:"exclusiveLockVerified"`
}

type InstanceRestorePreflightError struct {
	ReasonCode  string
	Remediation string
	Err         error
}

func (e *InstanceRestorePreflightError) Error() string {
	if e == nil {
		return ""
	}
	message := e.ReasonCode
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	if e.Remediation != "" {
		message += "; " + e.Remediation
	}
	return message
}

func (e *InstanceRestorePreflightError) Unwrap() error { return e.Err }

func preflightDenied(plan InstanceRestorePreflightPlan, reason, remediation string, err error) (InstanceRestorePreflightPlan, error) {
	plan.Allowed = false
	plan.ReasonCode = reason
	plan.Remediation = remediation
	return plan, &InstanceRestorePreflightError{ReasonCode: reason, Remediation: remediation, Err: err}
}

func normalizeBackupReleaseIdentity(identity compatibility.ReleaseIdentity) (compatibility.ReleaseIdentity, error) {
	if identity == (compatibility.ReleaseIdentity{}) {
		return compatibility.ReleaseIdentity{}, fmt.Errorf("exact backup release identity is required; unknown provenance is not a development release")
	}
	return identity, nil
}

func instanceTransitionPolicy(policy *compatibility.Policy) (*compatibility.Policy, error) {
	if policy == nil {
		return compatibility.EmbeddedPolicy()
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return policy, nil
}

func normalizeBackupStorageTopology(topology InstanceBackupStorageTopology) InstanceBackupStorageTopology {
	if topology.ControlPlane == "" {
		topology.ControlPlane = "local"
	}
	if topology.ManagedData == "" {
		topology.ManagedData = "local"
	}
	if topology.DuckLake == "" {
		topology.DuckLake = "local"
	}
	if topology.ExternalStores == nil {
		topology.ExternalStores = []InstanceBackupExternalStoreReference{}
	} else {
		external := make([]InstanceBackupExternalStoreReference, len(topology.ExternalStores))
		copy(external, topology.ExternalStores)
		topology.ExternalStores = external
	}
	sort.Slice(topology.ExternalStores, func(i, j int) bool {
		left, right := topology.ExternalStores[i], topology.ExternalStores[j]
		return externalStoreReferenceKey(left) < externalStoreReferenceKey(right)
	})
	return topology
}

func externalStoreIdentityKey(reference InstanceBackupExternalStoreReference) string {
	return strings.Join([]string{
		reference.Role, reference.Provider, reference.Endpoint, reference.Region, reference.Bucket, reference.Prefix,
	}, "\x00")
}

func externalStoreReferenceKey(reference InstanceBackupExternalStoreReference) string {
	return strings.Join([]string{externalStoreIdentityKey(reference), reference.RecoveryPoint, reference.EvidenceKey}, "\x00")
}

func newBackupID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "lvbackup_" + hex.EncodeToString(value[:]), nil
}

func backupMemberRole(name string) string {
	switch {
	case name == instanceBackupDBName:
		return "control-plane-database"
	case name == "managed-data" || strings.HasPrefix(name, "managed-data/"):
		return "managed-data-object"
	case name == "ducklake/catalog" || strings.HasPrefix(name, "ducklake/catalog/") || strings.HasPrefix(name, "ducklake/catalog."):
		return "ducklake-catalog"
	case name == "ducklake/data" || strings.HasPrefix(name, "ducklake/data/"):
		return "ducklake-data"
	default:
		return "recovery-metadata"
	}
}

func collectInstanceBackupMembers(homeAbs, dbAbs, dbCopy string, excluded []string) ([]instanceBackupStagedMember, error) {
	members := make([]instanceBackupStagedMember, 0, 32)
	seen := make(map[string]struct{})
	appendMember := func(name, source string, info os.FileInfo) error {
		name = filepath.ToSlash(name)
		if _, err := secureArchiveMemberPath(name); err != nil {
			return err
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate backup member %q", name)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("instance backup contains unsupported entry %q", name)
		}
		digest, err := fileSHA256(source)
		if err != nil {
			return err
		}
		seen[name] = struct{}{}
		members = append(members, instanceBackupStagedMember{source: source, manifest: InstanceBackupMember{
			Path: name, Role: backupMemberRole(name), Size: info.Size(), Mode: uint32(info.Mode().Perm()), SHA256: digest,
		}})
		return nil
	}
	dbInfo, err := os.Stat(dbCopy)
	if err != nil {
		return nil, err
	}
	if err := appendMember(instanceBackupDBName, dbCopy, dbInfo); err != nil {
		return nil, err
	}
	err = walkInstanceBackupFiles(homeAbs, dbAbs, excluded, func(rel, current string, info os.FileInfo) error {
		return appendMember(rel, current, info)
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(members, func(i, j int) bool { return members[i].manifest.Path < members[j].manifest.Path })
	return members, nil
}

func walkInstanceBackupFiles(homeAbs, dbAbs string, excluded []string, visit func(string, string, os.FileInfo) error) error {
	return filepath.WalkDir(homeAbs, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		currentAbs, err := filepath.Abs(current)
		if err != nil {
			return err
		}
		if samePath(currentAbs, homeAbs) {
			return nil
		}
		if samePath(currentAbs, dbAbs) || samePath(currentAbs, dbAbs+"-wal") || samePath(currentAbs, dbAbs+"-shm") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(homeAbs, currentAbs)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == instanceBackupManifestName {
			return nil
		}
		if instanceRelativePathMatches(rel, excluded) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(currentAbs)
			if err != nil {
				return err
			}
			if err := validateInstanceBackupSymlink(rel, target); err != nil {
				return err
			}
			return fmt.Errorf("instance backup symlink entries are not supported: %s", rel)
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("instance backup contains unsupported entry %q", rel)
		}
		return visit(rel, currentAbs, info)
	})
}

func writeManifestV2Archive(out io.Writer, manifest InstanceBackupManifestV2, members []instanceBackupStagedMember) error {
	document, err := writeManifestV2JSON(manifest)
	if err != nil {
		return err
	}
	gzw := gzip.NewWriter(out)
	tw := tar.NewWriter(gzw)
	manifestHeader := &tar.Header{
		Name: instanceBackupManifestName, Typeflag: tar.TypeReg, Mode: int64(instanceRestoreFileMode.Perm()),
		Size: int64(len(document)), ModTime: manifest.CreatedAt,
	}
	if err := tw.WriteHeader(manifestHeader); err != nil {
		_ = closeArchiveStreamWriters(tw, gzw)
		return err
	}
	if _, err := tw.Write(document); err != nil {
		_ = closeArchiveStreamWriters(tw, gzw)
		return err
	}
	buffer := make([]byte, InstanceBackupValidationBufferSize)
	for _, member := range members {
		header := &tar.Header{
			Name: member.manifest.Path, Typeflag: tar.TypeReg, Mode: int64(member.manifest.Mode),
			Size: member.manifest.Size, ModTime: manifest.CreatedAt,
		}
		if err := tw.WriteHeader(header); err != nil {
			_ = closeArchiveStreamWriters(tw, gzw)
			return err
		}
		file, err := os.Open(member.source)
		if err != nil {
			_ = closeArchiveStreamWriters(tw, gzw)
			return err
		}
		digest := sha256.New()
		written, copyErr := io.CopyBuffer(io.MultiWriter(tw, digest), file, buffer)
		closeErr := file.Close()
		if copyErr != nil {
			_ = closeArchiveStreamWriters(tw, gzw)
			return copyErr
		}
		if closeErr != nil {
			_ = closeArchiveStreamWriters(tw, gzw)
			return closeErr
		}
		if written != member.manifest.Size || hex.EncodeToString(digest.Sum(nil)) != member.manifest.SHA256 {
			_ = closeArchiveStreamWriters(tw, gzw)
			return fmt.Errorf("backup member %q changed while writing archive", member.manifest.Path)
		}
	}
	return closeArchiveStreamWriters(tw, gzw)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.CopyBuffer(digest, file, make([]byte, InstanceBackupValidationBufferSize)); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func inventorySHA256(members []InstanceBackupMember) (string, error) {
	encoded, err := json.Marshal(members)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateManifestV2Document(document []byte, manifest *InstanceBackupManifestV2) error {
	if len(document) == 0 || len(document) > instanceBackupManifestMaxBytes {
		return fmt.Errorf("manifest size must be between 1 and %d bytes", instanceBackupManifestMaxBytes)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		return fmt.Errorf("decode backup manifest document: %w", err)
	}
	schemaDocument, err := jsonschema.UnmarshalJSON(bytes.NewReader(instanceBackupManifestV2SchemaJSON))
	if err != nil {
		return fmt.Errorf("decode backup manifest schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	const schemaURL = "https://leapview.dev/schemas/instance-backup-manifest-v2.schema.json"
	if err := compiler.AddResource(schemaURL, schemaDocument); err != nil {
		return err
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		return err
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("validate backup manifest schema: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(manifest); err != nil {
		return fmt.Errorf("decode backup manifest: %w", err)
	}
	return validateManifestV2(manifest)
}

type InstanceBackupEvidenceExpectation struct {
	ArtifactIdentity string
	PolicyVersion    string
	TargetScope      string
}

// ValidateInstanceBackupManifestDocument is the backup owner's canonical
// evidence validator. It applies the same schema, topology, inventory, and
// release checks used by restore preflight and additionally binds scheduled
// ledger intent when an expectation is supplied.
func ValidateInstanceBackupManifestDocument(document []byte, expected InstanceBackupEvidenceExpectation) (InstanceBackupManifestV2, error) {
	var manifest InstanceBackupManifestV2
	if err := validateManifestV2Document(document, &manifest); err != nil {
		return InstanceBackupManifestV2{}, err
	}
	if expected.ArtifactIdentity != "" && manifest.ReleaseIdentity.Image != expected.ArtifactIdentity {
		return InstanceBackupManifestV2{}, fmt.Errorf("backup manifest release does not match scheduled artifact")
	}
	if expected.PolicyVersion != "" && manifest.RequiredTransitionPolicyVersion != expected.PolicyVersion {
		return InstanceBackupManifestV2{}, fmt.Errorf("backup manifest policy does not match scheduled policy")
	}
	if expected.TargetScope != "" && expected.TargetScope != "instance:"+manifest.InstanceID {
		return InstanceBackupManifestV2{}, fmt.Errorf("backup manifest instance does not match scheduled target")
	}
	return manifest, nil
}

// ReadInstanceBackupManifestDocument returns the exact manifest bytes written
// as the first member of a v2 archive. The returned document is validated by
// the same owner validator used by restore preflight.
func ReadInstanceBackupManifestDocument(archivePath string) ([]byte, InstanceBackupManifestV2, error) {
	file, err := os.Open(strings.TrimSpace(archivePath))
	if err != nil {
		return nil, InstanceBackupManifestV2{}, err
	}
	defer file.Close()
	gzr, err := gzip.NewReader(file)
	if err != nil {
		return nil, InstanceBackupManifestV2{}, err
	}
	header, err := tar.NewReader(gzr).Next()
	if err != nil || header.Name != instanceBackupManifestName ||
		(header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) ||
		header.Size < 1 || header.Size > instanceBackupManifestMaxBytes {
		if err == nil {
			err = fmt.Errorf("backup archive does not begin with a bounded v2 manifest")
		}
		_ = gzr.Close()
		return nil, InstanceBackupManifestV2{}, err
	}
	// Recreate the reader so the manifest body follows the validated header.
	if err := gzr.Close(); err != nil {
		return nil, InstanceBackupManifestV2{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, InstanceBackupManifestV2{}, err
	}
	gzr, err = gzip.NewReader(file)
	if err != nil {
		return nil, InstanceBackupManifestV2{}, err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	header, err = tr.Next()
	if err != nil {
		return nil, InstanceBackupManifestV2{}, err
	}
	document, err := io.ReadAll(io.LimitReader(tr, instanceBackupManifestMaxBytes+1))
	if err != nil || len(document) > instanceBackupManifestMaxBytes || int64(len(document)) != header.Size {
		if err == nil {
			err = fmt.Errorf("backup manifest is truncated or exceeds its size limit")
		}
		return nil, InstanceBackupManifestV2{}, err
	}
	manifest, err := ValidateInstanceBackupManifestDocument(document, InstanceBackupEvidenceExpectation{})
	return document, manifest, err
}

// ValidateInstanceRestorePreflightDocument is the restore owner's canonical
// validator for a persisted successful preflight report and its exact manifest.
func ValidateInstanceRestorePreflightDocument(document []byte, manifestDocument []byte, expected InstanceBackupEvidenceExpectation) (InstanceRestorePreflightPlan, InstanceBackupManifestV2, error) {
	manifest, err := ValidateInstanceBackupManifestDocument(manifestDocument, expected)
	if err != nil {
		return InstanceRestorePreflightPlan{}, InstanceBackupManifestV2{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var plan InstanceRestorePreflightPlan
	if err := decoder.Decode(&plan); err != nil {
		return InstanceRestorePreflightPlan{}, InstanceBackupManifestV2{}, fmt.Errorf("decode restore preflight evidence: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return InstanceRestorePreflightPlan{}, InstanceBackupManifestV2{}, fmt.Errorf("restore preflight evidence contains trailing data")
	}
	manifestDigest := sha256.Sum256(manifestDocument)
	if plan.SchemaVersion != 1 || !plan.Allowed || plan.ReasonCode != RestorePreflightAllowed ||
		plan.BackupID != manifest.BackupID || plan.ManifestVersion != manifest.SchemaVersion ||
		plan.ManifestSHA256 != hex.EncodeToString(manifestDigest[:]) ||
		plan.PolicyVersion != manifest.RequiredTransitionPolicyVersion || plan.Environment != manifest.Environment ||
		plan.ArchiveRelease != manifest.ReleaseIdentity ||
		(expected.ArtifactIdentity != "" && plan.TargetRelease.Image != expected.ArtifactIdentity) ||
		!plan.ExclusiveLockVerified || plan.ArchiveSHA256 == "" || plan.TargetTreeSHA256 == "" {
		return InstanceRestorePreflightPlan{}, InstanceBackupManifestV2{}, fmt.Errorf("restore preflight evidence is incomplete or does not bind the supplied manifest and schedule")
	}
	if !reflect.DeepEqual(plan.ExternalPrerequisites, manifest.StorageTopology.ExternalStores) ||
		!sameBackupStorageIdentity(plan.TargetStorageTopology, manifest.StorageTopology) {
		return InstanceRestorePreflightPlan{}, InstanceBackupManifestV2{}, fmt.Errorf("restore preflight evidence does not preserve archive storage topology")
	}
	if err := validateBackupStorageIdentity(plan.TargetStorageTopology); err != nil {
		return InstanceRestorePreflightPlan{}, InstanceBackupManifestV2{}, fmt.Errorf("restore preflight target topology: %w", err)
	}
	for _, external := range plan.ExternalPrerequisites {
		if err := validateExternalRecoveryReference(external); err != nil {
			return InstanceRestorePreflightPlan{}, InstanceBackupManifestV2{}, err
		}
	}
	return plan, manifest, nil
}

func validateManifestV2(manifest *InstanceBackupManifestV2) error {
	if manifest.SchemaVersion != InstanceBackupManifestVersion {
		return fmt.Errorf("unsupported backup manifest version %d", manifest.SchemaVersion)
	}
	if manifest.CompletedAt.Before(manifest.CreatedAt) {
		return fmt.Errorf("backup completion precedes creation")
	}
	if manifest.ReleaseIdentity == (compatibility.ReleaseIdentity{}) {
		return fmt.Errorf("backup release identity is required")
	}
	identity := manifest.ReleaseIdentity
	if identity.ReleaseID == "development" {
		if identity.Distribution != "development" || identity.Image != "development://local-binary" {
			return fmt.Errorf("development backup identity is invalid")
		}
	} else {
		if strings.TrimSpace(identity.ReleaseID) == "" || strings.TrimSpace(identity.Version) == "" ||
			len(identity.SourceRevision) != 40 || strings.TrimSpace(identity.Distribution) == "" || strings.TrimSpace(identity.Platform) == "" {
			return fmt.Errorf("released backup identity is incomplete")
		}
		if err := ociref.ValidateImmutable(identity.Image); err != nil {
			return fmt.Errorf("released backup image identity: %w", err)
		}
	}
	externalKeys := make(map[string]struct{}, len(manifest.StorageTopology.ExternalStores))
	previousExternal := ""
	for _, external := range manifest.StorageTopology.ExternalStores {
		key := externalStoreReferenceKey(external)
		if previousExternal != "" && key < previousExternal {
			return fmt.Errorf("external store references are not sorted")
		}
		if _, duplicate := externalKeys[external.EvidenceKey]; duplicate {
			return fmt.Errorf("duplicate external evidence key %q", external.EvidenceKey)
		}
		if err := validateExternalRecoveryReference(external); err != nil {
			return err
		}
		externalKeys[external.EvidenceKey] = struct{}{}
		previousExternal = key
	}
	if err := validateBackupStorageTopology(manifest.StorageTopology); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(manifest.Members))
	previous := ""
	seenDatabase := false
	for _, member := range manifest.Members {
		clean, err := secureArchiveMemberPath(member.Path)
		if err != nil {
			return err
		}
		if clean != member.Path || member.Path == instanceBackupManifestName {
			return fmt.Errorf("manifest contains non-canonical member path %q", member.Path)
		}
		if _, ok := seen[member.Path]; ok {
			return fmt.Errorf("manifest contains duplicate member path %q", member.Path)
		}
		if previous != "" && member.Path < previous {
			return fmt.Errorf("manifest member inventory is not sorted")
		}
		seen[member.Path] = struct{}{}
		if member.Path == instanceBackupDBName {
			if member.Role != "control-plane-database" {
				return fmt.Errorf("control-plane database member has role %q", member.Role)
			}
			seenDatabase = true
		}
		previous = member.Path
	}
	if !seenDatabase {
		return fmt.Errorf("manifest is missing the control-plane database")
	}
	digest, err := inventorySHA256(manifest.Members)
	if err != nil {
		return err
	}
	if digest != manifest.InventorySHA256 {
		return fmt.Errorf("manifest inventory checksum mismatch")
	}
	return nil
}

func validateExternalRecoveryReference(reference InstanceBackupExternalStoreReference) error {
	if err := validateExternalStoreIdentity(reference); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"recovery point": reference.RecoveryPoint, "evidence key": reference.EvidenceKey,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("external store %s is required", field)
		}
	}
	return validateExternalStoreSafeValues(reference)
}

func validateExternalStoreIdentity(reference InstanceBackupExternalStoreReference) error {
	for field, value := range map[string]string{
		"role": reference.Role, "provider": reference.Provider, "endpoint": reference.Endpoint,
		"region": reference.Region, "bucket": reference.Bucket,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("external store %s is required", field)
		}
	}
	if reference.Provider != "aws" && reference.Provider != "s3-compatible" {
		return fmt.Errorf("external store provider %q is unsupported", reference.Provider)
	}
	return validateExternalStoreSafeValues(reference)
}

func validateExternalStoreSafeValues(reference InstanceBackupExternalStoreReference) error {
	for field, value := range map[string]string{
		"endpoint": reference.Endpoint, "region": reference.Region, "bucket": reference.Bucket,
		"prefix": reference.Prefix, "recovery point": reference.RecoveryPoint,
	} {
		if strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return fmt.Errorf("external store %s contains control characters", field)
		}
		lower := strings.ToLower(value)
		for _, marker := range []string{"authorization=", "credential=", "password=", "secret=", "token="} {
			if strings.Contains(lower, marker) {
				return fmt.Errorf("external store %s contains secret-bearing material", field)
			}
		}
		if field == "endpoint" {
			parsed, err := url.Parse(value)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
				parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
				return fmt.Errorf("external store endpoint must be a credential-free canonical origin")
			}
		}
	}
	if reference.Prefix != strings.Trim(reference.Prefix, "/") || strings.Contains(reference.Prefix, "//") ||
		(reference.Prefix != "" && (path.Clean(reference.Prefix) != reference.Prefix || path.IsAbs(reference.Prefix) || strings.HasPrefix(reference.Prefix, "../"))) {
		return fmt.Errorf("external store prefix must be a canonical relative object prefix")
	}
	return nil
}

func backupStorageIdentity(topology InstanceBackupStorageTopology) InstanceBackupStorageTopology {
	topology = normalizeBackupStorageTopology(topology)
	for index := range topology.ExternalStores {
		topology.ExternalStores[index].RecoveryPoint = ""
		topology.ExternalStores[index].EvidenceKey = ""
	}
	sort.Slice(topology.ExternalStores, func(i, j int) bool {
		return externalStoreIdentityKey(topology.ExternalStores[i]) < externalStoreIdentityKey(topology.ExternalStores[j])
	})
	return topology
}

func validateBackupStorageIdentity(topology InstanceBackupStorageTopology) error {
	topology = backupStorageIdentity(topology)
	if err := validateBackupStorageTopology(topology); err != nil {
		return err
	}
	for _, external := range topology.ExternalStores {
		if err := validateExternalStoreIdentity(external); err != nil {
			return err
		}
	}
	return nil
}

func sameBackupStorageIdentity(left, right InstanceBackupStorageTopology) bool {
	return reflect.DeepEqual(backupStorageIdentity(left), backupStorageIdentity(right))
}

func validateBackupStorageTopology(topology InstanceBackupStorageTopology) error {
	if topology.ControlPlane != "local" {
		return fmt.Errorf("control-plane storage topology must be local")
	}
	requiredRoles := make(map[string]bool)
	switch topology.ManagedData {
	case "local":
	case "external":
		requiredRoles["managed-data"] = false
	default:
		return fmt.Errorf("unsupported managed-data storage topology %q", topology.ManagedData)
	}
	switch topology.DuckLake {
	case "local":
	case "external":
		requiredRoles["ducklake-catalog"] = false
		requiredRoles["ducklake-data"] = false
	default:
		return fmt.Errorf("unsupported DuckLake storage topology %q", topology.DuckLake)
	}
	for _, external := range topology.ExternalStores {
		if _, expected := requiredRoles[external.Role]; !expected {
			return fmt.Errorf("external store role %q does not match declared storage topology", external.Role)
		}
		requiredRoles[external.Role] = true
	}
	for role, present := range requiredRoles {
		if !present {
			return fmt.Errorf("external storage topology requires a %q recovery reference", role)
		}
	}
	return nil
}

func secureArchiveMemberPath(value string) (string, error) {
	clean := path.Clean(strings.TrimSpace(value))
	if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(value, "\\") {
		return "", fmt.Errorf("unsafe archive path %q", value)
	}
	if clean != value {
		return "", fmt.Errorf("non-canonical archive path %q", value)
	}
	return clean, nil
}

func PreflightInstanceRestore(ctx context.Context, options InstanceRestorePreflightOptions) (InstanceRestorePreflightPlan, error) {
	archivePath := strings.TrimSpace(options.ArchivePath)
	targetHome := strings.TrimSpace(options.TargetHomeDir)
	plan := InstanceRestorePreflightPlan{
		SchemaVersion: 1, ReasonCode: RestorePreflightArchiveInvalid,
		ArchivePath: archivePath, TargetHome: targetHome,
		Replace: []string{}, Preserve: []string{}, Reset: []string{},
		ExternalPrerequisites: []InstanceBackupExternalStoreReference{},
		ValidationBufferBytes: InstanceBackupValidationBufferSize,
		ExclusiveLockVerified: options.ExclusiveLockHeld,
		TargetStorageTopology: normalizeBackupStorageTopology(options.TargetStorageTopology),
		CheckpointTopology:    normalizeBackupStorageTopology(options.CurrentStorageTopology),
	}
	if archivePath == "" || targetHome == "" {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "provide an archive path and target home", fmt.Errorf("archive path and target home are required"))
	}
	if options.RequireExclusiveLock && !options.ExclusiveLockHeld {
		return preflightDenied(plan, RestorePreflightStoppedRequired, "stop the instance and acquire its exclusive offline lock", fmt.Errorf("exclusive instance lock is not held"))
	}
	archiveAbs, err := filepath.Abs(archivePath)
	if err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "provide a readable archive", err)
	}
	targetAbs, err := filepath.Abs(targetHome)
	if err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "provide a valid target home", err)
	}
	plan.ArchivePath, plan.TargetHome = archiveAbs, targetAbs
	_, currentBackupAbs, exists, nonEmpty, err := validateInstanceRestoreDestination(targetAbs, archiveAbs, options.CurrentBackupOut, options.DiscardCurrentBackup, options.PreserveRelativeFile)
	if err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "correct the target and current-backup paths before restore", err)
	}
	if exists && nonEmpty && strings.TrimSpace(options.CurrentBackupOut) == "" {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "provide a current instance backup path before replacing non-empty state", fmt.Errorf("current instance backup path is required when restoring over an existing home dir"))
	}
	plan.CheckpointPath = currentBackupAbs
	plan.Reset, err = normalizeInstanceRelativePaths(options.ResetRelativePaths)
	if err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "use safe reset paths", err)
	}
	if err := validateBackupStorageIdentity(plan.TargetStorageTopology); err != nil {
		return preflightDenied(plan, RestorePreflightStorageTopology, "configure the exact target storage provider, endpoint, region, bucket, and prefix", err)
	}
	if exists && nonEmpty {
		if err := validateBackupStorageTopology(plan.CheckpointTopology); err != nil {
			return preflightDenied(plan, RestorePreflightStorageTopology, "supply exact external recovery references for the pre-restore safety checkpoint", err)
		}
		for _, external := range plan.CheckpointTopology.ExternalStores {
			if err := validateExternalRecoveryReference(external); err != nil {
				return preflightDenied(plan, RestorePreflightStorageTopology, "supply exact external recovery references for the pre-restore safety checkpoint", err)
			}
		}
		if !sameBackupStorageIdentity(plan.TargetStorageTopology, plan.CheckpointTopology) {
			return preflightDenied(plan, RestorePreflightStorageTopology, "checkpoint the current instance against its configured external store identity", fmt.Errorf("current checkpoint topology does not match target storage topology"))
		}
		if _, err := validateInstanceBackupReadiness(ctx, InstanceBackupOptions{
			HomeDir: targetAbs, DBPath: filepath.Join(targetAbs, instanceBackupDBName),
			ExcludeRelativePaths: plan.Reset, Environment: options.ExpectedEnvironment,
			StorageTopology: plan.CheckpointTopology,
		}); err != nil {
			return preflightDenied(plan, RestorePreflightCheckpointInvalid, "repair current instance state so the safety checkpoint can be created", err)
		}
	}
	file, err := os.Open(archiveAbs)
	if err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "provide a readable archive", err)
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("archive is not a regular file")
		}
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "provide a regular backup archive", err)
	}
	plan.ArchiveSize = stat.Size()
	archiveDigest := sha256.New()
	gzr, err := gzip.NewReader(io.TeeReader(file, archiveDigest))
	if err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "provide an intact gzip archive", err)
	}
	tr := tar.NewReader(gzr)
	header, err := tr.Next()
	if err != nil {
		_ = gzr.Close()
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "provide an archive beginning with its manifest", err)
	}
	if header.Name != instanceBackupManifestName || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) {
		_ = gzr.Close()
		return preflightDenied(plan, RestorePreflightUnsupportedManifest, "use a supported archive with leapview-backup.json as its first regular member", fmt.Errorf("first archive member is %q", header.Name))
	}
	if header.Size < 1 || header.Size > instanceBackupManifestMaxBytes {
		_ = gzr.Close()
		return preflightDenied(plan, RestorePreflightUnsupportedManifest, "use a bounded supported manifest", fmt.Errorf("manifest size %d is invalid", header.Size))
	}
	document, err := io.ReadAll(io.LimitReader(tr, instanceBackupManifestMaxBytes+1))
	if err != nil {
		_ = gzr.Close()
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "provide an intact manifest", err)
	}
	var versionProbe struct {
		SchemaVersion int `json:"schemaVersion"`
		Version       int `json:"version"`
	}
	if err := json.Unmarshal(document, &versionProbe); err != nil {
		_ = gzr.Close()
		return preflightDenied(plan, RestorePreflightUnsupportedManifest, "use a supported backup manifest", err)
	}
	if versionProbe.SchemaVersion != InstanceBackupManifestVersion {
		_ = gzr.Close()
		plan.ManifestVersion = versionProbe.Version
		policy, policyErr := instanceTransitionPolicy(options.TransitionPolicy)
		targetRelease, identityErr := normalizeBackupReleaseIdentity(options.TargetReleaseIdentity)
		if policyErr != nil || identityErr != nil {
			return preflightDenied(plan, RestorePreflightIncompatibleRelease, "supply an exact target release identity and valid transition policy", errors.Join(policyErr, identityErr))
		}
		plan.TargetRelease = targetRelease
		if versionProbe.Version == 1 && policy.AllowsLegacyBackup(targetRelease, 1) {
			return preflightLegacyV1(ctx, plan, file, targetAbs, options)
		}
		return preflightDenied(plan, RestorePreflightUnsupportedManifest, "create a manifest v2 backup or use an explicit policy-approved v1 recovery workflow", fmt.Errorf("unsupported manifest version"))
	}
	var manifest InstanceBackupManifestV2
	if err := validateManifestV2Document(document, &manifest); err != nil {
		_ = gzr.Close()
		reason := RestorePreflightUnsupportedManifest
		if strings.Contains(err.Error(), "inventory checksum") {
			reason = RestorePreflightChecksumMismatch
		}
		return preflightDenied(plan, reason, "recreate the backup from an intact stopped instance", err)
	}
	plan.ManifestVersion = manifest.SchemaVersion
	manifestDigest := sha256.Sum256(document)
	plan.ManifestSHA256 = hex.EncodeToString(manifestDigest[:])
	plan.BackupID = manifest.BackupID
	plan.PolicyVersion = manifest.RequiredTransitionPolicyVersion
	plan.Environment = manifest.Environment
	plan.ArchiveRelease = manifest.ReleaseIdentity
	plan.ExternalPrerequisites = append([]InstanceBackupExternalStoreReference{}, manifest.StorageTopology.ExternalStores...)
	if !sameBackupStorageIdentity(manifest.StorageTopology, plan.TargetStorageTopology) {
		return preflightDenied(plan, RestorePreflightStorageTopology, "restore only after configuring the exact external provider, endpoint, region, bucket, and prefix recorded by the backup", fmt.Errorf("archive storage topology does not match target storage topology"))
	}
	expectedMembers := make(map[string]InstanceBackupMember, len(manifest.Members))
	for _, member := range manifest.Members {
		expectedMembers[member.Path] = member
		plan.Replace = append(plan.Replace, member.Path)
		if uint64(member.Size) > math.MaxUint64-plan.RequiredBytes {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightInsufficientDisk, "reduce the archive size", fmt.Errorf("required byte count overflows uint64"))
		}
		plan.RequiredBytes += uint64(member.Size)
	}
	targetBytes, err := instanceTreeRegularBytes(targetAbs, options.PreserveRelativeFile)
	if err != nil {
		_ = gzr.Close()
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "make the target tree readable for preflight", err)
	}
	currentDatabaseBytes := uint64(0)
	if exists && nonEmpty {
		if info, statErr := os.Stat(filepath.Join(targetAbs, instanceBackupDBName)); statErr != nil {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightArchiveInvalid, "repair the current control-plane database before checkpointing", statErr)
		} else if !info.Mode().IsRegular() {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightArchiveInvalid, "repair the current control-plane database before checkpointing", fmt.Errorf("current database is not a regular file"))
		} else {
			currentDatabaseBytes = uint64(info.Size())
		}
	}
	if currentDatabaseBytes > math.MaxUint64-plan.RequiredBytes || options.MinimumFreeBytes > math.MaxUint64-plan.RequiredBytes-currentDatabaseBytes {
		_ = gzr.Close()
		return preflightDenied(plan, RestorePreflightInsufficientDisk, "use representable disk requirements", fmt.Errorf("required byte count overflows uint64"))
	}
	plan.RequiredBytes += currentDatabaseBytes + options.MinimumFreeBytes
	capacityPath, err := existingInstancePath(filepath.Dir(targetAbs))
	if err != nil {
		_ = gzr.Close()
		return preflightDenied(plan, RestorePreflightInsufficientDisk, "make target filesystem capacity measurable", err)
	}
	plan.AvailableBytes, err = instanceFilesystemFreeBytes(capacityPath)
	if err != nil {
		_ = gzr.Close()
		return preflightDenied(plan, RestorePreflightInsufficientDisk, "make target filesystem capacity measurable", err)
	}
	if exists && nonEmpty {
		plan.CheckpointRequiredBytes, err = instanceArchiveCapacityBound(targetBytes)
		if err != nil {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightInsufficientDisk, "use representable checkpoint disk requirements", err)
		}
		checkpointPath, pathErr := existingInstancePath(filepath.Dir(currentBackupAbs))
		if pathErr != nil {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightInsufficientDisk, "make checkpoint filesystem capacity measurable", pathErr)
		}
		plan.CheckpointAvailableBytes, err = instanceFilesystemFreeBytes(checkpointPath)
		if err != nil {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightInsufficientDisk, "make checkpoint filesystem capacity measurable", err)
		}
		targetFilesystem, targetIdentityErr := instanceFilesystemIdentity(capacityPath)
		checkpointFilesystem, checkpointIdentityErr := instanceFilesystemIdentity(checkpointPath)
		if targetIdentityErr != nil || checkpointIdentityErr != nil {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightInsufficientDisk, "identify restore and checkpoint filesystems", errors.Join(targetIdentityErr, checkpointIdentityErr))
		}
		if targetFilesystem == checkpointFilesystem {
			if plan.CheckpointRequiredBytes > math.MaxUint64-plan.RequiredBytes {
				_ = gzr.Close()
				return preflightDenied(plan, RestorePreflightInsufficientDisk, "use representable combined disk requirements", fmt.Errorf("required byte count overflows uint64"))
			}
			plan.RequiredBytes += plan.CheckpointRequiredBytes
		}
		if plan.CheckpointAvailableBytes < plan.CheckpointRequiredBytes {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightInsufficientDisk, "free enough checkpoint disk space before archive decompression", fmt.Errorf("checkpoint available bytes %d are below required bytes %d", plan.CheckpointAvailableBytes, plan.CheckpointRequiredBytes))
		}
	}
	if plan.AvailableBytes < plan.RequiredBytes {
		if err == nil {
			err = fmt.Errorf("available bytes %d are below required bytes %d", plan.AvailableBytes, plan.RequiredBytes)
		}
		_ = gzr.Close()
		return preflightDenied(plan, RestorePreflightInsufficientDisk, "free enough target disk space before archive decompression", err)
	}
	seen := map[string]struct{}{instanceBackupManifestName: {}}
	buffer := make([]byte, InstanceBackupValidationBufferSize)
	databaseMember := expectedMembers[instanceBackupDBName]
	temporaryFreeBytes, err := instanceFilesystemFreeBytes(os.TempDir())
	if err != nil || uint64(databaseMember.Size) > temporaryFreeBytes {
		if err == nil {
			err = fmt.Errorf("temporary preflight space %d is below database bytes %d", temporaryFreeBytes, databaseMember.Size)
		}
		_ = gzr.Close()
		return preflightDenied(plan, RestorePreflightInsufficientDisk, "free temporary space for read-only database validation", err)
	}
	databaseCopy, err := os.CreateTemp("", ".leapview-preflight-database-*.db")
	if err != nil {
		_ = gzr.Close()
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "make bounded preflight staging available", err)
	}
	databaseCopyPath := databaseCopy.Name()
	defer func() {
		_ = databaseCopy.Close()
		_ = os.Remove(databaseCopyPath)
	}()
	for {
		header, err = tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightArchiveInvalid, "recreate the truncated or corrupt backup", err)
		}
		name, pathErr := secureArchiveMemberPath(header.Name)
		if pathErr != nil {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightUnsafePath, "recreate the backup without unsafe paths", pathErr)
		}
		if _, duplicate := seen[name]; duplicate {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightDuplicatePath, "recreate the backup without duplicate members", fmt.Errorf("duplicate archive member %q", name))
		}
		seen[name] = struct{}{}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightUnsupportedEntry, "recreate the backup with regular-file members only", fmt.Errorf("unsupported member type for %q", name))
		}
		member, ok := expectedMembers[name]
		if !ok {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightMemberUnexpected, "recreate the backup from the authoritative inventory", fmt.Errorf("unexpected member %q", name))
		}
		if header.Size != member.Size || uint32(header.Mode)&0o777 != member.Mode {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightChecksumMismatch, "recreate the backup from the authoritative inventory", fmt.Errorf("member metadata mismatch for %q", name))
		}
		digest := sha256.New()
		writer := io.Writer(digest)
		if name == instanceBackupDBName {
			writer = io.MultiWriter(digest, databaseCopy)
		}
		written, copyErr := io.CopyBuffer(writer, tr, buffer)
		if copyErr != nil || written != member.Size {
			_ = gzr.Close()
			if copyErr == nil {
				copyErr = fmt.Errorf("member %q size = %d, want %d", name, written, member.Size)
			}
			return preflightDenied(plan, RestorePreflightArchiveInvalid, "recreate the truncated backup", copyErr)
		}
		if got := hex.EncodeToString(digest.Sum(nil)); got != member.SHA256 {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightChecksumMismatch, "recreate the backup from authoritative state", fmt.Errorf("member checksum mismatch for %q", name))
		}
	}
	if _, err := io.CopyBuffer(io.Discard, gzr, buffer); err != nil {
		_ = gzr.Close()
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "recreate the truncated or corrupt backup", err)
	}
	if err := gzr.Close(); err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "recreate the corrupt gzip archive", err)
	}
	if _, err := io.CopyBuffer(archiveDigest, file, buffer); err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "provide a readable archive", err)
	}
	plan.ArchiveSHA256 = hex.EncodeToString(archiveDigest.Sum(nil))
	if err := databaseCopy.Sync(); err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "make bounded preflight staging durable", err)
	}
	if err := databaseCopy.Close(); err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "finish bounded preflight staging", err)
	}
	for _, member := range manifest.Members {
		if _, ok := seen[member.Path]; !ok {
			return preflightDenied(plan, RestorePreflightMemberMissing, "recreate the complete backup", fmt.Errorf("missing member %q", member.Path))
		}
	}
	if expected := strings.TrimSpace(options.ExpectedEnvironment); expected != "" && manifest.Environment != expected {
		return preflightDenied(plan, RestorePreflightWrongEnvironment, "restore into the matching environment or create a separate instance", fmt.Errorf("archive environment %q does not match %q", manifest.Environment, expected))
	}
	if err := ValidateDatabaseInstanceEnvironment(ctx, databaseCopyPath, manifest.Environment); err != nil {
		return preflightDenied(plan, RestorePreflightWrongEnvironment, "recreate the backup from the declared instance environment", err)
	}
	databaseInstanceID, err := readBackupDatabaseInstanceID(ctx, databaseCopyPath)
	if err != nil || databaseInstanceID != manifest.InstanceID {
		if err == nil {
			err = fmt.Errorf("manifest instance %q does not match archived database instance %q", manifest.InstanceID, databaseInstanceID)
		}
		return preflightDenied(plan, RestorePreflightChecksumMismatch, "recreate the backup from the authoritative instance database", err)
	}
	policy, err := instanceTransitionPolicy(options.TransitionPolicy)
	if err != nil {
		return preflightDenied(plan, RestorePreflightIncompatibleRelease, "repair the embedded transition policy", err)
	}
	if manifest.RequiredTransitionPolicyVersion != policy.PolicyVersion {
		return preflightDenied(plan, RestorePreflightIncompatibleRelease, "use a binary carrying the required transition policy", fmt.Errorf("archive requires policy %q, current policy is %q", manifest.RequiredTransitionPolicyVersion, policy.PolicyVersion))
	}
	targetRelease, err := normalizeBackupReleaseIdentity(options.TargetReleaseIdentity)
	if err != nil {
		return preflightDenied(plan, RestorePreflightIncompatibleRelease, "supply the exact target release identity", err)
	}
	plan.TargetRelease = targetRelease
	if manifest.ReleaseIdentity != targetRelease {
		request, directionErr := restoreTransitionRequest(manifest.ReleaseIdentity, targetRelease)
		if directionErr != nil {
			return preflightDenied(plan, RestorePreflightIncompatibleRelease, "use an explicitly supported upgrade or rollback identity", directionErr)
		}
		decision := policy.Evaluate(request)
		if err := decision.Err(); err != nil {
			return preflightDenied(plan, RestorePreflightIncompatibleRelease, decision.Remediation, err)
		}
	}
	for _, external := range manifest.StorageTopology.ExternalStores {
		if strings.TrimSpace(options.ExternalEvidence[external.EvidenceKey]) != external.RecoveryPoint {
			return preflightDenied(plan, RestorePreflightExternalEvidence, "supply operator evidence for every external recovery point", fmt.Errorf("missing evidence %q", external.EvidenceKey))
		}
	}
	plan.TargetTreeSHA256, err = instanceTreeSHA256(targetAbs, options.PreserveRelativeFile)
	if err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "make the target tree readable for preflight", err)
	}
	if preserve := strings.TrimSpace(options.PreserveRelativeFile); preserve != "" {
		plan.Preserve = []string{filepath.ToSlash(preserve)}
	}
	plan.Allowed = true
	plan.ReasonCode = RestorePreflightAllowed
	plan.Remediation = ""
	return plan, nil
}

func existingInstancePath(value string) (string, error) {
	value = filepath.Clean(value)
	for {
		if _, err := os.Stat(value); err == nil {
			return value, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(value)
		if parent == value {
			return "", fmt.Errorf("no existing ancestor for %q", value)
		}
		value = parent
	}
}

func instanceArchiveCapacityBound(uncompressedBytes uint64) (uint64, error) {
	const fixedArchiveOverhead = uint64(1 << 20)
	compressionOverhead := uncompressedBytes/16 + 1
	if compressionOverhead > math.MaxUint64-uncompressedBytes || fixedArchiveOverhead > math.MaxUint64-uncompressedBytes-compressionOverhead {
		return 0, fmt.Errorf("checkpoint archive capacity overflows uint64")
	}
	return uncompressedBytes + compressionOverhead + fixedArchiveOverhead, nil
}

func preflightLegacyV1(ctx context.Context, plan InstanceRestorePreflightPlan, file *os.File, target string, options InstanceRestorePreflightOptions) (InstanceRestorePreflightPlan, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "provide a seekable v1 archive", err)
	}
	archiveDigest := sha256.New()
	gzr, err := gzip.NewReader(io.TeeReader(file, archiveDigest))
	if err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "provide an intact v1 gzip archive", err)
	}
	tr := tar.NewReader(gzr)
	buffer := make([]byte, InstanceBackupValidationBufferSize)
	seen := make(map[string]struct{})
	seenManifest, seenDatabase := false, false
	databaseCopy, err := os.CreateTemp("", ".leapview-preflight-v1-database-*.db")
	if err != nil {
		_ = gzr.Close()
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "make bounded v1 staging available", err)
	}
	databaseCopyPath := databaseCopy.Name()
	defer func() {
		_ = databaseCopy.Close()
		_ = os.Remove(databaseCopyPath)
	}()
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightArchiveInvalid, "recreate the truncated v1 archive", err)
		}
		name, err := secureArchiveMemberPath(header.Name)
		if err != nil {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightUnsafePath, "recreate the v1 archive without unsafe paths", err)
		}
		if _, duplicate := seen[name]; duplicate {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightDuplicatePath, "recreate the v1 archive without duplicate members", fmt.Errorf("duplicate archive member %q", name))
		}
		seen[name] = struct{}{}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightUnsupportedEntry, "recreate the v1 archive without links or devices", fmt.Errorf("unsupported member type for %q", name))
		}
		if header.Size < 0 || uint64(header.Size) > math.MaxUint64-plan.RequiredBytes {
			_ = gzr.Close()
			return preflightDenied(plan, RestorePreflightInsufficientDisk, "use a representable v1 archive size", fmt.Errorf("archive member sizes overflow uint64"))
		}
		plan.RequiredBytes += uint64(header.Size)
		writer := io.Writer(io.Discard)
		if name == instanceBackupDBName {
			writer = databaseCopy
			seenDatabase = true
			plan.Replace = append(plan.Replace, name)
		} else if name == instanceBackupManifestName {
			if header.Size > instanceBackupManifestMaxBytes {
				_ = gzr.Close()
				return preflightDenied(plan, RestorePreflightUnsupportedManifest, "use a bounded v1 manifest", fmt.Errorf("v1 manifest is too large"))
			}
			var document bytes.Buffer
			writer = &document
			written, copyErr := io.CopyBuffer(writer, tr, buffer)
			if copyErr != nil || written != header.Size {
				_ = gzr.Close()
				return preflightDenied(plan, RestorePreflightArchiveInvalid, "recreate the truncated v1 manifest", copyErr)
			}
			var legacy struct {
				Version int    `json:"version"`
				Kind    string `json:"kind"`
				DBPath  string `json:"dbPath"`
			}
			if err := json.Unmarshal(document.Bytes(), &legacy); err != nil || legacy.Version != 1 || legacy.Kind != "leapview-instance" || legacy.DBPath != instanceBackupDBName {
				_ = gzr.Close()
				if err == nil {
					err = fmt.Errorf("v1 manifest contract is invalid")
				}
				return preflightDenied(plan, RestorePreflightUnsupportedManifest, "use a valid policy-approved v1 archive", err)
			}
			seenManifest = true
			continue
		} else {
			plan.Replace = append(plan.Replace, name)
		}
		written, copyErr := io.CopyBuffer(writer, tr, buffer)
		if copyErr != nil || written != header.Size {
			_ = gzr.Close()
			if copyErr == nil {
				copyErr = fmt.Errorf("member %q is truncated", name)
			}
			return preflightDenied(plan, RestorePreflightArchiveInvalid, "recreate the truncated v1 archive", copyErr)
		}
	}
	if _, err := io.CopyBuffer(io.Discard, gzr, buffer); err != nil {
		_ = gzr.Close()
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "recreate the corrupt v1 gzip archive", err)
	}
	if err := gzr.Close(); err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "recreate the corrupt v1 gzip archive", err)
	}
	if _, err := io.CopyBuffer(archiveDigest, file, buffer); err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "provide a readable v1 archive", err)
	}
	if !seenManifest || !seenDatabase {
		return preflightDenied(plan, RestorePreflightMemberMissing, "use a complete v1 archive containing its manifest and database", fmt.Errorf("v1 archive is incomplete"))
	}
	if err := databaseCopy.Sync(); err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "finish bounded v1 database staging", err)
	}
	if err := databaseCopy.Close(); err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "finish bounded v1 database staging", err)
	}
	if expected := strings.TrimSpace(options.ExpectedEnvironment); expected != "" {
		if err := ValidateDatabaseInstanceEnvironment(ctx, databaseCopyPath, expected); err != nil {
			return preflightDenied(plan, RestorePreflightWrongEnvironment, "use a v1 archive from the target environment", err)
		}
	} else if err := validateBackupDatabase(ctx, databaseCopyPath); err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "use a valid v1 control-plane database", err)
	}
	targetBytes, err := instanceTreeRegularBytes(target, options.PreserveRelativeFile)
	if err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "make the target tree readable for preflight", err)
	}
	if targetBytes > math.MaxUint64-plan.RequiredBytes || options.MinimumFreeBytes > math.MaxUint64-plan.RequiredBytes-targetBytes {
		return preflightDenied(plan, RestorePreflightInsufficientDisk, "reduce the v1 archive, target, or disk reserve", fmt.Errorf("required byte count overflows uint64"))
	}
	plan.RequiredBytes += targetBytes + options.MinimumFreeBytes
	capacityPath, err := existingInstancePath(filepath.Dir(target))
	if err != nil {
		return preflightDenied(plan, RestorePreflightInsufficientDisk, "make target filesystem capacity measurable", err)
	}
	plan.AvailableBytes, err = instanceFilesystemFreeBytes(capacityPath)
	if err != nil || plan.AvailableBytes < plan.RequiredBytes {
		if err == nil {
			err = fmt.Errorf("available bytes %d are below required bytes %d", plan.AvailableBytes, plan.RequiredBytes)
		}
		return preflightDenied(plan, RestorePreflightInsufficientDisk, "free enough target disk space before v1 restore", err)
	}
	plan.ArchiveSHA256 = hex.EncodeToString(archiveDigest.Sum(nil))
	plan.TargetTreeSHA256, err = instanceTreeSHA256(target, options.PreserveRelativeFile)
	if err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "make the target tree readable for preflight", err)
	}
	targetRelease, err := normalizeBackupReleaseIdentity(options.TargetReleaseIdentity)
	if err != nil {
		return preflightDenied(plan, RestorePreflightIncompatibleRelease, "supply the exact target release identity", err)
	}
	plan.TargetRelease = targetRelease
	if preserve := strings.TrimSpace(options.PreserveRelativeFile); preserve != "" {
		plan.Preserve = []string{filepath.ToSlash(preserve)}
	}
	plan.Reset, err = normalizeInstanceRelativePaths(options.ResetRelativePaths)
	if err != nil {
		return preflightDenied(plan, RestorePreflightArchiveInvalid, "use safe reset paths", err)
	}
	sort.Strings(plan.Replace)
	plan.Allowed = true
	plan.ReasonCode = RestorePreflightAllowed
	plan.Remediation = "legacy v1 acceptance is authorized by the target release policy; migrate this backup to manifest v2"
	return plan, nil
}

func restoreTransitionRequest(archive, target compatibility.ReleaseIdentity) (compatibility.Request, error) {
	archiveVersion := "v" + strings.TrimPrefix(strings.TrimSpace(archive.Version), "v")
	targetVersion := "v" + strings.TrimPrefix(strings.TrimSpace(target.Version), "v")
	if !semver.IsValid(archiveVersion) || !semver.IsValid(targetVersion) {
		return compatibility.Request{}, fmt.Errorf("restore release versions must be semantic")
	}
	switch semver.Compare(archiveVersion, targetVersion) {
	case -1:
		return compatibility.Request{Operation: compatibility.OperationUpgrade, Current: archive, Next: target}, nil
	case 1:
		return compatibility.Request{Operation: compatibility.OperationRollback, Current: archive, Next: target}, nil
	default:
		return compatibility.Request{}, fmt.Errorf("different artifacts with the same release version are not a supported restore transition")
	}
}

func instanceTreeSHA256(root, ignoredRelativeFile string) (string, error) {
	digest := sha256.New()
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		_, _ = io.WriteString(digest, "absent\n")
		return hex.EncodeToString(digest.Sum(nil)), nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("target home is not a regular directory")
	}
	buffer := make([]byte, InstanceBackupValidationBufferSize)
	err = filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if samePath(current, root) {
			return nil
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == instanceOperationMarkerName {
			return nil
		}
		if ignoredRelativeFile != "" && rel == filepath.ToSlash(ignoredRelativeFile) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(digest, "%s\x00%d\x00%d\n", rel, info.Mode().Type(), info.Size()); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			if info.IsDir() {
				return nil
			}
			return fmt.Errorf("target contains unsupported entry %q", rel)
		}
		file, err := os.Open(current)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyBuffer(digest, file, buffer)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func instanceTreeRegularBytes(root, ignoredRelativeFile string) (uint64, error) {
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	var total uint64
	err := filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if samePath(current, root) {
			return nil
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == instanceOperationMarkerName || (ignoredRelativeFile != "" && rel == filepath.ToSlash(ignoredRelativeFile)) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if uint64(info.Size()) > math.MaxUint64-total {
			return fmt.Errorf("target byte count overflows uint64")
		}
		total += uint64(info.Size())
		return nil
	})
	return total, err
}

func verifyRestorePlanInputs(plan InstanceRestorePreflightPlan, archivePath, targetHome, checkpointPath, ignoredRelativeFile string) error {
	archiveAbs, err := filepath.Abs(archivePath)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(targetHome)
	if err != nil {
		return err
	}
	if archiveAbs != plan.ArchivePath {
		return &InstanceRestorePreflightError{ReasonCode: RestorePreflightStaleArchive, Remediation: "rerun preflight for the selected archive", Err: fmt.Errorf("archive path changed after preflight")}
	}
	if targetAbs != plan.TargetHome {
		return &InstanceRestorePreflightError{ReasonCode: RestorePreflightStaleTarget, Remediation: "rerun preflight for the selected target", Err: fmt.Errorf("target path changed after preflight")}
	}
	checkpointAbs := ""
	if strings.TrimSpace(checkpointPath) != "" {
		checkpointAbs, err = filepath.Abs(checkpointPath)
		if err != nil {
			return err
		}
	}
	if checkpointAbs != plan.CheckpointPath {
		return &InstanceRestorePreflightError{ReasonCode: RestorePreflightStaleTarget, Remediation: "rerun preflight for the selected safety checkpoint", Err: fmt.Errorf("checkpoint path changed after preflight")}
	}
	archiveDigest, err := fileSHA256(archivePath)
	if err != nil {
		return err
	}
	if archiveDigest != plan.ArchiveSHA256 {
		return &InstanceRestorePreflightError{ReasonCode: RestorePreflightStaleArchive, Remediation: "rerun preflight for the exact archive", Err: fmt.Errorf("archive checksum changed after preflight")}
	}
	targetDigest, err := instanceTreeSHA256(targetHome, ignoredRelativeFile)
	if err != nil {
		return err
	}
	if targetDigest != plan.TargetTreeSHA256 {
		return &InstanceRestorePreflightError{ReasonCode: RestorePreflightStaleTarget, Remediation: "stop target changes and rerun preflight", Err: fmt.Errorf("target identity changed after preflight")}
	}
	return nil
}

func readBackupInstanceIdentity(ctx context.Context, store *Store) (string, string, error) {
	instanceID, err := store.InstanceID(ctx)
	if err != nil {
		return "", "", err
	}
	environment, err := store.InstanceEnvironment(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		environment = "unbound"
		err = nil
	}
	if err != nil {
		return "", "", err
	}
	return instanceID, environment, nil
}

func readBackupDatabaseInstanceID(ctx context.Context, path string) (string, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=query_only(1)")
	if err != nil {
		return "", err
	}
	defer db.Close()
	var instanceID string
	if err := db.QueryRowContext(ctx, `SELECT value FROM platform_settings WHERE key = ?`, instanceIDSetting).Scan(&instanceID); err != nil {
		return "", fmt.Errorf("read archived database instance identity: %w", err)
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return "", fmt.Errorf("archived database instance identity is empty")
	}
	return instanceID, nil
}

func validateExistingBackupDatabaseInstanceID(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", path+"?_pragma=query_only(1)")
	if err != nil {
		return err
	}
	defer db.Close()
	var instanceID string
	if err := db.QueryRowContext(ctx, `SELECT value FROM platform_settings WHERE key = ?`, instanceIDSetting).Scan(&instanceID); errors.Is(err, sql.ErrNoRows) {
		// Older instances acquire their durable identity when the authoritative
		// checkpoint snapshot is created. Preflight remains read-only.
		return nil
	} else if err != nil {
		return fmt.Errorf("read current database instance identity: %w", err)
	}
	if strings.TrimSpace(instanceID) == "" {
		return fmt.Errorf("current database instance identity is empty")
	}
	return nil
}

func writeManifestV2JSON(value InstanceBackupManifestV2) ([]byte, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	encoded = append(encoded, '\n')
	var decoded InstanceBackupManifestV2
	if err := validateManifestV2Document(encoded, &decoded); err != nil {
		return nil, err
	}
	return encoded, nil
}
