package recoveryset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

// Normalize returns a validated copy with child rows in their persistence
// order. Callers should normalize before insertion or equality comparison.
func (s RecoverySet) Normalize() (RecoverySet, error) {
	if err := s.Validate(); err != nil {
		return RecoverySet{}, err
	}
	// PostgreSQL timestamptz stores microsecond precision; canonicalize before
	// hashing/insertion so exact replay does not differ on sub-microsecond
	// caller clock values.
	s.CreatedAt = s.CreatedAt.UTC().Truncate(time.Microsecond)
	if s.ManagedEvidence != nil {
		binding := *s.ManagedEvidence
		s.ManagedEvidence = &binding
	}
	s.sortChildren()
	return s, nil
}

func (s *RecoverySet) sortChildren() {
	s.ClusterPoints = s.CanonicalPoints()
	s.ObjectRoots = append([]ObjectRoot(nil), s.ObjectRoots...)
	sort.Slice(s.ObjectRoots, func(i, j int) bool {
		a, b := s.ObjectRoots[i], s.ObjectRoots[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.URI != b.URI {
			return a.URI < b.URI
		}
		return a.VersionID < b.VersionID
	})
}

// CanonicalJSON and Digest provide a stable evidence identity for audit and
// idempotent replay. Child collections are sorted before encoding.
func (s RecoverySet) CanonicalJSON() ([]byte, error) {
	n, err := s.Normalize()
	if err != nil {
		return nil, err
	}
	return json.Marshal(n)
}

func (s RecoverySet) Digest() (string, error) {
	// Digest covers only the immutable frontier projection. Publication status,
	// fence, audit actor, and creation metadata remain separate record fields.
	n := s
	n.Status, n.FrontierDigest, n.PublishedValidationAttemptID = StatusPrepared, "", ""
	if err := n.Validate(); err != nil {
		return "", err
	}
	n.FenceEpoch, n.AuditIdentity, n.CreatedBy, n.CreatedAt = 0, "", "", time.Time{}
	n.sortChildren()
	b, err := json.Marshal(n)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
