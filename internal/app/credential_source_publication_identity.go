package app

import (
	"crypto/sha256"

	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/google/uuid"
)

// Delivery uses the publication identity for its canonical activation event.
// Retain the verified server generation's UUIDv7 timestamp, and derive the
// remaining bits from the exact credential operation and generation so retries
// retain one domain-separated publication and immutable event identity.
func sourceCredentialPublicationID(operationID string, generationID uuid.UUID) (string, error) {
	operation, err := uuid.Parse(operationID)
	if err != nil || operation == uuid.Nil || operation.String() != operationID {
		return "", credentialmodule.ErrValidationConflict
	}
	if generationID == uuid.Nil || generationID.Version() != 7 || generationID.Variant() != uuid.RFC4122 {
		return "", credentialmodule.ErrValidationConflict
	}
	digest := sha256.Sum256([]byte("leapview/source-credential-publication/" + operationID + "/" + generationID.String()))
	id := generationID
	copy(id[6:], digest[:10])
	id[6] = id[6]&0x0f | 0x70
	id[8] = id[8]&0x3f | 0x80
	return id.String(), nil
}
