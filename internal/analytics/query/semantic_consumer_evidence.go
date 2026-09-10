package query

import (
	"fmt"

	"github.com/flidai/leapview/internal/analytics/resultidentity"
)

// CacheIdentity returns detached, value-free authorization evidence for a
// protected semantic-access consumer. Public consumers deliberately return a
// nil identity because they have no principal-bound authorization decision.
// The existing authority path re-reads coherent state and rejects a decision
// that differs from this consumer's pinned admission identity.
func (consumer *SemanticAccessConsumer) CacheIdentity() (*resultidentity.SemanticAccessIdentity, error) {
	if consumer == nil || consumer.planner == nil {
		return nil, fmt.Errorf("semantic access consumer is required")
	}
	if !consumer.protected {
		return nil, nil
	}
	policy, decision, _, err := consumer.authority()
	if err != nil {
		return nil, err
	}
	if policy == nil || decision == nil {
		return nil, fmt.Errorf("semantic access authority returned no protected decision")
	}
	if consumer.config.PublicationPolicy == (resultidentity.PublicationPolicyIdentity{}) {
		return nil, fmt.Errorf("semantic access publication policy identity is unavailable")
	}
	if policy.SemanticModelID() != consumer.config.ModelID || decision.SemanticModelID != consumer.config.ModelID || decision.PolicyDigest != policy.Digest() {
		return nil, fmt.Errorf("semantic access authority identity is inconsistent with consumer")
	}
	identity := resultidentity.SemanticAccessIdentity{
		ProjectID:                      consumer.config.ProjectID,
		Environment:                    consumer.config.Environment,
		InstanceID:                     decision.InstanceID,
		ModelID:                        decision.SemanticModelID,
		Generation:                     decision.SemanticGeneration,
		PrincipalID:                    decision.PrincipalID,
		ActorID:                        decision.ActorID,
		Profile:                        decision.Profile,
		RegistryProfile:                decision.Registry.Profile,
		RegistryRevision:               decision.Registry.Revision,
		RegistryDigest:                 decision.Registry.Digest,
		ControlProfile:                 decision.Control.Profile,
		ControlRevision:                decision.Control.Revision,
		ControlDigest:                  decision.Control.Digest,
		EffectiveAttributeDigest:       decision.EffectiveAttributeDigest,
		PolicyDigest:                   decision.PolicyDigest,
		DecisionDigest:                 decision.IdentityDigest,
		DirectAssignmentEvidenceDigest: decision.DirectAssignmentEvidenceDigest,
		TrustedClaimEvidenceDigest:     decision.TrustedClaimEvidenceDigest,
		PublicationPolicy:              consumer.config.PublicationPolicy,
	}
	return resultidentity.NewSemanticAccessIdentity(identity)
}

// EvidenceJSON returns the deterministic raw-value-free decision projection
// used for semantic-access audit evidence. Public consumers deliberately
// return nil because they have no protected decision to project.
func (consumer *SemanticAccessConsumer) EvidenceJSON() ([]byte, error) {
	if consumer == nil || consumer.planner == nil {
		return nil, fmt.Errorf("semantic access consumer is required")
	}
	if !consumer.protected {
		return nil, nil
	}
	_, decision, _, err := consumer.authority()
	if err != nil {
		return nil, err
	}
	return semanticAccessDecisionIdentity(decision)
}
