// Package artifactadmission defines the immutable OCI admission record owned
// by release maintenance and resolved read-only by transition preflight.
package artifactadmission

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/distribution/reference"
	"github.com/flidai/leapview/internal/platform/compatibility"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/platform/ociref"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

const (
	AdmissionVersion      = "oci-artifact-admission/v1"
	SecurityPolicyVersion = "oci-security-policy/v1"
	DecisionAdmitted      = "admitted"
	DigestDomain          = "leapview/oci-artifact-admission/v1\n"
	MaxCanonicalBytes     = 262144
	SourceRepository      = "flidai/leapview"
	SBOMPredicateSPDX     = "https://spdx.dev/Document/v2.3"
	SBOMProducerBuildx    = "docker/buildx"
	SecurityScannerTrivy  = "trivy"
)

var sourceRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Authority is the read-only owner contract needed by transition preflight.
// Runtime callers supply only an exact immutable lookup reference; they cannot
// inject an artifact projection or admission decision.
type Authority interface {
	ResolveArtifact(context.Context, string) (transitionpreflight.ArtifactIdentity, error)
}

type ProvenanceResult struct {
	Reference      string `json:"reference"`
	Repository     string `json:"repository"`
	Workflow       string `json:"workflow"`
	SourceRevision string `json:"sourceRevision"`
	Verified       bool   `json:"verified"`
}

type SBOMResult struct {
	Reference     string `json:"reference"`
	PredicateType string `json:"predicateType"`
	Producer      string `json:"producer"`
	Verified      bool   `json:"verified"`
}

type SecurityPolicyResult struct {
	Version   string `json:"version"`
	Reference string `json:"reference"`
	Scanner   string `json:"scanner"`
	Passed    bool   `json:"passed"`
}

// Admission is the complete owner-produced decision over one exact OCI
// artifact. ArtifactAdmissionDigest is deliberately absent: it is derived
// from these canonical bytes and added to the transition projection.
type Admission struct {
	Version            string                        `json:"version"`
	Release            compatibility.ReleaseIdentity `json:"release"`
	ArchitectureMarker string                        `json:"architectureMarker"`
	Repository         string                        `json:"repository"`
	OCIDigest          string                        `json:"ociDigest"`
	Decision           string                        `json:"decision"`
	Provenance         ProvenanceResult              `json:"provenance"`
	SBOM               SBOMResult                    `json:"sbom"`
	SecurityPolicy     SecurityPolicyResult          `json:"securityPolicy"`
	AdmittedAt         time.Time                     `json:"admittedAt"`
}

func (a Admission) CanonicalJSON() ([]byte, error) {
	normalized, err := a.normalized()
	if err != nil {
		return nil, err
	}
	document, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("encode OCI artifact admission: %w", err)
	}
	if len(document) > MaxCanonicalBytes {
		return nil, errors.New("OCI artifact admission exceeds bounded storage size")
	}
	return document, nil
}

