package module

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
)

// These aliases keep the refresh module's public contract stable while the
// value types live in the authority package. Keeping the contract there also
// lets process-composition adapters import it without creating a module test
// import cycle.
var (
	ErrPublicationIdentityUnavailable = refreshpostgres.ErrPublicationIdentityUnavailable
	ErrPublicationIdentityMismatch    = refreshpostgres.ErrPublicationIdentityMismatch
)

type PostgresPublicationIdentity = refreshpostgres.PostgresPublicationIdentity
type PostgresPublicationIdentityRequest = refreshpostgres.PostgresPublicationIdentityRequest
type PostgresPublicationIdentityResolver = refreshpostgres.PostgresPublicationIdentityResolver

// PostgresPublicationIdentityResolverFunc adapts a function to the resolver
// capability, which is useful for composition adapters and focused tests.
type PostgresPublicationIdentityResolverFunc func(context.Context, refreshpostgres.Tx, PostgresPublicationIdentityRequest) (PostgresPublicationIdentity, error)

func (f PostgresPublicationIdentityResolverFunc) ResolvePublicationIdentityTx(ctx context.Context, tx refreshpostgres.Tx, req PostgresPublicationIdentityRequest) (PostgresPublicationIdentity, error) {
	if f == nil {
		return PostgresPublicationIdentity{}, ErrPublicationIdentityUnavailable
	}
	return f(ctx, tx, req)
}

func validatePublicationIdentity(identity PostgresPublicationIdentity) error {
	for label, value := range map[string]string{
		"physical pool id": identity.PhysicalPoolID,
		"catalog id":       identity.CatalogID,
	} {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 255 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fmt.Errorf("%w: %s is not canonical", ErrPublicationIdentityUnavailable, label)
		}
	}
	return nil
}

func resolvePublicationIdentityTx(ctx context.Context, tx refreshpostgres.Tx, resolver PostgresPublicationIdentityResolver, request PostgresPublicationIdentityRequest) (PostgresPublicationIdentity, error) {
	if resolver == nil || tx == nil {
		return PostgresPublicationIdentity{}, ErrPublicationIdentityUnavailable
	}
	identity, err := resolver.ResolvePublicationIdentityTx(ctx, tx, request)
	if err != nil {
		if errors.Is(err, ErrPublicationIdentityUnavailable) || errors.Is(err, ErrPublicationIdentityMismatch) {
			return PostgresPublicationIdentity{}, err
		}
		return PostgresPublicationIdentity{}, fmt.Errorf("%w: %v", ErrPublicationIdentityUnavailable, err)
	}
	if err := validatePublicationIdentity(identity); err != nil {
		return PostgresPublicationIdentity{}, err
	}
	return identity, nil
}

func publicationIdentityMismatchf(format string, args ...any) error {
	// Replay failures remain refresh authority conflicts for existing callers,
	// while also exposing the typed mismatch to callers that need to distinguish
	// a physical-namespace drift from a generic replay conflict.
	return fmt.Errorf("%w: %w: %s", refreshpostgres.ErrConflict, ErrPublicationIdentityMismatch, fmt.Sprintf(format, args...))
}
