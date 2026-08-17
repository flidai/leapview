package access

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/flidai/leapview/internal/project/graph"
)

var (
	// ErrInvalidResourceRef indicates that a canonical resource reference has
	// an invalid graph ID or kind.
	ErrInvalidResourceRef = errors.New("invalid canonical resource reference")
	// ErrResourceNotFound indicates that a reference ID is absent from the
	// authoritative project graph.
	ErrResourceNotFound = errors.New("canonical resource is not in project graph")
	// ErrResourceKindMismatch indicates that a reference kind does not match the
	// authoritative graph node for its ID.
	ErrResourceKindMismatch = errors.New("canonical resource kind does not match project graph")
	// ErrInvalidCapability indicates a capability outside the canonical access
	// contract.
	ErrInvalidCapability = errors.New("invalid canonical capability")
	// ErrCapabilityNotAllowed indicates that a capability is valid in general,
	// but not for the referenced graph kind.
	ErrCapabilityNotAllowed = errors.New("canonical capability is not allowed for resource kind")
	// ErrInvalidCanonicalGrant indicates a grant with a missing subject or an
	// invalid resource reference.
	ErrInvalidCanonicalGrant = errors.New("invalid canonical grant")
	// ErrUnboundCanonicalGrant indicates a grant that was not validated against
	// the immutable project graph that authorizes its resource identity.
	ErrUnboundCanonicalGrant = errors.New("canonical grant is not bound to a project graph")
	// ErrInvalidSubjectRef indicates a missing or unsupported subject identity.
	ErrInvalidSubjectRef = errors.New("invalid canonical subject reference")
	// ErrInvalidProjectRole indicates an unsupported project role name.
	ErrInvalidProjectRole = errors.New("invalid canonical project role")
	// ErrInvalidPlatformRole indicates an unsupported instance-wide platform
	// role name. Platform roles are deliberately distinct from project roles.
	ErrInvalidPlatformRole = errors.New("invalid canonical platform role")
)

// ResourceRef is the canonical authorization and audit reference for one
// project graph resource. The ID is the sole identity component; kind is
// retained for validation and capability selection, but is not part of the
// canonical key. Descriptive metadata, paths, domains, and parent references
// intentionally do not appear in this type.
//
// ResourceRef values are constructor-only. The constructor is the trust
// boundary for authored references; ValidateAgainst must additionally be
// called before installing a reference into an authorization store.
type ResourceRef struct {
	id   graph.ResourceID
	kind graph.Kind
}

// NewResourceRef validates an explicit graph resource ID and kind.
func NewResourceRef(id graph.ResourceID, kind graph.Kind) (ResourceRef, error) {
	validatedID, err := graph.NewResourceID(id.String())
	if err != nil {
		return ResourceRef{}, fmt.Errorf("%w: %w", ErrInvalidResourceRef, err)
	}
	validatedKind, err := graph.ParseKind(string(kind))
	if err != nil {
		return ResourceRef{}, fmt.Errorf("%w: %w", ErrInvalidResourceRef, err)
	}
	return ResourceRef{id: validatedID, kind: validatedKind}, nil
}

// ID returns the validated graph resource ID.
func (r ResourceRef) ID() graph.ResourceID { return r.id }

// Kind returns the validated graph resource kind.
func (r ResourceRef) Kind() graph.Kind { return r.kind }

// Validate checks a reference. Zero values and values assembled inside this
// package are rejected unless they came through NewResourceRef.
func (r ResourceRef) Validate() error {
	validated, err := NewResourceRef(r.id, r.kind)
	if err != nil {
		return err
	}
	if validated.id != r.id || validated.kind != r.kind {
		return ErrInvalidResourceRef
	}
	return nil
}

// ValidateAgainst checks that the ID and kind agree with the authoritative
// project graph. This check is mandatory before installing authored grants;
// otherwise a caller could relabel a resource ID with another kind.
func (r ResourceRef) ValidateAgainst(project graph.ProjectGraph) error {
	if err := r.Validate(); err != nil {
		return err
	}
	resource, ok := project.Resource(r.id)
	if !ok {
		return fmt.Errorf("%w: %q", ErrResourceNotFound, r.id)
	}
	if resource.Kind != r.kind {
		return fmt.Errorf("%w: id %q is %q, reference is %q", ErrResourceKindMismatch, r.id, resource.Kind, r.kind)
	}
	return nil
}

