package access

import (
	"context"
	"errors"
)

// GroupMutationReader locks a group for a revision-checked mutation.
// The reader must retain the lock in the caller-owned transaction until
// the rename and its audit event commit or roll back together.
type GroupMutationReader interface {
	GroupByIDForUpdate(context.Context, string) (Group, error)
}

// GroupForMutation requires a locking read rather than an ordinary read
// whose revision may become stale before the write.
func GroupForMutation(ctx context.Context, repository Repository, id string) (Group, error) {
	reader, ok := repository.(GroupMutationReader)
	if !ok {
		return Group{}, errors.New("group revision locking is unavailable")
	}
	return reader.GroupByIDForUpdate(ctx, id)
}
