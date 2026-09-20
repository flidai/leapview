package access

import (
	"errors"
	"fmt"
	"time"
)

// Credential expiry is finite by policy. These values intentionally mirror the
// bounds enforced by the PostgreSQL access schema while keeping defaults and
// validation at the domain boundary rather than in a database adapter.
const (
	APITokenDefaultLifetime               = 90 * 24 * time.Hour
	APITokenMaxLifetime                   = 365 * 24 * time.Hour
	ServicePrincipalSecretDefaultLifetime = 180 * 24 * time.Hour
	ServicePrincipalSecretMaxLifetime     = 365 * 24 * time.Hour
)

var (
	ErrCredentialExpiryInPast = errors.New("credential expiry must be in the future")
	ErrCredentialExpiryTooFar = errors.New("credential expiry exceeds maximum lifetime")
)

// ResolveAPITokenExpiry applies the personal API-token expiry policy. An
// omitted expiry receives the finite default; an explicit expiry must be in
// the future and no farther than the finite maximum from now.
func ResolveAPITokenExpiry(expiresAt, now time.Time) (time.Time, error) {
	return resolveCredentialExpiry("api token", expiresAt, now, APITokenDefaultLifetime, APITokenMaxLifetime)
}

// ResolveServicePrincipalSecretExpiry applies the service-principal secret
// expiry policy. An omitted expiry receives the finite default; an explicit
// expiry must be in the future and no farther than the finite maximum from
// now.
func ResolveServicePrincipalSecretExpiry(expiresAt, now time.Time) (time.Time, error) {
	return resolveCredentialExpiry("service principal secret", expiresAt, now, ServicePrincipalSecretDefaultLifetime, ServicePrincipalSecretMaxLifetime)
}

func resolveCredentialExpiry(label string, expiresAt, now time.Time, defaultLifetime, maxLifetime time.Duration) (time.Time, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if expiresAt.IsZero() {
		return now.Add(defaultLifetime), nil
	}
	expiresAt = expiresAt.UTC()
	if !expiresAt.After(now) {
		return time.Time{}, fmt.Errorf("%s expiry must be in the future: %w", label, ErrCredentialExpiryInPast)
	}
	if expiresAt.After(now.Add(maxLifetime)) {
		return time.Time{}, fmt.Errorf("%s expiry exceeds maximum lifetime of %s: %w", label, maxLifetime, ErrCredentialExpiryTooFar)
	}
	return expiresAt, nil
}
