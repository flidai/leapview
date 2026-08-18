// Package extension contains the small, target-owned domain contracts used to
// admit DuckDB extensions.  It deliberately has no dependency on project
// authoring, SQL, or the DuckDB driver: a serving generation receives an
// already verified absolute artifact path and nothing else.
package extension

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

var (
	ErrInvalidManifest        = errors.New("extension manifest invalid")
	ErrExtensionUnavailable   = errors.New("extension artifact unavailable")
	ErrExtensionUnapproved    = errors.New("extension origin is not approved")
	ErrExtensionIntegrity     = errors.New("extension artifact integrity verification failed")
	ErrExtensionUnsigned      = errors.New("extension artifact is unsigned")
	ErrExtensionOffline       = errors.New("extension artifact is unavailable offline")
	ErrExtensionConfiguration = errors.New("extension runtime configuration invalid")
)

// Identity is the complete compatibility identity of one extension artifact.
// Every field participates in the canonical identity; no caller may substitute
// a platform, DuckDB runtime, or LeapView support profile at admission time.
type Identity struct {
	DuckDBVersion    string `json:"duckdbVersion"`
	ExtensionVersion string `json:"extensionVersion"`
	GOOS             string `json:"goos"`
	GOARCH           string `json:"goarch"`
	Platform         string `json:"platform"`
	Name             string `json:"name"`
	Digest           string `json:"digest"`
	SupportProfile   string `json:"supportProfile"`
}

func (i Identity) Validate() error {
	fields := []struct{ name, value string }{
		{"DuckDB version", i.DuckDBVersion}, {"extension version", i.ExtensionVersion}, {"GOOS", i.GOOS}, {"GOARCH", i.GOARCH},
		{"platform", i.Platform}, {"extension name", i.Name}, {"digest", i.Digest},
		{"support profile", i.SupportProfile},
	}
	for _, field := range fields {
		if field.value == "" || field.value != strings.TrimSpace(field.value) || strings.IndexFunc(field.value, unicode.IsControl) >= 0 {
			return fmt.Errorf("%w: %s is required and must be canonical", ErrInvalidManifest, field.name)
		}
	}
	if err := validateDigest(i.Digest); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if filepath.Base(i.Name) != i.Name || i.Name == "." || i.Name == ".." || strings.ContainsAny(i.Name, `/\\`) {
		return fmt.Errorf("%w: extension name is not a safe literal", ErrInvalidManifest)
	}
	if strings.ContainsAny(i.ExtensionVersion, `/\\`) || strings.ContainsAny(i.Platform, `/\\`) {
		return fmt.Errorf("%w: extension version and platform must be safe path tokens", ErrInvalidManifest)
	}
	// Platform is intentionally explicit even when it can be derived from
	// GOOS/GOARCH.  This prevents a manifest silently changing its platform
	// naming convention without changing the pinned identity.
	if !strings.Contains(i.Platform, i.GOOS) || !strings.Contains(i.Platform, i.GOARCH) {
		return fmt.Errorf("%w: platform %q does not bind GOOS/GOARCH %s/%s", ErrInvalidManifest, i.Platform, i.GOOS, i.GOARCH)
	}
	return nil
}

// Canonical returns a stable, content-addressed identity that binds every
// compatibility field (including the support profile) without exposing any
// target credentials.
func (i Identity) Canonical() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(i)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func validateDigest(value string) error {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return fmt.Errorf("digest must be sha256:<64 hex characters>")
	}
	encoded := strings.TrimPrefix(value, "sha256:")
	if strings.ToLower(encoded) != encoded {
		return fmt.Errorf("digest must use lowercase hexadecimal")
	}
	if _, err := hex.DecodeString(encoded); err != nil {
		return fmt.Errorf("digest is not lowercase hexadecimal: %w", err)
	}
	return nil
}

// AdmittedExtension is immutable evidence returned to a runtime.  Path is
// target-owned and absolute; it is never read from a project artifact or SQL.
type AdmittedExtension struct {
	Name             string `json:"name"`
	Identity         string `json:"identity"`
	Version          string `json:"version"`
	ExtensionVersion string `json:"extensionVersion"`
	DuckDBVersion    string `json:"duckdbVersion"`
	GOOS             string `json:"goos"`
	GOARCH           string `json:"goarch"`
	Platform         string `json:"platform"`
	SupportProfile   string `json:"supportProfile"`
	Digest           string `json:"digest"`
	Path             string `json:"path"`
	Origin           string `json:"origin,omitempty"`
	Provenance       string `json:"provenance,omitempty"`
	Signature        string `json:"signature,omitempty"`
}

// Admission is the narrow runtime seam.  Implementations must return only an
// artifact that has already been verified and atomically admitted to cache.
type Admission interface {
	AdmitExtension(context.Context, string) (AdmittedExtension, error)
}

// Preparation is the bounded packaging/admin seam used by candidate
// preparation. Implementations resolve names against a reviewed exact
// manifest and admit every requested artifact before activation.
type Preparation interface {
	PrepareExtensions(context.Context, []string) ([]Evidence, error)
}

// Evidence is non-secret candidate provenance for one admitted extension.
// It contains no origins' credentials or artifact bytes.
type Evidence struct {
	Name             string `json:"name"`
	Identity         string `json:"identity"`
	DuckDBVersion    string `json:"duckdbVersion"`
	ExtensionVersion string `json:"extensionVersion"`
	GOOS             string `json:"goos"`
	GOARCH           string `json:"goarch"`
	Platform         string `json:"platform"`
	SupportProfile   string `json:"supportProfile"`
	Digest           string `json:"digest"`
	Origin           string `json:"origin,omitempty"`
	Provenance       string `json:"provenance,omitempty"`
	Signature        string `json:"signature,omitempty"`
}

func (a AdmittedExtension) Evidence() Evidence {
	return Evidence{Name: a.Name, Identity: a.Identity, DuckDBVersion: a.DuckDBVersion, ExtensionVersion: a.ExtensionVersion, GOOS: a.GOOS, GOARCH: a.GOARCH, Platform: a.Platform, SupportProfile: a.SupportProfile, Digest: a.Digest, Origin: a.Origin, Provenance: a.Provenance, Signature: a.Signature}
}
