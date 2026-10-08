package access

import (
	"context"
	"errors"
)

// PrincipalMutationReader locks a principal for a revision-checked mutation.
// The reader must use the caller-owned transaction and retain the lock until
// the profile change and its audit event commit or roll back together.
type PrincipalMutationReader interface {
	PrincipalByIDForUpdate(context.Context, string) (Principal, error)
}

// PrincipalForMutation requires a locking read instead of treating an
// ordinary read inside a transaction as an atomic revision check.
func PrincipalForMutation(ctx context.Context, repository Repository, id string) (Principal, error) {
	reader, ok := repository.(PrincipalMutationReader)
	if !ok {
		return Principal{}, errors.New("principal revision locking is unavailable")
	}
	return reader.PrincipalByIDForUpdate(ctx, id)
}
