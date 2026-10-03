package encryption

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxPlaintextSize int   = 16 * 1024
	MaxEncryptions   int64 = 1 << 31
	envelopeFormat         = "aes-256-gcm-random-nonce-v1"
)

var (
	ErrBudgetExhausted = errors.New("credential encryption budget exhausted")
	errInvalidBinding  = errors.New("credential binding is invalid")
	errInvalidEnvelope = errors.New("credential envelope is invalid")
	errAuthentication  = errors.New("credential ciphertext authentication failed")
	errReservation     = errors.New("credential encryption reservation failed")
)

// Binding is the authoritative identity authenticated with a credential value.
type Binding struct {
	DeploymentID string
	OwnerID      string
	ScopeKind    string
	TargetID     string
	ProjectID    string
	Environment  string
	ResourceID   string
	Purpose      string
	Provider     string
	Destination  string
	VersionID    string
}

// Validate checks the canonical fields required for each supported credential scope.
func (b Binding) Validate() error {
	if !canonicalValue(b.DeploymentID) || !canonicalValue(b.OwnerID) ||
		!canonicalValue(b.Purpose) || !canonicalValue(b.Provider) ||
		!canonicalValue(b.Destination) || !canonicalValue(b.VersionID) {
		return errInvalidBinding
	}
	switch b.ScopeKind {
	case "connection":
		if !canonicalValue(b.TargetID) || !canonicalValue(b.ProjectID) ||
			!canonicalValue(b.Environment) || !canonicalValue(b.ResourceID) {
			return errInvalidBinding
		}
	case "agent":
		if b.TargetID != "" || b.ProjectID != "" || b.Environment != "" || !canonicalValue(b.ResourceID) {
			return errInvalidBinding
		}
	default:
		return errInvalidBinding
	}
	return nil
}

// Envelope stores nonce-prefixed ciphertext and its encryption metadata.
type Envelope struct {
	Format     string `json:"format"`
	KeyID      string `json:"key_id"`
	Ciphertext []byte `json:"-"`
}

// MarshalJSON deliberately omits ciphertext from generic JSON serialization.
// Persistence adapters should store the envelope columns explicitly.
func (e Envelope) MarshalJSON() ([]byte, error) {
	type envelopeMetadata struct {
		Format string `json:"format"`
		KeyID  string `json:"key_id"`
	}
	return json.Marshal(envelopeMetadata{Format: e.Format, KeyID: e.KeyID})
}

// String avoids dumping ciphertext bytes when an envelope is formatted for logs.
func (e Envelope) String() string {
	return fmt.Sprintf("credential envelope format=%q key_id=%q ciphertext_bytes=%d", e.Format, e.KeyID, len(e.Ciphertext))
}

// GoString avoids dumping ciphertext bytes in %#v formatting.
func (e Envelope) GoString() string { return e.String() }

// Budget reserves one irreversible encryption use before encryption begins.
type Budget interface {
	ReserveEncryption(ctx context.Context, deploymentID, keyID string, commitment KeyCommitment) error
}

// Encrypt reserves a key use and encrypts plaintext with its exact binding.
func (k *Keyring) Encrypt(ctx context.Context, budget Budget, binding Binding, plaintext []byte) (Envelope, error) {
	if k == nil || budget == nil || binding.Validate() != nil || binding.DeploymentID != k.deploymentID || len(plaintext) > MaxPlaintextSize {
		return Envelope{}, errInvalidEnvelope
	}
	key, ok := k.keys[k.activeWriteKey]
	if !ok {
		return Envelope{}, errInvalidEnvelope
	}
	commitment, ok := k.KeyCommitment(k.activeWriteKey)
	if !ok {
		return Envelope{}, errInvalidEnvelope
	}
	if err := budget.ReserveEncryption(ctx, k.deploymentID, k.activeWriteKey, commitment); err != nil {
		if errors.Is(err, ErrBudgetExhausted) {
			return Envelope{}, ErrBudgetExhausted
		}
		return Envelope{}, errReservation
	}

	block, err := aes.NewCipher(key[:])
	if err != nil {
		return Envelope{}, errInvalidEnvelope
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return Envelope{}, errInvalidEnvelope
	}
	aad, err := associatedData(envelopeFormat, binding, k.activeWriteKey)
	if err != nil {
		return Envelope{}, errInvalidBinding
	}
	ciphertext := aead.Seal(nil, nil, plaintext, aad)
	return Envelope{Format: envelopeFormat, KeyID: k.activeWriteKey, Ciphertext: ciphertext}, nil
}

// Decrypt authenticates the envelope and exact binding before returning plaintext.
func (k *Keyring) Decrypt(binding Binding, envelope Envelope) ([]byte, error) {
	if k == nil || binding.Validate() != nil || binding.DeploymentID != k.deploymentID || envelope.Format != envelopeFormat || !canonicalValue(envelope.KeyID) || len(envelope.Ciphertext) < 28 || len(envelope.Ciphertext) > MaxPlaintextSize+28 {
		return nil, errInvalidEnvelope
	}
	key, ok := k.keys[envelope.KeyID]
	if !ok {
		return nil, errInvalidEnvelope
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, errInvalidEnvelope
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, errInvalidEnvelope
	}
	aad, err := associatedData(envelope.Format, binding, envelope.KeyID)
	if err != nil {
		return nil, errInvalidBinding
	}
	plaintext, err := aead.Open(nil, nil, envelope.Ciphertext, aad)
	if err != nil || len(plaintext) > MaxPlaintextSize {
		return nil, errAuthentication
	}
	return plaintext, nil
}

func associatedData(format string, binding Binding, keyID string) ([]byte, error) {
	values := [...]string{
		format,
		binding.DeploymentID,
		binding.OwnerID,
		binding.ScopeKind,
		binding.TargetID,
		binding.ProjectID,
		binding.Environment,
		binding.ResourceID,
		binding.Purpose,
		binding.Provider,
		binding.Destination,
		binding.VersionID,
		keyID,
	}
	var out []byte
	for _, value := range values {
		if uint64(len(value)) > uint64(^uint32(0)) {
			return nil, errInvalidBinding
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		out = append(out, length[:]...)
		out = append(out, value...)
	}
	return out, nil
}

func canonicalValue(value string) bool {
	if value == "" || len(value) > 255 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
