package trustedclaims

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/semanticvalue"
)

// SourceKind is the closed set of authentication evidence sources accepted by
// the trusted-claim boundary. Values are deliberately lower-case and exact;
// aliases and request transport names are not accepted.
type SourceKind string

const MaxClaimNameBytes = 1024

const (
	SourceSAML         SourceKind = "saml"
	SourceOIDC         SourceKind = "oidc"
	SourceEmbed        SourceKind = "embed"
	SourceServiceToken SourceKind = "service_token"
)

const (
	SourceKindSAML         = SourceSAML
	SourceKindOIDC         = SourceOIDC
	SourceKindEmbed        = SourceEmbed
	SourceKindServiceToken = SourceServiceToken
)

// Short aliases make the source vocabulary convenient without introducing a
// second set of wire values.
const (
	SAML         = SourceSAML
	OIDC         = SourceOIDC
	Embed        = SourceEmbed
	ServiceToken = SourceServiceToken
)

// Valid reports whether kind is one of the four supported cryptographic
// evidence sources.
func (kind SourceKind) Valid() bool {
	switch kind {
	case SourceSAML, SourceOIDC, SourceEmbed, SourceServiceToken:
		return true
	default:
		return false
	}
}

var (
	ErrInvalidEvidence      = errors.New("invalid trusted claim evidence")
	ErrUnsupportedSource    = errors.New("unsupported trusted claim source")
	ErrVerifierUnavailable  = errors.New("trusted claim verifier is unavailable")
	ErrVerificationFailed   = errors.New("trusted claim cryptographic verification failed")
	ErrTrustIdentityMissing = errors.New("trusted claim trust identity is missing")
	ErrClaimInvalid         = errors.New("trusted claim is invalid")
	ErrClaimNameInvalid     = errors.New("trusted claim name is invalid")
	ErrClaimDuplicate       = errors.New("trusted claim name is duplicated")
	ErrEvidenceExpired      = errors.New("trusted claim evidence is expired")
	ErrEvidenceNotYetValid  = errors.New("trusted claim evidence is not yet valid")
	ErrEvidenceTimeInvalid  = errors.New("trusted claim evidence time is invalid")
	ErrRawEvidenceEmpty     = errors.New("trusted claim raw evidence is empty")
	ErrRawEvidenceMutated   = errors.New("trusted claim verifier mutated raw evidence")
)

// RawEvidence is unverified source evidence. It can only be consumed by
// Verify; no admission API accepts this type. Verify copies Raw before
// invoking the verifier and checks that the verifier did not mutate it.
type RawEvidence struct {
	Source SourceKind
	Raw    []byte
}

// Evidence is the concise alias used by adapters at call sites.
type Evidence = RawEvidence

// NewRawEvidence creates a raw evidence value while copying the caller's
// bytes. Verify still copies again because a caller can mutate a slice after
// construction and before verification.
func NewRawEvidence(source SourceKind, raw []byte) RawEvidence {
	return RawEvidence{Source: source, Raw: append([]byte(nil), raw...)}
}

// VerifiedClaims is the authenticated output of a source-specific
// cryptographic verifier. It is intentionally not the admission envelope:
// callers can construct this result, but only Verify can turn it into an
// Envelope after validating every field and value.
//
// Claims use an ordered slice rather than a map so exact top-level names are
// retained and duplicate names can be rejected. Values are limited by Verify
// to semanticvalue-compatible scalars or one-dimensional homogeneous lists;
// admission remains responsible for logical-type canonicalization.
type VerifiedClaims struct {
	Provider              string
	Issuer                string
	Audience              string
	Subject               string
	IssuedAt              time.Time
	ExpiresAt             time.Time
	CredentialFingerprint string
	TokenFingerprint      string
	Claims                []Claim
}

// Verification is an alias for the authenticated verifier result. It is not
// an admission value; only Verify can produce an Envelope from it.
type Verification = VerifiedClaims

// Claim is one exact top-level claim. Name is never trimmed, folded, or
// canonicalized. Value is copied into the verified envelope and can only be a
// supported scalar or homogeneous list.
type Claim struct {
	Name  string
	Value any
}

// CryptographicVerifier authenticates raw evidence for one source. The
// implementation owns signature, issuer, audience, nonce, key, and token
// validation appropriate to its source. Verify binds the returned metadata to
// the requested source and applies the common structural/time boundary.
type CryptographicVerifier interface {
	Verify(context.Context, []byte) (VerifiedClaims, error)
}

