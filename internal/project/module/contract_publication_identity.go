package module

import (
	"fmt"

	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/project/contractpublication"
)

// PublicationPolicyIdentityFromContractPublication validates and projects one
// persisted FAI-622 publication into the detached analytics cache identity.
//
// baseline is nil only for an explicit genesis publication. For every update
// it must be the exact immutable baseline used to derive the publication's
// policy evidence. ValidateHistoricalPublication remains the sole authority
// for replaying policy classification, baseline binding, and widening
// approval evidence; this adapter only copies its already-validated summary.
func PublicationPolicyIdentityFromContractPublication(candidate contractpublication.ContractPublication, baseline *contractpublication.ContractPublication) (resultidentity.PublicationPolicyIdentity, error) {
	context := contractpublication.PolicyContext{BaselineKind: contractpublication.BaselineGenesis}
	if baseline != nil {
		context.BaselineKind = contractpublication.BaselineExisting
		context.Existing = baseline
	}
	if err := contractpublication.ValidateHistoricalPublication(context, candidate); err != nil {
		return resultidentity.PublicationPolicyIdentity{}, fmt.Errorf("validate historical contract publication: %w", err)
	}
	evidence := candidate.Validation.PolicyEvidence
	if evidence == nil {
		// ValidateHistoricalPublication currently guarantees this. Keep the
		// check at the conversion boundary so a future domain relaxation cannot
		// turn absent policy evidence into a cache identity.
		return resultidentity.PublicationPolicyIdentity{}, fmt.Errorf("historical contract publication has no policy evidence")
	}
	policy := *evidence
	approvalDigest := ""
	if policy.RequiresSecurityApproval {
		approval := candidate.Validation.ApprovalEvidence
		if approval == nil || approval.EvidenceDigest == "" {
			return resultidentity.PublicationPolicyIdentity{}, fmt.Errorf("historical widening publication has no approval evidence digest")
		}
		approvalDigest = approval.EvidenceDigest
	}
	identity := resultidentity.PublicationPolicyIdentity{
		Candidate: detachedPublicationIdentity(candidate.Identity()),
		Policy: resultidentity.PolicyIdentity{
			BaselineKind:             string(policy.BaselineKind),
			Baseline:                 detachedPublicationIdentity(policy.Baseline),
			Class:                    string(policy.Classification.Class),
			Compatibility:            string(policy.Classification.Compatibility),
			StructuralCompatibility:  string(policy.Classification.StructuralCompatibility),
			SemanticCompatibility:    string(policy.Classification.SemanticCompatibility),
			SecurityImpact:           string(policy.Classification.SecurityImpact),
			RequiresMajor:            policy.Classification.RequiresMajor,
			RequiresSecurityApproval: policy.RequiresSecurityApproval,
			PolicyEvidenceDigest:     policy.EvidenceDigest,
			ApprovalEvidenceDigest:   approvalDigest,
		},
	}
	if err := identity.Validate(); err != nil {
		return resultidentity.PublicationPolicyIdentity{}, fmt.Errorf("validate detached publication policy identity: %w", err)
	}
	return identity, nil
}

func detachedPublicationIdentity(identity contractpublication.PublicationIdentity) resultidentity.PublicationIdentity {
	return resultidentity.PublicationIdentity{
		InstanceID: identity.InstanceID, AuthoredID: identity.AuthoredID.String(),
		ResourceKind: string(identity.ResourceKind), Version: identity.Version,
		VersionBaseline: identity.VersionBaseline, ProjectionProfile: identity.ProjectionProfile,
		Digest: identity.Digest,
	}
}
