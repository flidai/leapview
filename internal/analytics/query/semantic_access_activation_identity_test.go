package query

import "testing"

func TestSemanticAccessActivationIdentitySeparatesDefinitionAndGeneration(t *testing.T) {
	model := semanticAccessTestModel(t)
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	registry := semanticAccessRegistry(semanticAccessDefinitions())
	planned, err := QualifySemanticAccessActivation("instance-1", "semantic-model:sales", "activation-plan:bundle", model, compiled, registry)
	if err != nil {
		t.Fatal(err)
	}
	deployed, err := QualifySemanticAccessActivation("instance-1", "semantic-model:sales", "generation-9", model, compiled, registry)
	if err != nil {
		t.Fatal(err)
	}
	if planned.DefinitionDigest == "" || planned.DefinitionDigest != deployed.DefinitionDigest {
		t.Fatalf("policy definition identity changed across generations: planned=%#v deployed=%#v", planned, deployed)
	}
	if planned.GenerationDigest == deployed.GenerationDigest {
		t.Fatalf("generation-qualified policy identity was not isolated: %#v", planned)
	}
	otherTarget, err := QualifySemanticAccessActivation("instance-2", "semantic-model:sales", "generation-9", model, compiled, registry)
	if err != nil {
		t.Fatal(err)
	}
	if otherTarget.DefinitionDigest == deployed.DefinitionDigest {
		t.Fatalf("policy definition identity did not bind the target instance: %#v", deployed)
	}
}
