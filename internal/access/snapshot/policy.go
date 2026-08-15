// Package snapshot defines the immutable, project-generation-bound access
// policy installed at activation. Authorization identity is the canonical
// graph resource ID plus serving environment and generation; workspace,
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
	Identity     graph.ServingIdentity
	Grants       []Grant
	DataPolicies []DataPolicy
}

// Grant is one graph-validated capability binding. Canonical is immutable
// after construction and cannot be assembled without an authoritative graph.
type Grant struct {
	ID        string
	Name      string
	Canonical access.CanonicalGrant
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

type snapshotWire struct {
	Identity     graph.ServingIdentity `json:"identity"`
	Grants       []grantWire           `json:"grants,omitempty"`
	DataPolicies []dataPolicyWire      `json:"dataPolicies,omitempty"`
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
	if err := project.Validate(); err != nil {
		return AuthorizationSnapshot{}, fmt.Errorf("authorization snapshot project graph: %w", err)
	}
	if identity.ProjectID != project.ProjectID() {
		return AuthorizationSnapshot{}, fmt.Errorf("authorization snapshot project %q does not match graph %q", identity.ProjectID, project.ProjectID())
	}
	if identity.Environment == "" || identity.GenerationID == "" {
		return AuthorizationSnapshot{}, errors.New("authorization snapshot requires environment and generation")
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
	return AuthorizationSnapshot{Identity: identity, Grants: normalizedGrants, DataPolicies: normalizedPolicies}, nil
}

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
	return NewAuthorizationSnapshot(wire.Identity, project, grants, policies)
}

func (s AuthorizationSnapshot) Validate(project graph.ProjectGraph) error {
	_, err := NewAuthorizationSnapshot(s.Identity, project, s.Grants, s.DataPolicies)
	return err
}

func (s AuthorizationSnapshot) Digest() (string, error) {
	encoded, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func (s AuthorizationSnapshot) MarshalJSON() ([]byte, error) {
	grants := make([]grantWire, 0, len(s.Grants))
	for _, item := range s.Grants {
		if err := item.Canonical.Validate(); err != nil {
			return nil, err
		}
		grants = append(grants, grantWire{ID: item.ID, Name: item.Name, Subject: item.Canonical.Subject(), Resource: item.Canonical.Resource(), Capability: item.Canonical.Capability()})
	}
	policies := make([]dataPolicyWire, 0, len(s.DataPolicies))
	for _, item := range s.DataPolicies {
		if item.ID == "" || item.PolicyType == "" || item.ExpressionJSON == "" {
			return nil, fmt.Errorf("data policy %q is incomplete", item.ID)
		}
		policies = append(policies, dataPolicyWire{ID: item.ID, Name: item.Name, Resource: item.Resource, Subject: cloneSubject(item.Subject), PolicyType: item.PolicyType, ExpressionJSON: item.ExpressionJSON})
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].ID < grants[j].ID })
	sort.Slice(policies, func(i, j int) bool { return policies[i].ID < policies[j].ID })
	return json.Marshal(snapshotWire{Identity: s.Identity, Grants: grants, DataPolicies: policies})
}

func (s *AuthorizationSnapshot) UnmarshalJSON(data []byte) error {
	return errors.New("authorization snapshot requires Decode with an authoritative project graph")
}

func cloneGrants(input []Grant) []Grant { return append([]Grant(nil), input...) }
func clonePolicies(input []DataPolicy) []DataPolicy {
	output := append([]DataPolicy(nil), input...)
	for i := range output {
		output[i].Subject = cloneSubject(output[i].Subject)
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
