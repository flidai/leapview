package postgres

import (
	"context"
	"errors"
)

// ErrPublicationIdentityUnavailable indicates that the physical publication
// identity has not been admitted (or could not be resolved) for a mutation.
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

// PostgresPublicationIdentityRequest scopes an identity lookup. The
// deployment adapter supplies the target binding; these fields carry the
// refresh operation's immutable scope and publication evidence.
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
// while the caller-owned refresh authority transaction is open.
type PostgresPublicationIdentityResolver interface {
	ResolvePublicationIdentityTx(context.Context, Tx, PostgresPublicationIdentityRequest) (PostgresPublicationIdentity, error)
}