// SourceVerifier associates a cryptographic verifier with exactly one source.
// A verifier must not be reused for another source: Verify rejects a source
// mismatch before calling it.
type SourceVerifier interface {
	SourceKind() SourceKind
	CryptographicVerifier
}

// Verifier is the primary name for the source-bound verifier contract.
type Verifier = SourceVerifier

// VerifyOptions controls the common temporal check. A zero Now uses the UTC
// wall clock. ClockSkew is intentionally not supported: accepting evidence
// outside its exact issued/expiry interval is an authorization policy decision
// that belongs to the source adapter or admission layer.
type VerifyOptions struct {
	Now time.Time
}

// Verify authenticates and structurally validates one source's raw evidence,
// returning the only value accepted by trusted-claim admission. The returned
// Envelope contains no raw evidence and cannot be constructed by a caller.
func Verify(ctx context.Context, evidence RawEvidence, verifier SourceVerifier, options ...VerifyOptions) (Envelope, error) {
	if ctx == nil {
		return Envelope{}, fmt.Errorf("%w: nil context", ErrInvalidEvidence)
	}
	if !evidence.Source.Valid() {
		if evidence.Source == "" {
			return Envelope{}, fmt.Errorf("%w: source is required", ErrInvalidEvidence)
		}
		return Envelope{}, fmt.Errorf("%w: %q", ErrUnsupportedSource, evidence.Source)
	}
	if len(evidence.Raw) == 0 {
		return Envelope{}, ErrRawEvidenceEmpty
	}
	if verifier == nil || isNilInterface(verifier) {
		return Envelope{}, ErrVerifierUnavailable
	}
	if verifier.SourceKind() != evidence.Source {
		return Envelope{}, fmt.Errorf("%w: evidence source %q does not match verifier source %q", ErrInvalidEvidence, evidence.Source, verifier.SourceKind())
	}

	now := time.Now().UTC()
	if len(options) > 0 && !options[0].Now.IsZero() {
		now = options[0].Now.UTC()
	}
	if len(options) > 1 {
		return Envelope{}, fmt.Errorf("%w: at most one options value is accepted", ErrInvalidEvidence)
	}

	raw := append([]byte(nil), evidence.Raw...)
	verified, err := verifier.Verify(ctx, raw)
	if err != nil {
		return Envelope{}, fmt.Errorf("%w: %w", ErrVerificationFailed, err)
	}
	if !bytes.Equal(raw, evidence.Raw) {
		return Envelope{}, ErrRawEvidenceMutated
	}
	if err := validateVerifiedClaims(verified, evidence.Source, now); err != nil {
		return Envelope{}, err
	}

	claims := make([]storedClaim, len(verified.Claims))
	for index, claim := range verified.Claims {
		value, err := copyClaimValue(claim.Value)
		if err != nil {
			return Envelope{}, fmt.Errorf("%w: claim %q: %v", ErrClaimInvalid, claim.Name, err)
		}
		claims[index] = storedClaim{name: claim.Name, value: value}
	}

	return Envelope{
		source:                evidence.Source,
		provider:              verified.Provider,
		issuer:                verified.Issuer,
		audience:              verified.Audience,
		subject:               verified.Subject,
		issuedAt:              verified.IssuedAt.UTC(),
		expiresAt:             verified.ExpiresAt.UTC(),
		credentialFingerprint: verified.CredentialFingerprint,
		tokenFingerprint:      verified.TokenFingerprint,
		claims:                claims,
	}, nil
}

