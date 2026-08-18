// Package extensionsupply owns the reviewed, target-side supply of DuckDB
// extension artifacts.  It is intentionally usable by packaging and bounded
// administrative preparation without importing project YAML or authored SQL.
package extensionsupply

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
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/flidai/leapview/internal/extension"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

const (
	ManifestVersion  = 1
	MaxArtifactBytes = 256 << 20
	// CurrentDuckDBVersion is the pinned DuckDB binding/runtime identity used
	// by the application composition. A supply document must match it exactly.
	CurrentDuckDBVersion = "v1.5.4"
)

// Origin is target configuration, not project authoring.  Reviewed must be
// explicitly true; an origin merely named by a manifest is never sufficient.
// Fetch is supplied by packaging/admin adapters (file-backed tests are enough
// for offline and network-free operation).  The runtime never invokes it.
type Origin struct {
	ID       string
	URL      string
	Reviewed bool
	Fetch    func(context.Context, Artifact) (io.ReadCloser, error)
}

// Artifact is one exact manifest entry.  Origins contains identifiers only;
// credentials and network clients stay in the configured Origin adapters.
type Artifact struct {
	Identity   extension.Identity `json:"identity"`
	Origins    []string           `json:"origins"`
	Provenance string             `json:"provenance"`
	Signature  string             `json:"signature"`
}

// Manifest is reviewed deployment input.  It is not loaded from a project
// bundle; packaging or bounded admin preparation supplies it explicitly.
type Manifest struct {
	Version        int              `json:"version"`
	DuckDBVersion  string           `json:"duckdbVersion"`
	GOOS           string           `json:"goos"`
	GOARCH         string           `json:"goarch"`
	Platform       string           `json:"platform"`
	SupportProfile string           `json:"supportProfile"`
	Origins        []ManifestOrigin `json:"origins"`
	Artifacts      []Artifact       `json:"artifacts"`
}

// ManifestOrigin is the packaged reviewed local origin. Network URLs and
// credentials are intentionally not representable.
type ManifestOrigin struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Reviewed bool   `json:"reviewed"`
}

// SignatureVerifier can enforce a deployment's signature/provenance policy.
// The verifier receives immutable bytes and non-secret manifest metadata.
type SignatureVerifier func(context.Context, Artifact, []byte) error

type Config struct {
	DuckDBVersion   string
	GOOS            string
	GOARCH          string
	Platform        string
	SupportProfile  string
	CacheDir        string
	Manifest        Manifest
	Origins         []Origin
	Offline         bool
	VerifySignature SignatureVerifier
}

// Supply implements extension.Admission.  It has no methods that can fetch
// implicitly from DuckDB or from authored resources.
type Supply struct {
	config   Config
	entries  map[string]Artifact
	origins  map[string]Origin
	mu       sync.Mutex
	admitted map[string]extension.AdmittedExtension
	locks    sync.Map // cache path -> *sync.Mutex
}

// ExtensionSupply is a descriptive alias used by deployment composition.
type ExtensionSupply = Supply

// NewSupply is an explicit constructor alias for callers that prefer the
// domain name in dependency-injection code.
func NewSupply(config Config) (*Supply, error) { return New(config) }

var _ extension.Admission = (*Supply)(nil)
var _ extension.Preparation = (*Supply)(nil)

