// Package snapshot defines the immutable, project-generation-bound access
// policy installed at activation. Authorization identity is the canonical
// graph resource ID plus serving environment and generation; namespace,
// domain, path, and descriptive metadata are intentionally absent.
package snapshot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesspolicy "github.com/flidai/leapview/internal/access/policy"
	"github.com/flidai/leapview/internal/project/graph"
)

type AuthorizationSnapshot struct {
	identity     graph.ServingIdentity
	roleBindings []RoleBinding
	grants       []Grant
	dataPolicies []DataPolicy
	// project is retained privately so exported fields cannot be replaced with
	// values from another graph and then serialized as an installable snapshot.
	project graph.ProjectGraph
}

// Grant is one graph-validated capability binding. Canonical is immutable
// after construction and cannot be assembled without an authoritative graph.
type Grant struct {
	ID        string
	Name      string
	Canonical access.CanonicalGrant
}

// RoleBinding is one explicit project-wide RBAC assignment. Capabilities are
// captured when the snapshot is compiled so later edits to mutable role
// templates cannot change an installed generation.
type RoleBinding struct {
	ID           string
	Name         string
	Subject      access.SubjectRef
	Role         access.ProjectRole
	Capabilities []access.Capability
}

type DataPolicy struct {
	ID             string
	Name           string
	Resource       access.ResourceRef
	Subject        *access.SubjectRef
	PolicyType     string
	ExpressionJSON string
	Compiled       accesspolicy.Compiled
}

// Allows evaluates one exact canonical subject/resource/capability tuple
// against this immutable snapshot. It deliberately does not walk graph
// parents, infer kinds from IDs, expand roles, or consult serving state outside
// the snapshot. Group membership is resolved by the global identity layer and
// should be passed as a group SubjectRef by the caller.
func (s AuthorizationSnapshot) Allows(subject access.SubjectRef, resource access.ResourceRef, capability access.Capability) (bool, error) {
	if err := s.ValidateBound(); err != nil {
		return false, err
	}
	if err := subject.Validate(); err != nil {
		return false, err
	}
	if err := resource.ValidateAgainst(s.project); err != nil {
		return false, err
	}
	if err := access.ValidateCapabilityForKind(resource.Kind(), capability); err != nil {
		return false, err
	}
	for _, grant := range s.grants {
		canonical := grant.Canonical
		if canonical.Subject() == subject && canonical.Resource().ID() == resource.ID() && canonical.Resource().Kind() == resource.Kind() && canonical.Capability() == capability {
			return true, nil
		}
	}
	for _, binding := range s.roleBindings {
		if binding.Subject != subject {
			continue
		}
		for _, captured := range binding.Capabilities {
			if captured == capability {
				return true, nil
			}
		}
	}
	return false, nil
}

// EffectiveCapabilities returns the canonical capabilities that are currently
// effective for the supplied identity subjects in this immutable snapshot.
// Subjects normally contain the authenticated principal followed by the
// groups resolved by the identity layer. Resource grants are evaluated
// against the bound project graph; no mutable repository or role template is
// consulted. The result is deduplicated and returned in canonical contract
// order so callers can safely use it as a token allowlist.
func (s AuthorizationSnapshot) EffectiveCapabilities(subjects []access.SubjectRef) ([]access.Capability, error) {
	if err := s.ValidateBound(); err != nil {
		return nil, err
	}
	validatedSubjects := make([]access.SubjectRef, 0, len(subjects))
	seenSubjects := make(map[access.SubjectRef]struct{}, len(subjects))
	for _, subject := range subjects {
		if err := subject.Validate(); err != nil {
			return nil, err
		}
		if _, seen := seenSubjects[subject]; seen {
			continue
		}
		seenSubjects[subject] = struct{}{}
		validatedSubjects = append(validatedSubjects, subject)
	}
	if len(validatedSubjects) == 0 {
		return []access.Capability{}, nil
	}
	// Compute the capability universe once from the immutable graph. Project
	// role bindings are project-wide, so a captured capability is effective if
	// at least one graph resource supports it; direct grants are already
	// validated against their concrete resource by the snapshot constructor.
	supported := make(map[access.Capability]struct{})
	for _, graphResource := range s.project.Resources() {
		for _, capability := range access.CapabilitiesForKind(graphResource.Kind) {
			supported[capability] = struct{}{}
		}
	}
	allowed := make(map[access.Capability]struct{})
	for _, grant := range s.grants {
		if _, ok := seenSubjects[grant.Canonical.Subject()]; ok {
			allowed[grant.Canonical.Capability()] = struct{}{}
		}
	}
	for _, binding := range s.roleBindings {
		if _, ok := seenSubjects[binding.Subject]; !ok {
			continue
		}
		for _, capability := range binding.Capabilities {
			if _, ok := supported[capability]; ok {
				allowed[capability] = struct{}{}
			}
		}
	}
	result := make([]access.Capability, 0, len(allowed))
	for _, capability := range access.CanonicalCapabilities() {
		if _, ok := allowed[capability]; ok {
			result = append(result, capability)
		}
	}
	return result, nil
}

