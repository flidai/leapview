package deploymentpostgres

import (
	"encoding/json"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
)

// validateGenerationAuthorizationSnapshot proves that the target-owned policy
// revision locked by admission is exactly the immutable serving document and
// graph-bound authorization fingerprint retained by the generation.
func validateGenerationAuthorizationSnapshot(policy access.AuthorizationPolicy, admission GenerationAdmissionInput) error {
	manifestPolicy := projectmanifest.AccessPolicy{RoleBindings: make(map[string]projectmanifest.RoleBinding, len(policy.RoleBindings))}
	for _, binding := range policy.RoleBindings {
		if err := access.ValidateAuthorizationRoleBinding(binding); err != nil {
			return fmt.Errorf("%w: target authorization role binding %q: %v", deploymentnative.ErrInvalid, binding.ID, err)
		}
		subject := projectmanifest.Subject{Kind: string(binding.Subject.Kind)}
		switch binding.Subject.Kind {
		case access.SubjectKindPrincipal:
			subject.PrincipalID = binding.Subject.ID
		case access.SubjectKindGroup:
			subject.Group = binding.Subject.ID
		default:
			return fmt.Errorf("%w: unsupported target authorization subject kind %q", deploymentnative.ErrInvalid, binding.Subject.Kind)
		}
		if _, exists := manifestPolicy.RoleBindings[binding.ID]; exists {
			return fmt.Errorf("%w: duplicate target authorization role binding %q", deploymentnative.ErrConflict, binding.ID)
		}
		manifestPolicy.RoleBindings[binding.ID] = projectmanifest.RoleBinding{ID: binding.ID, Name: binding.Name, Role: string(binding.Role), Subject: subject}
	}
	encoded, err := json.Marshal(manifestPolicy)
	if err != nil {
		return fmt.Errorf("%w: encode target authorization policy: %v", deploymentnative.ErrInvalid, err)
	}
	if !sameBundleObject(string(encoded), admission.Bundle.AccessPolicyJSON) {
		return fmt.Errorf("%w: serving access policy differs from target authorization policy", deploymentnative.ErrConflict)
	}
	identity, err := projectgraph.NewServingIdentity(admission.Bundle.ProjectID, string(admission.Bundle.Environment), release.CandidatePolicyGenerationID)
	if err != nil {
		return fmt.Errorf("%w: serving authorization identity: %v", deploymentnative.ErrInvalid, err)
	}
	snapshot, err := projectmanifest.CompileAuthorizationSnapshot(identity, admission.Graph, manifestPolicy)
	if err != nil {
		return fmt.Errorf("%w: compile target authorization policy: %v", deploymentnative.ErrInvalid, err)
	}
	digest, err := snapshot.Digest()
	if err != nil {
		return fmt.Errorf("%w: digest target authorization snapshot: %v", deploymentnative.ErrInvalid, err)
	}
	if digest != admission.Generation.SecurityDomainFingerprint {
		return fmt.Errorf("%w: serving authorization fingerprint differs from target authorization policy", deploymentnative.ErrConflict)
	}
	return nil
}