func New(config Config) (*Supply, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	entries := make(map[string]Artifact, len(config.Manifest.Artifacts))
	identities := make(map[string]struct{}, len(config.Manifest.Artifacts))
	for _, artifact := range config.Manifest.Artifacts {
		if err := validateArtifact(artifact); err != nil {
			return nil, err
		}
		identityKey, keyErr := artifact.Identity.Canonical()
		if keyErr != nil {
			return nil, keyErr
		}
		if _, exists := identities[identityKey]; exists {
			return nil, fmt.Errorf("%w: duplicate exact artifact identity for %q", extension.ErrInvalidManifest, artifact.Identity.Name)
		}
		identities[identityKey] = struct{}{}
		if !identityMatchesTarget(artifact.Identity, config) {
			// A reviewed manifest may carry multiple platform tuples. Only the
			// exact target tuple enters this runtime index.
			continue
		}
		if _, exists := entries[artifact.Identity.Name]; exists {
			return nil, fmt.Errorf("%w: duplicate target artifact %q", extension.ErrInvalidManifest, artifact.Identity.Name)
		}
		entries[artifact.Identity.Name] = cloneArtifact(artifact)
	}
	origins := make(map[string]Origin, len(config.Origins))
	for _, origin := range config.Origins {
		if err := validateOrigin(origin); err != nil {
			return nil, err
		}
		if _, exists := origins[origin.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate configured origin %q", extension.ErrInvalidManifest, origin.ID)
		}
		if origin.Fetch == nil {
			origin.Fetch = fileOriginFetcher(origin.URL)
		}
		origins[origin.ID] = origin
	}
	return &Supply{config: config, entries: entries, origins: origins, admitted: map[string]extension.AdmittedExtension{}}, nil
}

func validateConfig(config Config) error {
	fields := []struct{ name, value string }{
		{"DuckDB version", config.DuckDBVersion}, {"GOOS", config.GOOS}, {"GOARCH", config.GOARCH},
		{"platform", config.Platform}, {"support profile", config.SupportProfile}, {"cache directory", config.CacheDir},
	}
	for _, field := range fields {
		if field.value == "" || field.value != strings.TrimSpace(field.value) || strings.IndexFunc(field.value, unicode.IsControl) >= 0 {
			return fmt.Errorf("%w: %s is required and canonical", extension.ErrInvalidManifest, field.name)
		}
	}
	if !strings.Contains(config.Platform, config.GOOS) || !strings.Contains(config.Platform, config.GOARCH) {
		return fmt.Errorf("%w: platform does not bind GOOS/GOARCH", extension.ErrInvalidManifest)
	}
	if !filepath.IsAbs(config.CacheDir) {
		return fmt.Errorf("%w: cache directory must be absolute", extension.ErrInvalidManifest)
	}
	if config.Manifest.Version != ManifestVersion {
		return fmt.Errorf("%w: unsupported manifest version %d", extension.ErrInvalidManifest, config.Manifest.Version)
	}
	for _, field := range []struct {
		name, declared, target string
	}{
		{"manifest DuckDB version", config.Manifest.DuckDBVersion, config.DuckDBVersion},
		{"manifest GOOS", config.Manifest.GOOS, config.GOOS},
		{"manifest GOARCH", config.Manifest.GOARCH, config.GOARCH},
		{"manifest platform", config.Manifest.Platform, config.Platform},
		{"manifest support profile", config.Manifest.SupportProfile, config.SupportProfile},
	} {
		if field.declared == "" || field.declared != field.target {
			return fmt.Errorf("%w: %s does not match configured target", extension.ErrInvalidManifest, field.name)
		}
	}
	if config.VerifySignature == nil {
		return fmt.Errorf("%w: a signature/provenance verifier is required", extension.ErrInvalidManifest)
	}
	return nil
}

func validateOrigin(origin Origin) error {
	if origin.ID == "" || origin.ID != strings.TrimSpace(origin.ID) || strings.IndexFunc(origin.ID, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: origin id is invalid", extension.ErrInvalidManifest)
	}
	if origin.URL == "" || origin.URL != strings.TrimSpace(origin.URL) || strings.IndexFunc(origin.URL, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: origin %q URL is invalid", extension.ErrInvalidManifest, origin.ID)
	}
	parsed, parseErr := url.Parse(origin.URL)
	if parseErr != nil || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("%w: origin %q URL contains credentials or fragment", extension.ErrInvalidManifest, origin.ID)
	}
	if !origin.Reviewed {
		return fmt.Errorf("%w: origin %q is not reviewed", extension.ErrExtensionUnapproved, origin.ID)
	}
	if origin.Fetch == nil {
		parsedScheme := ""
		if parsed != nil {
			parsedScheme = parsed.Scheme
		}
		if parsedScheme != "" && parsedScheme != "file" {
			return fmt.Errorf("%w: origin %q requires a bounded fetch adapter", extension.ErrInvalidManifest, origin.ID)
		}
	}
	return nil
}

