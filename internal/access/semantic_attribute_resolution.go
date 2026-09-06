package access

import "context"

// SemanticAttributeResolution is the coherent, read-only view of semantic
// attribute authority for one subject. Registry metadata and control state
// are returned with the effective direct/group values so consumers can bind
// all three to one database snapshot.
type SemanticAttributeResolution struct {
	Subject      SubjectRef
	Registry     SemanticAttributeRegistrySnapshot
	ControlState SemanticAttributeControlState
	Attributes   []EffectiveSemanticAttribute
}

// SemanticAttributeResolutionReader exposes the narrow runtime resolution
// boundary. Implementations must resolve the subject, registry, control
// revision, and effective direct/group assignments coherently.
type SemanticAttributeResolutionReader interface {
	ResolveSemanticAttributes(context.Context, SubjectRef) (SemanticAttributeResolution, error)
}
