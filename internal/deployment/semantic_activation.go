package deployment

import (
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/analytics/resultidentity"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

const (
	SemanticActivationEvidenceVersion = 1
	SemanticAccessProfile             = "leapview.semantic-access/v1"
	SemanticBarrierProfile            = "planir.security-barrier/v1"
	SemanticConsumerProfile           = "semantic-access-consumer/v1"
	SemanticCacheProfile              = "semantic-access-cache/v1"
	SemanticAuditProfile              = "semantic-access-audit/v1"
)

// SemanticActivationModelEvidence is the immutable, value-free authorization
// authority selected for one protected SemanticModel while a delivery plan is
// created. Deployment approval binds this value through the plan evidence
// digest; activation re-derives it from current authorities before commit.
type SemanticActivationModelEvidence struct {
	ModelID                string                                   `json:"modelId"`
	ModelDigest            string                                   `json:"modelDigest"`
	PolicyDefinitionDigest string                                   `json:"policyDefinitionDigest"`
	PublicationPolicy      resultidentity.PublicationPolicyIdentity `json:"publicationPolicy"`
}

// SemanticActivationEvidence binds protected activation to the FAI-622
// publication, registry/control snapshots, and the already-qualified runtime
// enforcement profiles. It deliberately excludes subject values and decision
// digests, which remain request-bound FAI-639/645 evidence.
type SemanticActivationEvidence struct {
	Version          int                               `json:"version"`
	InstanceID       string                            `json:"instanceId"`
	RegistryProfile  string                            `json:"registryProfile"`
	RegistryRevision int64                             `json:"registryRevision"`
	RegistryDigest   string                            `json:"registryDigest"`
	ControlProfile   string                            `json:"controlProfile"`
	ControlRevision  int64                             `json:"controlRevision"`
	ControlDigest    string                            `json:"controlDigest"`
	BarrierProfile   string                            `json:"barrierProfile"`
	ConsumerProfile  string                            `json:"consumerProfile"`
	CacheProfile     string                            `json:"cacheProfile"`
	AuditProfile     string                            `json:"auditProfile"`
	Models           []SemanticActivationModelEvidence `json:"models"`
	Digest           string                            `json:"digest"`
}

// NewSemanticActivationEvidence canonicalizes, validates, and seals evidence.
func NewSemanticActivationEvidence(value SemanticActivationEvidence) (SemanticActivationEvidence, error) {
	value.Version = SemanticActivationEvidenceVersion
	value.Models = append([]SemanticActivationModelEvidence(nil), value.Models...)
	sort.Slice(value.Models, func(i, j int) bool { return value.Models[i].ModelID < value.Models[j].ModelID })
	value.Digest = ""
	if err := value.validate(false); err != nil {
		return SemanticActivationEvidence{}, err
	}
	digest, err := canonicalJSONDigest(value)
	if err != nil {
		return SemanticActivationEvidence{}, err
	}
	value.Digest = digest
	return value, value.Validate()
}

func (value SemanticActivationEvidence) Validate() error {
	if err := value.validate(true); err != nil {
		return err
	}
	want := value
	want.Digest = ""
	digest, err := canonicalJSONDigest(want)
	if err != nil || digest != value.Digest {
		return fmt.Errorf("%w: semantic activation evidence digest mismatch", ErrDeliveryConflict)
	}
	return nil
}

func (value SemanticActivationEvidence) validate(requireDigest bool) error {
	if value.Version != SemanticActivationEvidenceVersion || value.InstanceID == "" {
		return fmt.Errorf("%w: semantic activation identity is incomplete", ErrDeliveryInvalid)
	}
	if value.RegistryProfile != SemanticAccessProfile || value.ControlProfile != SemanticAccessProfile ||
		value.RegistryRevision <= 0 || value.ControlRevision <= 0 {
		return fmt.Errorf("%w: semantic activation authority state is incomplete", ErrDeliveryInvalid)
	}
	for label, digest := range map[string]string{"registry": value.RegistryDigest, "control": value.ControlDigest} {
		if err := platformdigest.ValidateSHA256Identity(digest); err != nil {
			return fmt.Errorf("%w: semantic activation %s digest: %v", ErrDeliveryInvalid, label, err)
		}
	}
	if value.BarrierProfile != SemanticBarrierProfile || value.ConsumerProfile != SemanticConsumerProfile ||
		value.CacheProfile != SemanticCacheProfile || value.AuditProfile != SemanticAuditProfile {
		return fmt.Errorf("%w: semantic activation enforcement profile is unsupported", ErrDeliveryInvalid)
	}
	if len(value.Models) == 0 {
		return fmt.Errorf("%w: semantic activation requires protected model evidence", ErrDeliveryInvalid)
	}
	for index, model := range value.Models {
		if model.ModelID == "" || model.ModelID != model.PublicationPolicy.Candidate.AuthoredID ||
			model.PublicationPolicy.Candidate.InstanceID != value.InstanceID ||
			model.PublicationPolicy.Candidate.ResourceKind != "semantic_model" {
			return fmt.Errorf("%w: semantic activation model identity is inconsistent", ErrDeliveryInvalid)
		}
		if index > 0 && value.Models[index-1].ModelID >= model.ModelID {
			return fmt.Errorf("%w: semantic activation models are not strictly ordered at %q", ErrDeliveryInvalid, model.ModelID)
		}
		if err := platformdigest.ValidateSHA256Identity(model.ModelDigest); err != nil {
			return fmt.Errorf("%w: semantic activation model digest: %v", ErrDeliveryInvalid, err)
		}
		if err := platformdigest.ValidateSHA256Identity(model.PolicyDefinitionDigest); err != nil {
			return fmt.Errorf("%w: semantic activation policy definition digest: %v", ErrDeliveryInvalid, err)
		}
		if err := model.PublicationPolicy.Validate(); err != nil {
			return fmt.Errorf("%w: semantic activation publication policy: %v", ErrDeliveryInvalid, err)
		}
	}
	if requireDigest {
		if err := platformdigest.ValidateSHA256Identity(value.Digest); err != nil {
			return fmt.Errorf("%w: semantic activation evidence digest: %v", ErrDeliveryInvalid, err)
		}
	}
	return nil
}
