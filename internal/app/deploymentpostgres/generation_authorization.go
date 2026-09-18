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
	manifestPolicy, err := projectmanifest.AccessPolicyFromAuthorizationPolicy(policy)
	if err != nil {
		return fmt.Errorf("%w: project target authorization policy: %v", deploymentnative.ErrInvalid, err)
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

// validateLegacyGenerationAuthorizationSnapshot validates the only policy
// representation native candidates could carry before target policy history:
// the immutable empty serving document and its graph-bound fingerprint. It is
// called only for a recovery-assembled pre-017 admission.
func validateLegacyGenerationAuthorizationSnapshot(admission GenerationAdmissionInput) error {
	legacyPolicy := projectmanifest.AccessPolicy{}
	if !sameBundleObject("{}", admission.Bundle.AccessPolicyJSON) {
		return fmt.Errorf("%w: legacy serving access policy is not the immutable empty document", deploymentnative.ErrConflict)
	}
	identity, err := projectgraph.NewServingIdentity(admission.Bundle.ProjectID, string(admission.Bundle.Environment), release.CandidatePolicyGenerationID)
	if err != nil {
		return fmt.Errorf("%w: legacy serving authorization identity: %v", deploymentnative.ErrInvalid, err)
	}
	snapshot, err := projectmanifest.CompileAuthorizationSnapshot(identity, admission.Graph, legacyPolicy)
	if err != nil {
		return fmt.Errorf("%w: compile legacy serving authorization policy: %v", deploymentnative.ErrInvalid, err)
	}
	digest, err := snapshot.Digest()
	if err != nil {
		return fmt.Errorf("%w: digest legacy serving authorization snapshot: %v", deploymentnative.ErrInvalid, err)
	}
	if digest != admission.Generation.SecurityDomainFingerprint {
		return fmt.Errorf("%w: legacy serving authorization fingerprint differs from immutable policy", deploymentnative.ErrConflict)
	}
	return nil
}
