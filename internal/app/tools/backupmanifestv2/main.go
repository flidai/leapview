package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/compatibility"
)

type archiveEntry struct {
	name     string
	mode     int64
	body     []byte
	symlink  bool
	linkname string
	typeflag byte
}

type fixtureResult struct {
	Name       string `json:"name"`
	ReasonCode string `json:"reasonCode"`
	Rejected   bool   `json:"rejected"`
	Unchanged  bool   `json:"targetUnchanged"`
}

type stateChecksums struct {
	TreeSHA256     string `json:"treeSha256"`
	DatabaseSHA256 string `json:"databaseSha256"`
}

func main() {
	evidenceDir := flag.String("evidence-dir", ".tmp/qualification/ubdr/backup-manifest-v2", "bounded evidence output directory")
	flag.Parse()
	if err := run(*evidenceDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(evidenceDir string) error {
	evidenceDir = strings.TrimSpace(evidenceDir)
	if evidenceDir == "" {
		return fmt.Errorf("evidence directory is required")
	}
	if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "leapview-backup-manifest-v2-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	ctx := context.Background()
	identity := compatibility.ReleaseIdentity{
		ReleaseID: "v1.0.0", Version: "1.0.0", SourceRevision: strings.Repeat("a", 40),
		Image:        "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64),
		Distribution: "qualification", Platform: "linux/amd64",
	}
	sourceHome := filepath.Join(work, "source")
	if err := seedInstance(ctx, sourceHome, "qualification", "source"); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(sourceHome, "managed-data", "objects"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(sourceHome, "managed-data", "objects", "blob"), []byte("managed-data\n"), 0o600); err != nil {
		return err
	}
	archivePath := filepath.Join(work, "backup.tar.gz")
	if err := platform.BackupInstance(ctx, platform.InstanceBackupOptions{
		HomeDir: sourceHome, DBPath: filepath.Join(sourceHome, "leapview.db"), OutPath: archivePath,
		Environment: "qualification", BackupID: "lvbackup_fai515_evidence",
		Now:             func() time.Time { return time.Date(2026, time.August, 25, 6, 0, 0, 0, time.UTC) },
		ReleaseIdentity: identity,
	}); err != nil {
		return err
	}
	manifest, manifestDocument, entries, err := readArchive(archivePath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(evidenceDir, "leapview-backup.json"), manifestDocument, 0o600); err != nil {
		return err
	}
	var inventory strings.Builder
	for _, member := range manifest.Members {
		fmt.Fprintf(&inventory, "%s  %s\n", member.SHA256, member.Path)
	}
	if err := os.WriteFile(filepath.Join(evidenceDir, "inventory.sha256"), []byte(inventory.String()), 0o600); err != nil {
		return err
	}

	targetHome := filepath.Join(work, "target")
	if err := seedInstance(ctx, targetHome, "qualification", "target"); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(targetHome, "preserve.txt"), []byte("unchanged\n"), 0o600); err != nil {
		return err
	}
	before, err := checksums(targetHome)
	if err != nil {
		return err
	}
	baseOptions := platform.InstanceRestorePreflightOptions{
		ArchivePath: archivePath, TargetHomeDir: targetHome, ExpectedEnvironment: "qualification",
		TargetReleaseIdentity: identity, ExclusiveLockHeld: true, RequireExclusiveLock: true,
	}
	plan, err := platform.PreflightInstanceRestore(ctx, baseOptions)
	if err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(evidenceDir, "preflight-report.json"), plan); err != nil {
		return err
	}

	type fixture struct {
		name    string
		reason  string
		archive string
		options platform.InstanceRestorePreflightOptions
	}
	fixtures := make([]fixture, 0, 12)
	addArchiveFixture := func(name, reason string, mutate func(*platform.InstanceBackupManifestV2, *[]archiveEntry)) error {
		copyManifest := manifest
		copyManifest.Members = append([]platform.InstanceBackupMember{}, manifest.Members...)
		copyManifest.StorageTopology.ExternalStores = append([]platform.InstanceBackupExternalStoreReference{}, manifest.StorageTopology.ExternalStores...)
		copyEntries := cloneEntries(entries)
		mutate(&copyManifest, &copyEntries)
		document, err := json.MarshalIndent(copyManifest, "", "  ")
		if err != nil {
			return err
		}
		copyEntries[0].body = append(document, '\n')
		path := filepath.Join(work, strings.ReplaceAll(name, " ", "-")+".tar.gz")
		if err := writeArchive(path, copyEntries); err != nil {
			return err
		}
		options := baseOptions
		options.ArchivePath = path
		fixtures = append(fixtures, fixture{name: name, reason: reason, archive: path, options: options})
		return nil
	}
	if err := addArchiveFixture("checksum mismatch", platform.RestorePreflightChecksumMismatch, func(_ *platform.InstanceBackupManifestV2, values *[]archiveEntry) {
		(*values)[len(*values)-1].body = append((*values)[len(*values)-1].body, 'x')
	}); err != nil {
		return err
	}
	if err := addArchiveFixture("missing member", platform.RestorePreflightMemberMissing, func(_ *platform.InstanceBackupManifestV2, values *[]archiveEntry) {
		*values = (*values)[:len(*values)-1]
	}); err != nil {
		return err
	}
	if err := addArchiveFixture("unexpected member", platform.RestorePreflightMemberUnexpected, func(_ *platform.InstanceBackupManifestV2, values *[]archiveEntry) {
		*values = append(*values, archiveEntry{name: "extra.txt", mode: 0o600, body: []byte("extra")})
	}); err != nil {
		return err
	}
	if err := addArchiveFixture("duplicate member", platform.RestorePreflightDuplicatePath, func(_ *platform.InstanceBackupManifestV2, values *[]archiveEntry) {
		*values = append(*values, cloneEntry((*values)[1]))
	}); err != nil {
		return err
	}
	if err := addArchiveFixture("unsafe path", platform.RestorePreflightUnsafePath, func(_ *platform.InstanceBackupManifestV2, values *[]archiveEntry) {
		*values = append(*values, archiveEntry{name: "../escape", mode: 0o600, body: []byte("escape")})
	}); err != nil {
		return err
	}
	if err := addArchiveFixture("link member", platform.RestorePreflightUnsupportedEntry, func(_ *platform.InstanceBackupManifestV2, values *[]archiveEntry) {
		*values = append(*values, archiveEntry{name: "link", mode: 0o777, symlink: true, linkname: "leapview.db"})
	}); err != nil {
		return err
	}
	if err := addArchiveFixture("device member", platform.RestorePreflightUnsupportedEntry, func(_ *platform.InstanceBackupManifestV2, values *[]archiveEntry) {
		*values = append(*values, archiveEntry{name: "device", mode: 0o600, typeflag: tar.TypeChar})
	}); err != nil {
		return err
	}
	if err := addArchiveFixture("wrong environment", platform.RestorePreflightWrongEnvironment, func(value *platform.InstanceBackupManifestV2, _ *[]archiveEntry) {
		value.Environment = "other"
	}); err != nil {
		return err
	}
	if err := addArchiveFixture("external evidence", platform.RestorePreflightExternalEvidence, func(value *platform.InstanceBackupManifestV2, _ *[]archiveEntry) {
		value.StorageTopology.ManagedData = "external"
		value.StorageTopology.ExternalStores = []platform.InstanceBackupExternalStoreReference{{Role: "managed-data", Backend: "s3", Namespace: "bucket/prefix", RecoveryPoint: "version-42", EvidenceKey: "managed-data-version"}}
	}); err != nil {
		return err
	}
	if err := addArchiveFixture("external reference secret", platform.RestorePreflightUnsupportedManifest, func(value *platform.InstanceBackupManifestV2, _ *[]archiveEntry) {
		value.StorageTopology.ManagedData = "external"
		value.StorageTopology.ExternalStores = []platform.InstanceBackupExternalStoreReference{{Role: "managed-data", Backend: "s3", Namespace: "s3://access:secret@bucket/prefix", RecoveryPoint: "version-42", EvidenceKey: "managed-data-version"}}
	}); err != nil {
		return err
	}
	if err := addArchiveFixture("incompatible release", platform.RestorePreflightIncompatibleRelease, func(value *platform.InstanceBackupManifestV2, _ *[]archiveEntry) {
		policy, _ := compatibility.EmbeddedPolicy()
		legacy, _ := policy.ReleaseByID("v0.1.0")
		value.ReleaseIdentity = legacy.IdentityForPlatform("linux/amd64")
	}); err != nil {
		return err
	}
	var databaseEntry archiveEntry
	for _, entry := range entries {
		if entry.name == "leapview.db" {
			databaseEntry = cloneEntry(entry)
			break
		}
	}
	if databaseEntry.name == "" {
		return fmt.Errorf("evidence archive has no control-plane database")
	}
	v1Path := filepath.Join(work, "legacy-v1.tar.gz")
	if err := writeArchive(v1Path, []archiveEntry{
		{name: "leapview-backup.json", mode: 0o600, body: []byte(`{"version":1,"kind":"leapview-instance","dbPath":"leapview.db"}` + "\n")},
		databaseEntry,
	}); err != nil {
		return err
	}
	v1Denied := baseOptions
	v1Denied.ArchivePath = v1Path
	fixtures = append(fixtures, fixture{name: "legacy v1 denied", reason: platform.RestorePreflightUnsupportedManifest, archive: v1Path, options: v1Denied})
	v1Allowed := v1Denied
	v1Allowed.AllowLegacyV1 = true
	v1AllowedPlan, err := platform.PreflightInstanceRestore(ctx, v1Allowed)
	if err != nil || !v1AllowedPlan.Allowed || v1AllowedPlan.ManifestVersion != 1 {
		return fmt.Errorf("explicit legacy v1 policy was not honored: plan=%#v err=%v", v1AllowedPlan, err)
	}
	if err := writeJSON(filepath.Join(evidenceDir, "legacy-v1-fixtures.json"), map[string]any{
		"schemaVersion": 1,
		"deny":          map[string]any{"allowed": false, "reasonCode": platform.RestorePreflightUnsupportedManifest},
		"explicitAllow": v1AllowedPlan,
	}); err != nil {
		return err
	}
	truncatedPath := filepath.Join(work, "truncated.tar.gz")
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(truncatedPath, archiveBytes[:len(archiveBytes)/2], 0o600); err != nil {
		return err
	}
	truncatedOptions := baseOptions
	truncatedOptions.ArchivePath = truncatedPath
	fixtures = append(fixtures, fixture{name: "truncated archive", reason: platform.RestorePreflightArchiveInvalid, archive: truncatedPath, options: truncatedOptions})
	insufficient := baseOptions
	insufficient.MinimumFreeBytes = ^uint64(0)
	fixtures = append(fixtures, fixture{name: "insufficient disk", reason: platform.RestorePreflightInsufficientDisk, archive: archivePath, options: insufficient})
	noLock := baseOptions
	noLock.ExclusiveLockHeld = false
	fixtures = append(fixtures, fixture{name: "exclusive lock", reason: platform.RestorePreflightStoppedRequired, archive: archivePath, options: noLock})

	results := make([]fixtureResult, 0, len(fixtures))
	for _, fixture := range fixtures {
		fixturePlan, fixtureErr := platform.PreflightInstanceRestore(ctx, fixture.options)
		if fixtureErr == nil || fixturePlan.ReasonCode != fixture.reason {
			return fmt.Errorf("fixture %q reason=%q err=%v, want %q", fixture.name, fixturePlan.ReasonCode, fixtureErr, fixture.reason)
		}
		after, err := checksums(targetHome)
		if err != nil {
			return err
		}
		unchanged := after == before
		if !unchanged {
			return fmt.Errorf("fixture %q mutated target", fixture.name)
		}
		results = append(results, fixtureResult{Name: fixture.name, ReasonCode: fixturePlan.ReasonCode, Rejected: true, Unchanged: unchanged})
	}
	if err := writeJSON(filepath.Join(evidenceDir, "negative-fixtures.json"), map[string]any{
		"schemaVersion": 1, "results": results,
	}); err != nil {
		return err
	}
	after, err := checksums(targetHome)
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(evidenceDir, "target-state-checksums.json"), map[string]any{
		"schemaVersion": 1, "unchanged": before == after, "before": before, "after": after,
	})
}

