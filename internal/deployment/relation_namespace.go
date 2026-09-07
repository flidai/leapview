package deployment

import (
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
)

// MaxRelationNamespaceBytes is PostgreSQL's maximum identifier length in
// bytes. Relation namespaces are emitted at this bound so they can be used as
// unquoted SQL identifiers without backend-specific truncation.
const MaxRelationNamespaceBytes = 63

// RelationNamespaceInput is the complete immutable identity of one physical
// build relation namespace. CandidateID and AttemptID must be canonical UUID
// strings, and FencingEpoch must be positive.
type RelationNamespaceInput struct {
	CandidateID  string
	AttemptID    string
	FencingEpoch int64
}

// DeriveRelationNamespace deterministically maps one candidate, attempt, and
// writer-fence tuple to a bounded SQL identifier. The binary tuple is encoded
// as a fixed-width lowercase base-36 value with an underscore prefix. The
// representation is injective for all canonical UUIDs and positive int64
// fencing epochs, so successor attempts and fences cannot share a namespace.
func DeriveRelationNamespace(input RelationNamespaceInput) (string, error) {
	candidate, err := canonicalRelationUUID(input.CandidateID, "candidate id")
	if err != nil {
		return "", err
	}
	attempt, err := canonicalRelationUUID(input.AttemptID, "attempt id")
	if err != nil {
		return "", err
	}
	if input.FencingEpoch <= 0 {
		return "", fmt.Errorf("relation namespace fencing epoch must be positive")
	}

	// UUIDs contribute 256 bits and the positive int64 fence contributes at
	// most 63 bits. A 62-character base-36 value has more than 319 bits of
	// capacity; the fixed width preserves leading zeroes and therefore keeps
	// the encoding one-to-one. The prefix makes every result a legal unquoted
	// SQL identifier (and is included within PostgreSQL's 63-byte limit).
	var tuple [40]byte
	copy(tuple[:16], candidate[:])
	copy(tuple[16:32], attempt[:])
	binary.BigEndian.PutUint64(tuple[32:], uint64(input.FencingEpoch))
	encoded := new(big.Int).SetBytes(tuple[:]).Text(36)
	if len(encoded) > MaxRelationNamespaceBytes-1 {
		// This is unreachable for the validated input domain, but keeps the
		// output bound explicit if the representation is changed later.
		return "", fmt.Errorf("relation namespace exceeds %d bytes", MaxRelationNamespaceBytes)
	}
	encoded = strings.Repeat("0", MaxRelationNamespaceBytes-1-len(encoded)) + encoded
	return "_" + encoded, nil
}

func canonicalRelationUUID(value, label string) (uuid.UUID, error) {
	if value == "" || value != strings.TrimSpace(value) {
		return uuid.Nil, fmt.Errorf("%s must be a canonical UUID", label)
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return uuid.Nil, fmt.Errorf("%s must be a canonical UUID", label)
	}
	return parsed, nil
}
