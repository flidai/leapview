package resultidentity

import (
	"fmt"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/semanticvalue"
)

// SemanticAccessIdentity is detached, value-free authorization evidence for
// a protected result dependency. It carries identities and digests only; raw
// attribute values, claims, and lowered predicate values are intentionally not
// part of this contract.
//
// ActorID and the direct/trusted evidence digests are optional because some
// authenticated subjects have no delegated actor or corresponding evidence
// source. All other fields identify the protected serving decision.
type SemanticAccessIdentity struct {
	ProjectID                      string `json:"projectId"`
	Environment                    string `json:"environment"`
	InstanceID                     string `json:"instanceId"`
	ModelID                        string `json:"modelId"`
	Generation                     string `json:"generation"`
	PrincipalID                    string `json:"principalId"`
	ActorID                        string `json:"actorId,omitempty"`
	Profile                        string `json:"profile"`
	RegistryProfile                string `json:"registryProfile"`
	RegistryRevision               int64  `json:"registryRevision"`
	RegistryDigest                 string `json:"registryDigest"`
	ControlProfile                 string `json:"controlProfile"`
	ControlRevision                int64  `json:"controlRevision"`
	ControlDigest                  string `json:"controlDigest"`
	EffectiveAttributeDigest       string `json:"effectiveAttributeDigest"`
	PolicyDigest                   string `json:"policyDigest"`
	DecisionDigest                 string `json:"decisionDigest"`
	DirectAssignmentEvidenceDigest string `json:"directAssignmentEvidenceDigest,omitempty"`
	TrustedClaimEvidenceDigest     string `json:"trustedClaimEvidenceDigest,omitempty"`
	// PublicationPolicy is the exact Project-owned publication and policy
	// evidence selected for this protected semantic result. It is required for
	// protected cache identity; public dependencies use a nil identity and do
	// not serialize this field.
	PublicationPolicy PublicationPolicyIdentity `json:"publicationPolicy"`
}

// NewSemanticAccessIdentity validates and returns a detached copy of input.
// The returned value is safe to retain independently of the caller's input;
// callers should treat its exported fields as immutable identity data.
func NewSemanticAccessIdentity(input SemanticAccessIdentity) (*SemanticAccessIdentity, error) {
	return normalizeSemanticAccess(&input)
}

// Validate checks the closed-world semantic access identity contract.
func (identity SemanticAccessIdentity) Validate() error {
	if err := projectgraph.ValidateServingScope(projectgraph.ResourceID(identity.ProjectID), identity.Environment); err != nil {
		return fmt.Errorf("%w: semantic access serving scope: %v", ErrInvalidDependency, err)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "project ID", value: identity.ProjectID},
		{name: "environment", value: identity.Environment},
		{name: "instance ID", value: identity.InstanceID},
		{name: "model ID", value: identity.ModelID},
		{name: "generation", value: identity.Generation},
		{name: "principal ID", value: identity.PrincipalID},
		{name: "profile", value: identity.Profile},
		{name: "registry profile", value: identity.RegistryProfile},
		{name: "control profile", value: identity.ControlProfile},
	} {
		if err := validateOpaqueText(field.value); err != nil {
			return fmt.Errorf("%w: semantic access %s: %v", ErrInvalidDependency, field.name, err)
		}
	}
	if identity.Profile != semanticvalue.Profile || identity.RegistryProfile != semanticvalue.Profile || identity.ControlProfile != semanticvalue.Profile {
		return fmt.Errorf("%w: semantic access profiles must use %q", ErrInvalidDependency, semanticvalue.Profile)
	}
	if identity.ActorID != "" {
		if err := validateOpaqueText(identity.ActorID); err != nil {
			return fmt.Errorf("%w: semantic access actor ID: %v", ErrInvalidDependency, err)
		}
	}
	if identity.RegistryRevision <= 0 || identity.ControlRevision <= 0 {
		return fmt.Errorf("%w: semantic access registry and control revisions must be positive", ErrInvalidDependency)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "registry digest", value: identity.RegistryDigest},
		{name: "control digest", value: identity.ControlDigest},
		{name: "effective attribute digest", value: identity.EffectiveAttributeDigest},
		{name: "policy digest", value: identity.PolicyDigest},
		{name: "decision digest", value: identity.DecisionDigest},
	} {
		if err := validateDigest(field.name, field.value); err != nil {
			return err
		}
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "direct assignment evidence digest", value: identity.DirectAssignmentEvidenceDigest},
		{name: "trusted claim evidence digest", value: identity.TrustedClaimEvidenceDigest},
	} {
		if field.value == "" {
			continue
		}
		if err := validateDigest(field.name, field.value); err != nil {
			return err
		}
	}
	if err := identity.PublicationPolicy.Validate(); err != nil {
		return fmt.Errorf("%w: semantic access publication/policy identity: %v", ErrInvalidDependency, err)
	}
	if identity.PublicationPolicy.Candidate.ResourceKind != string(projectgraph.KindSemanticModel) ||
		identity.PublicationPolicy.Candidate.InstanceID != identity.InstanceID ||
		identity.PublicationPolicy.Candidate.AuthoredID != identity.ModelID ||
		identity.PublicationPolicy.Policy.RequiresSecurityApproval != (identity.PublicationPolicy.Policy.SecurityImpact == "widening") {
		return fmt.Errorf("%w: semantic access publication/policy identity does not match consumer", ErrInvalidDependency)
	}
	return nil
}

func normalizeSemanticAccess(input *SemanticAccessIdentity) (*SemanticAccessIdentity, error) {
	if input == nil {
		return nil, nil
	}
	clone := *input
	if err := clone.Validate(); err != nil {
		return nil, err
	}
	return &clone, nil
}