// CanonicalID returns the globally unique graph resource ID. It is the sole
// resource component of authorization and audit keys.
func (r ResourceRef) CanonicalID() string {
	if err := r.Validate(); err != nil {
		return ""
	}
	return r.id.String()
}

type resourceRefWire struct {
	ID   graph.ResourceID `json:"id"`
	Kind graph.Kind       `json:"kind"`
}

// MarshalJSON emits only the canonical ID and descriptive kind, rejecting
// invalid values before they cross a serialization boundary.
func (r ResourceRef) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(resourceRefWire{ID: r.id, Kind: r.kind})
}

// UnmarshalJSON validates IDs and kinds, rejecting duplicate/unknown/trailing
// JSON values.
func (r *ResourceRef) UnmarshalJSON(data []byte) error {
	if r == nil {
		return errors.New("cannot unmarshal canonical resource reference into nil receiver")
	}
	var decoded resourceRefWire
	if err := decodeCanonicalJSON(data, &decoded, "canonical resource reference"); err != nil {
		return err
	}
	validated, err := NewResourceRef(decoded.ID, decoded.Kind)
	if err != nil {
		return err
	}
	*r = validated
	return nil
}

// Capability is an authorization action in the project/resource contract. It
// is distinct from the legacy privilege vocabulary.
type Capability string

const (
	CapabilityProjectAdmin    Capability = "PROJECT_ADMIN"
	CapabilityResourceUse     Capability = "RESOURCE_USE"
	CapabilityResourceRead    Capability = "RESOURCE_READ"
	CapabilityResourceEdit    Capability = "RESOURCE_EDIT"
	CapabilityResourceManage  Capability = "RESOURCE_MANAGE"
	CapabilityResourceShare   Capability = "RESOURCE_SHARE"
	CapabilityResourcePublish Capability = "RESOURCE_PUBLISH"
)

var canonicalCapabilityOrder = []Capability{
	CapabilityProjectAdmin,
	CapabilityResourceUse,
	CapabilityResourceRead,
	CapabilityResourceEdit,
	CapabilityResourceManage,
	CapabilityResourceShare,
	CapabilityResourcePublish,
}

var canonicalCapabilitySet = map[Capability]struct{}{
	CapabilityProjectAdmin:    {},
	CapabilityResourceUse:     {},
	CapabilityResourceRead:    {},
	CapabilityResourceEdit:    {},
	CapabilityResourceManage:  {},
	CapabilityResourceShare:   {},
	CapabilityResourcePublish: {},
}

// ParseCapability validates a canonical capability wire value.
func ParseCapability(value string) (Capability, error) {
	capability := Capability(value)
	if _, ok := canonicalCapabilitySet[capability]; !ok {
		return "", fmt.Errorf("%w %q", ErrInvalidCapability, value)
	}
	return capability, nil
}

// Valid reports whether capability belongs to the canonical capability set.
func (capability Capability) Valid() bool {
	_, ok := canonicalCapabilitySet[capability]
	return ok
}

// Validate checks a canonical capability value.
func (capability Capability) Validate() error {
	if !capability.Valid() {
		return fmt.Errorf("%w %q", ErrInvalidCapability, capability)
	}
	return nil
}

// String returns the wire representation of a capability.
func (capability Capability) String() string { return string(capability) }

