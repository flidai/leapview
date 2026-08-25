package platform

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
)

func TestBackupManifestV2InventoriesMembersDeterministically(t *testing.T) {
	ctx := context.Background()
	home := filepath.Join(t.TempDir(), "home")
	dbPath := filepath.Join(home, instanceBackupDBName)
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindInstanceEnvironment(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, "managed-data", "objects", "blob"), "managed")
	writeTestFile(t, filepath.Join(home, "ducklake", "data", "part.parquet"), "ducklake")

	archivePath := filepath.Join(t.TempDir(), "backup.tar.gz")
	createdAt := time.Date(2026, time.August, 25, 5, 0, 0, 0, time.UTC)
	identity := testBackupReleaseIdentity("1.2.3", "a")
	if err := BackupInstance(ctx, InstanceBackupOptions{
		HomeDir: home, DBPath: dbPath, OutPath: archivePath,
		BackupID: "backup_test", Now: func() time.Time { return createdAt },
		ReleaseIdentity: identity,
	}); err != nil {
		t.Fatal(err)
	}

	entries := readTarGzEntries(t, archivePath)
	var manifest InstanceBackupManifestV2
	if err := json.Unmarshal(entries[instanceBackupManifestName], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != InstanceBackupManifestVersion || manifest.BackupID != "backup_test" ||
		manifest.CreatedAt != createdAt || manifest.CompletedAt != createdAt || manifest.Environment != "prod" ||
		manifest.ReleaseIdentity != identity || manifest.RequiredTransitionPolicyVersion != "ubdr/v1" {
		t.Fatalf("manifest = %#v", manifest)
	}
	paths := make([]string, 0, len(manifest.Members))
	for _, member := range manifest.Members {
		paths = append(paths, member.Path)
		body, ok := entries[member.Path]
		if !ok {
			t.Fatalf("manifest member %q is absent from archive", member.Path)
		}
		digest := sha256.Sum256(body)
		if member.Size != int64(len(body)) || member.SHA256 != hex.EncodeToString(digest[:]) || member.Mode == 0 || member.Role == "" {
			t.Fatalf("member %q = %#v", member.Path, member)
		}
	}
	if !slices.IsSorted(paths) {
		t.Fatalf("member paths are not sorted: %v", paths)
	}
	if manifest.InventorySHA256 == "" {
		t.Fatal("manifest inventory checksum is empty")
	}
	secondArchive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := BackupInstance(ctx, InstanceBackupOptions{
		HomeDir: home, DBPath: dbPath, OutPath: secondArchive,
		BackupID: "backup_test", Now: func() time.Time { return createdAt }, ReleaseIdentity: identity,
	}); err != nil {
		t.Fatal(err)
	}
	secondEntries := readTarGzEntries(t, secondArchive)
	if string(secondEntries[instanceBackupManifestName]) != string(entries[instanceBackupManifestName]) {
		t.Fatalf("equivalent inputs produced different manifests:\nfirst=%s\nsecond=%s", entries[instanceBackupManifestName], secondEntries[instanceBackupManifestName])
	}
}

func TestRestorePreflightRejectsBeforeTargetMutation(t *testing.T) {
	ctx := context.Background()
	archivePath, identity := createManifestV2TestArchive(t, ctx, "prod")
	target := filepath.Join(t.TempDir(), "target")
	writeTestFile(t, filepath.Join(target, "preserve.txt"), "unchanged")
	before := hashTestTree(t, target)

	plan, err := PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{
		ArchivePath: archivePath, TargetHomeDir: target, ExpectedEnvironment: "staging",
		TargetReleaseIdentity: identity, ExclusiveLockHeld: true,
	})
	if err == nil || plan.ReasonCode != RestorePreflightWrongEnvironment {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if after := hashTestTree(t, target); after != before {
		t.Fatalf("preflight mutated target: before=%s after=%s", before, after)
	}
}

