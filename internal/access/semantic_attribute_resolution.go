package access

import (
	"context"
	"time"
)

// SemanticAttributeResolution is one coherent, read-only observation of the
// semantic-attribute authority for an authenticated principal. The subject
// closure, registry, control state, and effective values are all read from the
// same authority snapshot so consumers cannot accidentally combine values
// from different membership or control revisions.
type SemanticAttributeResolution struct {
	Subject    SubjectRef
	Subjects   []SubjectRef
	Registry   SemanticAttributeRegistrySnapshot
	Control    SemanticAttributeControlSnapshot
	Attributes []EffectiveSemanticAttribute
	ObservedAt time.Time
}

// SemanticAttributeResolutionReader is the narrow consumer capability for
// coherent semantic-attribute reads. Implementations must resolve only the
// requested subject and must fail closed when the principal or authority
// snapshot is not live and internally consistent.
type SemanticAttributeResolutionReader interface {
	ResolveSemanticAttributes(context.Context, SubjectRef) (SemanticAttributeResolution, error)
}