func fileOriginFetcher(raw string) func(context.Context, Artifact) (io.ReadCloser, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || (parsed.Scheme != "" && parsed.Scheme != "file") {
		return nil
	}
	path := parsed.Path
	if parsed.Scheme == "" {
		path = raw
	}
	return func(ctx context.Context, _ Artifact) (io.ReadCloser, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return os.Open(path)
	}
}

func validateArtifact(artifact Artifact) error {
	if err := artifact.Identity.Validate(); err != nil {
		return err
	}
	if len(artifact.Origins) == 0 {
		return fmt.Errorf("%w: artifact %q has no configured origins", extension.ErrInvalidManifest, artifact.Identity.Name)
	}
	if strings.TrimSpace(artifact.Provenance) == "" || strings.TrimSpace(artifact.Signature) == "" {
		return fmt.Errorf("%w: artifact %q is missing signature or provenance", extension.ErrExtensionUnsigned, artifact.Identity.Name)
	}
	if err := validateReference(artifact.Provenance, "provenance"); err != nil {
		return err
	}
	if err := validateReference(artifact.Signature, "signature"); err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for _, origin := range artifact.Origins {
		if origin == "" || origin != strings.TrimSpace(origin) || strings.IndexFunc(origin, unicode.IsControl) >= 0 {
			return fmt.Errorf("%w: artifact %q has invalid origin", extension.ErrInvalidManifest, artifact.Identity.Name)
		}
		if _, ok := seen[origin]; ok {
			return fmt.Errorf("%w: artifact %q repeats origin %q", extension.ErrInvalidManifest, artifact.Identity.Name, origin)
		}
		seen[origin] = struct{}{}
	}
	return nil
}

func validateReference(value, label string) error {
	if value == "" || value != strings.TrimSpace(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 || strings.Contains(value, "://") || strings.ContainsAny(value, `/\\`) {
		return fmt.Errorf("%w: %s reference is not a constrained non-secret identifier", extension.ErrInvalidManifest, label)
	}
	if strings.HasPrefix(value, "sha256:") {
		if platformdigest.ValidateSHA256Identity(value) != nil {
			return fmt.Errorf("%w: %s reference digest is invalid", extension.ErrInvalidManifest, label)
		}
		return nil
	}
	if !strings.HasPrefix(value, "sig:") && !strings.HasPrefix(value, "attest:") {
		return fmt.Errorf("%w: %s reference must be sig:, attest:, or sha256:", extension.ErrInvalidManifest, label)
	}
	for _, r := range value {
		if !(r == ':' || r == '-' || r == '_' || r == '.' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return fmt.Errorf("%w: %s reference contains unsupported characters", extension.ErrInvalidManifest, label)
		}
	}
	return nil
}

func identityMatchesTarget(identity extension.Identity, config Config) bool {
	return identity.DuckDBVersion == config.DuckDBVersion && identity.GOOS == config.GOOS && identity.GOARCH == config.GOARCH && identity.Platform == config.Platform && identity.SupportProfile == config.SupportProfile
}

func cloneArtifact(value Artifact) Artifact {
	value.Origins = append([]string(nil), value.Origins...)
	return value
}

// ResolveRequirements maps generated connector requirements to exact reviewed
// manifest entries.  Requirements are sorted and deduplicated, so the result
// is deterministic independent of authored map iteration order.
func (s *Supply) ResolveRequirements(requirements []string) ([]Artifact, error) {
	if s == nil {
		return nil, extension.ErrExtensionUnavailable
	}
	seen := map[string]struct{}{}
	for _, name := range requirements {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("%w: empty extension requirement", extension.ErrInvalidManifest)
		}
		seen[name] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]Artifact, 0, len(names))
	for _, name := range names {
		artifact, ok := s.entries[name]
		if !ok {
			return nil, fmt.Errorf("%w: no exact manifest entry for extension %q", extension.ErrExtensionUnavailable, name)
		}
		result = append(result, cloneArtifact(artifact))
	}
	return result, nil
}

