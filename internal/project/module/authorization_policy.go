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
// retained by a serving generation. The boolean reports whether the complete
// document can be represented by the target policy's role-binding-only model.
// Known legacy constructs are left attached to their active generation rather
// than discarded during upgrade; malformed documents still fail closed.
func DecodeAuthorizationRoleBindingsJSON(encoded string) ([]access.RoleBinding, bool, error) {
	if strings.TrimSpace(encoded) == "" {
		return nil, false, errors.New("active generation has no serving authorization policy document")
	}
	var policy projectmanifest.AccessPolicy
	decoder := json.NewDecoder(bytes.NewBufferString(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return nil, false, fmt.Errorf("decode active serving authorization policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, false, errors.New("active serving authorization policy has trailing JSON")
		}
		return nil, false, fmt.Errorf("decode active serving authorization policy trailing data: %w", err)
	}
	if len(policy.Groups) != 0 || len(policy.Grants) != 0 || len(policy.DataPolicies) != 0 {
		return nil, false, nil
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
			return nil, false, fmt.Errorf("active serving role binding key %q does not match id %q", id, item.ID)
		}
		var subject access.SubjectRef
		var err error
		switch item.Subject.Kind {
		case string(access.SubjectKindPrincipal):
			if item.Subject.Group != "" || item.Subject.Email != "" || item.Subject.DisplayName != "" || item.Subject.Publication != "" {
				return nil, false, fmt.Errorf("active serving role binding %q has non-canonical principal identity", id)
			}
			subject, err = access.NewSubjectRef(access.SubjectKindPrincipal, item.Subject.PrincipalID)
		case string(access.SubjectKindGroup):
			if item.Subject.PrincipalID != "" || item.Subject.Email != "" || item.Subject.DisplayName != "" || item.Subject.Publication != "" {
				return nil, false, fmt.Errorf("active serving role binding %q has non-canonical group identity", id)
			}
			subject, err = access.NewSubjectRef(access.SubjectKindGroup, item.Subject.Group)
		default:
			return nil, false, fmt.Errorf("active serving role binding %q has unsupported subject kind %q", id, item.Subject.Kind)
		}
		if err != nil {
			return nil, false, fmt.Errorf("active serving role binding %q subject: %w", id, err)
		}
		role, err := access.ParseProjectRole(item.Role)
		if err != nil {
			return nil, false, fmt.Errorf("active serving role binding %q role: %w", id, err)
		}
		binding := access.RoleBinding{ID: item.ID, Name: item.Name, Subject: subject, Role: role, Capabilities: access.ProjectRoleCapabilities(role)}
		if err := access.ValidateAuthorizationRoleBinding(binding); err != nil {
			// Serving snapshots historically did not constrain descriptive role-
			// binding names. If every authorization-bearing field is valid under
			// the new target-policy contract, retain the immutable generation and
			// defer migration rather than normalizing or rejecting its legacy name.
			legacy := binding
			legacy.Name = ""
			if access.ValidateAuthorizationRoleBinding(legacy) == nil {
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("active serving role binding %q: %w", id, err)
		}
		bindings = append(bindings, binding)
	}
	return bindings, true, nil
}
