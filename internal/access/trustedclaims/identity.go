package trustedclaims

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The source identity limits are part of the trusted-claim contract. They
// bound the exact values used to select a durable mapping and must therefore
// be shared by verifier and admission/repository callers.
const (
	MaxProviderBytes = 128
	MaxIssuerBytes   = 1024
	MaxAudienceBytes = 512
	MaxSubjectBytes  = 1024
)

// ValidateSourceIdentity validates the exact provider, issuer, and audience
// tuple used to identify a trusted claim source. Values are never trimmed or
// normalized: a caller must provide the canonical identity it intends to
// match. Limits are measured in bytes, as they are for persisted text.
func ValidateSourceIdentity(provider, issuer, audience string) error {
	if err := ValidateProvider(provider); err != nil {
		return err
	}
	if err := ValidateIssuer(issuer); err != nil {
		return err
	}
	if err := ValidateAudience(audience); err != nil {
		return err
	}
	return nil
}

// ValidateProvider validates one trusted source provider identifier. It is
// also useful to validate optional provider filters without treating an empty
// filter as a complete source identity.
func ValidateProvider(provider string) error {
	return validateIdentity("provider", provider, MaxProviderBytes)
}

// ValidateIssuer validates one trusted source issuer identifier.
func ValidateIssuer(issuer string) error {
	return validateIdentity("issuer", issuer, MaxIssuerBytes)
}

// ValidateAudience validates one trusted source audience identifier.
func ValidateAudience(audience string) error {
	return validateIdentity("audience", audience, MaxAudienceBytes)
}

// ValidateSubjectIdentity validates the subject returned by a trusted
// evidence verifier. Subjects use the existing verified-identity limit and
// the same exact-text rules as source identity fields.
func ValidateSubjectIdentity(subject string) error {
	return validateIdentity("subject", subject, MaxSubjectBytes)
}

func validateIdentity(label, value string, maxBytes int) error {
	if value == "" {
		return fmt.Errorf("%w: %s is required", ErrTrustIdentityMissing, label)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%w: %s must not contain surrounding whitespace", ErrTrustIdentityMissing, label)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not valid UTF-8", ErrTrustIdentityMissing, label)
	}
	if hasControl(value) {
		return fmt.Errorf("%w: %s contains a control character", ErrTrustIdentityMissing, label)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrTrustIdentityMissing, label, maxBytes)
	}
	return nil
}

func hasControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
