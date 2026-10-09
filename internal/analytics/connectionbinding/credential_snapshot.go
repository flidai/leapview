package connectionbinding

import (
	"fmt"
	"github.com/google/uuid"
	"log/slog"
	"strings"
	"time"
)

// CredentialIdentity names the origin of one credential snapshot. A provider
// version and a locally managed credential version are mutually exclusive.
// Pass and retain it by value so snapshots and pools do not share mutable
// identity state.
type CredentialIdentity struct {
	ProviderVersion     string
	CredentialVersionID string
}

func (identity CredentialIdentity) Validate() error {
	if identity.ProviderVersion != "" && identity.CredentialVersionID == "" {
		if identity.ProviderVersion == strings.TrimSpace(identity.ProviderVersion) {
			return nil
		}
		return fmt.Errorf("%w: provider credential identity must be canonical", ErrInvalidBinding)
	}
	if identity.ProviderVersion == "" && identity.CredentialVersionID != "" {
		parsed, err := uuid.Parse(identity.CredentialVersionID)
		if err == nil && parsed != uuid.Nil && parsed.String() == identity.CredentialVersionID {
			return nil
		}
		return fmt.Errorf("%w: local credential version identity must be a canonical UUID", ErrInvalidBinding)
	}
	return fmt.Errorf("%w: exactly one credential identity origin is required", ErrInvalidBinding)
}

type CredentialSnapshot struct {
	values      map[string]string
	identity    CredentialIdentity
	retrievedAt time.Time
	expiresAt   time.Time
}

func NewCredentialSnapshot(values map[string]string, providerVersion string, retrievedAt, expiresAt time.Time) (CredentialSnapshot, error) {
	providerVersion = strings.TrimSpace(providerVersion)
	return newCredentialSnapshot(values, CredentialIdentity{ProviderVersion: providerVersion}, retrievedAt, expiresAt)
}

// NewLocalCredentialSnapshot associates a secret bundle with one canonical
// locally managed credential version. The identifier is identity metadata; it
// does not authorize or activate the credential.
func NewLocalCredentialSnapshot(values map[string]string, credentialVersionID string, retrievedAt, expiresAt time.Time) (CredentialSnapshot, error) {
	return newCredentialSnapshot(values, CredentialIdentity{CredentialVersionID: credentialVersionID}, retrievedAt, expiresAt)
}

func newCredentialSnapshot(values map[string]string, identity CredentialIdentity, retrievedAt, expiresAt time.Time) (CredentialSnapshot, error) {
	retrievedAt = retrievedAt.UTC()
	expiresAt = expiresAt.UTC()
	if len(values) == 0 || identity.Validate() != nil || retrievedAt.IsZero() ||
		!expiresAt.IsZero() && !expiresAt.After(retrievedAt) {
		return CredentialSnapshot{}, fmt.Errorf("%w: credential fields, identity, and retrieval time are required", ErrInvalidBinding)
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		if !optionKeyPattern.MatchString(key) || value == "" {
			return CredentialSnapshot{}, fmt.Errorf("%w: credential bundle contains an invalid field", ErrInvalidBinding)
		}
		cloned[key] = value
	}
	return CredentialSnapshot{values: cloned, identity: identity, retrievedAt: retrievedAt, expiresAt: expiresAt}, nil
}

// NewNoAuthCredentialSnapshot produces non-secret activation evidence for a
// public binding. It contains no credential values and is consumed only by
// target-pool preparation.
func NewNoAuthCredentialSnapshot(now time.Time) CredentialSnapshot {
	return CredentialSnapshot{identity: CredentialIdentity{ProviderVersion: NoAuthProviderVersion}, retrievedAt: now.UTC()}
}

func (snapshot CredentialSnapshot) Identity() CredentialIdentity { return snapshot.identity }
func (snapshot CredentialSnapshot) ProviderVersion() string      { return snapshot.identity.ProviderVersion }
func (snapshot CredentialSnapshot) ExpiresAt() time.Time         { return snapshot.expiresAt }

func (snapshot CredentialSnapshot) Use(consumer func(map[string]string) error) error {
	if consumer == nil || len(snapshot.values) == 0 {
		return fmt.Errorf("%w: credential snapshot consumer is required", ErrInvalidBinding)
	}
	values := make(map[string]string, len(snapshot.values))
	for key, value := range snapshot.values {
		values[key] = value
	}
	defer clear(values)
	return consumer(values)
}

func (snapshot *CredentialSnapshot) Destroy() {
	if snapshot == nil {
		return
	}
	clear(snapshot.values)
	snapshot.values = nil
	snapshot.identity = CredentialIdentity{}
	snapshot.retrievedAt = time.Time{}
	snapshot.expiresAt = time.Time{}
}

func (CredentialSnapshot) MarshalJSON() ([]byte, error) {
	return nil, ErrCredentialSerialization
}

func (CredentialSnapshot) MarshalYAML() (any, error) {
	return nil, ErrCredentialSerialization
}

func (CredentialSnapshot) String() string { return "<credential-snapshot:redacted>" }
func (CredentialSnapshot) GoString() string {
	return "connectionbinding.CredentialSnapshot{<redacted>}"
}

func (snapshot CredentialSnapshot) LogValue() slog.Value {
	attributes := []slog.Attr{
		slog.String("provider_version", snapshot.identity.ProviderVersion),
		slog.Time("retrieved_at", snapshot.retrievedAt),
	}
	if !snapshot.expiresAt.IsZero() {
		attributes = append(attributes, slog.Time("expires_at", snapshot.expiresAt))
	}
	return slog.GroupValue(attributes...)
}
