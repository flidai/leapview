// Command extensionsupply builds LeapView's production DuckDB extension
// supply. It is packaging tooling only: all network acquisition happens at
// image build time, each artifact is loaded by its exact absolute path for
// DuckDB's official signature check, and the resulting runtime manifest is
// consumed offline by the application.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/deployment/extensionsupply"
	"github.com/flidai/leapview/internal/extension"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

const (
	defaultOutputRoot = "/out/extension-supply"
	defaultProfile    = "official-v1"
	runtimeSupplyRoot = "/usr/local/share/leapview/extensions"
	originID          = "duckdb-core-pinned"
)

type manifest struct {
	Version        int                              `json:"version"`
	DuckDBVersion  string                           `json:"duckdbVersion"`
	GOOS           string                           `json:"goos"`
	GOARCH         string                           `json:"goarch"`
	Platform       string                           `json:"platform"`
	SupportProfile string                           `json:"supportProfile"`
	Origins        []extensionsupply.ManifestOrigin `json:"origins"`
	Artifacts      []extensionsupply.Artifact       `json:"artifacts"`
}

func main() {
	output := flag.String("out", defaultOutputRoot, "absolute output directory for the offline supply")
	profile := flag.String("support-profile", defaultProfile, "reviewed LeapView extension support profile")
	runtimeRoot := flag.String("runtime-root", runtimeSupplyRoot, "absolute path where the output directory is mounted at runtime")
	checkOnly := flag.Bool("check", false, "verify an existing supply without downloading extensions")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	var err error
	if *checkOnly {
		err = check(ctx, *output, *profile, *runtimeRoot)
	} else {
		err = build(ctx, *output, *profile, *runtimeRoot)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "build production DuckDB extension supply: %v\n", err)
		os.Exit(1)
	}
}