// ValidateTokenCapabilities validates an API-token attenuation request against
// the principal's current effective capabilities. A nil request is the
// dynamic form: the token carries no allowlist and each authorization decision
// must use the principal's effective capabilities at that time. A non-nil
// request is an explicit least-privilege allowlist and every capability must be
// present in the current effective set.
func ValidateTokenCapabilities(requested, effective []Capability) error {
	if requested == nil {
		return nil
	}
	allowed := make(map[Capability]struct{}, len(effective))
	for _, capability := range effective {
		if err := capability.Validate(); err != nil {
			return err
		}
		allowed[capability] = struct{}{}
	}
	seen := make(map[Capability]struct{}, len(requested))
	for _, capability := range requested {
		if err := capability.Validate(); err != nil {
			return err
		}
		if _, duplicate := seen[capability]; duplicate {
			return fmt.Errorf("%w: duplicate API token capability %q", ErrInvalidCapability, capability)
		}
		seen[capability] = struct{}{}
		if _, ok := allowed[capability]; !ok {
			return fmt.Errorf("%w: API token capability %q exceeds effective access", ErrCapabilityNotAllowed, capability)
		}
	}
	return nil
}

// IntersectTokenCapabilities applies a stored API-token allowlist to the
// principal's effective capabilities. A nil allowlist is intentionally
// dynamic and therefore returns a defensive copy of effective. Explicit
// allowlists are intersected as defense in depth even after creation-time
// subset validation.
func IntersectTokenCapabilities(token, effective []Capability) []Capability {
	if len(effective) == 0 {
		return []Capability{}
	}
	if token == nil {
		return append([]Capability(nil), effective...)
	}
	allowed := make(map[Capability]struct{}, len(token))
	for _, capability := range token {
		allowed[capability] = struct{}{}
	}
	result := make([]Capability, 0, len(effective))
	for _, capability := range effective {
		if _, ok := allowed[capability]; ok {
			result = append(result, capability)
		}
	}
	return result
}

// MarshalText rejects invalid capability values and supports the canonical
// text representation in encoders using encoding.TextMarshaler.
func (capability Capability) MarshalText() ([]byte, error) {
	if err := capability.Validate(); err != nil {
		return nil, err
	}
	return []byte(capability), nil
}

// UnmarshalText validates a canonical capability wire value.
func (capability *Capability) UnmarshalText(data []byte) error {
	if capability == nil {
		return errors.New("cannot unmarshal canonical capability into nil receiver")
	}
	parsed, err := ParseCapability(string(data))
	if err != nil {
		return err
	}
	*capability = parsed
	return nil
}

// CanonicalCapabilities returns every capability in deterministic contract
// order. The returned slice is defensive.
func CanonicalCapabilities() []Capability {
	return append([]Capability(nil), canonicalCapabilityOrder...)
}

// canonicalCapabilityMatrix is immutable package state. CapabilitiesForKind
// always returns defensive copies.
var canonicalCapabilityMatrix = map[graph.Kind][]Capability{
	graph.KindProject:       {CapabilityProjectAdmin},
	graph.KindConnection:    {CapabilityResourceUse, CapabilityResourceRead, CapabilityResourceEdit, CapabilityResourceManage},
	graph.KindSource:        {CapabilityResourceUse, CapabilityResourceRead, CapabilityResourceEdit, CapabilityResourceManage},
	graph.KindModel:         {CapabilityResourceUse, CapabilityResourceRead, CapabilityResourceEdit, CapabilityResourceManage},
	graph.KindSemanticModel: {CapabilityResourceUse, CapabilityResourceRead, CapabilityResourceEdit, CapabilityResourceManage},
	graph.KindPipeline:      {CapabilityResourceUse, CapabilityResourceRead, CapabilityResourceEdit, CapabilityResourceManage},
	graph.KindDashboard:     {CapabilityResourceRead, CapabilityResourceEdit, CapabilityResourceManage, CapabilityResourceShare, CapabilityResourcePublish},
}

// CapabilitiesForKind returns the capabilities allowed for kind in stable
// contract order. Invalid kinds return nil.
func CapabilitiesForKind(kind graph.Kind) []Capability {
	if !kind.Valid() {
		return nil
	}
	return append([]Capability(nil), canonicalCapabilityMatrix[kind]...)
}

// SupportsCapability reports whether capability is valid for kind. Invalid
// kinds and capabilities are denied.
func SupportsCapability(kind graph.Kind, capability Capability) bool {
	if !kind.Valid() || !capability.Valid() {
		return false
	}
	for _, allowed := range canonicalCapabilityMatrix[kind] {
		if allowed == capability {
			return true
		}
	}
	return false
}

