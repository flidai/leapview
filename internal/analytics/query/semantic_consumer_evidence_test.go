package query

import (
	"bytes"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/resultidentity"
)

func TestSemanticAccessConsumerCacheIdentityAndEvidenceAreDetached(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	consumer, err := NewSemanticAccessConsumer(mustNewCompiledPlanner(t, semanticAccessTestModel(t)), SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "semantic-model:test", Generation: "generation-1", PrincipalID: snapshot.PrincipalID,
		PublicationPolicy: semanticConsumerTestPublicationPolicy(),
		Authority: func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
			return snapshot, authority, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := consumer.CacheIdentity()
	if err != nil {
		t.Fatalf("CacheIdentity() error = %v", err)
	}
	if identity == nil || identity.ProjectID != "project:test" || identity.Environment != "prod" || identity.ModelID != "semantic-model:test" || identity.PrincipalID != snapshot.PrincipalID || identity.PolicyDigest == "" || identity.DecisionDigest == "" {
		t.Fatalf("CacheIdentity() = %#v", identity)
	}
	originalDecision := identity.DecisionDigest
	identity.DecisionDigest = "mutated"
	second, err := consumer.CacheIdentity()
	if err != nil {
		t.Fatalf("CacheIdentity() second error = %v", err)
	}
	if second.DecisionDigest != originalDecision {
		t.Fatalf("CacheIdentity() returned mutable/shared identity: %#v", second)
	}

	firstEvidence, err := consumer.EvidenceJSON()
	if err != nil {
		t.Fatalf("EvidenceJSON() error = %v", err)
	}
	secondEvidence, err := consumer.EvidenceJSON()
	if err != nil {
		t.Fatalf("EvidenceJSON() second error = %v", err)
	}
	if !bytes.Equal(firstEvidence, secondEvidence) {
		t.Fatalf("EvidenceJSON() is not deterministic: %s != %s", firstEvidence, secondEvidence)
	}
	for _, raw := range []string{"sales", "west", "9007199254740993", "9007199254740993.125"} {
		if strings.Contains(string(firstEvidence), raw) {
			t.Fatalf("EvidenceJSON() leaked raw authority value %q: %s", raw, firstEvidence)
		}
	}
}

func semanticConsumerTestPublicationPolicy() resultidentity.PublicationPolicyIdentity {
	return resultidentity.PublicationPolicyIdentity{
		Candidate: resultidentity.PublicationIdentity{InstanceID: "instance-1", AuthoredID: "semantic-model:test", ResourceKind: "semantic_model", Version: "1.0.0", VersionBaseline: "1.0.0", ProjectionProfile: "leapview.contract/v1", Digest: "sha256:" + strings.Repeat("a", 64)},
		Policy:    resultidentity.PolicyIdentity{BaselineKind: "genesis", Class: "compatible", Compatibility: "additive", StructuralCompatibility: "additive", SemanticCompatibility: "additive", SecurityImpact: "none", PolicyEvidenceDigest: "sha256:" + strings.Repeat("b", 64)},
	}
}

func TestSemanticAccessConsumerEvidenceRejectsStalePinnedDecision(t *testing.T) {
	attributes := semanticAccessEffective(t, semanticAccessDefinitions())
	snapshot, authority := semanticAccessSnapshot(t, attributes)
	currentSnapshot, currentAuthority := snapshot, authority
	consumer, err := NewSemanticAccessConsumer(mustNewCompiledPlanner(t, semanticAccessTestModel(t)), SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "semantic-model:test", Generation: "generation-1", PrincipalID: snapshot.PrincipalID,
		Authority: func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
			return currentSnapshot, currentAuthority, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	currentSnapshot, currentAuthority = semanticAccessSnapshot(t, nil)
	if _, err := consumer.CacheIdentity(); err == nil || !strings.Contains(err.Error(), "stale or inconsistent") {
		t.Fatalf("CacheIdentity() stale error = %v", err)
	}
	if _, err := consumer.EvidenceJSON(); err == nil || !strings.Contains(err.Error(), "stale or inconsistent") {
		t.Fatalf("EvidenceJSON() stale error = %v", err)
	}
}

func TestSemanticAccessConsumerCacheIdentityRequiresPublicationPolicy(t *testing.T) {
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	consumer, err := NewSemanticAccessConsumer(mustNewCompiledPlanner(t, semanticAccessTestModel(t)), SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "semantic-model:test", Generation: "generation-1", PrincipalID: snapshot.PrincipalID,
		Authority: func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
			return snapshot, authority, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.CacheIdentity(); err == nil || !strings.Contains(err.Error(), "publication policy identity is unavailable") {
		t.Fatalf("CacheIdentity() missing publication policy error = %v", err)
	}
	if err := consumer.Authorize(SemanticAccessTarget{Dataset: "orders"}); err != nil {
		t.Fatalf("missing cache publication evidence changed authorization: %v", err)
	}
}

func TestSemanticAccessConsumerPublicEvidenceIsNil(t *testing.T) {
	consumer, err := NewSemanticAccessConsumer(mustNewCompiledPlanner(t, testModel()), SemanticAccessConsumerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := consumer.CacheIdentity()
	if err != nil || identity != nil {
		t.Fatalf("public CacheIdentity() = %#v, %v; want nil, nil", identity, err)
	}
	evidence, err := consumer.EvidenceJSON()
	if err != nil || evidence != nil {
		t.Fatalf("public EvidenceJSON() = %s, %v; want nil, nil", evidence, err)
	}
}