func build(ctx context.Context, rawRoot, profile, rawRuntimeRoot string) error {
	root, err := canonicalOutputRoot(rawRoot)
	if err != nil {
		return err
	}
	if strings.TrimSpace(profile) == "" || profile != strings.TrimSpace(profile) {
		return errors.New("support profile must be canonical")
	}
	runtimeRoot, err := canonicalOutputRoot(rawRuntimeRoot)
	if err != nil {
		return fmt.Errorf("runtime root: %w", err)
	}
	if err := ensurePrivateRoot(root); err != nil {
		return err
	}
	if err := resetGeneratedOutput(root); err != nil {
		return err
	}
	// DuckDB INSTALL creates a version/platform directory tree. Keep that
	// mutable installer state outside the published output so the image only
	// carries the flat, reviewed artifacts selected below.
	stagingRoot, err := os.MkdirTemp(filepath.Dir(root), ".leapview-extension-supply-*")
	if err != nil {
		return fmt.Errorf("create private extension staging root: %w", err)
	}
	defer os.RemoveAll(stagingRoot)
	if err := os.Chmod(stagingRoot, 0o700); err != nil {
		return err
	}
	version, platform, err := runtimeTarget(ctx)
	if err != nil {
		return err
	}
	originRoot := filepath.Join(root, "artifacts")
	if err := ensurePrivateRoot(originRoot); err != nil {
		return err
	}
	manifestValue := manifest{
		Version: extensionsupply.ManifestVersion, DuckDBVersion: version,
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Platform: platform,
		SupportProfile: profile,
		// The artifact bytes are staged under the build output root, but the
		// manifest is consumed after Docker copies the complete directory into
		// this fixed runtime location.
		Origins: []extensionsupply.ManifestOrigin{{ID: originID, Path: filepath.Join(runtimeRoot, "artifacts"), Reviewed: true}},
	}
	for _, name := range projectcontracts.RequiredExtensionNames() {
		artifact, err := prepareOne(ctx, stagingRoot, originRoot, version, platform, profile, name)
		if err != nil {
			return err
		}
		manifestValue.Artifacts = append(manifestValue.Artifacts, artifact)
	}
	sort.Slice(manifestValue.Artifacts, func(i, j int) bool {
		return manifestValue.Artifacts[i].Identity.Name < manifestValue.Artifacts[j].Identity.Name
	})
	payload, err := json.MarshalIndent(manifestValue, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	payload = append(payload, '\n')
	manifestPath := filepath.Join(root, "extension-supply.json")
	if err := writePrivateFile(manifestPath, payload); err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	digestPath := manifestPath + ".sha256"
	if err := writePrivateFile(digestPath, []byte(hex.EncodeToString(digest[:])+"\n")); err != nil {
		return err
	}
	if err := validatePackagedLayout(root, len(manifestValue.Artifacts)); err != nil {
		return err
	}
	return nil
}

func check(ctx context.Context, rawRoot, profile, rawRuntimeRoot string) error {
	root, err := canonicalOutputRoot(rawRoot)
	if err != nil {
		return err
	}
	runtimeRoot, err := canonicalOutputRoot(rawRuntimeRoot)
	if err != nil {
		return fmt.Errorf("runtime root: %w", err)
	}
	if strings.TrimSpace(profile) == "" || profile != strings.TrimSpace(profile) {
		return errors.New("support profile must be canonical")
	}
	manifestPath := filepath.Join(root, "extension-supply.json")
	payload, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read extension supply manifest: %w", err)
	}
	digestPayload, err := os.ReadFile(manifestPath + ".sha256")
	if err != nil {
		return fmt.Errorf("read extension supply manifest digest: %w", err)
	}
	if err := verifyManifestDigest(payload, digestPayload); err != nil {
		return err
	}
	var document manifest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode extension supply manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("extension supply manifest must contain exactly one JSON value")
		}
		return fmt.Errorf("decode trailing extension supply manifest data: %w", err)
	}
	version, platform, err := runtimeTarget(ctx)
	if err != nil {
		return err
	}
	if document.Version != extensionsupply.ManifestVersion || document.DuckDBVersion != version || document.GOOS != runtime.GOOS || document.GOARCH != runtime.GOARCH || document.Platform != platform || document.SupportProfile != profile {
		return errors.New("extension supply manifest does not match the current DuckDB target/profile")
	}
	required := projectcontracts.RequiredExtensionNames()
	if len(document.Artifacts) != len(required) {
		return fmt.Errorf("extension supply manifest has %d artifacts, want %d", len(document.Artifacts), len(required))
	}
	if len(document.Origins) != 1 || document.Origins[0].Path != filepath.Join(runtimeRoot, "artifacts") || !document.Origins[0].Reviewed {
		return errors.New("extension supply manifest origin does not match the runtime artifact root")
	}
	seen := make(map[string]struct{}, len(document.Artifacts))
	for _, artifact := range document.Artifacts {
		if err := artifact.Identity.Validate(); err != nil {
			return fmt.Errorf("validate %s artifact identity: %w", artifact.Identity.Name, err)
		}
		identity := artifact.Identity
		if identity.DuckDBVersion != version || identity.GOOS != runtime.GOOS || identity.GOARCH != runtime.GOARCH || identity.Platform != platform || identity.SupportProfile != profile {
			return fmt.Errorf("extension supply artifact %q does not match the current DuckDB target/profile", identity.Name)
		}
		if _, ok := seen[artifact.Identity.Name]; ok {
			return fmt.Errorf("extension supply manifest repeats %q", artifact.Identity.Name)
		}
		seen[artifact.Identity.Name] = struct{}{}
		if strings.TrimSpace(artifact.Signature) == "" || strings.TrimSpace(artifact.Provenance) == "" {
			return fmt.Errorf("extension supply artifact %q is missing signature or provenance evidence", artifact.Identity.Name)
		}
		if !isRequiredExtension(required, artifact.Identity.Name) || len(artifact.Origins) != 1 || artifact.Origins[0] != document.Origins[0].ID {
			return fmt.Errorf("extension supply manifest contains an unapproved artifact %q", artifact.Identity.Name)
		}
		artifactPath := filepath.Join(root, "artifacts", extension.ArtifactFilenameStem(artifact.Identity.Name)+"-"+artifact.Identity.ExtensionVersion+"-"+artifact.Identity.Platform+".duckdb_extension")
		contents, readErr := os.ReadFile(artifactPath)
		if readErr != nil {
			return fmt.Errorf("read packaged %s artifact: %w", artifact.Identity.Name, readErr)
		}
		if got := sha256.Sum256(contents); "sha256:"+hex.EncodeToString(got[:]) != artifact.Identity.Digest {
			return fmt.Errorf("packaged %s artifact digest does not match the manifest", artifact.Identity.Name)
		}
	}
	for _, name := range required {
		if _, ok := seen[name]; !ok {
			return fmt.Errorf("extension supply manifest is missing required artifact %q", name)
		}
	}
	return validatePackagedLayout(root, len(required))
}

