package access

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/project/graph"
)

// AuthorizationGrant is target-owned policy intent. The resource is validated
// against the candidate graph when the policy is compiled, before admission.
// It never becomes serving authority without that graph-bound compilation.
type AuthorizationGrant struct {
	ID         string      `json:"id"`
	Name       string      `json:"name,omitempty"`
	Subject    SubjectRef  `json:"subject"`
	Resource   ResourceRef `json:"resource"`
	Capability Capability  `json:"capability"`
}

type AuthorizationGrantInput struct {
	Scope            AuthorizationPolicyScope
	Grant            AuthorizationGrant
	ExpectedRevision int64
	IdempotencyKey   string
}

type AuthorizationGrantWriter interface {
	UpsertAuthorizationGrant(context.Context, AuthorizationGrantInput) (AuthorizationPolicy, error)
}

func ValidateAuthorizationGrant(grant AuthorizationGrant) error {
	invalid := func(err error) error { return fmt.Errorf("%w: grant: %v", ErrAuthorizationPolicyInvalidBinding, err) }
	if grant.ID == "" || strings.TrimSpace(grant.ID) != grant.ID || len(grant.ID) > 255 || strings.ContainsAny(grant.ID, "\x00\r\n") {
		return invalid(fmt.Errorf("invalid id"))
	}
	if strings.TrimSpace(grant.Name) != grant.Name || len(grant.Name) > 255 || strings.ContainsAny(grant.Name, "\x00\r\n") {
		return invalid(fmt.Errorf("invalid name"))
	}
	if err := grant.Subject.Validate(); err != nil {
		return invalid(err)
	}
	if err := grant.Resource.Validate(); err != nil {
		return invalid(err)
	}
	if _, err := ParseCapability(string(grant.Capability)); err != nil {
		return invalid(err)
	}
	// NewCanonicalGrant performs authoritative graph validation at compilation;
	// reject kind/capability combinations here as well.
	if !SupportsCapability(grant.Resource.Kind(), grant.Capability) {
		return invalid(fmt.Errorf("capability not allowed for %s", grant.Resource.Kind()))
	}
	return nil
}

func canonicalAuthorizationGrants(input []AuthorizationGrant) ([]AuthorizationGrant, error) {
	grants := append([]AuthorizationGrant(nil), input...)
	sort.Slice(grants, func(i, j int) bool { return grants[i].ID < grants[j].ID })
	ids := map[string]bool{}
	keys := map[string]bool{}
	for _, grant := range grants {
		if err := ValidateAuthorizationGrant(grant); err != nil {
			return nil, err
		}
		key := string(grant.Subject.Kind) + "\x00" + grant.Subject.ID + "\x00" + string(grant.Resource.Kind()) + "\x00" + string(grant.Resource.ID()) + "\x00" + string(grant.Capability)
		if ids[grant.ID] || keys[key] {
			return nil, fmt.Errorf("%w: duplicate grant %q", ErrAuthorizationPolicyConflict, grant.ID)
		}
		ids[grant.ID] = true
		keys[key] = true
	}
	return grants, nil
}

// ValidateAgainst binds policy intent to an authoritative candidate graph.
func (grant AuthorizationGrant) ValidateAgainst(project graph.ProjectGraph) error {
	if err := ValidateAuthorizationGrant(grant); err != nil {
		return err
	}
	_, err := NewCanonicalGrant(project, grant.Subject, grant.Resource, grant.Capability)
	return err
}
