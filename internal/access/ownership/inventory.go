// Package ownership composes read-only ownership authorities from product
// domains. It intentionally contains no SQL and does not know the schemas of
// any owning domain.
package ownership

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

// Inventory is the access-lifecycle view over domain-owned objects. Each
// authority is responsible for discovering only the objects in its own
// durable store; Inventory merely validates, merges, and orders those reports.
type Inventory struct {
	authorities []access.OwnershipAuthority
}

// New composes the supplied domain authorities. Nil authorities are rejected
// at construction so production composition cannot silently omit a domain.
func New(authorities ...access.OwnershipAuthority) (*Inventory, error) {
	result := &Inventory{authorities: make([]access.OwnershipAuthority, 0, len(authorities))}
	for index, authority := range authorities {
		if authority == nil {
			return nil, fmt.Errorf("ownership authority %d is nil", index)
		}
		result.authorities = append(result.authorities, authority)
	}
	return result, nil
}

// Authorities reports the configured authority count for composition tests.
func (i *Inventory) Authorities() int {
	if i == nil {
		return 0
	}
	return len(i.authorities)
}

// ListOwnedObjects returns a deterministic merged snapshot. No source
// authority is queried for an empty principal ID.
func (i *Inventory) ListOwnedObjects(ctx context.Context, principalID string) (access.OwnershipReport, error) {
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		return access.OwnershipReport{}, errors.New("principal id is required")
	}
	if i == nil || len(i.authorities) == 0 {
		return access.OwnershipReport{PrincipalID: principalID, Objects: []access.OwnedObject{}}, nil
	}
	objects := make([]access.OwnedObject, 0)
	for index, authority := range i.authorities {
		report, err := authority.ListOwnedObjects(ctx, principalID)
		if err != nil {
			return access.OwnershipReport{}, fmt.Errorf("ownership authority %d: %w", index, err)
		}
		if reported := strings.TrimSpace(report.PrincipalID); reported != "" && reported != principalID {
			return access.OwnershipReport{}, fmt.Errorf("ownership authority %d returned principal %q for %q", index, reported, principalID)
		}
		for objectIndex, object := range report.Objects {
			object.Kind = strings.TrimSpace(object.Kind)
			object.ID = strings.TrimSpace(object.ID)
			object.OwnerPrincipalID = strings.TrimSpace(object.OwnerPrincipalID)
			object.Lifecycle = strings.TrimSpace(object.Lifecycle)
			object.Name = strings.TrimSpace(object.Name)
			if object.Kind == "" || object.ID == "" || object.OwnerPrincipalID != principalID || object.Lifecycle == "" {
				return access.OwnershipReport{}, fmt.Errorf("ownership authority %d object %d is inconsistent", index, objectIndex)
			}
			objects = append(objects, object)
		}
	}
	sort.Slice(objects, func(left, right int) bool {
		if objects[left].Kind != objects[right].Kind {
			return objects[left].Kind < objects[right].Kind
		}
		if objects[left].ID != objects[right].ID {
			return objects[left].ID < objects[right].ID
		}
		return objects[left].Name < objects[right].Name
	})
	for index := 1; index < len(objects); index++ {
		if objects[index-1].Kind == objects[index].Kind && objects[index-1].ID == objects[index].ID {
			return access.OwnershipReport{}, fmt.Errorf("duplicate owned object %s/%s", objects[index].Kind, objects[index].ID)
		}
	}
	return access.OwnershipReport{PrincipalID: principalID, Objects: objects}, nil
}

// EnsureOffboardingSafe blocks deletion while any composed authority reports
// a live object. Transfer/tombstone policy belongs to that object's owner and
// is represented as evidence on the returned conflict.
func (i *Inventory) EnsureOffboardingSafe(ctx context.Context, principalID string) error {
	report, err := i.ListOwnedObjects(ctx, principalID)
	if err != nil {
		return err
	}
	if len(report.Objects) != 0 {
		return &access.OwnershipConflictError{Report: report}
	}
	return nil
}

// WithOwnershipDB returns a transaction-bound inventory. Authorities that
// support transaction binding are rebound; read-only authorities without that
// optional capability remain available so fixtures and external stores can
// still participate in inventory composition.
func (i *Inventory) WithOwnershipDB(db access.OwnershipDBTX) access.OwnershipGuard {
	if i == nil {
		return (*Inventory)(nil)
	}
	bound := make([]access.OwnershipAuthority, 0, len(i.authorities))
	for _, authority := range i.authorities {
		if transactional, ok := authority.(access.TransactionalOwnershipAuthority); ok {
			bound = append(bound, transactional.WithOwnershipDB(db))
			continue
		}
		bound = append(bound, authority)
	}
	return &Inventory{authorities: bound}
}