func verifyManifestDigest(payload, sidecar []byte) error {
	fields := strings.Fields(string(sidecar))
	if len(fields) != 1 || len(fields[0]) != sha256.Size*2 || strings.ToLower(fields[0]) != fields[0] {
		return errors.New("extension supply manifest digest sidecar is not canonical")
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return errors.New("extension supply manifest digest sidecar is invalid")
	}
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != fields[0] {
		return errors.New("extension supply manifest digest mismatch")
	}
	return nil
}

func isRequiredExtension(required []string, candidate string) bool {
	for _, name := range required {
		if name == candidate {
			return true
		}
	}
	return false
}

func prepareOne(ctx context.Context, installRoot, outputRoot, version, platform, profile, name string) (extensionsupply.Artifact, error) {
	if !isProductionExtension(name) {
		return extensionsupply.Artifact{}, fmt.Errorf("extension %q is not in the reviewed production set", name)
	}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return extensionsupply.Artifact{}, fmt.Errorf("open DuckDB extension provisioner: %w", err)
	}
	defer db.Close()
	escapedRoot := strings.ReplaceAll(installRoot, "'", "''")
	if _, err := db.ExecContext(ctx, "SET extension_directory = '"+escapedRoot+"'"); err != nil {
		return extensionsupply.Artifact{}, fmt.Errorf("set extension directory for %s: %w", name, err)
	}
	if _, err := db.ExecContext(ctx, "INSTALL "+name+" FROM core"); err != nil {
		return extensionsupply.Artifact{}, fmt.Errorf("install pinned %s extension: %w", name, err)
	}
	path, extVersion, err := locateInstalled(db, installRoot, version, platform, name)
	if err != nil {
		return extensionsupply.Artifact{}, err
	}
	if err := verifyExactLoad(ctx, path); err != nil {
		return extensionsupply.Artifact{}, fmt.Errorf("verify signed %s extension: %w", name, err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return extensionsupply.Artifact{}, fmt.Errorf("read %s extension: %w", name, err)
	}
	digest := sha256.Sum256(contents)
	digestValue := "sha256:" + hex.EncodeToString(digest[:])
	canonicalName := extension.ArtifactFilenameStem(name)
	packagedPath := filepath.Join(outputRoot, canonicalName+"-"+extVersion+"-"+platform+".duckdb_extension")
	if packagedPath != path {
		if err := copyPrivateFile(packagedPath, contents); err != nil {
			return extensionsupply.Artifact{}, fmt.Errorf("stage %s extension: %w", name, err)
		}
	}
	identity := extension.Identity{DuckDBVersion: version, ExtensionVersion: extVersion, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Platform: platform, Name: name, Digest: digestValue, SupportProfile: profile}
	if err := identity.Validate(); err != nil {
		return extensionsupply.Artifact{}, fmt.Errorf("extension %s identity: %w", name, err)
	}
	return extensionsupply.Artifact{Identity: identity, Origins: []string{originID}, Provenance: "attest:duckdb-core-pinned", Signature: "sig:duckdb-official"}, nil
}

func locateInstalled(db *sql.DB, root, version, platform, name string) (string, string, error) {
	filename := extension.ArtifactFilenameStem(name) + ".duckdb_extension"
	path := filepath.Join(root, version, strings.ReplaceAll(platform, "-", "_"), filename)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", "", fmt.Errorf("installed %s extension is unavailable at the exact target path", name)
	}
	var extensionVersion string
	stem := extension.ArtifactFilenameStem(name)
	if err := db.QueryRow("SELECT extension_version FROM duckdb_extensions() WHERE extension_name IN (?, ?) ORDER BY extension_name LIMIT 1", name, stem).Scan(&extensionVersion); err != nil {
		return "", "", fmt.Errorf("read %s extension version: %w", name, err)
	}
	extensionVersion = strings.TrimSpace(extensionVersion)
	if extensionVersion == "" || strings.ContainsAny(extensionVersion, `/\\`) || extensionVersion != strings.TrimSpace(extensionVersion) {
		return "", "", fmt.Errorf("%s extension version is not a safe token", name)
	}
	return path, extensionVersion, nil
}

