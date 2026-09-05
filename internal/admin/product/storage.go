package product

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// MutationKind identifies the small set of product writes. Storage
// implementations own the transaction and CAS details; the service owns
// validation, command invocation, and blob handling.
type MutationKind string

const (
	MutationDisplayName MutationKind = "display_name"
	MutationLogo        MutationKind = "logo"
	MutationDeleteLogo  MutationKind = "delete_logo"
	MutationReset       MutationKind = "reset"
)

type MutationRequest struct {
	ExpectedRevision int64
	Mutation         Mutation
	Action           string
	MetadataJSON     string
	Kind             MutationKind
	DisplayName      string
	Logo             *Logo
	CheckConcurrency func(context.Context, int64) error
}

// Storage is the product-owned persistence boundary. The native PostgreSQL
// repository implements this contract; callers never reach into persistence
// details directly.
type Storage interface {
	Get(context.Context) (Identity, error)
	Ping(context.Context) error
	Mutate(context.Context, MutationRequest) (Identity, error)
}

// AuditEventID derives an RFC 9562 UUIDv5 from immutable mutation identity.
// It is exported for native storage adapters that append the audit row.
func AuditEventID(m Mutation, action, metadata string, expectedRevision int64) string {
	seed := strings.Join([]string{m.PrincipalID, m.RequestID, m.CorrelationID, action, metadata, fmt.Sprint(expectedRevision)}, "\x00")
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(seed)).String()
}