type snapshotWire struct {
	Identity     graph.ServingIdentity `json:"identity"`
	RoleBindings []roleBindingWire     `json:"roleBindings,omitempty"`
	Grants       []grantWire           `json:"grants,omitempty"`
	DataPolicies []dataPolicyWire      `json:"dataPolicies,omitempty"`
}

type roleBindingWire struct {
	ID           string              `json:"id"`
	Name         string              `json:"name,omitempty"`
	Subject      access.SubjectRef   `json:"subject"`
	Role         access.ProjectRole  `json:"role"`
	Capabilities []access.Capability `json:"capabilities"`
}

type grantWire struct {
	ID         string             `json:"id"`
	Name       string             `json:"name,omitempty"`
	Subject    access.SubjectRef  `json:"subject"`
	Resource   access.ResourceRef `json:"resource"`
	Capability access.Capability  `json:"capability"`
}

type dataPolicyWire struct {
	ID             string             `json:"id"`
	Name           string             `json:"name,omitempty"`
	Resource       access.ResourceRef `json:"resource"`
	Subject        *access.SubjectRef `json:"subject,omitempty"`
	PolicyType     string             `json:"policyType"`
	ExpressionJSON string             `json:"expressionJson"`
}

// NewAuthorizationSnapshot validates every grant and policy against the
// immutable project graph and serving identity.
func NewAuthorizationSnapshot(identity graph.ServingIdentity, project graph.ProjectGraph, grants []Grant, policies []DataPolicy) (AuthorizationSnapshot, error) {
	return NewAuthorizationSnapshotWithRoleBindings(identity, project, nil, grants, policies)
}