// WithOwnershipMutationDB returns a transaction-bound inventory with the
// optional mutation adapters rebound to db. Read-only authorities remain in
// the composition so a live object they own fails closed instead of being
// silently skipped.
func (i *Inventory) WithOwnershipMutationDB(db access.OwnershipDBTX) access.OwnershipMutator {
	if i == nil {
		return (*Inventory)(nil)
	}
	bound := make([]access.OwnershipAuthority, 0, len(i.authorities))
	for _, authority := range i.authorities {
		if transactional, ok := authority.(access.TransactionalOwnershipMutator); ok {
			bound = append(bound, transactional.WithOwnershipMutationDB(db))
			continue
		}
		bound = append(bound, authority)
	}
	return &Inventory{authorities: bound}
}

// TransferOwnedObjects delegates one ownership transfer to every domain that
// reports a live object. A domain without a mutation adapter is a deliberate
// fail-closed error when it has live objects; historical empty domains are
// allowed to participate in the inventory without gaining write authority.
func (i *Inventory) TransferOwnedObjects(ctx context.Context, principalID, targetPrincipalID string) (access.OwnershipReport, error) {
	principalID = strings.TrimSpace(principalID)
	targetPrincipalID = strings.TrimSpace(targetPrincipalID)
	if principalID == "" || targetPrincipalID == "" {
		return access.OwnershipReport{}, errors.New("principal and target principal ids are required")
	}
	if principalID == targetPrincipalID {
		return access.OwnershipReport{}, errors.New("principal and target principal ids must differ")
	}
	return i.resolveOwnedObjects(ctx, principalID, targetPrincipalID, true)
}

// TombstoneOwnedObjects safely retires every live object that explicitly
// supports domain tombstoning. The returned report contains the objects
// resolved by this call and is suitable for an audit envelope.
func (i *Inventory) TombstoneOwnedObjects(ctx context.Context, principalID string) (access.OwnershipReport, error) {
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		return access.OwnershipReport{}, errors.New("principal id is required")
	}
	return i.resolveOwnedObjects(ctx, principalID, "", false)
}

func (i *Inventory) resolveOwnedObjects(ctx context.Context, principalID, targetPrincipalID string, transfer bool) (access.OwnershipReport, error) {
	if i == nil || len(i.authorities) == 0 {
		return access.OwnershipReport{PrincipalID: principalID, Objects: []access.OwnedObject{}}, nil
	}
	resolved := access.OwnershipReport{PrincipalID: principalID, Objects: []access.OwnedObject{}}
	for index, authority := range i.authorities {
		report, err := authority.ListOwnedObjects(ctx, principalID)
		if err != nil {
			return access.OwnershipReport{}, fmt.Errorf("ownership authority %d: %w", index, err)
		}
		if len(report.Objects) == 0 {
			continue
		}
		mutator, ok := authority.(access.OwnershipMutator)
		if !ok {
			action := "tombstone"
			if transfer {
				action = "transfer"
			}
			return access.OwnershipReport{}, fmt.Errorf("ownership authority %d does not support %s", index, action)
		}
		var changed access.OwnershipReport
		if transfer {
			changed, err = mutator.TransferOwnedObjects(ctx, principalID, targetPrincipalID)
		} else {
			changed, err = mutator.TombstoneOwnedObjects(ctx, principalID)
		}
		if err != nil {
			return access.OwnershipReport{}, fmt.Errorf("resolve ownership authority %d: %w", index, err)
		}
		for _, object := range changed.Objects {
			object.OwnerPrincipalID = principalID
			resolved.Objects = append(resolved.Objects, object)
		}
	}
	sort.Slice(resolved.Objects, func(left, right int) bool {
		if resolved.Objects[left].Kind != resolved.Objects[right].Kind {
			return resolved.Objects[left].Kind < resolved.Objects[right].Kind
		}
		return resolved.Objects[left].ID < resolved.Objects[right].ID
	})
	return resolved, nil
}

var _ access.OwnershipGuard = (*Inventory)(nil)
var _ access.TransactionalOwnershipGuard = (*Inventory)(nil)
var _ access.OwnershipMutator = (*Inventory)(nil)
var _ access.TransactionalOwnershipMutator = (*Inventory)(nil)
