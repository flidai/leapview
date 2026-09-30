package module

import (
	"context"

	"github.com/flidai/leapview/internal/credential"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// These contracts let application composition consume an authorized runtime
// reader through the credential module without importing its implementation.
type RuntimeResource = credential.Resource
type RuntimeScope = credential.Scope
type RuntimeCredentialReference = credential.RuntimeCredentialReference
type RuntimeUseAuthority = credential.RuntimeUseAuthority

// RuntimeCredentialReader must enforce the exact committed-reference and
// current workload authority contract. Consumers must hold runtime admission
// through the lifetime and actual cleanup of every client they create.
type RuntimeCredentialReader interface {
	WithCredential(context.Context, projectgraph.ServingIdentity, RuntimeResource, func(RuntimeCredentialReference, map[string]string) error) error
}

var _ RuntimeCredentialReader = (*credential.RuntimeResolver)(nil)

// RuntimeReader binds one caller's runtime authority to the repository and
// keyring verified at setup. Authority must remain request-scoped; the resulting
// reader neither grants permission itself nor owns the consumer's runtime lease.
func (services *Services) RuntimeReader(authority RuntimeUseAuthority) (RuntimeCredentialReader, error) {
	if services == nil {
		return nil, credential.ErrUnavailable
	}
	reader, err := credential.NewRuntimeResolver(services.runtimeRepository, services.runtimeKeys, authority)
	if err != nil {
		return nil, credential.ErrUnavailable
	}
	return reader, nil
}

var (
	ErrRuntimeInvalid     = credential.ErrInvalid
	ErrRuntimeConflict    = credential.ErrConflict
	ErrRuntimeUnavailable = credential.ErrUnavailable
	ErrRuntimeNotFound    = credential.ErrNotFound
	ErrRuntimeForbidden   = credential.ErrForbidden
)