func validateVerifiedClaims(claims VerifiedClaims, source SourceKind, now time.Time) error {
	provider := strings.TrimSpace(claims.Provider)
	issuer := strings.TrimSpace(claims.Issuer)
	audience := strings.TrimSpace(claims.Audience)
	subject := strings.TrimSpace(claims.Subject)
	if provider == "" || issuer == "" || audience == "" || subject == "" {
		return fmt.Errorf("%w: provider, issuer, audience, and subject are required", ErrTrustIdentityMissing)
	}
	if provider != claims.Provider || issuer != claims.Issuer || audience != claims.Audience || subject != claims.Subject {
		return fmt.Errorf("%w: provider, issuer, audience, and subject must not contain surrounding whitespace", ErrTrustIdentityMissing)
	}
	if !validIdentity(provider) || !validIdentity(issuer) || !validIdentity(audience) || !validIdentity(subject) {
		return fmt.Errorf("%w: trust identity contains invalid text", ErrTrustIdentityMissing)
	}
	if claims.IssuedAt.IsZero() || claims.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: issued and expiry times are required", ErrTrustIdentityMissing)
	}
	issuedAt := claims.IssuedAt.UTC()
	expiresAt := claims.ExpiresAt.UTC()
	if !expiresAt.After(issuedAt) {
		return fmt.Errorf("%w: expiry must be after issued time", ErrEvidenceTimeInvalid)
	}
	if now.Before(issuedAt) {
		return fmt.Errorf("%w: issued at %s", ErrEvidenceNotYetValid, issuedAt.Format(time.RFC3339Nano))
	}
	if !now.Before(expiresAt) {
		return fmt.Errorf("%w: expired at %s", ErrEvidenceExpired, expiresAt.Format(time.RFC3339Nano))
	}

	if claims.CredentialFingerprint == "" && claims.TokenFingerprint == "" {
		return fmt.Errorf("%w: credential or token fingerprint is required", ErrTrustIdentityMissing)
	}
	if claims.CredentialFingerprint != "" && !validFingerprint(claims.CredentialFingerprint) {
		return fmt.Errorf("%w: credential fingerprint is invalid", ErrTrustIdentityMissing)
	}
	if claims.TokenFingerprint != "" && !validFingerprint(claims.TokenFingerprint) {
		return fmt.Errorf("%w: token fingerprint is invalid", ErrTrustIdentityMissing)
	}
	if source == SourceServiceToken && claims.TokenFingerprint == "" {
		return fmt.Errorf("%w: service_token requires token fingerprint", ErrTrustIdentityMissing)
	}

	seenNames := make(map[string]struct{}, len(claims.Claims))
	for _, claim := range claims.Claims {
		if err := validateClaimName(claim.Name); err != nil {
			return err
		}
		if _, exists := seenNames[claim.Name]; exists {
			return fmt.Errorf("%w: %q", ErrClaimDuplicate, claim.Name)
		}
		seenNames[claim.Name] = struct{}{}
		if err := validateClaimValue(claim.Value); err != nil {
			return fmt.Errorf("%w: claim %q: %v", ErrClaimInvalid, claim.Name, err)
		}
	}
	return nil
}

func validateClaimName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrClaimNameInvalid)
	}
	if name != strings.TrimSpace(name) {
		return fmt.Errorf("%w: name must not contain surrounding whitespace", ErrClaimNameInvalid)
	}
	if len(name) > MaxClaimNameBytes {
		return fmt.Errorf("%w: name exceeds %d bytes", ErrClaimNameInvalid, MaxClaimNameBytes)
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: name is not valid UTF-8", ErrClaimNameInvalid)
	}
	if hasControl(name) {
		return fmt.Errorf("%w: name contains a control character", ErrClaimNameInvalid)
	}
	return nil
}

func validateClaimValue(value any) error {
	if value == nil {
		return errors.New("null is not supported")
	}
	if isScalarValue(value) {
		return nil
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return fmt.Errorf("value of type %T is not a semantic scalar or list", value)
	}
	if rv.Type().Elem().Kind() == reflect.Uint8 {
		return fmt.Errorf("binary value of type %T is not supported", value)
	}
	if rv.Len() == 0 {
		return errors.New("empty lists are not supported")
	}
	if rv.Len() > semanticvalue.MaxSetValues {
		return fmt.Errorf("list exceeds %d values", semanticvalue.MaxSetValues)
	}
	var listKind reflect.Type
	for index := 0; index < rv.Len(); index++ {
		item := rv.Index(index).Interface()
		if item == nil {
			return fmt.Errorf("list item %d is null", index)
		}
		if !isScalarValue(item) {
			return fmt.Errorf("list item %d of type %T is not a supported scalar", index, item)
		}
		itemType := reflect.TypeOf(item)
		if listKind == nil {
			listKind = itemType
		} else if listKind != itemType {
			return fmt.Errorf("list mixes scalar types %s and %s", listKind, itemType)
		}
	}
	return nil
}

func isScalarValue(value any) bool {
	switch typed := value.(type) {
	case string:
		return utf8.ValidString(typed) && !hasControl(typed)
	case bool:
		return true
	case json.Number:
		return validJSONNumber(typed)
	case int, int8, int16, int32, int64:
		return true
	case uint:
		return uint64(typed) <= math.MaxInt64
	case uint8:
		return uint64(typed) <= math.MaxInt64
	case uint16:
		return uint64(typed) <= math.MaxInt64
	case uint32:
		return uint64(typed) <= math.MaxInt64
	case uint64:
		return typed <= math.MaxInt64
	default:
		return false
	}
}

func validJSONNumber(value json.Number) bool {
	if value == "" || !json.Valid([]byte(value)) {
		return false
	}
	first := value[0]
	return first == '-' || first >= '0' && first <= '9'
}