func (s *Supply) AdmitExtension(ctx context.Context, name string) (extension.AdmittedExtension, error) {
	if s == nil {
		return extension.AdmittedExtension{}, extension.ErrExtensionUnavailable
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return extension.AdmittedExtension{}, fmt.Errorf("%w: extension name is required", extension.ErrExtensionUnavailable)
	}
	s.mu.Lock()
	if admitted, ok := s.admitted[name]; ok {
		s.mu.Unlock()
		return admitted, nil
	}
	artifact, ok := s.entries[name]
	s.mu.Unlock()
	if !ok {
		return extension.AdmittedExtension{}, fmt.Errorf("%w: extension %q is not in the exact manifest", extension.ErrExtensionUnavailable, name)
	}
	path, originID, err := s.ensureArtifact(ctx, artifact)
	if err != nil {
		return extension.AdmittedExtension{}, err
	}
	identity := artifact.Identity
	admitted := extension.AdmittedExtension{
		Name: name, Identity: identityKey(identity), Version: identity.ExtensionVersion,
		ExtensionVersion: identity.ExtensionVersion, DuckDBVersion: identity.DuckDBVersion,
		GOOS: identity.GOOS, GOARCH: identity.GOARCH, Platform: identity.Platform,
		SupportProfile: identity.SupportProfile, Digest: identity.Digest, Path: path,
		Origin: originID, Provenance: artifact.Provenance, Signature: artifact.Signature,
	}
	s.mu.Lock()
	if existing, exists := s.admitted[name]; exists {
		s.mu.Unlock()
		return existing, nil
	}
	s.admitted[name] = admitted
	s.mu.Unlock()
	return admitted, nil
}