func seedInstance(ctx context.Context, home, environment, value string) error {
	store, err := platform.Open(ctx, filepath.Join(home, "leapview.db"))
	if err != nil {
		return err
	}
	if err := store.BindInstanceEnvironment(ctx, environment); err != nil {
		_ = store.Close()
		return err
	}
	if err := store.UpsertSetting(ctx, "fai-515-evidence", value); err != nil {
		_ = store.Close()
		return err
	}
	return store.Close()
}

func readArchive(path string) (platform.InstanceBackupManifestV2, []byte, []archiveEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return platform.InstanceBackupManifestV2{}, nil, nil, err
	}
	defer file.Close()
	gzr, err := gzip.NewReader(file)
	if err != nil {
		return platform.InstanceBackupManifestV2{}, nil, nil, err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	var entries []archiveEntry
	var manifest platform.InstanceBackupManifestV2
	var manifestDocument []byte
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return manifest, nil, nil, err
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return manifest, nil, nil, err
		}
		entries = append(entries, archiveEntry{name: header.Name, mode: header.Mode, body: body})
		if header.Name == "leapview-backup.json" {
			manifestDocument = append([]byte{}, body...)
			if err := json.Unmarshal(body, &manifest); err != nil {
				return manifest, nil, nil, err
			}
		}
	}
	return manifest, manifestDocument, entries, nil
}

