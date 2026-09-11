package module

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

// DecodeAuthorizationRoleBindingsJSON decodes the immutable project policy
// retained by a serving generation. Callers performing legacy target-policy
// upgrades receive only canonical role bindings; unsupported mutable policy
// constructs fail closed at the Project module boundary.
func DecodeAuthorizationRoleBindingsJSON(encoded string) ([]access.RoleBinding, error) {
	if strings.TrimSpace(encoded) == "" {
		return nil, errors.New("active generation has no serving authorization policy document")
	}
	var policy projectmanifest.AccessPolicy
	decoder := json.NewDecoder(bytes.NewBufferString(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return nil, fmt.Errorf("decode active serving authorization policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("active serving authorization policy has trailing JSON")
		}
		return nil, fmt.Errorf("decode active serving authorization policy trailing data: %w", err)
	}
	if len(policy.Groups) != 0 || len(policy.Grants) != 0 || len(policy.DataPolicies) != 0 {
		return nil, errors.New("active serving authorization policy contains unsupported groups, grants, or data policies")
	}
	ids := make([]string, 0, len(policy.RoleBindings))
	for id := range policy.RoleBindings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	bindings := make([]access.RoleBinding, 0, len(ids))
	for _, id := range ids {
		item := policy.RoleBindings[id]
		if item.ID != id {
			return nil, fmt.Errorf("active serving role binding key %q does not match id %q", id, item.ID)
		}
		var subject access.SubjectRef
		var err error
		switch item.Subject.Kind {
		case string(access.SubjectKindPrincipal):
			if item.Subject.Group != "" || item.Subject.Email != "" || item.Subject.DisplayName != "" || item.Subject.Publication != "" {
				return nil, fmt.Errorf("active serving role binding %q has non-canonical principal identity", id)
			}
			subject, err = access.NewSubjectRef(access.SubjectKindPrincipal, item.Subject.PrincipalID)
		case string(access.SubjectKindGroup):
			if item.Subject.PrincipalID != "" || item.Subject.Email != "" || item.Subject.DisplayName != "" || item.Subject.Publication != "" {
				return nil, fmt.Errorf("active serving role binding %q has non-canonical group identity", id)
			}
			subject, err = access.NewSubjectRef(access.SubjectKindGroup, item.Subject.Group)
		default:
			return nil, fmt.Errorf("active serving role binding %q has unsupported subject kind %q", id, item.Subject.Kind)
		}
		if err != nil {
			return nil, fmt.Errorf("active serving role binding %q subject: %w", id, err)
		}
		role, err := access.ParseProjectRole(item.Role)
		if err != nil {
			return nil, fmt.Errorf("active serving role binding %q role: %w", id, err)
		}
		binding := access.RoleBinding{ID: item.ID, Name: item.Name, Subject: subject, Role: role, Capabilities: access.ProjectRoleCapabilities(role)}
		if err := access.ValidateAuthorizationRoleBinding(binding); err != nil {
			return nil, fmt.Errorf("active serving role binding %q: %w", id, err)
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}