// NewAuthorizationSnapshotWithRoleBindings validates explicit project role
// bindings, grants, and policies against one immutable graph and serving
// identity.
func NewAuthorizationSnapshotWithRoleBindings(identity graph.ServingIdentity, project graph.ProjectGraph, roleBindings []RoleBinding, grants []Grant, policies []DataPolicy) (AuthorizationSnapshot, error) {
	if err := project.Validate(); err != nil {
		return AuthorizationSnapshot{}, fmt.Errorf("authorization snapshot project graph: %w", err)
	}
	if err := identity.Validate(); err != nil {
		return AuthorizationSnapshot{}, fmt.Errorf("authorization snapshot identity: %w", err)
	}
	if identity.ProjectID != project.ProjectID() {
		return AuthorizationSnapshot{}, fmt.Errorf("authorization snapshot project %q does not match graph %q", identity.ProjectID, project.ProjectID())
	}
	normalizedBindings := cloneRoleBindings(roleBindings)
	sort.Slice(normalizedBindings, func(i, j int) bool { return normalizedBindings[i].ID < normalizedBindings[j].ID })
	seenBindingIDs := make(map[string]struct{}, len(normalizedBindings))
	seenBindingKeys := make(map[string]struct{}, len(normalizedBindings))
	for i := range normalizedBindings {
		binding := &normalizedBindings[i]
		if binding.ID == "" {
			return AuthorizationSnapshot{}, fmt.Errorf("role binding %d requires id", i)
		}
		if _, ok := seenBindingIDs[binding.ID]; ok {
			return AuthorizationSnapshot{}, fmt.Errorf("duplicate role binding id %q", binding.ID)
		}
		seenBindingIDs[binding.ID] = struct{}{}
		if err := validateRoleBinding(binding); err != nil {
			return AuthorizationSnapshot{}, fmt.Errorf("role binding %q: %w", binding.ID, err)
		}
		key := string(binding.Subject.Kind) + "\x00" + binding.Subject.ID + "\x00" + string(binding.Role)
		if _, ok := seenBindingKeys[key]; ok {
			return AuthorizationSnapshot{}, fmt.Errorf("duplicate role binding subject/role for %q", binding.ID)
		}
		seenBindingKeys[key] = struct{}{}
	}
	normalizedGrants := cloneGrants(grants)
	sort.Slice(normalizedGrants, func(i, j int) bool { return normalizedGrants[i].ID < normalizedGrants[j].ID })
	seenGrantIDs := make(map[string]struct{}, len(normalizedGrants))
	seenGrantKeys := make(map[string]struct{}, len(normalizedGrants))
	for i := range normalizedGrants {
		grants := &normalizedGrants[i]
		if grants.ID == "" {
			return AuthorizationSnapshot{}, fmt.Errorf("grant %d requires id", i)
		}
		if _, ok := seenGrantIDs[grants.ID]; ok {
			return AuthorizationSnapshot{}, fmt.Errorf("duplicate grant id %q", grants.ID)
		}
		seenGrantIDs[grants.ID] = struct{}{}
		if err := grants.Canonical.ValidateAgainst(project); err != nil {
			return AuthorizationSnapshot{}, fmt.Errorf("grant %q: %w", grants.ID, err)
		}
		key := string(grants.Canonical.Subject().Kind) + "\x00" + grants.Canonical.Subject().ID + "\x00" + grants.Canonical.Resource().ID().String() + "\x00" + string(grants.Canonical.Resource().Kind()) + "\x00" + grants.Canonical.Capability().String()
		if _, ok := seenGrantKeys[key]; ok {
			return AuthorizationSnapshot{}, fmt.Errorf("duplicate grant subject/resource/capability for %q", grants.ID)
		}
		seenGrantKeys[key] = struct{}{}
	}
	normalizedPolicies := clonePolicies(policies)
	sort.Slice(normalizedPolicies, func(i, j int) bool { return normalizedPolicies[i].ID < normalizedPolicies[j].ID })
	seenPolicyIDs := make(map[string]struct{}, len(normalizedPolicies))
	for i := range normalizedPolicies {
		policy := &normalizedPolicies[i]
		if policy.ID == "" {
			return AuthorizationSnapshot{}, fmt.Errorf("data policy %d requires id", i)
		}
		if _, ok := seenPolicyIDs[policy.ID]; ok {
			return AuthorizationSnapshot{}, fmt.Errorf("duplicate data policy id %q", policy.ID)
		}
		seenPolicyIDs[policy.ID] = struct{}{}
		if err := policy.Resource.ValidateAgainst(project); err != nil {
			return AuthorizationSnapshot{}, fmt.Errorf("data policy %q resource: %w", policy.ID, err)
		}
		if policy.Subject != nil {
			if err := policy.Subject.Validate(); err != nil {
				return AuthorizationSnapshot{}, fmt.Errorf("data policy %q subject: %w", policy.ID, err)
			}
		}
		compiled, err := accesspolicy.Compile(policy.ID, policy.PolicyType, policy.ExpressionJSON)
		if err != nil {
			return AuthorizationSnapshot{}, fmt.Errorf("data policy %q: %w", policy.ID, err)
		}
		policy.Compiled = compiled
	}
	return AuthorizationSnapshot{identity: identity, roleBindings: normalizedBindings, grants: normalizedGrants, dataPolicies: normalizedPolicies, project: project}, nil
}

// Identity returns the immutable serving identity by value.
func (s AuthorizationSnapshot) Identity() graph.ServingIdentity { return s.identity }

// Project returns the immutable project graph bound to this snapshot. The
// graph is a value with defensive-copy accessors, so callers cannot replace or
// mutate the snapshot's authoritative binding.
func (s AuthorizationSnapshot) Project() graph.ProjectGraph { return s.project }

// Grants returns a defensive copy of the validated grant list.
func (s AuthorizationSnapshot) Grants() []Grant { return cloneGrants(s.grants) }

// RoleBindings returns defensive copies of explicit project assignments.
func (s AuthorizationSnapshot) RoleBindings() []RoleBinding { return cloneRoleBindings(s.roleBindings) }

// DataPolicies returns a defensive copy of the validated policy list.
func (s AuthorizationSnapshot) DataPolicies() []DataPolicy { return clonePolicies(s.dataPolicies) }