func writeArchive(path string, entries []archiveEntry) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	gzw := gzip.NewWriter(file)
	tw := tar.NewWriter(gzw)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: entry.mode, Size: int64(len(entry.body)), Typeflag: tar.TypeReg}
		if entry.typeflag != 0 {
			header.Typeflag, header.Size = entry.typeflag, 0
		} else if entry.symlink {
			header.Typeflag, header.Linkname, header.Size = tar.TypeSymlink, entry.linkname, 0
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			if _, err := tw.Write(entry.body); err != nil {
				return err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gzw.Close(); err != nil {
		return err
	}
	return file.Close()
}

func cloneEntries(values []archiveEntry) []archiveEntry {
	result := make([]archiveEntry, len(values))
	for index, value := range values {
		result[index] = cloneEntry(value)
	}
	return result
}

func cloneEntry(value archiveEntry) archiveEntry {
	value.body = append([]byte{}, value.body...)
	return value
}

func checksums(home string) (stateChecksums, error) {
	tree := sha256.New()
	var paths []string
	if err := filepath.WalkDir(home, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(home, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return stateChecksums{}, err
	}
	sort.Strings(paths)
	var database string
	for _, rel := range paths {
		contents, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(rel)))
		if err != nil {
			return stateChecksums{}, err
		}
		digest := sha256.Sum256(contents)
		fmt.Fprintf(tree, "%s\x00%s\n", rel, hex.EncodeToString(digest[:]))
		if rel == "leapview.db" {
			database = hex.EncodeToString(digest[:])
		}
	}
	return stateChecksums{TreeSHA256: hex.EncodeToString(tree.Sum(nil)), DatabaseSHA256: database}, nil
}

func writeJSON(path string, value any) error {
	document, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	document = append(bytes.TrimSpace(document), '\n')
	return os.WriteFile(path, document, 0o600)
}