func TestRestorePreflightDetectsMemberChecksumAndStaleTarget(t *testing.T) {
	ctx := context.Background()
	archivePath, identity := createManifestV2TestArchive(t, ctx, "prod")
	target := filepath.Join(t.TempDir(), "target")
	writeTestFile(t, filepath.Join(target, "state.txt"), "before")
	options := InstanceRestorePreflightOptions{
		ArchivePath: archivePath, TargetHomeDir: target, ExpectedEnvironment: "prod",
		TargetReleaseIdentity: identity, ExclusiveLockHeld: true,
	}
	plan, err := PreflightInstanceRestore(ctx, options)
	if err != nil || !plan.Allowed || plan.ArchiveSHA256 == "" || plan.TargetTreeSHA256 == "" {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	wantArchiveSHA, err := fileSHA256(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ArchiveSHA256 != wantArchiveSHA || plan.ManifestSHA256 == "" {
		t.Fatalf("archive sha=%q manifest sha=%q want archive=%q", plan.ArchiveSHA256, plan.ManifestSHA256, wantArchiveSHA)
	}
	writeTestFile(t, filepath.Join(target, "state.txt"), "changed")
	err = RestoreInstance(ctx, InstanceRestoreOptions{
		TargetHomeDir: target, BackupPath: archivePath, CurrentBackupOut: filepath.Join(t.TempDir(), "before.tar.gz"),
		ExpectedEnvironment: "prod", TargetReleaseIdentity: identity, ExclusiveLockHeld: true,
		ValidatedPlan: &plan,
	})
	var preflightErr *InstanceRestorePreflightError
	if err == nil || !strings.Contains(err.Error(), RestorePreflightStaleTarget) || !errors.As(err, &preflightErr) {
		t.Fatalf("restore error = %v", err)
	}
}

func TestRestoreRefusesSubstitutedArchivePathAfterPreflight(t *testing.T) {
	ctx := context.Background()
	archivePath, identity := createManifestV2TestArchive(t, ctx, "prod")
	target := filepath.Join(t.TempDir(), "target")
	plan, err := PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{
		ArchivePath: archivePath, TargetHomeDir: target, ExpectedEnvironment: "prod",
		TargetReleaseIdentity: identity, ExclusiveLockHeld: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	substitute := filepath.Join(t.TempDir(), "same-bytes.tar.gz")
	if err := os.WriteFile(substitute, readTestBytes(t, archivePath), 0o600); err != nil {
		t.Fatal(err)
	}
	err = RestoreInstance(ctx, InstanceRestoreOptions{
		TargetHomeDir: target, BackupPath: substitute, ExpectedEnvironment: "prod",
		TargetReleaseIdentity: identity, ExclusiveLockHeld: true, ValidatedPlan: &plan,
	})
	if err == nil || !strings.Contains(err.Error(), RestorePreflightStaleArchive) {
		t.Fatalf("substituted archive error = %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("substituted archive created target: %v", err)
	}
}

func TestRestorePreflightRejectsUnsupportedV1Explicitly(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "source", instanceBackupDBName)
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "v1.tar.gz")
	writeInstanceBackupArchive(t, archivePath, []testTarEntry{
		{name: instanceBackupManifestName, mode: 0o600, body: []byte(`{"version":1,"kind":"leapview-instance","dbPath":"leapview.db"}` + "\n")},
		{name: instanceBackupDBName, mode: 0o600, body: readTestBytes(t, dbPath)},
	})
	plan, err := PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{ArchivePath: archivePath, TargetHomeDir: filepath.Join(dir, "target")})
	if err == nil || plan.ReasonCode != RestorePreflightUnsupportedManifest {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	plan, err = PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{
		ArchivePath: archivePath, TargetHomeDir: filepath.Join(dir, "target"), AllowLegacyV1: true,
	})
	if err != nil || !plan.Allowed || plan.ManifestVersion != 1 || !strings.Contains(plan.Remediation, "explicitly enabled") {
		t.Fatalf("explicit v1 plan=%#v err=%v", plan, err)
	}
}

func TestRestorePreflightNegativeFixtureMatrixIsNonMutating(t *testing.T) {
	ctx := context.Background()
	baseArchive, identity := createManifestV2TestArchive(t, ctx, "prod")
	baseManifest := readBackupManifestFromArchive(t, baseArchive)
	baseEntries := manifestV2TestEntries(t, baseArchive, baseManifest)
	policy, err := compatibility.EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	legacy, ok := policy.ReleaseByID("v0.1.0")
	if !ok {
		t.Fatal("embedded policy has no v0.1.0 release")
	}

	tests := []struct {
		name     string
		reason   string
		mutate   func(*InstanceBackupManifestV2, *[]testTarEntry)
		minimum  uint64
		truncate bool
	}{
		{name: "checksum mismatch", reason: RestorePreflightChecksumMismatch, mutate: func(_ *InstanceBackupManifestV2, entries *[]testTarEntry) {
			(*entries)[len(*entries)-1].body = append((*entries)[len(*entries)-1].body, 'x')
		}},
		{name: "missing member", reason: RestorePreflightMemberMissing, mutate: func(_ *InstanceBackupManifestV2, entries *[]testTarEntry) {
			*entries = (*entries)[:len(*entries)-1]
		}},
		{name: "unexpected member", reason: RestorePreflightMemberUnexpected, mutate: func(_ *InstanceBackupManifestV2, entries *[]testTarEntry) {
			*entries = append(*entries, testTarEntry{name: "extra.txt", mode: 0o600, body: []byte("extra")})
		}},
		{name: "duplicate member", reason: RestorePreflightDuplicatePath, mutate: func(_ *InstanceBackupManifestV2, entries *[]testTarEntry) {
			*entries = append(*entries, (*entries)[1])
		}},
		{name: "unsafe path", reason: RestorePreflightUnsafePath, mutate: func(_ *InstanceBackupManifestV2, entries *[]testTarEntry) {
			*entries = append(*entries, testTarEntry{name: "../escape", mode: 0o600, body: []byte("escape")})
		}},
		{name: "link", reason: RestorePreflightUnsupportedEntry, mutate: func(_ *InstanceBackupManifestV2, entries *[]testTarEntry) {
			*entries = append(*entries, testTarEntry{name: "link", mode: 0o777, symlink: true, linkname: "leapview.db"})
		}},
		{name: "device", reason: RestorePreflightUnsupportedEntry, mutate: func(_ *InstanceBackupManifestV2, entries *[]testTarEntry) {
			*entries = append(*entries, testTarEntry{name: "device", mode: 0o600, typeflag: tar.TypeChar})
		}},
		{name: "truncated archive", reason: RestorePreflightArchiveInvalid, truncate: true},
		{name: "wrong environment", reason: RestorePreflightWrongEnvironment, mutate: func(manifest *InstanceBackupManifestV2, _ *[]testTarEntry) {
			manifest.Environment = "staging"
		}},
		{name: "external evidence", reason: RestorePreflightExternalEvidence, mutate: func(manifest *InstanceBackupManifestV2, _ *[]testTarEntry) {
			manifest.StorageTopology.ManagedData = "external"
			manifest.StorageTopology.ExternalStores = []InstanceBackupExternalStoreReference{{
				Role: "managed-data", Backend: "s3", Namespace: "bucket/prefix",
				RecoveryPoint: "version-42", EvidenceKey: "managed-data-version",
			}}
		}},
		{name: "external reference secret", reason: RestorePreflightUnsupportedManifest, mutate: func(manifest *InstanceBackupManifestV2, _ *[]testTarEntry) {
			manifest.StorageTopology.ManagedData = "external"
			manifest.StorageTopology.ExternalStores = []InstanceBackupExternalStoreReference{{
				Role: "managed-data", Backend: "s3", Namespace: "s3://access:secret@bucket/prefix",
				RecoveryPoint: "version-42", EvidenceKey: "managed-data-version",
			}}
		}},
		{name: "incompatible release", reason: RestorePreflightIncompatibleRelease, mutate: func(manifest *InstanceBackupManifestV2, _ *[]testTarEntry) {
			manifest.ReleaseIdentity = legacy.IdentityForPlatform("linux/amd64")
		}},
		{name: "insufficient disk", reason: RestorePreflightInsufficientDisk, minimum: ^uint64(0)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := baseManifest
			manifest.Members = append([]InstanceBackupMember{}, baseManifest.Members...)
			manifest.StorageTopology.ExternalStores = append([]InstanceBackupExternalStoreReference{}, baseManifest.StorageTopology.ExternalStores...)
			entries := append([]testTarEntry{}, baseEntries...)
			if test.mutate != nil {
				test.mutate(&manifest, &entries)
			}
			entries[0].body = marshalManifestV2ForTest(t, manifest)
			archivePath := filepath.Join(t.TempDir(), "negative.tar.gz")
			writeInstanceBackupArchive(t, archivePath, entries)
			if test.truncate {
				contents := readTestBytes(t, archivePath)
				if err := os.WriteFile(archivePath, contents[:len(contents)/2], 0o600); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(t.TempDir(), "target")
			writeTestFile(t, filepath.Join(target, "state.txt"), "unchanged")
			before := hashTestTree(t, target)
			plan, err := PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{
				ArchivePath: archivePath, TargetHomeDir: target, ExpectedEnvironment: "prod",
				TargetReleaseIdentity: identity, ExclusiveLockHeld: true, MinimumFreeBytes: test.minimum,
			})
			if err == nil || plan.ReasonCode != test.reason {
				t.Fatalf("plan=%#v err=%v, want %s", plan, err, test.reason)
			}
			if after := hashTestTree(t, target); after != before {
				t.Fatalf("negative preflight mutated target: before=%s after=%s", before, after)
			}
		})
	}
}