// ValidateCapabilityForKind validates a capability against the deliberate
// kind matrix.
func ValidateCapabilityForKind(kind graph.Kind, capability Capability) error {
	if !kind.Valid() {
		return fmt.Errorf("%w: %w", ErrInvalidResourceRef, graph.ErrInvalidKind)
	}
	if err := capability.Validate(); err != nil {
		return err
	}
	if !SupportsCapability(kind, capability) {
		return fmt.Errorf("%w: kind %q, capability %q", ErrCapabilityNotAllowed, kind, capability)
	}
	return nil
}

// ProjectRole is an explicit project-wide RBAC role. Roles are captured in a
// serving authorization snapshot with their capability bundle; they are not
// expanded into one grant per graph node.
type ProjectRole string

const (
	ProjectRoleOwner        ProjectRole = "owner"
	ProjectRoleAdmin        ProjectRole = "admin"
	ProjectRoleDeployer     ProjectRole = "deployer"
	ProjectRoleDataDeployer ProjectRole = "data_deployer"
	ProjectRoleContributor  ProjectRole = "contributor"
	ProjectRoleEditor       ProjectRole = "editor"
	ProjectRoleMember       ProjectRole = "member"
	ProjectRoleViewer       ProjectRole = "viewer"
)

var projectRoleCapabilities = map[ProjectRole][]Capability{
	ProjectRoleOwner:        {CapabilityProjectAdmin, CapabilityResourceUse, CapabilityResourceRead, CapabilityResourceEdit, CapabilityResourceManage, CapabilityResourceShare, CapabilityResourcePublish},
	ProjectRoleAdmin:        {CapabilityProjectAdmin, CapabilityResourceUse, CapabilityResourceRead, CapabilityResourceEdit, CapabilityResourceManage, CapabilityResourceShare, CapabilityResourcePublish},
	ProjectRoleDeployer:     {CapabilityResourceUse, CapabilityResourceRead, CapabilityResourcePublish},
	ProjectRoleDataDeployer: {CapabilityResourceUse, CapabilityResourceEdit},
	ProjectRoleContributor:  {CapabilityResourceUse, CapabilityResourceRead, CapabilityResourceEdit},
	ProjectRoleEditor:       {CapabilityResourceUse, CapabilityResourceRead, CapabilityResourceEdit},
	ProjectRoleMember:       {CapabilityResourceUse, CapabilityResourceRead, CapabilityResourceEdit, CapabilityResourceManage},
	ProjectRoleViewer:       {CapabilityResourceUse, CapabilityResourceRead},
}

var projectRoleOrder = []ProjectRole{
	ProjectRoleOwner, ProjectRoleAdmin, ProjectRoleDeployer, ProjectRoleDataDeployer, ProjectRoleContributor,
	ProjectRoleEditor, ProjectRoleMember, ProjectRoleViewer,
}

// ParseProjectRole validates a canonical project role name.
func ParseProjectRole(value string) (ProjectRole, error) {
	role := ProjectRole(value)
	if _, ok := projectRoleCapabilities[role]; !ok {
		return "", fmt.Errorf("%w %q", ErrInvalidProjectRole, value)
	}
	return role, nil
}

// ProjectRoleCapabilities returns the immutable role bundle in deterministic
// capability order. Callers receive a defensive copy.
func ProjectRoleCapabilities(role ProjectRole) []Capability {
	return append([]Capability(nil), projectRoleCapabilities[role]...)
}

// CanonicalProjectRoles returns the supported project roles in contract order.
func CanonicalProjectRoles() []ProjectRole { return append([]ProjectRole(nil), projectRoleOrder...) }

func (role ProjectRole) Valid() bool {
	_, ok := projectRoleCapabilities[role]
	return ok
}

// PlatformRole is intentionally separate from project RBAC. Platform access
// controls instance-wide administration and is never serialized into a
// project-generation authorization snapshot.
type PlatformRole string

const PlatformRoleAdmin PlatformRole = "platform_admin"

