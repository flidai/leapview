package identityledger

import (
	"fmt"
	"reflect"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/project/contractversion"
)

const PolicyActivationReferenceVersion = 1

// PolicyActivationReference is an exact reference to existing immutable
// publication and policy evidence. It is neither a publication identity nor
// an approval; delivery retains it inside its canonical plan evidence.
type PolicyActivationReference struct {
	Version      int                        `json:"version"`
	Publication  PolicyPublicationIdentity  `json:"publication"`
	BaselineKind PolicyBaselineKind         `json:"baselineKind"`
	Baseline     *PolicyPublicationIdentity `json:"baseline,omitempty"`
	// LifecycleSequence and ActiveBundleID identify the current ACTIVE base
	// observed during planning. The immutable publication's original lifecycle
	// binding remains covered by PolicyEvidenceDigest and is validated again by
	// Matches; these current fields may advance across unchanged deployments.
	LifecycleSequence     int64                                  `json:"lifecycleSequence"`
	ActiveBundleID        string                                 `json:"activeBundleId"`
	GraphDigest           string                                 `json:"graphDigest"`
	PolicyEvidenceVersion int                                    `json:"policyEvidenceVersion"`
	PolicyEvidenceDigest  string                                 `json:"policyEvidenceDigest"`
	ApprovalState         PolicyApprovalState                    `json:"approvalState"`
	RegistryTypes         *contractversion.SemanticRegistryTypes `json:"registryTypes,omitempty"`
}

func (r PolicyActivationReference) Clone() PolicyActivationReference {
	if r.Baseline != nil {
		baseline := *r.Baseline
		r.Baseline = &baseline
	}
	if r.RegistryTypes != nil {
		registry := r.RegistryTypes.Clone()
		r.RegistryTypes = &registry
	}
	return r
}

func NewPolicyActivationReference(publication ContractPublication, graphDigest string) (PolicyActivationReference, error) {
	decision, err := publication.PolicyDecision()
	if err != nil {
		return PolicyActivationReference{}, err
	}
	if publication.Validation.PolicyEvidence == nil {
		return PolicyActivationReference{}, fmt.Errorf("%w: publication policy evidence is unavailable", ErrPolicyEvidenceInvalid)
	}
	result := PolicyActivationReference{
		Version: PolicyActivationReferenceVersion, Publication: decision.Publication,
		BaselineKind: decision.BaselineKind, LifecycleSequence: decision.LifecycleSequence,
		ActiveBundleID: decision.ActiveBundleID, GraphDigest: graphDigest,
		PolicyEvidenceVersion: publication.Validation.PolicyEvidence.Version,
		PolicyEvidenceDigest:  decision.EvidenceDigest, ApprovalState: decision.ApprovalState,
	}
	if decision.BaselineKind == PolicyBaselineExisting {
		baseline := decision.Baseline
		result.Baseline = &baseline
	}
	if decision.RegistryTypes != nil {
		registry := decision.RegistryTypes.Clone()
		result.RegistryTypes = &registry
	}
	return result, result.Validate()
}

func (r PolicyActivationReference) Validate() error {
	if r.Version != PolicyActivationReferenceVersion {
		return fmt.Errorf("%w: unsupported activation reference version", ErrPolicyEvidenceInvalid)
	}
	if err := r.Publication.Validate(); err != nil {
		return err
	}
	if r.BaselineKind == PolicyBaselineGenesis {
		if r.Baseline != nil {
			return fmt.Errorf("%w: genesis activation reference carries a baseline", ErrPolicyEvidenceInvalid)
		}
	} else if r.BaselineKind == PolicyBaselineExisting {
		if r.Baseline == nil || r.Baseline.Validate() != nil || r.Baseline.InstanceID != r.Publication.InstanceID || r.Baseline.AuthoredID != r.Publication.AuthoredID || r.Baseline.ResourceKind != r.Publication.ResourceKind {
			return fmt.Errorf("%w: activation baseline reference is invalid", ErrPolicyEvidenceInvalid)
		}
	} else {
		return fmt.Errorf("%w: activation baseline kind is invalid", ErrPolicyEvidenceInvalid)
	}
	if r.LifecycleSequence <= 0 || !validPolicyToken(r.ActiveBundleID, 255) {
		return fmt.Errorf("%w: activation lifecycle evidence is incomplete", ErrPolicyEvidenceInvalid)
	}
	if err := platformdigest.ValidateSHA256Identity(r.GraphDigest); err != nil {
		return fmt.Errorf("%w: activation graph digest: %v", ErrPolicyEvidenceInvalid, err)
	}
	if r.PolicyEvidenceVersion != PolicyEvidenceVersion && r.PolicyEvidenceVersion != RegistryPolicyEvidenceVersion {
		return fmt.Errorf("%w: activation policy evidence version is unsupported", ErrPolicyEvidenceInvalid)
	}
	if (r.PolicyEvidenceVersion == RegistryPolicyEvidenceVersion) != (r.RegistryTypes != nil) {
		return fmt.Errorf("%w: activation registry evidence does not match its policy evidence version", ErrPolicyEvidenceInvalid)
	}
	if err := platformdigest.ValidateSHA256Identity(r.PolicyEvidenceDigest); err != nil {
		return fmt.Errorf("%w: activation policy digest: %v", ErrPolicyEvidenceInvalid, err)
	}
	if r.ApprovalState != PolicyApprovalRequired && r.ApprovalState != PolicyApprovalNotRequired {
		return fmt.Errorf("%w: activation approval state is invalid", ErrPolicyEvidenceInvalid)
	}
	if r.RegistryTypes != nil {
		if err := r.RegistryTypes.Validate(); err != nil {
			return fmt.Errorf("%w: activation registry evidence: %v", ErrPolicyEvidenceInvalid, err)
		}
		if r.RegistryTypes.InstanceID != r.Publication.InstanceID {
			return fmt.Errorf("%w: activation registry instance differs from publication", ErrPolicyEvidenceInvalid)
		}
	}
	return nil
}

func (r PolicyActivationReference) Matches(publication ContractPublication, graphDigest string) error {
	expected, err := NewPolicyActivationReference(publication, graphDigest)
	if err != nil {
		return err
	}
	// The exact publication retains its authored lifecycle binding inside the
	// policy evidence digest. Compare it while preserving the independently
	// observed current ACTIVE base carried by this plan.
	expected.LifecycleSequence = r.LifecycleSequence
	expected.ActiveBundleID = r.ActiveBundleID
	if !reflect.DeepEqual(r, expected) {
		return fmt.Errorf("%w: activation reference differs from immutable publication evidence", ErrPolicyEvidenceConflict)
	}
	return nil
}
