package deployment

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/analytics/resultidentity"
)

func TestSemanticActivationEvidenceIsDeterministicAndFailClosed(t *testing.T) {
	digest := func(value string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value))) }
	model := func(id, marker string) SemanticActivationModelEvidence {
		return SemanticActivationModelEvidence{
			ModelID: id, ModelDigest: digest(marker + "-model"), CompiledPolicyDigest: digest(marker + "-policy"),
			PublicationPolicy: resultidentity.PublicationPolicyIdentity{
				Candidate: resultidentity.PublicationIdentity{InstanceID: "instance-1", AuthoredID: id, ResourceKind: "semantic_model", Version: "1.0.0", VersionBaseline: "1.0.0", ProjectionProfile: "leapview.contract/v1", Digest: digest(marker + "-publication")},
				Policy:    resultidentity.PolicyIdentity{BaselineKind: "genesis", Class: "compatible", Compatibility: "additive", StructuralCompatibility: "additive", SemanticCompatibility: "additive", SecurityImpact: "none", PolicyEvidenceDigest: digest(marker + "-evidence")},
			},
		}
	}
	input := SemanticActivationEvidence{
		InstanceID: "instance-1", RegistryProfile: SemanticAccessProfile, RegistryRevision: 7, RegistryDigest: digest("registry"),
		ControlProfile: SemanticAccessProfile, ControlRevision: 9, ControlDigest: digest("control"),
		BarrierProfile: SemanticBarrierProfile, ConsumerProfile: SemanticConsumerProfile, CacheProfile: SemanticCacheProfile, AuditProfile: SemanticAuditProfile,
		Models: []SemanticActivationModelEvidence{model("semantic_z", "z"), model("semantic_a", "a")},
	}
	first, err := NewSemanticActivationEvidence(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Models[0], input.Models[1] = input.Models[1], input.Models[0]
	second, err := NewSemanticActivationEvidence(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || first.Models[0].ModelID != "semantic_a" {
		t.Fatalf("canonical evidence differs by input order:\n%#v\n%#v", first, second)
	}

	for name, mutate := range map[string]func(*SemanticActivationEvidence){
		"stale registry": func(value *SemanticActivationEvidence) { value.RegistryRevision++ },
		"noncanonical model order": func(value *SemanticActivationEvidence) {
			value.Models[0], value.Models[1] = value.Models[1], value.Models[0]
		},
		"policy mismatch": func(value *SemanticActivationEvidence) {
			value.Models[0].CompiledPolicyDigest = digest("tampered")
		},
		"unsupported barrier": func(value *SemanticActivationEvidence) { value.BarrierProfile = "final-output-filter/v1" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := first
			changed.Models = append([]SemanticActivationModelEvidence(nil), first.Models...)
			mutate(&changed)
			if err := changed.Validate(); err == nil {
				t.Fatal("tampered activation evidence was accepted")
			}
		})
	}
}
