package module

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
)

// ErrPublicationIdentityUnavailable indicates that the physical publication
// identity has not been admitted (or could not be resolved) for a mutation.
// Callers must treat this as a hard stop: refresh data is never written with
// an empty or placeholder physical identity.
var ErrPublicationIdentityUnavailable = errors.New("refresh publication identity unavailable")

// ErrPublicationIdentityMismatch indicates that independently verified
// publication evidence names a different physical namespace than the one
// resolved for this transaction.
var ErrPublicationIdentityMismatch = errors.New("refresh publication identity mismatch")

// PostgresPublicationIdentity is the exact physical namespace attached to a
// refresh publication and its data-version provenance.
type PostgresPublicationIdentity struct {
	PhysicalPoolID string
	CatalogID      string
}

// PostgresPublicationIdentityRequest scopes an identity lookup. The resolver
// receives all operation identity available at each call site so it can select
// the exact admitted target rather than relying on process-global state.
type PostgresPublicationIdentityRequest struct {
	ProjectID       string
	Environment     string
	GenerationID    string
	SemanticModelID string
	PipelineID      string
	RunID           string
	SnapshotID      int64
	Source          string
	TargetRevision  int64
}

// PostgresPublicationIdentityResolver resolves the exact physical namespace
// while the caller-owned refresh authority transaction is open. Implementations
// must read/admit identity through tx; the transaction is retained by the
// repository until the complete mutation commits or rolls back.
type PostgresPublicationIdentityResolver interface {
	ResolvePublicationIdentityTx(context.Context, refreshpostgres.Tx, PostgresPublicationIdentityRequest) (PostgresPublicationIdentity, error)
}

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
