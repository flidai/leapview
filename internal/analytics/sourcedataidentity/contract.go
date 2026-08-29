// Package sourcedataidentity defines stable, engine-independent evidence that
// two reads of a project Source are equivalent for analytical result reuse.
package sourcedataidentity

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const (
	// EvidenceVersion is the canonical source-data evidence serialization
	// version.
	EvidenceVersion = 1

	// EvidenceDigestDomain separates source-data equivalence identities from
	// other SHA-256 identities. The preimage is this domain, one NUL byte, and
	// the canonical evidence serialization.
	EvidenceDigestDomain = "flid.sourcedataidentity.evidence.v1"
)

// ErrInvalidEvidence indicates source-data evidence that cannot be assigned a
// complete, collision-resistant equivalence identity.
var ErrInvalidEvidence = errors.New("invalid source data identity evidence")

// EvidenceInput supplies an authoritative content revision for one project
// Source. RevisionDigest must already be a canonical SHA-256 identity; this
// contract never invents a fallback from paths, timestamps, or configuration.
type EvidenceInput struct {
	SourceID       projectgraph.ResourceID
	RevisionDigest string
}

// Evidence is immutable, opaque source-data equivalence evidence. The zero
// value is unavailable and cannot authorize result reuse.
type Evidence struct {
	sourceID          projectgraph.ResourceID
	canonical         []byte
	equivalenceDigest string
}

// NewEvidence validates, canonically serializes, and hashes authoritative
// source revision evidence.
func NewEvidence(input EvidenceInput) (Evidence, error) {
	if err := input.SourceID.Validate(); err != nil {
		return Evidence{}, fmt.Errorf("%w: source ID: %v", ErrInvalidEvidence, err)
	}
	if err := platformdigest.ValidateSHA256Identity(input.RevisionDigest); err != nil {
		return Evidence{}, fmt.Errorf("%w: revision digest: %v", ErrInvalidEvidence, err)
	}

	canonical, err := json.Marshal(evidenceWire{
		Version: EvidenceVersion, SourceID: input.SourceID.String(), RevisionDigest: input.RevisionDigest,
	})
	if err != nil {
		return Evidence{}, fmt.Errorf("%w: serialize: %v", ErrInvalidEvidence, err)
	}
	capacity, err := evidencePreimageCapacity(len(canonical))
	if err != nil {
		return Evidence{}, err
	}
	preimage := make([]byte, 0, capacity)
	preimage = append(preimage, EvidenceDigestDomain...)
	preimage = append(preimage, 0)
	preimage = append(preimage, canonical...)
	digest := sha256.Sum256(preimage)

	return Evidence{
		sourceID: input.SourceID, canonical: append([]byte(nil), canonical...),
		equivalenceDigest: fmt.Sprintf("sha256:%x", digest),
	}, nil
}

// Available reports whether this value contains validated source-data
// evidence. It is deliberately false for the zero value.
func (e Evidence) Available() bool {
	return e.sourceID.Valid() && len(e.canonical) > 0 && e.equivalenceDigest != ""
}

// Version returns the canonical serialization version, or zero for an
// unavailable value.
func (e Evidence) Version() int {
	if !e.Available() {
		return 0
	}
	return EvidenceVersion
}

// SourceID returns the stable project Source identity.
func (e Evidence) SourceID() projectgraph.ResourceID { return e.sourceID }

// Canonical returns a defensive copy of the versioned canonical evidence.
func (e Evidence) Canonical() []byte { return append([]byte(nil), e.canonical...) }

// EquivalenceDigest returns the canonical, domain-separated SHA-256 identity,
// or an empty string when evidence is unavailable.
func (e Evidence) EquivalenceDigest() string { return e.equivalenceDigest }

type evidenceWire struct {
	Version        int    `json:"version"`
	SourceID       string `json:"sourceId"`
	RevisionDigest string `json:"revisionDigest"`
}

func evidencePreimageCapacity(canonicalLength int) (int, error) {
	maximumInt := int(^uint(0) >> 1)
	domainLength := len(EvidenceDigestDomain)
	if canonicalLength < 0 || domainLength > maximumInt-1 {
		return 0, fmt.Errorf("%w: evidence preimage size cannot be represented", ErrInvalidEvidence)
	}
	overhead := domainLength + 1
	if canonicalLength > maximumInt-overhead {
		return 0, fmt.Errorf("%w: evidence preimage size cannot be represented", ErrInvalidEvidence)
	}
	return overhead + canonicalLength, nil
}
