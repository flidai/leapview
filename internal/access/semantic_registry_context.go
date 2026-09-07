package access

import (
	"context"
	"fmt"
	"reflect"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/semanticvalue"
)

// SemanticRegistryContext binds the existing registry snapshot to the existing
// instance control identity. It contains definitions, never principal values.
// Its authority is the Access reader, not a caller-supplied digest or a second
// registry. Consumers must re-read it when crossing a preparation boundary.
type SemanticRegistryContext struct {
	Control  AuthorizationControlRevision
	Registry SemanticAttributeRegistrySnapshot
}

type SemanticRegistryReader interface {
	ReadSemanticRegistry(context.Context, string) (SemanticRegistryContext, error)
}

func (value SemanticRegistryContext) Validate(instanceID string, projectID projectgraph.ResourceID) error {
	if err := value.Control.Validate(); err != nil {
		return err
	}
	if value.Control.InstanceID != instanceID || value.Control.ProjectID != projectID.String() {
		return fmt.Errorf("%w: semantic registry instance/project mismatch", ErrControlIdentityConflict)
	}
	if value.Registry.State.Profile != semanticvalue.Profile || value.Registry.State.Revision <= 0 {
		return fmt.Errorf("%w: semantic registry revision/profile is unavailable", ErrControlInvalidInput)
	}
	return platformdigest.ValidateSHA256Identity(value.Registry.State.Digest)
}

// Equal includes the definition projection so even an accidentally mutated
// in-memory snapshot cannot retain a stale digest and compare equal.
func (value SemanticRegistryContext) Equal(other SemanticRegistryContext) bool {
	return value.Control == other.Control && reflect.DeepEqual(value.Registry, other.Registry)
}