func copyClaimValue(value any) (any, error) {
	if err := validateClaimValue(value); err != nil {
		return nil, err
	}
	if isScalarValue(value) {
		return value, nil
	}
	rv := reflect.ValueOf(value)
	copy := make([]any, rv.Len())
	for index := range copy {
		item, err := copyClaimValue(rv.Index(index).Interface())
		if err != nil {
			return nil, err
		}
		copy[index] = item
	}
	return copy, nil
}

func validFingerprint(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || hasControl(value) {
		return false
	}
	if len(value) == 64 {
		return lowerHex(value)
	}
	return len(value) == len("sha256:")+64 && strings.HasPrefix(value, "sha256:") && lowerHex(value[len("sha256:"):])
}

func lowerHex(value string) bool {
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validIdentity(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) && !hasControl(value) && len(value) <= 1024
}

func hasControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

type storedClaim struct {
	name  string
	value any
}

// Envelope is an immutable, verifier-gated trusted-claim envelope. Its
// fields are private by design: callers cannot forge or mutate a verified
// value. Use Verify to produce one and the accessors below to inspect it.
type Envelope struct {
	source                SourceKind
	provider              string
	issuer                string
	audience              string
	subject               string
	issuedAt              time.Time
	expiresAt             time.Time
	credentialFingerprint string
	tokenFingerprint      string
	claims                []storedClaim
}

func (envelope Envelope) Valid() bool                   { return envelope.source.Valid() }
func (envelope Envelope) Source() SourceKind            { return envelope.source }
func (envelope Envelope) SourceKind() SourceKind        { return envelope.source }
func (envelope Envelope) Provider() string              { return envelope.provider }
func (envelope Envelope) Issuer() string                { return envelope.issuer }
func (envelope Envelope) Audience() string              { return envelope.audience }
func (envelope Envelope) Subject() string               { return envelope.subject }
func (envelope Envelope) IssuedAt() time.Time           { return envelope.issuedAt }
func (envelope Envelope) ExpiresAt() time.Time          { return envelope.expiresAt }
func (envelope Envelope) CredentialFingerprint() string { return envelope.credentialFingerprint }
func (envelope Envelope) TokenFingerprint() string      { return envelope.tokenFingerprint }

// Fingerprint returns the first verifier-bound credential or token identity.
// Callers that need to distinguish both can use the explicit accessors above.
func (envelope Envelope) Fingerprint() string {
	if envelope.credentialFingerprint != "" {
		return envelope.credentialFingerprint
	}
	return envelope.tokenFingerprint
}

// Claims returns a deep copy preserving verifier order and exact names.
func (envelope Envelope) Claims() []Claim {
	claims := make([]Claim, len(envelope.claims))
	for index, claim := range envelope.claims {
		value, err := copyClaimValue(claim.value)
		if err != nil {
			// Values were validated before entering the envelope. Returning nil
			// here is defensive in case package internals are changed later.
			return nil
		}
		claims[index] = Claim{Name: claim.name, Value: value}
	}
	return claims
}

// ClaimNames returns a copy of the exact top-level claim names.
func (envelope Envelope) ClaimNames() []string {
	names := make([]string, len(envelope.claims))
	for index, claim := range envelope.claims {
		names[index] = claim.name
	}
	return names
}

// Claim returns a copied claim by exact, case-sensitive name. The name is not
// normalized, so callers cannot accidentally turn a different spelling into a
// trusted match.
func (envelope Envelope) Claim(name string) (Claim, bool) {
	for _, claim := range envelope.claims {
		if claim.name != name {
			continue
		}
		value, err := copyClaimValue(claim.value)
		if err != nil {
			return Claim{}, false
		}
		return Claim{Name: claim.name, Value: value}, true
	}
	return Claim{}, false
}

// Value returns a copied raw value by exact, case-sensitive claim name. The
// returned value is intended for the admission repository's canonicalizer and
// is never canonicalized here.
func (envelope Envelope) Value(name string) (any, bool) {
	claim, ok := envelope.Claim(name)
	if !ok {
		return nil, false
	}
	return claim.Value, true
}

// HasClaim reports whether an exact top-level claim exists.
func (envelope Envelope) HasClaim(name string) bool {
	for _, claim := range envelope.claims {
		if claim.name == name {
			return true
		}
	}
	return false
}

// NotBefore and NotAfter aliases make temporal checks readable at consumers
// while retaining the exact issued/expiry vocabulary in the contract.
func (envelope Envelope) NotBefore() time.Time { return envelope.issuedAt }
func (envelope Envelope) NotAfter() time.Time  { return envelope.expiresAt }
