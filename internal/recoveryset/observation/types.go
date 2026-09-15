// Package observation defines the provider-observation contract used by
// recovery qualification. It is deliberately separate from recovery-set
// admission and activation: a manifest records captured evidence, but does
// not authenticate a provider or publish a recovery frontier.
package observation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/pkg/strictjson"
	"golang.org/x/text/unicode/norm"
)

const (
	ProtocolVersion int32 = 1
	SchemaVersion   int32 = 1
	// Keep the document bounded while allowing the declared inventory/object
	// limits to be represented without truncation. The managed-data ingress
	// contract admits up to 10,000 files in one revision, so the observation
	// document budget must cover that revision rather than the old 4,096-file
	// capture bound. The aggregate object count follows the declared maximum
	// revision/file product; the byte budget remains the primary aggregate cap.
	MaxManifestBytes      = 256 << 20
	MaxInventoryRevisions = 4096
	MaxRevisionFiles      = manageddata.MaxManifestFiles
	MaxManifestObjects    = MaxInventoryRevisions * MaxRevisionFiles
)

var (
	ErrInvalid = errors.New("provider observation manifest is invalid")

	namePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	lsnPattern     = regexp.MustCompile(`^[0-9A-F]+/[0-9A-F]+$`)
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	rawHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Boundary identifies the captured PostgreSQL recovery point and the
// inventory digest it was observed against. Trusted capture produces it;
// parsed fields are not an authenticity proof by themselves.
type Boundary struct {
	ProtocolVersion  int32  `json:"protocol_version"`
	DatabaseIdentity string `json:"database_identity"`
	SystemIdentity   string `json:"system_identity"`
	Timeline         uint32 `json:"timeline"`
	LSN              string `json:"lsn"`
	RestorePointName string `json:"restore_point_name"`
	InventoryDigest  string `json:"inventory_digest"`
}

// Inventory is the immutable, ready-revision metadata captured alongside a
// provider observation. Revision and file ordering is canonicalized only
// after duplicate identities have been rejected.
type Inventory struct {
	SchemaVersion int32      `json:"schema_version"`
	Revisions     []Revision `json:"revisions"`
}

type Revision struct {
	RevisionID     string `json:"revision_id"`
	ManifestDigest string `json:"manifest_digest"`
	Files          []File `json:"files"`
}

type File struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	StorageKey string `json:"storage_key"`
	Size       int64  `json:"size"`
}