func TestRestorePreflightValidationMemoryIsBounded(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	home := filepath.Join(dir, "source")
	dbPath := filepath.Join(home, instanceBackupDBName)
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindInstanceEnvironment(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	largePath := filepath.Join(home, "managed-data", "objects", "large.bin")
	if err := os.MkdirAll(filepath.Dir(largePath), 0o700); err != nil {
		t.Fatal(err)
	}
	const memberBytes = 32 << 20
	large, err := os.OpenFile(largePath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(memberBytes); err != nil {
		_ = large.Close()
		t.Fatal(err)
	}
	if err := large.Close(); err != nil {
		t.Fatal(err)
	}
	identity := testBackupReleaseIdentity("1.2.3", "a")
	archivePath := filepath.Join(dir, "large.tar.gz")
	if err := BackupInstance(ctx, InstanceBackupOptions{
		HomeDir: home, DBPath: dbPath, OutPath: archivePath, Environment: "prod",
		BackupID: "backup_memory", ReleaseIdentity: identity,
	}); err != nil {
		t.Fatal(err)
	}
	options := InstanceRestorePreflightOptions{
		ArchivePath: archivePath, TargetHomeDir: filepath.Join(dir, "target"), ExpectedEnvironment: "prod",
		TargetReleaseIdentity: identity, ExclusiveLockHeld: true,
	}
	if _, err := PreflightInstanceRestore(ctx, options); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	plan, err := PreflightInstanceRestore(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	const maximumValidationAllocation = 12 << 20
	if allocated > maximumValidationAllocation {
		t.Fatalf("preflight allocated %d bytes while validating a %d-byte member", allocated, memberBytes)
	}
	if plan.ValidationBufferBytes != InstanceBackupValidationBufferSize || plan.ValidationBufferBytes >= memberBytes {
		t.Fatalf("validation buffer = %d, member bytes = %d", plan.ValidationBufferBytes, memberBytes)
	}
}

func createManifestV2TestArchive(t *testing.T, ctx context.Context, environment string) (string, compatibility.ReleaseIdentity) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "source")
	dbPath := filepath.Join(home, instanceBackupDBName)
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindInstanceEnvironment(ctx, environment); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, "artifacts", "release.tar.gz"), "artifact")
	identity := testBackupReleaseIdentity("1.2.3", "a")
	archivePath := filepath.Join(dir, "backup.tar.gz")
	if err := BackupInstance(ctx, InstanceBackupOptions{
		HomeDir: home, DBPath: dbPath, OutPath: archivePath, BackupID: "backup_test",
		Now:             func() time.Time { return time.Date(2026, time.August, 25, 5, 0, 0, 0, time.UTC) },
		ReleaseIdentity: identity,
	}); err != nil {
		t.Fatal(err)
	}
	return archivePath, identity
}

