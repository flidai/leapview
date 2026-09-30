package credential

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

const runtimeConnectionPurpose = "connection-authentication"

var errRuntimeConsumerPanicked = errors.New("credential runtime consumer panicked")

// RuntimeCredentialReference is the resource-owned authority's exact choice
// of saved version and authenticated scope. It is never accepted from a public
// request or inferred from draft metadata.
type RuntimeCredentialReference struct {
	Scope     Scope
	VersionID string
}

// RuntimeUseAuthority resolves the credential committed for one runtime
// identity and resource. Implementations must prove current governed workload
// authority for that identity and that the exact saved version is referenced
// by durable resource-owned runtime authority. Validation receipts and draft
// listings are not runtime authority.
type RuntimeUseAuthority interface {
	ResolveRuntimeCredential(context.Context, projectgraph.ServingIdentity, Resource) (RuntimeCredentialReference, error)
}

// RuntimeRepository retrieves one exact encrypted version. It must not
// implement latest-version lookup or fallback semantics.
type RuntimeRepository interface {
	GetStoredDraft(context.Context, string, string, Resource, string) (StoredVersion, error)
}

// RuntimeResolver exposes a stored PostgreSQL password only to an internal
// consumer authorized by the current runtime authority.
type RuntimeResolver struct {
	repository RuntimeRepository
	keys       ValidationKeyring
	authority  RuntimeUseAuthority
}

// NewRuntimeResolver constructs the internal exact-version reader. It remains
// unusable without the mandatory resource-owned authority port.
func NewRuntimeResolver(repository RuntimeRepository, keys ValidationKeyring, authority RuntimeUseAuthority) (*RuntimeResolver, error) {
	if typednil.IsNil(repository) || typednil.IsNil(keys) || typednil.IsNil(authority) || !canonical(keys.DeploymentID()) {
		return nil, ErrUnavailable
	}
	return &RuntimeResolver{repository: repository, keys: keys, authority: authority}, nil
}

// WithCredential resolves the exact version committed for identity/resource,
// then provides its exact authority-rechecked reference and typed fields only
// for the duration of consumer. The caller must hold its runtime admission
// lease until every client or pool created by
// consumer has drained and closed; holding it only until this method returns
// is insufficient for consumers that create long-lived clients. The two
// authority reads detect observed changes but do not provide an activation
// barrier or prevent a concurrent authority change.
//
// Only customer PostgreSQL password credentials are supported in this v1
// foundation. No ordinary runtime consumer is wired to it until activation
// commits and enforces that authority boundary. consumer is trusted internal
// code and must not log credential values. Go cannot erase copied strings; a
// consumer may derive client authentication state only for clients admitted
// under the caller's lease, which must remain held through that client's full
// lifetime.
func (resolver *RuntimeResolver) WithCredential(
	ctx context.Context,
	identity projectgraph.ServingIdentity,
	resource Resource,
	consumer func(RuntimeCredentialReference, map[string]string) error,
) error {
	if resolver == nil || ctx == nil || typednil.IsNil(resolver.repository) ||
		typednil.IsNil(resolver.keys) || typednil.IsNil(resolver.authority) {
		return ErrUnavailable
	}
	if consumer == nil || resource.Validate() != nil || resource.ScopeKind != "connection" || identity.Validate() != nil ||
		identity.ProjectID.String() != resource.ProjectID || identity.Environment != resource.Environment {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	reference, err := resolver.authorizedReference(ctx, identity, resource)
	if err != nil {
		return err
	}
	stored, err := resolver.repository.GetStoredDraft(ctx, resolver.keys.DeploymentID(), reference.Scope.OwnerID, resource, reference.VersionID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Re-resolve after storage I/O so a reference that changed while the row was
	// being read cannot authorize decryption. This observation still depends on
	// the caller-held admission lease for stability through consumer execution.
	current, err := resolver.authorizedReference(ctx, identity, resource)
	if err != nil {
		return err
	}
	if current != reference {
		return ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if !metadataMatchesScope(stored.Metadata, resolver.keys.DeploymentID(), reference.Scope.OwnerID, resource) ||
		stored.Metadata.Binding != expectedEncryptionBinding(resolver.keys.DeploymentID(), reference.Scope, reference.VersionID) {
		return ErrNotFound
	}
	plaintext, decryptErr := resolver.keys.Decrypt(stored.Metadata.Binding, stored.Envelope)
	defer clear(plaintext)
	if decryptErr != nil {
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fields, ok := decodePostgresPassword(plaintext)
	if !ok {
		return ErrValidationFailed
	}
	defer clear(fields)
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := invokeRuntimeConsumer(consumer, current, fields); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (resolver *RuntimeResolver) authorizedReference(
	ctx context.Context,
	identity projectgraph.ServingIdentity,
	resource Resource,
) (RuntimeCredentialReference, error) {
	reference, err := resolver.authority.ResolveRuntimeCredential(ctx, identity, resource)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RuntimeCredentialReference{}, ctxErr
		}
		if errors.Is(err, ErrNotFound) {
			return RuntimeCredentialReference{}, ErrNotFound
		}
		if errors.Is(err, ErrForbidden) {
			return RuntimeCredentialReference{}, ErrForbidden
		}
		return RuntimeCredentialReference{}, ErrUnavailable
	}
	versionID, parseErr := uuid.Parse(reference.VersionID)
	if parseErr != nil || versionID == uuid.Nil || versionID.String() != reference.VersionID ||
		!runtimeScopeValid(reference.Scope, resource) {
		return RuntimeCredentialReference{}, ErrForbidden
	}
	return reference, nil
}

func runtimeScopeValid(scope Scope, resource Resource) bool {
	return scope.Resource == resource && canonical(scope.OwnerID) &&
		scope.Purpose == runtimeConnectionPurpose && scope.Provider == "postgres" &&
		destinationDigest(scope.Destination)
}

func invokeRuntimeConsumer(
	consumer func(RuntimeCredentialReference, map[string]string) error,
	reference RuntimeCredentialReference,
	fields map[string]string,
) (err error) {
	panicked := false
	func() {
		returned := false
		defer func() {
			if !returned {
				// Consumer panics can contain provider errors with secret values.
				// Recover here, retain only a boolean, and let this frame finish
				// unwinding before raising the fixed sentinel below.
				_ = recover()
				panicked = true
			}
		}()
		err = consumer(reference, fields)
		returned = true
	}()
	if panicked {
		panic(errRuntimeConsumerPanicked)
	}
	return err
}