func verifyExactLoad(ctx context.Context, path string) error {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return err
	}
	defer db.Close()
	for _, statement := range []string{"SET autoinstall_known_extensions = false", "SET autoload_known_extensions = false"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	escaped := strings.ReplaceAll(path, "'", "''")
	_, err = db.ExecContext(ctx, "LOAD '"+escaped+"'")
	return err
}

func runtimeTarget(ctx context.Context) (string, string, error) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return "", "", err
	}
	defer db.Close()
	var version, platform string
	if err := db.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil {
		return "", "", err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA platform").Scan(&platform); err != nil {
		return "", "", err
	}
	version, platform = strings.TrimSpace(version), strings.TrimSpace(platform)
	if version != extensionsupply.CurrentDuckDBVersion {
		return "", "", fmt.Errorf("DuckDB runtime %q does not match pinned extension ABI %q", version, extensionsupply.CurrentDuckDBVersion)
	}
	if platform == "" || strings.ContainsAny(platform, `/\\`) || !strings.Contains(platform, runtime.GOOS) || !strings.Contains(platform, runtime.GOARCH) {
		return "", "", fmt.Errorf("DuckDB runtime platform %q does not bind %s/%s", platform, runtime.GOOS, runtime.GOARCH)
	}
	return version, platform, nil
}

func isProductionExtension(name string) bool {
	for _, candidate := range projectcontracts.RequiredExtensionNames() {
		if candidate == name {
			return true
		}
	}
	return false
}

func canonicalOutputRoot(raw string) (string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || !filepath.IsAbs(raw) || filepath.Clean(raw) != raw {
		return "", errors.New("extension supply output must be an absolute canonical path")
	}
	return raw, nil
}

func resetGeneratedOutput(root string) error {
	generated := []string{
		filepath.Join(root, "artifacts"),
		filepath.Join(root, "extension-supply.json"),
		filepath.Join(root, "extension-supply.json.sha256"),
	}
	for _, path := range generated {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to remove symlink from extension supply output: %s", path)
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("reset extension supply output %s: %w", path, err)
		}
	}
	return nil
}

func validatePackagedLayout(root string, artifactCount int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("inspect extension supply output: %w", err)
	}
	allowed := map[string]struct{}{
		"artifacts":                    {},
		"extension-supply.json":        {},
		"extension-supply.json.sha256": {},
	}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok {
			return fmt.Errorf("extension supply output contains unexpected entry %q", entry.Name())
		}
		if entry.Name() == "artifacts" {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("extension supply artifacts entry must be a non-symlink directory")
			}
			continue
		}
		if !entry.Type().IsRegular() || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("extension supply metadata entry %q must be a regular non-symlink file", entry.Name())
		}
	}
	artifactEntries, err := os.ReadDir(filepath.Join(root, "artifacts"))
	if err != nil {
		return fmt.Errorf("inspect packaged extension artifacts: %w", err)
	}
	if len(artifactEntries) != artifactCount {
		return fmt.Errorf("packaged extension artifact count %d does not match manifest count %d", len(artifactEntries), artifactCount)
	}
	for _, entry := range artifactEntries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".duckdb_extension") {
			return fmt.Errorf("packaged extension output contains non-flat artifact %q", entry.Name())
		}
	}
	return nil
}

func ensurePrivateRoot(root string) error {
	if info, err := os.Lstat(root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("extension supply root must be a private non-symlink directory")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	return os.Chmod(root, 0o700)
}

func writePrivateFile(path string, contents []byte) error {
	return copyPrivateFile(path, contents)
}

func copyPrivateFile(path string, contents []byte) error {
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
