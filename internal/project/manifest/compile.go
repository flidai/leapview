package manifest

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/project/graph"
)

// CompileAuthorizationSnapshot compiles the canonical project manifest policy
// into an immutable serving snapshot. Resource IDs and kinds are checked
// against the authoritative graph before any grant or policy is admitted.
// Identity resolution (for example, mapping an authored email to a principal
// ID) is outside this compiler; email-only and publication subjects fail
// closed instead of being guessed or translated.
//
// Role bindings are retained as explicit project-scoped assignments. They are
// never expanded into one grant per graph node; the role's canonical capability
// bundle is captured in the immutable snapshot.
func CompileAuthorizationSnapshot(identity graph.ServingIdentity, project graph.ProjectGraph, policy AccessPolicy) (accesssnapshot.AuthorizationSnapshot, error) {
	if err := identity.Validate(); err != nil {
		return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("authorization policy identity: %w", err)
	}
	if err := project.Validate(); err != nil {
		return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("authorization policy graph: %w", err)
	}
	if identity.ProjectID != project.ProjectID() {
		return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("authorization policy project %q does not match graph %q", identity.ProjectID, project.ProjectID())
	}
	roleNames := sortedPolicyKeys(policy.RoleBindings)
	roleBindings := make([]accesssnapshot.RoleBinding, 0, len(roleNames))
	for _, name := range roleNames {
		authored := policy.RoleBindings[name]
		id := strings.TrimSpace(authored.ID)
		if id == "" {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("role binding %q requires stable id", name)
		}
		role, err := access.ParseProjectRole(strings.TrimSpace(authored.Role))
		if err != nil {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("role binding %q role: %w", id, err)
		}
		subject, err := canonicalSubject(authored.Subject)
		if err != nil {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("role binding %q subject: %w", id, err)
		}
		roleBindings = append(roleBindings, accesssnapshot.RoleBinding{ID: id, Name: authored.Name, Subject: subject, Role: role, Capabilities: access.ProjectRoleCapabilities(role)})
	}

	grantNames := sortedPolicyKeys(policy.Grants)
	grants := make([]accesssnapshot.Grant, 0, len(grantNames))
	for _, name := range grantNames {
		authored := policy.Grants[name]
		id := strings.TrimSpace(authored.ID)
		if id == "" {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("grant %q requires stable id", name)
		}
		resource, err := canonicalResource(project, authored.Object.Kind, authored.Object.ID)
		if err != nil {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("grant %q resource: %w", id, err)
		}
		subject, err := canonicalSubject(authored.Subject)
		if err != nil {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("grant %q subject: %w", id, err)
		}
		capability, err := access.ParseCapability(strings.TrimSpace(authored.Capability))
		if err != nil {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("grant %q capability: %w", id, err)
		}
		canonical, err := access.NewCanonicalGrant(project, subject, resource, capability)
		if err != nil {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("grant %q: %w", id, err)
		}
		grants = append(grants, accesssnapshot.Grant{ID: id, Name: authored.Name, Canonical: canonical})
	}

	policyNames := sortedPolicyKeys(policy.DataPolicies)
	dataPolicies := make([]accesssnapshot.DataPolicy, 0, len(policyNames))
	for _, name := range policyNames {
		authored := policy.DataPolicies[name]
		id := strings.TrimSpace(authored.ID)
		if id == "" {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("data policy %q requires stable id", name)
		}
		resource, err := canonicalResource(project, authored.Object.Kind, authored.Object.ID)
		if err != nil {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("data policy %q resource: %w", id, err)
		}
		var subject *access.SubjectRef
		if authored.Subject.Kind != "" {
			resolved, err := canonicalSubject(authored.Subject)
			if err != nil {
				return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("data policy %q subject: %w", id, err)
			}
			subject = &resolved
		} else if authored.Subject.PrincipalID != "" || authored.Subject.Email != "" || authored.Subject.Group != "" || authored.Subject.Publication != "" {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("data policy %q subject requires an explicit kind", id)
		}
		dataPolicies = append(dataPolicies, accesssnapshot.DataPolicy{ID: id, Name: authored.Name, Resource: resource, Subject: subject, PolicyType: strings.TrimSpace(authored.PolicyType), ExpressionJSON: authored.ExpressionJSON})
	}
	return accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, project, roleBindings, grants, dataPolicies)
}

func sortedPolicyKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func canonicalResource(project graph.ProjectGraph, kind, id string) (access.ResourceRef, error) {
	parsedKind, err := graph.ParseKind(strings.TrimSpace(kind))
	if err != nil {
		return access.ResourceRef{}, err
	}
	ref, err := access.NewResourceRef(graph.ResourceID(strings.TrimSpace(id)), parsedKind)
	if err != nil {
		return access.ResourceRef{}, err
	}
	if err := ref.ValidateAgainst(project); err != nil {
		return access.ResourceRef{}, err
	}
	return ref, nil
}

func canonicalSubject(subject Subject) (access.SubjectRef, error) {
	kind := strings.TrimSpace(subject.Kind)
	var subjectKind access.SubjectKind
	switch kind {
	case string(access.SubjectKindPrincipal), "service_principal":
		subjectKind = access.SubjectKindPrincipal
	case string(access.SubjectKindGroup):
		subjectKind = access.SubjectKindGroup
	default:
		return access.SubjectRef{}, fmt.Errorf("unsupported subject kind %q", kind)
	}
	if strings.TrimSpace(subject.Email) != "" || strings.TrimSpace(subject.Publication) != "" {
		return access.SubjectRef{}, fmt.Errorf("subject %q must use an explicit principalId or group id", kind)
	}
	id := strings.TrimSpace(subject.PrincipalID)
	if subjectKind == access.SubjectKindGroup {
		if id != "" {
			return access.SubjectRef{}, fmt.Errorf("group subject cannot include principalId")
		}
		id = strings.TrimSpace(subject.Group)
	} else if strings.TrimSpace(subject.Group) != "" {
		return access.SubjectRef{}, fmt.Errorf("principal subject cannot include group")
	}
	return access.NewSubjectRef(subjectKind, id)
}