// PrepareExtensions resolves and admits the complete generated-registry
// requirement set in deterministic order. No project text or SQL can add a
// requirement at this boundary.
func (s *Supply) PrepareExtensions(ctx context.Context, requirements []string) ([]extension.Evidence, error) {
	artifacts, err := s.ResolveRequirements(requirements)
	if err != nil {
		return nil, err
	}
	values := make([]extension.Evidence, 0, len(artifacts))
	for _, artifact := range artifacts {
		admitted, admitErr := s.AdmitExtension(ctx, artifact.Identity.Name)
		if admitErr != nil {
			return nil, admitErr
		}
		values = append(values, admitted.Evidence())
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	return values, nil
}

func (s *Supply) ensureArtifact(ctx context.Context, artifact Artifact) (string, string, error) {
	if err := ensurePrivateCacheRoot(s.config.CacheDir); err != nil {
		return "", "", err
	}
	path, err := s.cachePath(artifact.Identity)
	if err != nil {
		return "", "", err
	}
	lockValue, _ := s.locks.LoadOrStore(path, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	if exists, validErr := verifyFile(path, artifact.Identity.Digest); exists {
		if validErr != nil {
			return "", "", validErr
		}
		if err := s.verifyCached(ctx, path, artifact); err != nil {
			return "", "", err
		}
		return path, "cache", nil
	}
	if s.config.Offline {
		return "", "", fmt.Errorf("%w: %s", extension.ErrExtensionOffline, artifact.Identity.Name)
	}

	// Manifest origin ordering is canonical; a fetch failure may fall through
	// to the next reviewed origin, but no unconfigured origin can be tried.
	originIDs := append([]string(nil), artifact.Origins...)
	sort.Strings(originIDs)
	var lastErr error
	for _, originID := range originIDs {
		origin, ok := s.origins[originID]
		if !ok || !origin.Reviewed {
			lastErr = fmt.Errorf("%w: origin %q", extension.ErrExtensionUnapproved, originID)
			continue
		}
		reader, fetchErr := origin.Fetch(ctx, artifact)
		if fetchErr != nil {
			lastErr = fetchErr
			continue
		}
		bytes, readErr := readBounded(reader, MaxArtifactBytes)
		closeErr := reader.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if closeErr != nil {
			lastErr = closeErr
			continue
		}
		if err := verifyBytes(bytes, artifact); err != nil {
			lastErr = err
			continue
		}
		if s.config.VerifySignature != nil {
			if err := s.config.VerifySignature(ctx, artifact, bytes); err != nil {
				lastErr = fmt.Errorf("%w: %v", extension.ErrExtensionIntegrity, err)
				continue
			}
		}
		if err := s.atomicAdmit(path, bytes, artifact.Identity.Digest); err != nil {
			return "", "", err
		}
		return path, originID, nil
	}
	if lastErr == nil {
		lastErr = extension.ErrExtensionUnavailable
	}
	// Adapter errors can contain endpoint details or credential-bearing driver
	// text. Preserve only the bounded operation class in runtime diagnostics.
	return "", "", fmt.Errorf("%w: %s", extension.ErrExtensionUnavailable, artifact.Identity.Name)
}

func (s *Supply) verifyCached(ctx context.Context, path string, artifact Artifact) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: open cached artifact", extension.ErrExtensionIntegrity)
	}
	bytes, readErr := readBounded(file, MaxArtifactBytes)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return fmt.Errorf("%w: read cached artifact", extension.ErrExtensionIntegrity)
	}
	if err := verifyBytes(bytes, artifact); err != nil {
		return err
	}
	if s.config.VerifySignature == nil {
		return fmt.Errorf("%w: signature verifier is unavailable", extension.ErrExtensionIntegrity)
	}
	if err := s.config.VerifySignature(ctx, artifact, bytes); err != nil {
		return fmt.Errorf("%w: %v", extension.ErrExtensionIntegrity, err)
	}
	return nil
}

func (s *Supply) cachePath(identity extension.Identity) (string, error) {
	if err := identity.Validate(); err != nil {
		return "", err
	}
	digest := strings.TrimPrefix(identity.Digest, "sha256:")
	name := fmt.Sprintf("%s-%s-%s.duckdb_extension", identity.Name, identity.ExtensionVersion, identity.Platform)
	path := filepath.Join(s.config.CacheDir, digest, name)
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: cache path: %v", extension.ErrInvalidManifest, err)
	}
	absolute = filepath.Clean(absolute)
	root, err := filepath.Abs(s.config.CacheDir)
	if err != nil {
		return "", fmt.Errorf("%w: cache root: %v", extension.ErrInvalidManifest, err)
	}
	relative, err := filepath.Rel(filepath.Clean(root), absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: artifact path escapes cache root", extension.ErrInvalidManifest)
	}
	return absolute, nil
}

func verifyFile(path, expected string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("%w: inspect cache artifact", extension.ErrExtensionIntegrity)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return true, fmt.Errorf("%w: cache artifact permissions", extension.ErrExtensionIntegrity)
	}
	if info.Size() > MaxArtifactBytes {
		return true, fmt.Errorf("%w: cache artifact exceeds size limit", extension.ErrExtensionIntegrity)
	}
	file, err := os.Open(path)
	if err != nil {
		return true, fmt.Errorf("%w: open cache artifact", extension.ErrExtensionIntegrity)
	}
	defer file.Close()
	hash := sha256.New()
	if n, err := io.Copy(hash, io.LimitReader(file, MaxArtifactBytes+1)); err != nil {
		return true, fmt.Errorf("%w: hash cache artifact", extension.ErrExtensionIntegrity)
	} else if n > MaxArtifactBytes {
		return true, fmt.Errorf("%w: cache artifact exceeds size limit", extension.ErrExtensionIntegrity)
	}
	if hex.EncodeToString(hash.Sum(nil)) != strings.TrimPrefix(expected, "sha256:") {
		return true, fmt.Errorf("%w: cache artifact digest mismatch", extension.ErrExtensionIntegrity)
	}
	return true, nil
}