func testBackupReleaseIdentity(version, digestCharacter string) compatibility.ReleaseIdentity {
	return compatibility.ReleaseIdentity{
		ReleaseID: "v" + version, Version: version, SourceRevision: strings.Repeat("a", 40),
		Image:        "ghcr.io/flidai/leapview@sha256:" + strings.Repeat(digestCharacter, 64),
		Distribution: "public", Platform: "linux/amd64",
	}
}

func hashTestTree(t *testing.T, root string) string {
	t.Helper()
	digest, err := instanceTreeSHA256(root, "")
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func readBackupManifestFromArchive(t *testing.T, archivePath string) InstanceBackupManifestV2 {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzr, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	header, err := tr.Next()
	if err != nil || header.Name != instanceBackupManifestName {
		t.Fatalf("manifest header=%#v err=%v", header, err)
	}
	var manifest InstanceBackupManifestV2
	if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func manifestV2TestEntries(t *testing.T, archivePath string, manifest InstanceBackupManifestV2) []testTarEntry {
	t.Helper()
	contents := readTarGzEntries(t, archivePath)
	entries := []testTarEntry{{name: instanceBackupManifestName, mode: 0o600, body: contents[instanceBackupManifestName]}}
	for _, member := range manifest.Members {
		entries = append(entries, testTarEntry{name: member.Path, mode: int64(member.Mode), body: contents[member.Path]})
	}
	return entries
}

func marshalManifestV2ForTest(t *testing.T, manifest InstanceBackupManifestV2) []byte {
	t.Helper()
	document, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(document, '\n')
}