func ParsePlatformRole(value string) (PlatformRole, error) {
	role := PlatformRole(value)
	if role != PlatformRoleAdmin {
		return "", fmt.Errorf("%w %q", ErrInvalidPlatformRole, value)
	}
	return role, nil
}

// SubjectKind identifies the explicit subject class used by canonical grants.
// Service principals use SubjectKindPrincipal.
type SubjectKind string

const (
	SubjectKindPrincipal SubjectKind = "principal"
	SubjectKindGroup     SubjectKind = "group"
)

// SubjectRef identifies one principal or group. Domain membership is not a
// subject kind and therefore cannot be represented as authorization.
type SubjectRef struct {
	Kind SubjectKind `json:"kind"`
	ID   string      `json:"id"`
}

// NewSubjectRef validates an explicit subject kind and opaque ID.
func NewSubjectRef(kind SubjectKind, id string) (SubjectRef, error) {
	if kind != SubjectKindPrincipal && kind != SubjectKindGroup {
		return SubjectRef{}, fmt.Errorf("%w: kind %q", ErrInvalidSubjectRef, kind)
	}
	if id == "" || id != strings.TrimSpace(id) || strings.ContainsAny(id, "\x00\r\n\t") {
		return SubjectRef{}, fmt.Errorf("%w: subject ID is blank or contains control characters", ErrInvalidSubjectRef)
	}
	return SubjectRef{Kind: kind, ID: id}, nil
}

// Validate checks a subject that may have been assembled as a struct literal.
func (subject SubjectRef) Validate() error {
	validated, err := NewSubjectRef(subject.Kind, subject.ID)
	if err != nil {
		return err
	}
	if validated != subject {
		return ErrInvalidSubjectRef
	}
	return nil
}

// CanonicalGrant records one explicit subject capability on one resource.
// There is no domain, path, display, or parent field: authorization is always
// evaluated against this resource and this capability directly.
type CanonicalGrant struct {
	subject    SubjectRef
	resource   ResourceRef
	capability Capability
	projectID  graph.ResourceID
}

// Subject returns the explicit principal or group by value.
func (grant CanonicalGrant) Subject() SubjectRef { return grant.subject }

// Resource returns the immutable canonical resource reference by value.
func (grant CanonicalGrant) Resource() ResourceRef { return grant.resource }

// Capability returns the authorized canonical capability.
func (grant CanonicalGrant) Capability() Capability { return grant.capability }

// NewCanonicalGrant validates a subject, resource, and kind-specific
// capability against the authoritative immutable project graph. There is no
// constructor for an installable grant that omits this graph check.
func NewCanonicalGrant(project graph.ProjectGraph, subject SubjectRef, resource ResourceRef, capability Capability) (CanonicalGrant, error) {
	if err := project.Validate(); err != nil {
		return CanonicalGrant{}, fmt.Errorf("%w: project graph: %w", ErrInvalidCanonicalGrant, err)
	}
	if err := subject.Validate(); err != nil {
		return CanonicalGrant{}, fmt.Errorf("%w: subject: %w", ErrInvalidCanonicalGrant, err)
	}
	if err := resource.Validate(); err != nil {
		return CanonicalGrant{}, fmt.Errorf("%w: resource: %w", ErrInvalidCanonicalGrant, err)
	}
	if err := ValidateCapabilityForKind(resource.kind, capability); err != nil {
		return CanonicalGrant{}, fmt.Errorf("%w: %w", ErrInvalidCanonicalGrant, err)
	}
	if err := resource.ValidateAgainst(project); err != nil {
		return CanonicalGrant{}, fmt.Errorf("%w: resource: %w", ErrInvalidCanonicalGrant, err)
	}
	return CanonicalGrant{subject: subject, resource: resource, capability: capability, projectID: project.ProjectID()}, nil
}

// Validate checks that the grant was constructed against an authoritative
// project graph and that its canonical fields remain valid.
func (grant CanonicalGrant) Validate() error {
	if grant.projectID == "" {
		return fmt.Errorf("%w: %w", ErrInvalidCanonicalGrant, ErrUnboundCanonicalGrant)
	}
	if err := grant.subject.Validate(); err != nil {
		return fmt.Errorf("%w: subject: %w", ErrInvalidCanonicalGrant, err)
	}
	if err := grant.resource.Validate(); err != nil {
		return fmt.Errorf("%w: resource: %w", ErrInvalidCanonicalGrant, err)
	}
	if err := ValidateCapabilityForKind(grant.resource.kind, grant.capability); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidCanonicalGrant, err)
	}
	return nil
}