// ProviderObject is the exact provider object identity and byte observation.
// SHA256 is the raw lowercase file hash, matching manageddata.File.
type ProviderObject struct {
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	VersionID string `json:"version_id"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

type Observation struct {
	RevisionID string         `json:"revision_id"`
	Path       string         `json:"path"`
	Object     ProviderObject `json:"object"`
}

type Manifest struct {
	SchemaVersion int32         `json:"schema_version"`
	Boundary      Boundary      `json:"boundary"`
	Inventory     Inventory     `json:"inventory"`
	Objects       []Observation `json:"objects"`
}

// Protection records retention compliance for one exact provider object. It
// does not retain an object or establish a recovery frontier.
type Protection struct {
	Object      ProviderObject `json:"object"`
	Mode        string         `json:"mode"`
	RetainUntil time.Time      `json:"retain_until"`
}

func canonicalText(value, label string, max int) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || len(value) > max {
		return "", fmt.Errorf("%w: %s must be nonempty and canonical", ErrInvalid, label)
	}
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalid, label)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: %s contains control character U+%04X", ErrInvalid, label, r)
		}
	}
	if norm.NFC.String(value) != value {
		return "", fmt.Errorf("%w: %s is not NFC-normalized", ErrInvalid, label)
	}
	return value, nil
}

func canonicalName(value, label string) (string, error) {
	value, err := canonicalText(value, label, 255)
	if err != nil {
		return "", err
	}
	if !namePattern.MatchString(value) {
		return "", fmt.Errorf("%w: %s has invalid name grammar", ErrInvalid, label)
	}
	return value, nil
}

func validateDigest(value, label string) error {
	if !digestPattern.MatchString(value) {
		return fmt.Errorf("%w: %s must be a canonical sha256 identity", ErrInvalid, label)
	}
	return nil
}

func validateRawHash(value, label string) error {
	if !rawHashPattern.MatchString(value) {
		return fmt.Errorf("%w: %s must be 64 lowercase hexadecimal characters", ErrInvalid, label)
	}
	return nil
}

func (b Boundary) Validate() error {
	if b.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: unsupported boundary protocol version %d", ErrInvalid, b.ProtocolVersion)
	}
	if _, err := canonicalName(b.DatabaseIdentity, "database identity"); err != nil {
		return err
	}
	if err := validateSystemIdentity(b.SystemIdentity); err != nil {
		return err
	}
	if b.Timeline == 0 {
		return fmt.Errorf("%w: boundary timeline must be positive", ErrInvalid)
	}
	if err := validateLSN(b.LSN); err != nil {
		return err
	}
	if _, err := canonicalName(b.RestorePointName, "restore point name"); err != nil {
		return err
	}
	return validateDigest(b.InventoryDigest, "boundary inventory digest")
}

func (b Boundary) normalize() (Boundary, error) {
	if err := b.Validate(); err != nil {
		return Boundary{}, err
	}
	var err error
	if b.DatabaseIdentity, err = canonicalName(b.DatabaseIdentity, "database identity"); err != nil {
		return Boundary{}, err
	}
	if b.SystemIdentity, err = canonicalSystemIdentity(b.SystemIdentity); err != nil {
		return Boundary{}, err
	}
	if b.RestorePointName, err = canonicalName(b.RestorePointName, "restore point name"); err != nil {
		return Boundary{}, err
	}
	return b, nil
}

// RecoveryIdentity is a stable, human-readable identity for this captured
// PostgreSQL point. It is meaningful only after Boundary.Validate succeeds.
func (b Boundary) RecoveryIdentity() string {
	normalized, err := b.normalize()
	if err != nil {
		return ""
	}
	return fmt.Sprintf("postgres:%s:%d:%s:%s", normalized.SystemIdentity, normalized.Timeline, normalized.LSN, normalized.RestorePointName)
}

// ClusterIdentity identifies the PostgreSQL system independently of a point.
func (b Boundary) ClusterIdentity() string {
	normalized, err := b.normalize()
	if err != nil {
		return ""
	}
	return "postgres:" + normalized.SystemIdentity
}

func (b Boundary) Matches(expected Boundary) bool {
	actual, actualErr := b.normalize()
	want, expectedErr := expected.normalize()
	return actualErr == nil && expectedErr == nil && actual == want
}

func (b Boundary) ValidateAgainst(expected Boundary) error {
	actual, err := b.normalize()
	if err != nil {
		return err
	}
	want, err := expected.normalize()
	if err != nil {
		return err
	}
	if actual != want {
		return fmt.Errorf("%w: boundary does not match expected recovery point", ErrInvalid)
	}
	return nil
}

func (b Boundary) CanonicalJSON() ([]byte, error) {
	normalized, err := b.normalize()
	if err != nil {
		return nil, err
	}
	return marshalBounded(normalized)
}

func (b Boundary) Digest() (string, error) {
	canonical, err := b.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return sha256Digest(canonical), nil
}

func ParseBoundary(raw []byte) (Boundary, error) {
	if len(raw) < 2 || len(raw) > MaxManifestBytes {
		return Boundary{}, fmt.Errorf("%w: boundary JSON is out of bounds", ErrInvalid)
	}
	var boundary Boundary
	if err := strictjson.DecodeWithOptions(raw, &boundary, strictjson.Options{MaxBytes: MaxManifestBytes, MaxDepth: 16, DuplicateKeys: strictjson.CaseFoldedKeys, AllowUnknownFields: false}); err != nil {
		return Boundary{}, fmt.Errorf("%w: decode boundary: %v", ErrInvalid, err)
	}
	if err := requireExactJSONShape(raw, boundary); err != nil {
		return Boundary{}, err
	}
	if err := boundary.Validate(); err != nil {
		return Boundary{}, err
	}
	return boundary, nil
}

func (p ProviderObject) Validate() error {
	if err := validateEndpoint(p.Endpoint); err != nil {
		return err
	}
	if _, err := canonicalText(p.Region, "provider region", 128); err != nil {
		return err
	}
	if _, err := canonicalName(p.Bucket, "provider bucket"); err != nil {
		return err
	}
	if _, err := canonicalText(p.Key, "provider object key", 1024); err != nil {
		return err
	}
	if p.VersionID == "" || p.VersionID != strings.TrimSpace(p.VersionID) || strings.EqualFold(p.VersionID, "null") {
		return fmt.Errorf("%w: provider object version ID must be nonempty and non-null", ErrInvalid)
	}
	if !utf8.ValidString(p.VersionID) || norm.NFC.String(p.VersionID) != p.VersionID || strings.IndexFunc(p.VersionID, unicode.IsControl) >= 0 || len(p.VersionID) > 1024 {
		return fmt.Errorf("%w: provider object version ID is not canonical", ErrInvalid)
	}
	if err := validateRawHash(p.SHA256, "provider object SHA-256"); err != nil {
		return err
	}
	if p.Size < 0 {
		return fmt.Errorf("%w: provider object size must be nonnegative", ErrInvalid)
	}
	return nil
}

func validateEndpoint(value string) error {
	if value == "" || value != strings.TrimSpace(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: provider endpoint must be nonempty and canonical", ErrInvalid)
	}
	if !utf8.ValidString(value) || norm.NFC.String(value) != value {
		return fmt.Errorf("%w: provider endpoint must be NFC-normalized", ErrInvalid)
	}
	u, err := url.Parse(value)
	if err != nil || value != u.String() || u.Scheme == "" || u.Scheme != strings.ToLower(u.Scheme) || u.Host == "" || u.Host != strings.ToLower(u.Host) || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" {
		return fmt.Errorf("%w: provider endpoint must not contain credentials, query, fragment, or path", ErrInvalid)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("%w: provider endpoint must use HTTPS or loopback HTTP", ErrInvalid)
}

func (o Observation) Validate() error {
	if _, err := manageddata.ParseRevisionID(o.RevisionID); err != nil {
		return fmt.Errorf("%w: observation revision ID: %v", ErrInvalid, err)
	}
	if _, err := canonicalText(o.Path, "observation path", 1024); err != nil {
		return err
	}
	return o.Object.Validate()
}

func (p Protection) Validate() error {
	if err := p.Object.Validate(); err != nil {
		return err
	}
	if _, err := canonicalName(p.Mode, "protection mode"); err != nil {
		return err
	}
	if p.RetainUntil.IsZero() {
		return fmt.Errorf("%w: protection retention deadline is required", ErrInvalid)
	}
	return nil
}

// ValidateAt checks only whether the exact captured object remains protected
// through requiredUntil. It performs no provider I/O and makes no frontier
// or authenticity claim.
func (p Protection) ValidateAt(requiredUntil time.Time) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if requiredUntil.IsZero() {
		return fmt.Errorf("%w: required retention deadline is required", ErrInvalid)
	}
	if p.RetainUntil.Before(requiredUntil) {
		return fmt.Errorf("%w: provider object protection expires before required deadline", ErrInvalid)
	}
	return nil
}

func sha256Digest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateSystemIdentity(value string) error {
	if _, err := canonicalSystemIdentity(value); err != nil {
		return err
	}
	return nil
}

func canonicalSystemIdentity(value string) (string, error) {
	value, err := canonicalText(value, "system identity", 20)
	if err != nil {
		return "", err
	}
	if value == "0" || (value[0] != '0' && isDecimal(value)) {
		if _, err := strconv.ParseUint(value, 10, 64); err == nil {
			return value, nil
		}
	}
	return "", fmt.Errorf("%w: system identity must be a canonical uint64", ErrInvalid)
}

func isDecimal(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}

func validateLSN(value string) error {
	if !lsnPattern.MatchString(value) {
		return fmt.Errorf("%w: boundary LSN is not canonical", ErrInvalid)
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if len(part) > 1 && part[0] == '0' {
			return fmt.Errorf("%w: boundary LSN contains a leading zero", ErrInvalid)
		}
		if _, err := strconv.ParseUint(part, 16, 32); err != nil {
			return fmt.Errorf("%w: boundary LSN component is out of range", ErrInvalid)
		}
	}
	return nil
}
