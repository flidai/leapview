package module

import (
	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// These aliases keep composition on the access module façade when it needs
// to wire a capability adapter. The underlying contracts remain owned by
// access and access/snapshot.
type APICredential = access.APICredential
type AuditIntent = access.AuditIntent
type AuditIntentRecorder = access.AuditIntentRecorder
type CanonicalAuditRecorder = access.CanonicalAuditRecorder
type Capability = access.Capability
type ResourceRef = access.ResourceRef
type SubjectKind = access.SubjectKind
type SubjectRef = access.SubjectRef
type AuthorizationSnapshot = accesssnapshot.AuthorizationSnapshot

const (
	CapabilityProjectAdmin    = access.CapabilityProjectAdmin
	CapabilityResourceUse     = access.CapabilityResourceUse
	CapabilityResourceRead    = access.CapabilityResourceRead
	CapabilityResourceEdit    = access.CapabilityResourceEdit
	CapabilityResourceManage  = access.CapabilityResourceManage
	CapabilityResourceShare   = access.CapabilityResourceShare
	CapabilityResourcePublish = access.CapabilityResourcePublish
	SubjectKindPrincipal      = access.SubjectKindPrincipal
	SubjectKindGroup          = access.SubjectKindGroup
)

func NewResourceRef(id projectgraph.ResourceID, kind projectgraph.Kind) (ResourceRef, error) {
	return access.NewResourceRef(id, kind)
}

func IntersectTokenCapabilities(token, effective []Capability) []Capability {
	return access.IntersectTokenCapabilities(token, effective)
}