// Decode validates strict canonical snapshot JSON against the supplied graph.
// The graph is mandatory: a decoded snapshot must never become installable
// before resource IDs and kinds are checked against authoritative metadata.
func Decode(data []byte, project graph.ProjectGraph) (AuthorizationSnapshot, error) {
	if err := rejectDuplicateKeys(data); err != nil {
		return AuthorizationSnapshot{}, fmt.Errorf("decode authorization snapshot: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire snapshotWire
	if err := decoder.Decode(&wire); err != nil {
		return AuthorizationSnapshot{}, fmt.Errorf("decode authorization snapshot: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return AuthorizationSnapshot{}, errors.New("decode authorization snapshot: trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return AuthorizationSnapshot{}, fmt.Errorf("decode authorization snapshot: trailing data: %w", err)
	}
	roleBindings := make([]RoleBinding, 0, len(wire.RoleBindings))
	for i, item := range wire.RoleBindings {
		if err := item.Subject.Validate(); err != nil {
			return AuthorizationSnapshot{}, fmt.Errorf("role binding %d subject: %w", i, err)
		}
		roleBindings = append(roleBindings, RoleBinding{ID: item.ID, Name: item.Name, Subject: item.Subject, Role: item.Role, Capabilities: append([]access.Capability(nil), item.Capabilities...)})
	}

	grants := make([]Grant, 0, len(wire.Grants))
	for i, item := range wire.Grants {
		canonical, err := access.NewCanonicalGrant(project, item.Subject, item.Resource, item.Capability)
		if err != nil {
			return AuthorizationSnapshot{}, fmt.Errorf("grant %d: %w", i, err)
		}
		grants = append(grants, Grant{ID: item.ID, Name: item.Name, Canonical: canonical})
	}
	policies := make([]DataPolicy, 0, len(wire.DataPolicies))
	for i, item := range wire.DataPolicies {
		compiled, err := accesspolicy.Compile(item.ID, item.PolicyType, item.ExpressionJSON)
		if err != nil {
			return AuthorizationSnapshot{}, fmt.Errorf("data policy %d: %w", i, err)
		}
		if err := item.Resource.ValidateAgainst(project); err != nil {
			return AuthorizationSnapshot{}, fmt.Errorf("data policy %d resource: %w", i, err)
		}
		if item.Subject != nil {
			if err := item.Subject.Validate(); err != nil {
				return AuthorizationSnapshot{}, fmt.Errorf("data policy %d subject: %w", i, err)
			}
		}
		policies = append(policies, DataPolicy{ID: item.ID, Name: item.Name, Resource: item.Resource, Subject: cloneSubject(item.Subject), PolicyType: item.PolicyType, ExpressionJSON: item.ExpressionJSON, Compiled: compiled})
	}
	return NewAuthorizationSnapshotWithRoleBindings(wire.Identity, project, roleBindings, grants, policies)
}

func (s AuthorizationSnapshot) Validate(project graph.ProjectGraph) error {
	if err := s.identity.Validate(); err != nil {
		return fmt.Errorf("authorization snapshot identity: %w", err)
	}
	_, err := NewAuthorizationSnapshotWithRoleBindings(s.identity, project, s.roleBindings, s.grants, s.dataPolicies)
	return err
}

// ValidateBound revalidates the immutable graph binding retained by the
// constructor. It is the install-time check and requires no caller-supplied
// project that could disagree with the snapshot's bound graph.
func (s AuthorizationSnapshot) ValidateBound() error {
	if err := s.identity.Validate(); err != nil {
		return fmt.Errorf("authorization snapshot identity: %w", err)
	}
	_, err := NewAuthorizationSnapshotWithRoleBindings(s.identity, s.project, s.roleBindings, s.grants, s.dataPolicies)
	return err
}

func (s AuthorizationSnapshot) Digest() (string, error) {
	encoded, err := s.MarshalJSON()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func (s AuthorizationSnapshot) MarshalJSON() ([]byte, error) {
	if err := s.identity.Validate(); err != nil {
		return nil, fmt.Errorf("authorization snapshot identity: %w", err)
	}
	if err := s.project.Validate(); err != nil {
		return nil, fmt.Errorf("authorization snapshot project graph: %w", err)
	}
	if s.identity.ProjectID != s.project.ProjectID() {
		return nil, fmt.Errorf("authorization snapshot project %q does not match graph %q", s.identity.ProjectID, s.project.ProjectID())
	}
	roleBindings := make([]roleBindingWire, 0, len(s.roleBindings))
	for _, item := range s.roleBindings {
		if err := validateRoleBinding(&item); err != nil {
			return nil, err
		}
		roleBindings = append(roleBindings, roleBindingWire{ID: item.ID, Name: item.Name, Subject: item.Subject, Role: item.Role, Capabilities: append([]access.Capability(nil), item.Capabilities...)})
	}
	grants := make([]grantWire, 0, len(s.grants))
	for _, item := range s.grants {
		if err := item.Canonical.ValidateAgainst(s.project); err != nil {
			return nil, err
		}
		grants = append(grants, grantWire{ID: item.ID, Name: item.Name, Subject: item.Canonical.Subject(), Resource: item.Canonical.Resource(), Capability: item.Canonical.Capability()})
	}
	policies := make([]dataPolicyWire, 0, len(s.dataPolicies))
	for _, item := range s.dataPolicies {
		if item.ID == "" || item.PolicyType == "" || item.ExpressionJSON == "" {
			return nil, fmt.Errorf("data policy %q is incomplete", item.ID)
		}
		if err := item.Resource.ValidateAgainst(s.project); err != nil {
			return nil, err
		}
		if item.Subject != nil {
			if err := item.Subject.Validate(); err != nil {
				return nil, err
			}
		}
		policies = append(policies, dataPolicyWire{ID: item.ID, Name: item.Name, Resource: item.Resource, Subject: cloneSubject(item.Subject), PolicyType: item.PolicyType, ExpressionJSON: item.ExpressionJSON})
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].ID < grants[j].ID })
	sort.Slice(policies, func(i, j int) bool { return policies[i].ID < policies[j].ID })
	sort.Slice(roleBindings, func(i, j int) bool { return roleBindings[i].ID < roleBindings[j].ID })
	return json.Marshal(snapshotWire{Identity: s.identity, RoleBindings: roleBindings, Grants: grants, DataPolicies: policies})
}

func (s *AuthorizationSnapshot) UnmarshalJSON(data []byte) error {
	return errors.New("authorization snapshot requires Decode with an authoritative project graph")
}

func cloneGrants(input []Grant) []Grant { return append([]Grant(nil), input...) }
func cloneRoleBindings(input []RoleBinding) []RoleBinding {
	output := append([]RoleBinding(nil), input...)
	for i := range output {
		output[i].Capabilities = append([]access.Capability(nil), output[i].Capabilities...)
	}
	return output
}

func validateRoleBinding(binding *RoleBinding) error {
	if err := binding.Subject.Validate(); err != nil {
		return fmt.Errorf("subject: %w", err)
	}
	role, err := access.ParseProjectRole(string(binding.Role))
	if err != nil {
		return err
	}
	binding.Role = role
	want := access.ProjectRoleCapabilities(role)
	if len(binding.Capabilities) != len(want) {
		return fmt.Errorf("capability bundle for role %q must contain exactly %d capabilities", role, len(want))
	}
	seen := make(map[access.Capability]struct{}, len(binding.Capabilities))
	for i, capability := range binding.Capabilities {
		if _, duplicate := seen[capability]; duplicate {
			return fmt.Errorf("capability bundle contains duplicate %q", capability)
		}
		seen[capability] = struct{}{}
		if capability != want[i] {
			return fmt.Errorf("capability bundle for role %q is not the canonical captured bundle", role)
		}
		if err := capability.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func clonePolicies(input []DataPolicy) []DataPolicy {
	output := append([]DataPolicy(nil), input...)
	for i := range output {
		output[i].Subject = cloneSubject(output[i].Subject)
		output[i].Compiled = output[i].Compiled.Clone()
	}
	return output
}
func cloneSubject(input *access.SubjectRef) *access.SubjectRef {
	if input == nil {
		return nil
	}
	copy := *input
	return &copy
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSON(decoder); err != nil {
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

func walkJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			keys := map[string]struct{}{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				canonicalName := strings.ToLower(name)
				if _, exists := keys[canonicalName]; exists {
					return fmt.Errorf("duplicate key %q", name)
				}
				keys[canonicalName] = struct{}{}
				if err := walkJSON(decoder); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walkJSON(decoder); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
	}
	return nil
}