// ValidateAgainst checks the resource ID and kind against the authoritative
// project graph, then checks the capability matrix. This prevents a caller
// from relabeling a model ID as a dashboard to obtain publication access.
func (grant CanonicalGrant) ValidateAgainst(project graph.ProjectGraph) error {
	if err := grant.Validate(); err != nil {
		return err
	}
	if project.ProjectID() != grant.projectID {
		return fmt.Errorf("%w: project identity %q does not match bound project %q", ErrInvalidCanonicalGrant, project.ProjectID(), grant.projectID)
	}
	if err := grant.resource.ValidateAgainst(project); err != nil {
		return fmt.Errorf("%w: resource: %w", ErrInvalidCanonicalGrant, err)
	}
	return nil
}

// MarshalJSON validates the whole grant before serialization.
func (grant CanonicalGrant) MarshalJSON() ([]byte, error) {
	if err := grant.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(canonicalGrantWire{Subject: grant.subject, Resource: grant.resource, Capability: grant.capability})
}

type canonicalGrantWire struct {
	Subject    SubjectRef  `json:"subject"`
	Resource   ResourceRef `json:"resource"`
	Capability Capability  `json:"capability"`
}

// DecodeCanonicalGrant validates canonical JSON against the authoritative
// graph. Standard json.Unmarshal cannot supply that graph and is deliberately
// rejected by UnmarshalJSON so decoded grants can never appear installable.
func DecodeCanonicalGrant(data []byte, project graph.ProjectGraph) (CanonicalGrant, error) {
	var decoded canonicalGrantWire
	if err := decodeCanonicalJSON(data, &decoded, "canonical grant"); err != nil {
		return CanonicalGrant{}, err
	}
	return NewCanonicalGrant(project, decoded.Subject, decoded.Resource, decoded.Capability)
}

// UnmarshalJSON rejects graph-free decoding. Use DecodeCanonicalGrant.
func (grant *CanonicalGrant) UnmarshalJSON(data []byte) error {
	if grant == nil {
		return errors.New("cannot unmarshal canonical grant into nil receiver")
	}
	return ErrUnboundCanonicalGrant
}

// GrantKey returns a deterministic subject/resource/capability key. Subject
// kind and ID are both included to avoid principal/group collisions; the
// resource portion is the globally unique graph ResourceID.
func (grant CanonicalGrant) GrantKey() string {
	if err := grant.Validate(); err != nil {
		return ""
	}
	return string(grant.subject.Kind) + "\x00" + grant.subject.ID + "\x00" + grant.resource.CanonicalID() + "\x00" + grant.capability.String()
}

func decodeCanonicalJSON(data []byte, target any, label string) error {
	if err := rejectDuplicateCanonicalJSONKeys(data); err != nil {
		return fmt.Errorf("decode %s: %w", label, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", label, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("decode %s: trailing JSON value", label)
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode %s: trailing data: %w", label, err)
	}
	return nil
}

func rejectDuplicateCanonicalJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkCanonicalJSONValue(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func walkCanonicalJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch delimiter := token.(type) {
	case json.Delim:
		switch delimiter {
		case '{':
			keys := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key is not a string")
				}
				canonicalKey := strings.ToLower(key)
				if _, exists := keys[canonicalKey]; exists {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				keys[canonicalKey] = struct{}{}
				if err := walkCanonicalJSONValue(decoder); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil {
				return err
			}
			if end != json.Delim('}') {
				return fmt.Errorf("JSON object ended with %v", end)
			}
		case '[':
			for decoder.More() {
				if err := walkCanonicalJSONValue(decoder); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil {
				return err
			}
			if end != json.Delim(']') {
				return fmt.Errorf("JSON array ended with %v", end)
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	}
	return nil
}
