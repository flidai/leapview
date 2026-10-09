package credential

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/flidai/leapview/pkg/strictjson"
)

type RuntimeCredentialRepository interface {
	GetStoredDraft(context.Context, string, string, Resource, string) (StoredVersion, error)
}

// RuntimeCredentials is an internal runtime port, never an HTTP read API. The
// caller must first prove the immutable serving generation/configuration pin.
// It has no latest-version fallback or plaintext-returning metadata operation.
type RuntimeCredentials struct {
	repository RuntimeCredentialRepository
	keys       ValidationKeyring
	scopes     ScopeResolver
}

func NewRuntimeCredentials(repository RuntimeCredentialRepository, keys ValidationKeyring, scopes ScopeResolver) (*RuntimeCredentials, error) {
	if typednil.IsNil(repository) || typednil.IsNil(keys) || typednil.IsNil(scopes) || !canonical(keys.DeploymentID()) {
		return nil, ErrUnavailable
	}
	return &RuntimeCredentials{repository: repository, keys: keys, scopes: scopes}, nil
}

func (runtime *RuntimeCredentials) UseVersion(ctx context.Context, resource Resource, versionID string, consume func(map[string]string) error) error {
	if runtime == nil || ctx == nil || consume == nil || resource.Validate() != nil || !activationUUID(versionID) {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	scope, err := runtime.scopes.ResolveCredentialScope(ctx, resource)
	if err != nil || scope.Resource != resource || !canonical(scope.OwnerID) || !canonical(scope.Provider) || !canonical(scope.Purpose) || !destinationDigest(scope.Destination) {
		return ErrNotFound
	}
	return runtime.useBoundVersion(ctx, scope, versionID, consume)
}

// UseBoundVersion is for a trusted immutable configuration/generation reader.
// The reader must prove this scope and exact version from committed authority,
// holding provider admission through all client cleanup. A current-scope read
// still verifies immutable customer ownership, but cannot substitute a newer
// provider or destination for the historical configuration's authenticated AAD.
func (runtime *RuntimeCredentials) UseBoundVersion(ctx context.Context, scope Scope, versionID string, consume func(map[string]string) error) error {
	if runtime == nil || ctx == nil || consume == nil || scope.Resource.Validate() != nil || !activationUUID(versionID) ||
		!canonical(scope.OwnerID) || !canonical(scope.Provider) || !canonical(scope.Purpose) || !destinationDigest(scope.Destination) {
		return ErrInvalid
	}
	current, err := runtime.scopes.ResolveCredentialScope(ctx, scope.Resource)
	if err != nil || current.Resource != scope.Resource || current.OwnerID != scope.OwnerID || current.Purpose != scope.Purpose {
		return ErrNotFound
	}
	return runtime.useBoundVersion(ctx, scope, versionID, consume)
}

func (runtime *RuntimeCredentials) useBoundVersion(ctx context.Context, scope Scope, versionID string, consume func(map[string]string) error) error {
	defer func() {
		if recover() != nil {
			panic(ErrUnavailable)
		}
	}()
	resource := scope.Resource
	stored, err := runtime.repository.GetStoredDraft(ctx, runtime.keys.DeploymentID(), scope.OwnerID, resource, versionID)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return ErrUnavailable
	}
	if stored.Metadata.Binding != expectedEncryptionBinding(runtime.keys.DeploymentID(), scope, versionID) {
		return ErrNotFound
	}
	plaintext, err := runtime.keys.Decrypt(stored.Metadata.Binding, stored.Envelope)
	if err != nil {
		return ErrUnavailable
	}
	defer clear(plaintext)
	fields := map[string]string{}
	defer clear(fields)
	if strictjson.Decode(plaintext, &fields) != nil {
		return ErrUnavailable
	}
	encoded, err := encodeFields(scope, fields)
	clear(encoded)
	if err != nil {
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Provider errors may contain credential-bearing URLs or driver details.
	// The public lifecycle boundary returns a fixed failure, never that text.
	if err := consume(fields); err != nil {
		return ErrUnavailable
	}
	return ctx.Err()
}