func (a Admission) Digest() (string, error) {
	document, err := a.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(DigestDomain), document...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (a Admission) ArtifactIdentity() (transitionpreflight.ArtifactIdentity, error) {
	normalized, err := a.normalized()
	if err != nil {
		return transitionpreflight.ArtifactIdentity{}, err
	}
	digest, err := normalized.Digest()
	if err != nil {
		return transitionpreflight.ArtifactIdentity{}, err
	}
	identity := transitionpreflight.ArtifactIdentity{
		Release: normalized.Release, ArchitectureMarker: normalized.ArchitectureMarker,
		ArtifactAdmissionDigest: digest,
	}
	if _, err := identity.Digest(); err != nil {
		return transitionpreflight.ArtifactIdentity{}, fmt.Errorf("construct transition artifact identity: %w", err)
	}
	return identity, nil
}

// ParseCanonical accepts only the exact canonical byte representation. It is
// used on every durable read so database metadata alone never establishes
// trust in an admission.
func ParseCanonical(document []byte) (Admission, error) {
	if len(document) == 0 || len(document) > MaxCanonicalBytes {
		return Admission{}, errors.New("OCI artifact admission bytes are invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var admission Admission
	if err := decoder.Decode(&admission); err != nil {
		return Admission{}, fmt.Errorf("decode OCI artifact admission: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Admission{}, errors.New("OCI artifact admission contains trailing data")
	}
	canonical, err := admission.CanonicalJSON()
	if err != nil {
		return Admission{}, err
	}
	if !bytes.Equal(document, canonical) {
		return Admission{}, errors.New("OCI artifact admission bytes are not canonical")
	}
	return admission, nil
}

// ValidateReference accepts only a repository pinned directly by SHA-256.
// A tag+digest reference remains byte-addressable, but the mutable tag is not
// part of this authority's identity contract and is therefore rejected.
func ValidateReference(value string) error {
	if err := ociref.ValidateImmutable(value); err != nil {
		return err
	}
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return err
	}
	if _, tagged := named.(reference.Tagged); tagged {
		return errors.New("image must not include a mutable tag")
	}
	return nil
}

func (a Admission) normalized() (Admission, error) {
	if a.Version != AdmissionVersion {
		return Admission{}, errors.New("unsupported OCI artifact admission version")
	}
	if a.Decision != DecisionAdmitted {
		return Admission{}, errors.New("OCI artifact was not admitted")
	}
	for _, field := range []struct{ name, value string }{
		{"release id", a.Release.ReleaseID}, {"release version", a.Release.Version},
		{"source revision", a.Release.SourceRevision}, {"distribution", a.Release.Distribution},
		{"platform", a.Release.Platform}, {"architecture marker", a.ArchitectureMarker},
		{"repository", a.Repository}, {"provenance repository", a.Provenance.Repository},
		{"provenance workflow", a.Provenance.Workflow}, {"provenance source revision", a.Provenance.SourceRevision},
		{"SBOM predicate type", a.SBOM.PredicateType}, {"SBOM producer", a.SBOM.Producer},
		{"security scanner", a.SecurityPolicy.Scanner},
	} {
		if err := canonicalText(field.value); err != nil {
			return Admission{}, fmt.Errorf("OCI artifact admission %s: %w", field.name, err)
		}
	}
	if err := ValidateReference(a.Release.Image); err != nil {
		return Admission{}, fmt.Errorf("OCI artifact admission image: %w", err)
	}
	named, err := reference.ParseNormalizedNamed(a.Release.Image)
	if err != nil {
		return Admission{}, fmt.Errorf("OCI artifact admission image: %w", err)
	}
	if named.String() != a.Release.Image {
		return Admission{}, errors.New("OCI artifact admission image is not a canonical repository reference")
	}
	digested, ok := named.(reference.Digested)
	if !ok {
		return Admission{}, errors.New("OCI artifact admission image must be digest pinned")
	}
	if got := reference.TrimNamed(named).String(); got != a.Repository {
		return Admission{}, errors.New("OCI artifact admission repository does not match image")
	}
	if got := digested.Digest().String(); got != a.OCIDigest {
		return Admission{}, errors.New("OCI artifact admission digest does not match image")
	}
	for _, field := range []struct{ name, value string }{
		{"OCI digest", a.OCIDigest}, {"provenance reference", a.Provenance.Reference},
		{"SBOM reference", a.SBOM.Reference}, {"security policy reference", a.SecurityPolicy.Reference},
	} {
		if err := platformdigest.ValidateSHA256Identity(field.value); err != nil {
			return Admission{}, fmt.Errorf("OCI artifact admission %s: %w", field.name, err)
		}
	}
	if a.Provenance.Repository != SourceRepository || !approvedWorkflow(a.Provenance.Workflow) {
		return Admission{}, errors.New("OCI artifact admission provenance authority is not approved")
	}
	if !sourceRevisionPattern.MatchString(a.Release.SourceRevision) ||
		a.Provenance.SourceRevision != a.Release.SourceRevision {
		return Admission{}, errors.New("OCI artifact admission provenance is not verified for the release")
	}
	if a.SBOM.PredicateType != SBOMPredicateSPDX || a.SBOM.Producer != SBOMProducerBuildx {
		return Admission{}, errors.New("OCI artifact admission SBOM evidence is unsupported")
	}
	if a.SecurityPolicy.Version != SecurityPolicyVersion || a.SecurityPolicy.Scanner != SecurityScannerTrivy {
		return Admission{}, errors.New("OCI artifact admission security policy is unsupported or failed")
	}
	if a.AdmittedAt.IsZero() {
		return Admission{}, errors.New("OCI artifact admission timestamp is missing")
	}
	normalized := a
	// Result flags are an output of the validated evidence profile. Callers do
	// not get to establish trust by setting these booleans themselves.
	normalized.Provenance.Verified = true
	normalized.SBOM.Verified = true
	normalized.SecurityPolicy.Passed = true
	// PostgreSQL timestamptz persists microsecond precision. Canonicalize to
	// that boundary before hashing so durable readback cannot change identity.
	normalized.AdmittedAt = a.AdmittedAt.UTC().Round(0).Truncate(time.Microsecond)
	return normalized, nil
}

func approvedWorkflow(value string) bool {
	switch value {
	case "flidai/leapview/.github/workflows/artifacts.yml",
		"flidai/leapview/.github/workflows/release.yml",
		"flidai/leapview/.github/workflows/site-image.yml":
		return true
	default:
		return false
	}
}

func canonicalText(value string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 2048 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return errors.New("text is not canonical")
	}
	return nil
}