func readBounded(reader io.Reader, max int64) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("nil artifact reader")
	}
	bytes, err := io.ReadAll(io.LimitReader(reader, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(bytes)) > max {
		return nil, fmt.Errorf("artifact exceeds %d-byte limit", max)
	}
	return bytes, nil
}

func verifyBytes(bytes []byte, artifact Artifact) error {
	hash := sha256.Sum256(bytes)
	if got := "sha256:" + hex.EncodeToString(hash[:]); got != artifact.Identity.Digest {
		return fmt.Errorf("%w: expected %s", extension.ErrExtensionIntegrity, artifact.Identity.Name)
	}
	if platformdigest.ValidateSHA256Identity(artifact.Identity.Digest) != nil {
		return fmt.Errorf("%w: manifest digest is not canonical", extension.ErrInvalidManifest)
	}
	if artifact.Signature == "" || artifact.Provenance == "" {
		return extension.ErrExtensionUnsigned
	}
	return nil
}

func (s *Supply) atomicAdmit(path string, bytes []byte, digest string) error {
	if err := ensurePrivateCacheRoot(s.config.CacheDir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(filepath.Dir(path)); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: extension cache directory is not a private directory", extension.ErrExtensionIntegrity)
	}
	if existing, err := verifyFile(path, digest); existing {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".extension-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := temporary.Name()
	cleanup := true
	defer func() {
		_ = temporary.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(bytes); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// Link is a no-replace admission primitive: unlike Rename it cannot
	// overwrite a raced target. A same-content target is accepted; a different
	// target is an integrity failure and remains untouched.
	if err := os.Link(tmpPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			if exists, verifyErr := verifyFile(path, digest); exists && verifyErr == nil {
				return nil
			}
			return fmt.Errorf("%w: raced cache target is corrupt", extension.ErrExtensionIntegrity)
		}
		return err
	}
	if err := os.Remove(tmpPath); err != nil {
		return err
	}
	cleanup = false
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func ensurePrivateCacheRoot(path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// Validate every existing ancestor before creating descendants. MkdirAll
	// alone would follow a symlinked parent supplied by target configuration.
	current := string(filepath.Separator)
	volume := filepath.VolumeName(path)
	if volume != "" {
		current = volume + string(filepath.Separator)
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: extension cache path contains symlink", extension.ErrExtensionIntegrity)
			}
			continue
		}
		if !os.IsNotExist(statErr) {
			return statErr
		}
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: extension cache root must be a private regular directory", extension.ErrExtensionIntegrity)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	return nil
}

func identityKey(identity extension.Identity) string {
	canonical, err := identity.Canonical()
	if err != nil {
		return ""
	}
	return canonical
}

// Evidence returns the non-secret admission evidence in deterministic order.
func (s *Supply) Evidence() []extension.Evidence {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	values := make([]extension.Evidence, 0, len(s.admitted))
	for _, admitted := range s.admitted {
		values = append(values, admitted.Evidence())
	}
	s.mu.Unlock()
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	return values
}

// ManifestJSON returns canonical non-secret manifest bytes for packaging
// provenance.  It intentionally excludes configured origin adapters.
func (s *Supply) ManifestJSON() ([]byte, error) {
	if s == nil {
		return nil, extension.ErrExtensionUnavailable
	}
	values := make([]Artifact, 0, len(s.entries))
	for _, artifact := range s.entries {
		values = append(values, cloneArtifact(artifact))
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Identity.Name < values[j].Identity.Name })
	manifest := s.config.Manifest
	manifest.Artifacts = values
	manifest.Origins = append([]ManifestOrigin(nil), manifest.Origins...)
	sort.Slice(manifest.Origins, func(i, j int) bool { return manifest.Origins[i].ID < manifest.Origins[j].ID })
	return json.Marshal(manifest)
}
